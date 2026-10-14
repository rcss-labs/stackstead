package stacks

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rcss-labs/stackstead/internal/composecli"
	"github.com/rcss-labs/stackstead/internal/composefile"
	"github.com/rcss-labs/stackstead/internal/engine"
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

type Deps struct {
	Registry     *Registry
	Ops          *Ops
	Compose      Compose
	Engine       Engine
	StacksDir    string   // where Create puts new stacks, one folder each
	AllowedRoots []string // confines build contexts of new stacks
	Now          func() time.Time
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
	// ChecksPartial is set by Create when the engine was unreachable and only
	// the ports declared by other stacks could be checked.
	ChecksPartial bool
}

type ContainerInfo struct{ ID, Name, Service, State, Status string }

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
}

// Service orchestrates stacks: registry, compose files, compose CLI, engine
// and the operations manager.
type Service struct {
	reg       *Registry
	ops       *Ops
	compose   Compose
	engine    Engine
	stacksDir string
	roots     []string

	locks sync.Map // stack name -> *sync.Mutex, serializes saves of one stack
}

func NewService(d Deps) *Service {
	return &Service{reg: d.Registry, ops: d.Ops, compose: d.Compose, engine: d.Engine,
		stacksDir: d.StacksDir, roots: d.AllowedRoots}
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
	data, hash, err = composefile.Load(e.ComposePath)
	if err != nil {
		return nil, "", nil, fmt.Errorf("%w: %v", ErrStackUnavailable, err)
	}
	f, err = composefile.Parse(data)
	if err != nil {
		return data, hash, nil, fmt.Errorf("%w: %v", ErrStackUnavailable, err)
	}
	return data, hash, f, nil
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
	have := map[string]bool{}
	for _, e := range known {
		have[e.Name], have[e.ComposePath] = true, true
	}
	var errs []error
	for _, d := range dirs {
		path := filepath.Join(base, d.Name(), "compose.yaml")
		info, err := os.Stat(path) // follows symlinked folders
		if !d.IsDir() && d.Type()&fs.ModeSymlink == 0 || err != nil || !info.Mode().IsRegular() {
			continue
		}
		if have[d.Name()] || have[path] || ValidateName(d.Name()) != nil {
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
		err = s.reg.Add(ctx, Entry{Name: d.Name(), ComposePath: path, Origin: OriginManaged, Project: project})
		if err != nil && !errors.Is(err, ErrStackExists) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// List summarizes every registered stack. A broken stack is reported as
// unavailable; an unreachable engine makes the rest unknown. Neither fails
// the call.
func (s *Service) List(ctx context.Context) ([]Summary, error) {
	entries, err := s.reg.List(ctx)
	if err != nil {
		return nil, err
	}
	containers, engErr := s.engine.Containers(ctx)
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
	containers, engErr := s.engine.Containers(ctx)
	if engErr != nil && ctx.Err() != nil {
		return Detail{}, ctx.Err()
	}
	return s.describe(e, containers, engErr), nil
}

// describe builds the full detail of a stack from its file and the engine's
// containers; it never fails.
func (s *Service) describe(e Entry, containers []engine.Container, engErr error) Detail {
	d := Detail{Summary: Summary{Name: e.Name, Path: e.ComposePath, Project: e.Project, Origin: e.Origin}}
	_, hash, f, err := readFile(e)
	if err == nil {
		err = fillServices(&d, f, e.Project, containers)
	}
	if err != nil {
		d.Status, d.ParseError = StatusUnavailable, err.Error()
		d.Services, d.Summary.Services, d.Running = nil, 0, 0
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
	for _, name := range names {
		svc, err := f.Service(name)
		if err != nil {
			return err
		}
		sd := ServiceDetail{Name: name, Config: svc.Config, ReadOnly: svc.ReadOnly}
		running := false
		for _, c := range containers {
			if project == "" || c.Project() != project || c.ComposeService() != name {
				continue
			}
			sd.Containers = append(sd.Containers, ContainerInfo{ID: c.ID, Name: c.Name, Service: name, State: c.State, Status: c.Status})
			running = running || c.State == "running"
		}
		if running {
			d.Running++
		}
		d.Services = append(d.Services, sd)
	}
	d.Summary.Services = len(names)
	return nil
}

// Raw returns the compose file as stored and its version hash.
func (s *Service) Raw(ctx context.Context, name string) ([]byte, string, error) {
	e, err := s.reg.Get(ctx, name)
	if err != nil {
		return nil, "", err
	}
	return composefile.Load(e.ComposePath)
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
	e, err := s.reg.Get(ctx, stack)
	if err != nil {
		return Operation{}, err
	}
	if _, _, _, err := readFile(e); err != nil {
		return Operation{}, err
	}
	if err := s.engine.Ping(ctx); err != nil {
		return Operation{}, err
	}
	return s.ops.Start(stack, action, func(ctx context.Context, out func(string)) error {
		return s.compose.Run(ctx, e.ComposePath, args, out)
	})
}

// Remove runs `compose down` and then unregisters the stack. Files on disk
// are never touched. If the compose file is already gone there is nothing for
// compose to bring down, so the stale row is simply dropped.
func (s *Service) Remove(ctx context.Context, stack string, withVolumes bool) (Operation, error) {
	e, err := s.reg.Get(ctx, stack)
	if err != nil {
		return Operation{}, err
	}
	_, statErr := os.Stat(e.ComposePath)
	missing := errors.Is(statErr, fs.ErrNotExist)
	if !missing {
		if err := s.engine.Ping(ctx); err != nil {
			return Operation{}, err
		}
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

func (s *Service) Networks(ctx context.Context) ([]engine.Network, error) {
	return s.engine.Networks(ctx)
}
