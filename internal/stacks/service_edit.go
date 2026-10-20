package stacks

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	"github.com/rcss-labs/stackstead/internal/composecli"
	"github.com/rcss-labs/stackstead/internal/composefile"
	"github.com/rcss-labs/stackstead/internal/engine"
)

// lockedEntry takes the stack's lock and then reads its entry, so the entry is
// never stale relative to an edit that finished while we waited. The first Get
// only avoids creating lock records for names that do not exist.
func (s *Service) lockedEntry(ctx context.Context, stack string) (Entry, func(), error) {
	if _, err := s.reg.Get(ctx, stack); err != nil {
		return Entry{}, nil, err
	}
	unlock := s.lockStack(stack)
	e, err := s.reg.Get(ctx, stack)
	if err != nil {
		unlock()
		return Entry{}, nil, err
	}
	return e, unlock, nil
}

// EditResult is the outcome of a saved edit. ChecksPartial is set when the
// engine was unreachable so only stack-declared ports were checked.
type EditResult struct {
	Hash          string
	ChecksPartial bool
}

// stagedEdit loads the stack's compose file under its lock, checks the
// caller's hash, and applies the service edit in memory. UpdateService and
// PreviewService both start here so a preview can never accept an edit the
// save would refuse. The caller must call unlock.
func (s *Service) stagedEdit(ctx context.Context, stack, service string, cfg composefile.ServiceConfig, hash string) (e Entry, old, edited *composefile.File, unlock func(), err error) {
	e, unlock, err = s.lockedEntry(ctx, stack)
	if err != nil {
		return Entry{}, nil, nil, nil, err
	}
	fail := func(err error) (Entry, *composefile.File, *composefile.File, func(), error) {
		unlock()
		return Entry{}, nil, nil, nil, err
	}
	data, current, err := loadFile(e)
	if err != nil {
		return fail(err)
	}
	if current != hash {
		return fail(composefile.ErrConflict)
	}
	old, err = parseFile(data)
	if err != nil {
		return fail(err)
	}
	edited, err = old.UpdateService(service, cfg)
	if err != nil {
		return fail(err)
	}
	if err := refuseOverrides(e.ComposePath); err != nil {
		return fail(err)
	}
	return e, old, edited, unlock, nil
}

// UpdateService applies a form edit to one service. It shares its save
// pipeline (commit) with SaveRaw.
// A non-nil df is written after compose accepts the edit.
func (s *Service) UpdateService(ctx context.Context, stack, service string, cfg composefile.ServiceConfig, hash string, df *DockerfileWrite) (EditResult, error) {
	e, old, edited, unlock, err := s.stagedEdit(ctx, stack, service, cfg, hash)
	if err != nil {
		return EditResult{}, err
	}
	defer unlock()
	preSave, err := dockerfilePreSave(e, edited, service, df)
	if err != nil {
		return EditResult{}, err
	}
	return s.commit(ctx, e, old, edited, hash, false, preSave)
}

// DockerfileWrite is a Dockerfile to write along with a service edit. When
// CheckHash is set, Hash is the version the caller loaded ("" for a file that
// must not exist yet); otherwise an existing file is overwritten.
type DockerfileWrite struct {
	Content   []byte
	Hash      string
	CheckHash bool
}

// dockerfilePreSave checks df against the edited compose file and returns the
// step commit runs once compose accepts the edit. It returns nil for a nil df.
// The target is resolved on the edited file, so the Dockerfile lands where the
// saved service will look for it, and only inside the stack folder.
func dockerfilePreSave(e Entry, edited *composefile.File, service string, df *DockerfileWrite) (func() (func() error, error), error) {
	if df == nil {
		return nil, nil
	}
	if err := ValidateDockerfile("dockerfile", df.Content); err != nil {
		return nil, err
	}
	stackDir := filepath.Dir(e.ComposePath)
	path, inside, err := dockerfileTarget(stackDir, edited, service)
	if errors.Is(err, ErrNoBuild) {
		return nil, &ValidationError{Fields: []FieldError{{Field: "dockerfile", Message: "this service doesn't build an image, so it has no Dockerfile"}}}
	}
	if err != nil {
		return nil, err
	}
	if !inside {
		return nil, &ValidationError{Fields: []FieldError{{Field: "dockerfile", Message: "a Dockerfile can only be written inside the stack's folder"}}}
	}
	// Add may replace a leftover file only at the managed path; anywhere else
	// (build: ., another service's file) it must find no file, like an update
	// that loaded none.
	var expected *string
	if df.CheckHash || path != filepath.Join(stackDir, service, "Dockerfile") {
		expected = &df.Hash
	}
	return func() (func() error, error) {
		_, rollback, err := writeDockerfile(stackDir, path, df.Content, expected)
		return rollback, err
	}, nil
}

// PreviewService returns the bytes UpdateService would write for the same
// edit, without validating with compose, checking ports, or touching the
// file. It shares stagedEdit with UpdateService, so a preview computed
// against a hash is what a save with that hash writes.
func (s *Service) PreviewService(ctx context.Context, stack, service string, cfg composefile.ServiceConfig, hash string) ([]byte, error) {
	_, _, edited, unlock, err := s.stagedEdit(ctx, stack, service, cfg, hash)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return edited.Bytes(), nil
}

// SaveRaw replaces the whole compose file with content.
func (s *Service) SaveRaw(ctx context.Context, stack string, content []byte, hash string) (EditResult, error) {
	e, unlock, err := s.lockedEntry(ctx, stack)
	if err != nil {
		return EditResult{}, err
	}
	defer unlock()

	data, current, err := loadFile(e)
	if err != nil {
		return EditResult{}, err
	}
	if current != hash {
		return EditResult{}, composefile.ErrConflict
	}
	edited, err := parseFile(content)
	if err != nil {
		return EditResult{}, err
	}
	old, _ := parseFile(data) // a currently broken file just means every port counts as changed
	return s.commit(ctx, e, old, edited, hash, true, nil)
}

// commit is the save pipeline: check the ports and IPs that changed, check
// the candidate bytes with compose, then replace the file under the loaded
// hash and refresh the recorded project name. old may be nil. preSave, when
// not nil, runs after compose accepted the candidate and just before the save;
// the rollback it returns is called if the project claim or the save fails.
func (s *Service) commit(ctx context.Context, e Entry, old, edited *composefile.File, hash string, qualify bool, preSave func() (rollback func() error, err error)) (EditResult, error) {
	if err := refuseOverrides(e.ComposePath); err != nil {
		return EditResult{}, err
	}
	newServices, err := configsOf(edited)
	if err != nil {
		return EditResult{}, err
	}
	cs := changeSet{stack: e.Name, project: e.Project, services: newServices, qualify: qualify,
		localNetworks: edited.LocalNetworks()}
	if old != nil {
		if oldServices, err := configsOf(old); err == nil {
			cs.old = map[string]composefile.ServiceConfig{}
			for _, o := range oldServices {
				cs.old[o.Name] = o.Config
			}
		}
	}
	partial, err := s.checkChanges(ctx, cs)
	if err != nil {
		return EditResult{}, err
	}

	candidate := edited.Bytes()
	if err := s.requireCompose(ctx); err != nil {
		return EditResult{}, err
	}
	project, err := s.validateCandidate(ctx, e.ComposePath, candidate)
	if err != nil {
		return EditResult{}, err
	}
	if project != e.Project {
		// Claiming a project is check-then-act across stacks, so it is
		// serialized by projectMu (taken after the stack lock, see Service).
		s.projectMu.Lock()
		defer s.projectMu.Unlock()
		p, err := s.projectAvailable(ctx, project, e.Name)
		if err != nil {
			return EditResult{}, err
		}
		partial = partial || p
		rollback, err := s.runPreSave(e, preSave)
		if err != nil {
			return EditResult{}, err
		}
		// Claim the project before saving so a failed claim writes nothing;
		// hand it back if the save then fails.
		if err := s.reg.SetProject(ctx, e.Name, project); err != nil {
			rollback()
			return EditResult{}, s.mapProjectTaken(ctx, err, project, e.Name)
		}
		if s.afterClaim != nil {
			s.afterClaim()
		}
		if s.beforeComposeSave != nil {
			s.beforeComposeSave()
		}
		if err := composefile.Save(e.ComposePath, candidate, hash); err != nil {
			rollback()
			return EditResult{}, s.revertProject(ctx, e, err)
		}
		return EditResult{Hash: composefile.Hash(candidate), ChecksPartial: partial}, nil
	}
	rollback, err := s.runPreSave(e, preSave)
	if err != nil {
		return EditResult{}, err
	}
	if s.beforeComposeSave != nil {
		s.beforeComposeSave()
	}
	if err := composefile.Save(e.ComposePath, candidate, hash); err != nil {
		rollback()
		return EditResult{}, err
	}
	return EditResult{Hash: composefile.Hash(candidate), ChecksPartial: partial}, nil
}

// runPreSave runs preSave, which may be nil; the returned rollback is never nil
// and logs, rather than drops, a failure to undo the write.
func (s *Service) runPreSave(e Entry, preSave func() (func() error, error)) (func(), error) {
	if preSave == nil {
		return func() {}, nil
	}
	undo, err := preSave()
	if err != nil {
		return func() {}, err
	}
	return func() {
		if err := undo(); err != nil {
			s.log.Error("Dockerfile rollback failed; the Dockerfile may disagree with the compose file",
				"stack", e.Name, "err", err)
		}
	}, nil
}

// revertProject hands the claimed project back after a failed save and returns
// saveErr, joined with the revert's own error if that fails too.
func (s *Service) revertProject(ctx context.Context, e Entry, saveErr error) error {
	// Not the request context: a cancelled request must not leave the claim behind.
	if err := s.reg.SetProject(context.WithoutCancel(ctx), e.Name, e.Project); err != nil {
		// Logged here so the failed revert is visible whatever the caller does with the error.
		s.log.Error("revert of claimed compose project failed; registry may disagree with the compose file",
			"stack", e.Name, "project", e.Project, "err", err, "save_err", saveErr)
		return errors.Join(saveErr, fmt.Errorf("revert project of stack %s to %s: %w", e.Name, e.Project, err))
	}
	return saveErr
}

// validateCandidate runs `compose config` on data placed next to the real
// file so relative paths, .env and the project name resolve identically, and
// returns the project name compose derives for it.
func (s *Service) validateCandidate(ctx context.Context, realPath string, data []byte) (string, error) {
	tmp, err := os.CreateTemp(filepath.Dir(realPath), ".stackstead-check-*.yaml")
	if err != nil {
		return "", fmt.Errorf("create check file: %w", err)
	}
	defer os.Remove(tmp.Name())
	_, werr := tmp.Write(data)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return "", fmt.Errorf("write check file: %w", werr)
	}
	if err := s.compose.Config(ctx, tmp.Name()); err != nil {
		return "", renameInExitError(err, filepath.Base(tmp.Name()), filepath.Base(realPath))
	}
	project, err := s.compose.ProjectName(ctx, tmp.Name())
	return project, renameInExitError(err, filepath.Base(tmp.Name()), filepath.Base(realPath))
}

// renameInExitError makes a compose error talk about the user's file rather
// than the temporary copy that was checked.
func renameInExitError(err error, from, to string) error {
	var exit *composecli.ExitError
	if errors.As(err, &exit) {
		fixed := *exit
		fixed.Output = strings.ReplaceAll(exit.Output, from, to)
		return &fixed
	}
	return err
}

func configsOf(f *composefile.File) ([]namedConfig, error) {
	names, err := f.Services()
	if err != nil {
		return nil, err
	}
	out := make([]namedConfig, 0, len(names))
	for _, n := range names {
		svc, err := f.Service(n)
		if err != nil {
			return nil, err
		}
		out = append(out, namedConfig{Name: n, Config: svc.Config})
	}
	return out, nil
}

type namedConfig struct {
	Name   string
	Config composefile.ServiceConfig
}

// changeSet describes the services a write will produce, and what they were.
type changeSet struct {
	stack   string // stack being edited: its declared ports are not "other stacks'"
	project string // its compose project: its containers are not conflicts
	// services is every service of the resulting file; old holds their
	// previous configs. Only ports and IPs absent from old are checked.
	services []namedConfig
	old      map[string]composefile.ServiceConfig
	indexed  bool  // prefix paths with the original services request index
	indices  []int // original indices when invalid create entries were excluded
	qualify  bool  // prefix field names with "services.<name>." (multi-service input)
	// localNetworks are the networks the edited file declares itself (not
	// external). One that does not exist on the engine yet is created by the
	// first `up`, so engine-dependent subnet and usage checks wait until then.
	// Same-file sibling conflicts are checked even before that first `up`.
	localNetworks map[string]bool
}

// checkChanges validates the new host ports and static IPs of cs against
// the engine's containers and networks and the ports declared by other
// stacks and sibling services. When the engine is unreachable it still
// checks declared ports and reports partial = true.
func (s *Service) checkChanges(ctx context.Context, cs changeSet) (partial bool, err error) {
	type job struct {
		svc          namedConfig
		index        int
		position     int
		changedPorts map[int]bool
		changedIPs   []int
	}
	var jobs []job
	for position, svc := range cs.services {
		old := cs.old[svc.Name]
		j := job{svc: svc, index: position, position: position, changedPorts: map[int]bool{}}
		if cs.indices != nil {
			j.index = cs.indices[position]
		}
		oldPorts := setOf(old.Ports)
		for i, p := range svc.Config.Ports {
			if !oldPorts[p] {
				j.changedPorts[i] = true
			}
		}
		for i, n := range svc.Config.Networks {
			if n.IPv4Address != "" && !hasAttachment(old.Networks, n) {
				j.changedIPs = append(j.changedIPs, i)
			}
		}
		if len(j.changedPorts) > 0 || len(j.changedIPs) > 0 {
			jobs = append(jobs, j)
		}
	}
	if len(jobs) == 0 {
		return false, nil
	}

	others, err := s.otherStacks(ctx, cs.stack)
	if err != nil {
		return false, err
	}
	containers, nets, engErr := s.engineView(ctx)
	if engErr != nil {
		if !errors.Is(engErr, engine.ErrUnavailable) || ctx.Err() != nil {
			return false, engErr
		}
		partial = true
	}
	inUse := append(declaredPorts(others), containerPorts(containers, cs.project)...)
	usedIPs := containerIPs(containers, cs.project)
	for network, uses := range declaredIPs(others, nets) {
		usedIPs[network] = append(usedIPs[network], uses...)
	}

	var fields []FieldError
	for _, j := range jobs {
		prefix := ""
		if cs.indexed {
			prefix = fmt.Sprintf("services[%d].", j.index)
		} else if cs.qualify {
			prefix = "services." + j.svc.Name + "."
		}
		use := append([]PortUse(nil), inUse...)
		// Derive sibling usage afresh for each job: later jobs must not see
		// their own static addresses inserted while checking an earlier job.
		siblingIPs := make(map[string][]IPUse)
		for position, sib := range cs.services {
			_, existed := cs.old[sib.Name]
			if sib.Name == j.svc.Name || (!existed && position >= j.position) {
				continue
			}
			use = append(use, declaredBy("service "+sib.Name, sib.Config.Ports)...)
			if partial {
				continue
			}
			for _, n := range sib.Config.Networks {
				addr, err := netip.ParseAddr(n.IPv4Address)
				if err != nil {
					continue
				}
				network := resolveNetwork(nets, cs.project, n.Name)
				siblingIPs[network] = append(siblingIPs[network], IPUse{Addr: addr, Owner: "service " + sib.Name})
			}
		}
		for _, fe := range PortConflicts("ports", j.svc.Config.Ports, use) {
			var idx int
			if _, err := fmt.Sscanf(fe.Field, "ports[%d]", &idx); err == nil && !j.changedPorts[idx] {
				continue
			}
			fe.Field = prefix + fe.Field
			fields = append(fields, fe)
		}
		if partial {
			continue
		}
		for _, i := range j.changedIPs {
			n := j.svc.Config.Networks[i]
			network := resolveNetwork(nets, cs.project, n.Name)
			field := fmt.Sprintf("%snetworks.%s.ipv4_address", prefix, n.Name)
			addr, parseErr := netip.ParseAddr(n.IPv4Address)
			conflict := false
			for _, use := range siblingIPs[network] {
				if parseErr == nil && use.Addr == addr {
					fields = append(fields, FieldError{Field: field, Message: fmt.Sprintf("%s is already used by %s", n.IPv4Address, use.Owner)})
					conflict = true
					break
				}
			}
			if conflict || (cs.localNetworks[n.Name] && !networkExists(nets, network)) {
				continue
			}
			for _, fe := range CheckStaticIP(field, network, n.IPv4Address, nets, usedIPs) {
				fields = append(fields, fe)
			}
		}
	}
	if len(fields) > 0 {
		return partial, &ValidationError{Fields: fields}
	}
	return partial, nil
}

func (s *Service) engineView(ctx context.Context) ([]engine.Container, []engine.Network, error) {
	containers, err := s.engine.Containers(ctx)
	if err != nil {
		return nil, nil, err
	}
	nets, err := s.engine.Networks(ctx)
	if err != nil {
		return nil, nil, err
	}
	return containers, nets, nil
}

func setOf(items []string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, it := range items {
		m[it] = true
	}
	return m
}

func hasAttachment(list []composefile.NetworkAttachment, n composefile.NetworkAttachment) bool {
	for _, o := range list {
		if o.Name == n.Name && o.IPv4Address == n.IPv4Address {
			return true
		}
	}
	return false
}

// resolveNetwork maps a compose network key to the engine's network name:
// external networks keep their name, the project's own networks are prefixed
// with the project name.
func resolveNetwork(nets []engine.Network, project, key string) string {
	for _, n := range nets {
		if n.Name == key {
			return key
		}
	}
	if project != "" {
		for _, n := range nets {
			if n.Name == project+"_"+key {
				return n.Name
			}
		}
	}
	return key
}

func networkExists(nets []engine.Network, name string) bool {
	for _, n := range nets {
		if n.Name == name {
			return true
		}
	}
	return false
}

// otherStack is a registered stack's parsed services.
type otherStack struct {
	Entry    Entry
	Services []namedConfig
}

// otherStacks parses every registered stack except exclude, skipping the
// unreadable ones.
func (s *Service) otherStacks(ctx context.Context, exclude string) ([]otherStack, error) {
	entries, err := s.reg.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []otherStack
	for _, e := range entries {
		if e.Name == exclude {
			continue
		}
		_, _, f, err := readFile(e)
		if err != nil {
			continue
		}
		services, err := configsOf(f)
		if err != nil {
			continue
		}
		out = append(out, otherStack{e, services})
	}
	return out, nil
}

func declaredPorts(others []otherStack) []PortUse {
	var out []PortUse
	for _, o := range others {
		for _, svc := range o.Services {
			out = append(out, declaredBy("stack "+o.Entry.Name, svc.Config.Ports)...)
		}
	}
	return out
}

// declaredIPs maps engine network names to the static IPs other stacks declare.
func declaredIPs(others []otherStack, nets []engine.Network) map[string][]IPUse {
	out := map[string][]IPUse{}
	for _, o := range others {
		for _, svc := range o.Services {
			for _, n := range svc.Config.Networks {
				a, err := netip.ParseAddr(n.IPv4Address)
				if err != nil {
					continue
				}
				network := resolveNetwork(nets, o.Entry.Project, n.Name)
				out[network] = append(out[network], IPUse{Addr: a, Owner: fmt.Sprintf("stack %s service %s", o.Entry.Name, svc.Name)})
			}
		}
	}
	return out
}

func declaredBy(owner string, specs []string) []PortUse {
	var out []PortUse
	for _, spec := range specs {
		ports, err := ParsePorts(spec)
		if err != nil {
			continue // reported separately by PortConflicts
		}
		for _, p := range ports {
			out = append(out, PortUse{HostPort: p, Owner: owner})
		}
	}
	return out
}

// containerPorts lists the host ports engine containers publish, leaving out
// the containers of ownProject. An unparsable bind address is treated as a
// wildcard so that the check fails closed.
func containerPorts(containers []engine.Container, ownProject string) []PortUse {
	var out []PortUse
	for _, c := range containers {
		if ownProject != "" && c.Project() == ownProject {
			continue
		}
		for _, p := range c.Ports {
			if p.PublicPort == 0 {
				continue
			}
			var ip netip.Addr
			if p.IP != "" {
				if a, err := netip.ParseAddr(p.IP); err == nil {
					ip = a
				}
			}
			proto := strings.ToLower(p.Type)
			if proto == "" {
				proto = "tcp"
			}
			out = append(out, PortUse{HostPort: HostPort{IP: ip, Port: p.PublicPort, Proto: proto}, Owner: "container " + c.Name})
		}
	}
	return out
}

func containerIPs(containers []engine.Container, ownProject string) map[string][]IPUse {
	used := map[string][]IPUse{}
	for _, c := range containers {
		if ownProject != "" && c.Project() == ownProject {
			continue
		}
		for network, ip := range c.Networks {
			if a, err := netip.ParseAddr(ip); err == nil {
				used[network] = append(used[network], IPUse{Addr: a, Owner: "container " + c.Name})
			}
		}
	}
	return used
}

// stagedAdd shares the locked hash and preparation checks between preview and save.
// The caller must release unlock after using the staged file.
func (s *Service) stagedAdd(ctx context.Context, stack, service string, cfg composefile.ServiceConfig, hash string) (e Entry, old, edited *composefile.File, unlock func(), err error) {
	e, unlock, err = s.lockedEntry(ctx, stack)
	if err != nil {
		return Entry{}, nil, nil, nil, err
	}
	fail := func(err error) (Entry, *composefile.File, *composefile.File, func(), error) {
		unlock()
		return Entry{}, nil, nil, nil, err
	}
	data, current, err := loadFile(e)
	if err != nil {
		return fail(err)
	}
	if current != hash {
		return fail(composefile.ErrConflict)
	}
	old, err = parseFile(data)
	if err != nil {
		return fail(err)
	}
	if err := ValidateServiceName(service); err != nil {
		return fail(err)
	}
	cfg, err = s.prepareConfig(stack, service, filepath.Dir(e.ComposePath), cfg)
	if err != nil {
		return fail(err)
	}
	edited, err = old.AddService(service, cfg)
	if err != nil {
		return fail(err)
	}
	if err := refuseOverrides(e.ComposePath); err != nil {
		return fail(err)
	}
	return e, old, edited, unlock, nil
}

// AddService saves a new service after checking its ports, IPs and compose config.
// A non-nil df is written after compose accepts the edit and replaces any
// leftover file at the managed path.
func (s *Service) AddService(ctx context.Context, stack, service string, cfg composefile.ServiceConfig, hash string, df *DockerfileWrite) (EditResult, error) {
	e, old, edited, unlock, err := s.stagedAdd(ctx, stack, service, cfg, hash)
	if err != nil {
		return EditResult{}, err
	}
	defer unlock()
	preSave, err := dockerfilePreSave(e, edited, service, df)
	if err != nil {
		return EditResult{}, err
	}
	result, err := s.commit(ctx, e, old, edited, hash, false, preSave)
	if err == nil {
		s.log.Info("added service", "stack", stack, "service", service)
	}
	return result, err
}

// PreviewAddService returns the candidate without compose calls or filesystem writes.
func (s *Service) PreviewAddService(ctx context.Context, stack, service string, cfg composefile.ServiceConfig, hash string) ([]byte, error) {
	_, _, edited, unlock, err := s.stagedAdd(ctx, stack, service, cfg, hash)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return edited.Bytes(), nil
}

// RemoveService validates and saves the removal; Up applies it to containers later.
func (s *Service) RemoveService(ctx context.Context, stack, service, hash string) (EditResult, error) {
	e, unlock, err := s.lockedEntry(ctx, stack)
	if err != nil {
		return EditResult{}, err
	}
	defer unlock()
	data, current, err := loadFile(e)
	if err != nil {
		return EditResult{}, err
	}
	if current != hash {
		return EditResult{}, composefile.ErrConflict
	}
	old, err := parseFile(data)
	if err != nil {
		return EditResult{}, err
	}
	if err := refuseOverrides(e.ComposePath); err != nil {
		return EditResult{}, err
	}
	edited, err := old.RemoveService(service)
	if err != nil {
		return EditResult{}, err
	}
	result, err := s.commit(ctx, e, old, edited, hash, false, nil)
	if err == nil {
		s.log.Info("removed service", "stack", stack, "service", service)
	}
	return result, err
}
