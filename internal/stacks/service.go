package stacks

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rcss-labs/stackstead/internal/composecli"
	"github.com/rcss-labs/stackstead/internal/composefile"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/imageref"
)

// Compile-time checks that the production types fit the interfaces.
var (
	_ Compose = (*composecli.Runner)(nil)
	_ Engine  = (*engine.Client)(nil)
)

// Compose is the docker compose CLI as the service uses it; satisfied by
// *composecli.Runner.
type Compose interface {
	Version(ctx context.Context) (string, error)
	Config(ctx context.Context, file string) error
	ProjectName(ctx context.Context, file string) (string, error)
	Run(ctx context.Context, file string, args []string, out func(string)) error
}

// Engine is the Docker engine as the service uses it; satisfied by *engine.Client.
type Engine interface {
	Ping(ctx context.Context) error
	Containers(ctx context.Context) ([]engine.Container, error)
	Networks(ctx context.Context) ([]engine.Network, error)
}

// ContainerSource supplies the containers status is derived from; satisfied by
// *engine.Client and by the events-driven state.Cache.
type ContainerSource interface {
	Containers(ctx context.Context) ([]engine.Container, error)
}

type Deps struct {
	Registry     *Registry
	Ops          *Ops
	Compose      Compose
	Engine       Engine
	Containers   ContainerSource // status source; nil means Engine
	StacksDir    string          // where Create puts new stacks, one folder each
	AllowedRoots []string        // confines build contexts of new stacks
	Now          func() time.Time
	Logger       *slog.Logger // nil means slog.Default()
	// Library makes library images present before up and pull; nil leaves
	// library references to compose.
	Library LibraryFetcher
}

// Status is the run state of a stack.
type Status string

const (
	StatusRunning     Status = "running"
	StatusPartial     Status = "partial"
	StatusStopped     Status = "stopped"
	StatusUnavailable Status = "unavailable" // compose file missing or unparseable
	StatusUnknown     Status = "unknown"     // the engine could not be asked
)

// ErrStackUnavailable means the stack's compose file is missing or unparseable.
var ErrStackUnavailable = errors.New("stack compose file is missing or unreadable")

type Summary struct {
	Name, Path, Project string
	Origin              Origin
	Status              Status
	Services, Running   int
	Health              HealthCounts
	Ports               []string // host ports the compose file publishes; ranges as "a-b"
	// ChecksPartial is set by Create when the engine was unreachable and only
	// the ports declared by other stacks could be checked.
	ChecksPartial bool
	// LibraryRefs lists the library images the stack's services use, sorted
	// and unique; filled by describe.
	LibraryRefs []string
}

type ContainerInfo struct{ ID, Name, Service, State, Status, Health string }

// HealthCounts tallies a stack's containers. Failed covers dead, restarting
// and non-zero-exit containers; clean exits, created and paused count nowhere.
type HealthCounts struct{ Healthy, Unhealthy, Starting, None, Failed int }

type ServiceDetail struct {
	Name       string
	Config     composefile.ServiceConfig
	ReadOnly   []composefile.Field
	Containers []ContainerInfo
}

type Detail struct {
	Summary
	Hash       string
	Services   []ServiceDetail
	ParseError string
	// Overrides lists override files next to the compose file; filled by
	// Details only.
	Overrides []string
}

// Service orchestrates stacks: registry, compose files, compose CLI, engine
// and the operations manager.
type Service struct {
	reg       *Registry
	ops       *Ops
	compose   Compose
	engine    Engine
	source    ContainerSource // status reads; validation that needs fresh data uses engine
	stacksDir string
	roots     []string
	log       *slog.Logger
	library   LibraryFetcher

	// Lock order: a stack's lock (lockStack) first, then projectMu; never the
	// reverse. projectMu makes "is this compose project free?" and the write
	// that claims it (Add, SetProject, adoption) one step across all stacks;
	// the unique index on stacks.project is the backstop.
	// afterClaim, when set by tests, runs between claiming a project and saving the file.
	afterClaim func()
	// beforeComposeSave, when set by tests, runs just before the compose file is saved.
	beforeComposeSave func()
	locks             sync.Map // stack name -> *sync.Mutex, serializes saves of one stack
	projectMu         sync.Mutex
}

func NewService(d Deps) *Service {
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	var source ContainerSource = d.Containers
	if source == nil {
		source = d.Engine
	}
	return &Service{reg: d.Registry, ops: d.Ops, compose: d.Compose, engine: d.Engine, source: source,
		stacksDir: d.StacksDir, roots: d.AllowedRoots, log: logger, library: d.Library}
}

// lockStack serializes read-modify-write cycles of one stack's file.
func (s *Service) lockStack(name string) (unlock func()) {
	m, _ := s.locks.LoadOrStore(name, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// readFile loads and parses a stack's compose file. A missing, unreadable or
// unparseable file is reported as ErrStackUnavailable.
func readFile(e Entry) (data []byte, hash string, f *composefile.File, err error) {
	data, hash, err = loadFile(e)
	if err != nil {
		return nil, "", nil, err
	}
	f, err = composefile.Parse(data)
	if err != nil {
		return data, hash, nil, fmt.Errorf("%w: %v", ErrStackUnavailable, err)
	}
	return data, hash, f, nil
}

// loadFile reads a stack's compose file; a missing or unreadable file is
// reported as ErrStackUnavailable.
func loadFile(e Entry) ([]byte, string, error) {
	data, hash, err := composefile.Load(e.ComposePath)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrStackUnavailable, err)
	}
	return data, hash, nil
}

// overrideNames are the files `docker compose` merges automatically, but only
// when no -f is given. Stackstead always passes -f, so it would silently drop
// them (and "up --remove-orphans" would remove their containers).
var overrideNames = []string{"compose.override.yaml", "compose.override.yml",
	"docker-compose.override.yaml", "docker-compose.override.yml"}

// overrideFiles lists the override files that sit next to composePath.
func overrideFiles(composePath string) []string {
	var found []string
	for _, n := range overrideNames {
		if _, err := os.Stat(filepath.Join(filepath.Dir(composePath), n)); err == nil {
			found = append(found, n)
		}
	}
	return found
}

// refuseOverrides fails when composePath has override files next to it.
func refuseOverrides(composePath string) error {
	if o := overrideFiles(composePath); len(o) > 0 {
		return fmt.Errorf("%w: override file %s present; remove or merge it", ErrStackUnavailable, o[0])
	}
	return nil
}

// requireCompose fails with composecli.ErrComposeUnavailable when the compose
// plugin is missing, so callers can answer 503 instead of a misleading
// compose error.
func (s *Service) requireCompose(ctx context.Context) error {
	_, err := s.compose.Version(ctx)
	return err
}

// parseFile parses user-supplied YAML; syntax errors count as invalid config
// so the API answers 422 rather than 500.
func parseFile(data []byte) (*composefile.File, error) {
	f, err := composefile.Parse(data)
	if err == nil || errors.Is(err, composefile.ErrNotMapping) || errors.Is(err, composefile.ErrMultipleDocuments) {
		return f, err
	}
	return nil, fmt.Errorf("%w: %v", composefile.ErrInvalidConfig, err)
}

// Sync adopts every <StacksDir>/<name>/compose.yaml not yet registered.
// One broken folder does not stop the others from being adopted.
func (s *Service) Sync(ctx context.Context) error {
	base, err := filepath.EvalSymlinks(s.stacksDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	dirs, err := os.ReadDir(base)
	if err != nil {
		return err
	}
	known, err := s.reg.List(ctx)
	if err != nil {
		return err
	}
	byName, byPath := map[string]bool{}, map[string]bool{}
	for _, e := range known {
		byName[e.Name], byPath[resolved(e.ComposePath)] = true, true
	}
	var errs []error
	for _, d := range dirs {
		path, err := filepath.EvalSymlinks(filepath.Join(base, d.Name(), composeFileName))
		if err != nil {
			continue
		}
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			continue
		}
		if byName[d.Name()] || byPath[path] || ValidateName(d.Name()) != nil {
			continue
		}
		if o := overrideFiles(path); len(o) > 0 {
			errs = append(errs, fmt.Errorf("skipped %s: override file %s present; Stackstead does not support override files yet", d.Name(), o[0]))
			continue
		}
		project, err := s.compose.ProjectName(ctx, path)
		if errors.Is(err, composecli.ErrComposeUnavailable) || ctx.Err() != nil {
			return errors.Join(append(errs, err)...)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("adopt %s: %w", d.Name(), err))
			continue
		}
		err = s.adopt(ctx, Entry{Name: d.Name(), ComposePath: path, Origin: OriginManaged, Project: project})
		if err != nil {
			errs = append(errs, err)
			continue
		}
		byName[d.Name()], byPath[path] = true, true
	}
	return errors.Join(errs...)
}

// adopt registers e unless its project is taken; a skip is reported as an error.
func (s *Service) adopt(ctx context.Context, e Entry) error {
	s.projectMu.Lock() // see Service for lock order
	defer s.projectMu.Unlock()
	owner, taken, err := s.projectOwner(ctx, e.Project, "")
	if err != nil {
		return err
	}
	if taken {
		return fmt.Errorf("skipped %s: compose project %q is already used by stack %s", e.Name, e.Project, owner)
	}
	err = s.reg.Add(ctx, e)
	if errors.Is(err, ErrStackExists) {
		return nil // adopted concurrently
	}
	if errors.Is(err, ErrProjectTaken) {
		return fmt.Errorf("skipped %s: compose project %q is already used by another stack", e.Name, e.Project)
	}
	return err
}

func resolved(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// projectOwner returns the registered stack other than exclude that uses
// project.
func (s *Service) projectOwner(ctx context.Context, project, exclude string) (string, bool, error) {
	entries, err := s.reg.List(ctx)
	if err != nil {
		return "", false, err
	}
	for _, e := range entries {
		if e.Project == project && e.Name != exclude {
			return e.Name, true, nil
		}
	}
	return "", false, nil
}

// mapProjectTaken turns the registry's unique-index violation into the same
// ValidationError the ownership check gives; other errors pass through.
func (s *Service) mapProjectTaken(ctx context.Context, err error, project, exclude string) error {
	if !errors.Is(err, ErrProjectTaken) {
		return err
	}
	if owner, taken, oerr := s.projectOwner(ctx, project, exclude); oerr == nil && taken {
		return projectTaken(project, owner)
	}
	return &ValidationError{Fields: []FieldError{{Field: "name",
		Message: fmt.Sprintf("compose project %q is already used by another stack", project)}}}
}

func projectTaken(project, owner string) error {
	return &ValidationError{Fields: []FieldError{{Field: "name",
		Message: fmt.Sprintf("compose project %q is already used by stack %s", project, owner)}}}
}

// List summarizes every registered stack. A broken stack is reported as
// unavailable; an unreachable engine makes the rest unknown. Neither fails
// the call.
func (s *Service) List(ctx context.Context) ([]Summary, error) {
	entries, err := s.reg.List(ctx)
	if err != nil {
		return nil, err
	}
	containers, engErr := s.source.Containers(ctx)
	if engErr != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	out := make([]Summary, 0, len(entries))
	for _, e := range entries {
		d := s.describe(e, containers, engErr)
		out = append(out, d.Summary)
	}
	return out, nil
}

func (s *Service) Get(ctx context.Context, name string) (Detail, error) {
	e, err := s.reg.Get(ctx, name)
	if err != nil {
		return Detail{}, err
	}
	containers, engErr := s.source.Containers(ctx)
	if engErr != nil && ctx.Err() != nil {
		return Detail{}, ctx.Err()
	}
	return s.describe(e, containers, engErr), nil
}

// Details describes every registered stack from one registry read and one
// container listing. Errors follow List: a registry failure fails the call, an
// unreachable engine leaves statuses unknown.
func (s *Service) Details(ctx context.Context) ([]Detail, error) {
	entries, err := s.reg.List(ctx)
	if err != nil {
		return nil, err
	}
	containers, engErr := s.source.Containers(ctx)
	if engErr != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	out := make([]Detail, 0, len(entries))
	for _, e := range entries {
		d := s.describe(e, containers, engErr)
		d.Overrides = overrideFiles(e.ComposePath)
		out = append(out, d)
	}
	return out, nil
}

// describe builds the full detail of a stack from its file and the engine's
// containers; it never fails.
func (s *Service) describe(e Entry, containers []engine.Container, engErr error) Detail {
	d := Detail{Summary: Summary{Name: e.Name, Path: e.ComposePath, Project: e.Project, Origin: e.Origin}}
	d.Health = countHealth(e.Project, containers)
	_, hash, f, err := readFile(e)
	if err == nil {
		d.Ports = publishedPorts(f)
		err = fillServices(&d, f, e.Project, containers)
	}
	if err != nil {
		d.Status, d.ParseError = StatusUnavailable, err.Error()
		d.Services, d.Summary.Services, d.Running, d.Ports = nil, 0, 0, nil
		return d
	}
	d.Hash = hash
	switch {
	case engErr != nil:
		d.Status = StatusUnknown
	case d.Running == 0 || d.Summary.Services == 0:
		d.Status = StatusStopped
	case d.Running < d.Summary.Services:
		d.Status = StatusPartial
	default:
		d.Status = StatusRunning
	}
	return d
}

func fillServices(d *Detail, f *composefile.File, project string, containers []engine.Container) error {
	names, err := f.Services()
	if err != nil {
		return err
	}
	// One pass over the containers instead of one per service.
	byService := map[string][]engine.Container{}
	if project != "" {
		for _, c := range containers {
			if c.Project() == project {
				byService[c.ComposeService()] = append(byService[c.ComposeService()], c)
			}
		}
	}
	for _, name := range names {
		svc, err := f.Service(name)
		if err != nil {
			return err
		}
		sd := ServiceDetail{Name: name, Config: svc.Config, ReadOnly: svc.ReadOnly}
		running := false
		for _, c := range byService[name] {
			sd.Containers = append(sd.Containers, ContainerInfo{ID: c.ID, Name: c.Name, Service: name, State: c.State, Status: c.Status, Health: c.Health()})
			running = running || c.State == "running"
		}
		if running {
			d.Running++
		}
		d.Services = append(d.Services, sd)
	}
	d.Summary.Services = len(names)
	d.LibraryRefs = libraryRefStrings(d.Services)
	return nil
}

// Raw returns the compose file as stored and its version hash.
func (s *Service) Raw(ctx context.Context, name string) ([]byte, string, error) {
	e, err := s.reg.Get(ctx, name)
	if err != nil {
		return nil, "", err
	}
	return loadFile(e)
}

// composeArgs maps an action to its `docker compose` arguments.
var composeArgs = map[string][]string{
	"up":      {"up", "-d", "--build", "--remove-orphans"},
	"down":    {"down"},
	"restart": {"restart"},
	"pull":    {"pull"},
	"build":   {"build"},
}

// Act starts a lifecycle action as an operation.
func (s *Service) Act(ctx context.Context, stack, action string) (Operation, error) {
	args, ok := composeArgs[action]
	if !ok {
		return Operation{}, &ValidationError{Fields: []FieldError{{Field: "action", Message: fmt.Sprintf("unknown action %q", action)}}}
	}
	// Held until the operation is started so a concurrent edit's project
	// claim cannot be reverted by refreshProject; same order as commit.
	e, unlock, err := s.lockedEntry(ctx, stack)
	if err != nil {
		return Operation{}, err
	}
	defer unlock()
	_, _, f, err := readFile(e)
	if err != nil {
		return Operation{}, err
	}
	refs, args, err := s.actPlan(f, action, args)
	if err != nil {
		return Operation{}, err
	}
	if err := refuseOverrides(e.ComposePath); err != nil {
		return Operation{}, err
	}
	if err := s.requireLifecycle(ctx); err != nil {
		return Operation{}, err
	}
	if e, err = s.refreshProject(ctx, e); err != nil {
		return Operation{}, err
	}
	return s.ops.Start(stack, action, func(ctx context.Context, out func(string)) error {
		if err := s.ensureLibrary(ctx, refs, out); err != nil {
			return err
		}
		if args == nil { // nothing left for compose to pull
			return nil
		}
		return s.compose.Run(ctx, e.ComposePath, args, out)
	})
}

// ActService rebuilds and restarts one service with `up -d --build --no-deps`,
// leaving the stack's other services alone. Unlike the stack-wide up it omits
// --remove-orphans, which acts on the whole project. The "--" keeps a
// service name that looks like a flag from becoming one. Only "up" is supported.
func (s *Service) ActService(ctx context.Context, stack, service, action string) (Operation, error) {
	if action != "up" {
		return Operation{}, &ValidationError{Fields: []FieldError{{Field: "action", Message: fmt.Sprintf("unknown service action %q", action)}}}
	}
	e, unlock, err := s.lockedEntry(ctx, stack)
	if err != nil {
		return Operation{}, err
	}
	defer unlock()
	_, _, f, err := readFile(e)
	if err != nil {
		return Operation{}, err
	}
	names, err := f.Services()
	if err != nil {
		return Operation{}, err
	}
	if !slices.Contains(names, service) {
		return Operation{}, composefile.ErrServiceNotFound
	}
	if err := refuseOverrides(e.ComposePath); err != nil {
		return Operation{}, err
	}
	if err := s.requireLifecycle(ctx); err != nil {
		return Operation{}, err
	}
	if e, err = s.refreshProject(ctx, e); err != nil {
		return Operation{}, err
	}
	args := []string{"up", "-d", "--build", "--no-deps", "--", service}
	var refs []imageref.Ref
	if s.library != nil {
		if svc, err := f.Service(service); err == nil {
			if ref, ok := imageref.Parse(svc.Config.Image); ok {
				if err := refuseAlwaysPull(f, service); err != nil {
					return Operation{}, err
				}
				refs = append(refs, ref)
			}
		}
	}
	return s.ops.StartService(stack, action, service, func(ctx context.Context, out func(string)) error {
		if err := s.ensureLibrary(ctx, refs, out); err != nil {
			return err
		}
		return s.compose.Run(ctx, e.ComposePath, args, out)
	})
}

// refreshProject re-derives the compose project from the file on disk. The
// file may have been edited outside Stackstead (a hand-added name:, or
// COMPOSE_PROJECT_NAME in .env), and compose would then act on a project the
// registry does not know. A changed project is claimed like an edit would.
func (s *Service) refreshProject(ctx context.Context, e Entry) (Entry, error) {
	project, err := s.compose.ProjectName(ctx, e.ComposePath)
	if err != nil {
		return e, err
	}
	if project == e.Project {
		return e, nil
	}
	s.projectMu.Lock() // see Service for lock order
	defer s.projectMu.Unlock()
	if _, err := s.projectAvailable(ctx, project, e.Name); err != nil {
		return e, err
	}
	if err := s.reg.SetProject(ctx, e.Name, project); err != nil {
		return e, s.mapProjectTaken(ctx, err, project, e.Name)
	}
	e.Project = project
	return e, nil
}

// requireLifecycle checks that both Docker and the compose plugin are usable.
func (s *Service) requireLifecycle(ctx context.Context) error {
	if err := s.engine.Ping(ctx); err != nil {
		return err
	}
	return s.requireCompose(ctx)
}

// Remove runs `compose down` and then unregisters the stack. Files on disk
// are never touched. If the compose file is already gone there is nothing for
// compose to bring down, so the stale row is dropped, but only once the
// engine confirms no containers of the project remain.
func (s *Service) Remove(ctx context.Context, stack string, withVolumes bool) (Operation, error) {
	e, unlock, err := s.lockedEntry(ctx, stack)
	if err != nil {
		return Operation{}, err
	}
	defer unlock()
	_, statErr := os.Stat(e.ComposePath)
	missing := errors.Is(statErr, fs.ErrNotExist)
	if missing {
		if err := s.confirmNoContainers(ctx, e); err != nil {
			return Operation{}, err
		}
	} else if err := refuseOverrides(e.ComposePath); err != nil {
		return Operation{}, err
	} else if err := s.requireLifecycle(ctx); err != nil {
		return Operation{}, err
	} else if e, err = s.refreshProject(ctx, e); err != nil {
		return Operation{}, err
	}
	args := []string{"down"}
	if withVolumes {
		args = append(args, "--volumes")
	}
	return s.ops.Start(stack, "remove", func(ctx context.Context, out func(string)) error {
		if missing {
			out("compose file is missing; unregistering without running compose down")
		} else if err := s.compose.Run(ctx, e.ComposePath, args, out); err != nil {
			return err
		}
		if err := s.reg.Remove(ctx, stack); err != nil && !errors.Is(err, ErrStackNotFound) {
			return err
		}
		return nil
	})
}

// confirmNoContainers refuses to unregister a stack with no compose file while
// its containers may still exist: without the file they could never be
// brought down through Stackstead.
func (s *Service) confirmNoContainers(ctx context.Context, e Entry) error {
	if e.Project == "" {
		return fmt.Errorf("%w: compose file is missing and the stack records no compose project, so its containers cannot be identified", ErrStackUnavailable)
	}
	containers, err := s.engine.Containers(ctx)
	if err != nil {
		return fmt.Errorf("%w: compose file is missing and docker cannot be queried to confirm no containers of project %s remain: %v", ErrStackUnavailable, e.Project, err)
	}
	n := 0
	for _, c := range containers {
		if c.Project() == e.Project {
			n++
		}
	}
	if n > 0 {
		return fmt.Errorf("%w: compose file is missing but %d containers of project %s still exist; restore the file or stop them manually", ErrStackUnavailable, n, e.Project)
	}
	return nil
}

func (s *Service) Networks(ctx context.Context) ([]engine.Network, error) {
	return s.engine.Networks(ctx)
}

// countHealth tallies every container labeled with the compose project,
// including orphans that no service in the file claims.
func countHealth(project string, containers []engine.Container) HealthCounts {
	var h HealthCounts
	if project == "" {
		return h
	}
	for _, c := range containers {
		if c.Project() != project {
			continue
		}
		switch c.State {
		case "running":
			switch c.Health() {
			case "healthy":
				h.Healthy++
			case "unhealthy":
				h.Unhealthy++
			case "starting":
				h.Starting++
			default:
				h.None++
			}
		case "dead", "restarting":
			h.Failed++
		case "exited":
			if code, ok := c.ExitCode(); ok && code != 0 {
				h.Failed++
			}
		}
	}
	return h
}

// publishedPorts lists the distinct host ports the file publishes, sorted by
// port number. IP and protocol are dropped; a range renders as "first-last".
// Container-only ports, unresolved variables and invalid specs are skipped.
func publishedPorts(f *composefile.File) []string {
	ports := []string{}
	names, err := f.Services()
	if err != nil {
		return ports
	}
	seen := map[string]bool{}
	for _, name := range names {
		svc, err := f.Service(name)
		if err != nil {
			continue
		}
		for _, spec := range svc.Config.Ports {
			hp, err := ParsePorts(spec)
			if err != nil || len(hp) == 0 {
				continue
			}
			label := strconv.Itoa(int(hp[0].Port))
			if len(hp) > 1 {
				label += "-" + strconv.Itoa(int(hp[len(hp)-1].Port))
			}
			if !seen[label] {
				seen[label] = true
				ports = append(ports, label)
			}
		}
	}
	sort.Slice(ports, func(i, j int) bool {
		if a, b := lowPort(ports[i]), lowPort(ports[j]); a != b {
			return a < b
		}
		return ports[i] < ports[j]
	})
	return ports
}

// lowPort is the numeric first port of a "80" or "8000-8010" label.
func lowPort(label string) int {
	first, _, _ := strings.Cut(label, "-")
	n, _ := strconv.Atoi(first)
	return n
}
