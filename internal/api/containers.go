package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/engine"
)

// RestartsAPI is satisfied by *host.Collector: restarts in the last 24 h by
// container id, nil before its first scan.
type RestartsAPI interface{ RestartCounts() map[string]int }

// ContainerEngineAPI is the part of *engine.Client the details and action
// routes use.
type ContainerEngineAPI interface {
	ContainerAction(ctx context.Context, id, action string, stopTimeout int) error
	InspectContainer(ctx context.Context, id string) (engine.ContainerInspect, error)
}

const (
	containerListTTL = 2 * time.Second
	// containerListTimeout bounds the shared list computation.
	containerListTimeout = 10 * time.Second

	// containerActionTimeout bounds one start/stop/restart: longer than the
	// 10 s stop grace plus Docker's own work, and than the engine client's
	// default timeout, which a deadline-less context would fall back to.
	containerActionTimeout = 30 * time.Second
	containerStopGrace     = 10 // seconds, Docker's ?t=

	managedStack   = "stack"
	managedCompose = "compose"
	managedNone    = "none"
)

// containerCache is the single-flight 2 s cache of the host-wide list. mu
// guards the fields; the computation runs outside it, shared through inflight.
type containerCache struct {
	mu       sync.Mutex
	at       time.Time
	body     []apitypes.ContainerRow
	ok       bool
	inflight *containerFlight
}

type containerFlight struct {
	done chan struct{}
	body []apitypes.ContainerRow
	err  error
	// unavailable marks a state-cache error wrapping engine.ErrUnavailable.
	unavailable bool
}

func (s *server) registerContainerRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/containers", s.requireSession(s.handleContainerList))
	mux.Handle("GET /api/containers/{id}/logs", s.requireSession(s.handleContainerLogs))
}

func (s *server) handleContainerList(w http.ResponseWriter, r *http.Request) {
	body, err := s.containerList(r.Context())
	switch {
	case r.Context().Err() != nil:
		// The client went away while waiting; nobody is left to answer.
	case errors.Is(err, engine.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, "docker engine is unavailable")
	case err != nil:
		s.internalError(w, r, err)
	default:
		writeJSON(w, http.StatusOK, body)
	}
}

// containerList returns the list from the 2 s cache; concurrent misses share
// one computation. Errors are not cached.
func (s *server) containerList(ctx context.Context) ([]apitypes.ContainerRow, error) {
	c := &s.containerCache
	c.mu.Lock()
	now := s.opts.Now()
	if c.ok && now.Sub(c.at) < containerListTTL {
		body := c.body
		c.mu.Unlock()
		return body, nil
	}
	f := c.inflight
	if f == nil {
		f = &containerFlight{done: make(chan struct{})}
		c.inflight = f
		go s.runContainerList(ctx, f, now)
	}
	c.mu.Unlock()

	// Each caller waits on its own context, so a cancelled request neither
	// blocks nor fails the others.
	select {
	case <-f.done:
		return f.body, f.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *server) runContainerList(ctx context.Context, f *containerFlight, now time.Time) {
	qctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), containerListTimeout)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			f.body, f.err = nil, fmt.Errorf("container list panicked: %v", r)
		}
		c := &s.containerCache
		c.mu.Lock()
		c.inflight = nil
		if f.err == nil {
			c.body, c.at, c.ok = f.body, now, true
		}
		c.mu.Unlock()
		close(f.done)
	}()
	f.body, f.err = s.computeContainerList(qctx)
}

func (s *server) computeContainerList(ctx context.Context) ([]apitypes.ContainerRow, error) {
	list, err := s.opts.Observe.Containers(ctx)
	if err != nil {
		return nil, err
	}
	summaries, err := s.opts.Stacks.List(ctx)
	if err != nil {
		return nil, err
	}
	projectToStack := make(map[string]string, len(summaries))
	for _, sum := range summaries {
		if sum.Project != "" {
			projectToStack[sum.Project] = sum.Name
		}
	}
	latest := make(map[string]int) // container name -> index into samples
	samples := s.opts.Observe.Latest()
	for i, m := range samples {
		latest[m.Container] = i
	}
	var restarts map[string]int // reading a nil map is fine
	if s.opts.Restarts != nil {
		restarts = s.opts.Restarts.RestartCounts()
	}

	rows := make([]apitypes.ContainerRow, 0, len(list))
	for _, c := range list {
		managed, stack := containerManaged(c, projectToStack)
		row := apitypes.ContainerRow{
			ID: shortID(c.ID), Name: c.Name, Image: c.Image, State: c.State, Status: c.Status,
			Health: c.Health(), CreatedAt: c.Created.UTC(),
			Project: c.Project(), Service: c.ComposeService(), Stack: stack, Managed: managed,
			Ports: formatPorts(c.Ports), Networks: map[string]string{}, Restarts24h: restarts[c.ID],
		}
		if code, ok := c.ExitCode(); ok && c.State == "exited" {
			row.ExitCode = &code
		}
		for name, ip := range c.Networks {
			row.Networks[name] = ip
		}
		// A stopped container keeps its last sample until the sampler's next
		// tick; showing it would make a stopped container look busy.
		if i, ok := latest[c.Name]; ok && c.State == "running" {
			m := samples[i]
			cpu, mem, limit := m.CPU, m.Mem, m.MemLimit
			row.CPU, row.Mem, row.MemLimit = &cpu, &mem, &limit
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, nil
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// containerManaged is the ownership rule shared by the list, details and
// actions: containers of a registered stack's project belong to that stack,
// other compose projects are read-only here, the rest are standalone.
func containerManaged(c engine.Container, projectToStack map[string]string) (managed, stack string) {
	project := c.Project()
	if project == "" {
		return managedNone, ""
	}
	if name, ok := projectToStack[project]; ok {
		return managedStack, name
	}
	return managedCompose, ""
}

// formatPorts renders published ports as "ip:public->private/proto", sorted
// by public port. A port Docker reports for both 0.0.0.0 and :: is shown once
// as 0.0.0.0. The result is never nil.
func formatPorts(ps []engine.Port) []string {
	type key struct {
		public, private uint16
		proto           string
	}
	hasV4 := map[key]bool{}
	for _, p := range ps {
		if p.PublicPort != 0 && p.IP == "0.0.0.0" {
			hasV4[key{p.PublicPort, p.PrivatePort, p.Type}] = true
		}
	}
	type entry struct {
		public uint16
		text   string
	}
	var entries []entry
	for _, p := range ps {
		if p.PublicPort == 0 {
			continue
		}
		if p.IP == "::" && hasV4[key{p.PublicPort, p.PrivatePort, p.Type}] {
			continue
		}
		entries = append(entries, entry{p.PublicPort, fmt.Sprintf("%s:%d->%d/%s", hostForPort(p.IP), p.PublicPort, p.PrivatePort, p.Type)})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].public != entries[j].public {
			return entries[i].public < entries[j].public
		}
		return entries[i].text < entries[j].text
	})
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.text)
	}
	return out
}

// hostForPort brackets IPv6 literals so "host:port" stays unambiguous.
func hostForPort(ip string) string {
	if addr, err := netip.ParseAddr(ip); err == nil && addr.Is6() {
		return "[" + ip + "]"
	}
	return ip
}

var containerIDPattern = regexp.MustCompile(`^[0-9a-f]{12,64}$`)

func (s *server) registerContainerActionRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/containers/{id}", s.requireSession(s.handleContainerDetails))
	mux.Handle("POST /api/containers/{id}/{action}", s.requireSession(s.handleContainerAction))
}

// projectStacks maps compose project -> registered stack name, read fresh
// from the registry so ownership checks never act on a cached view.
func (s *server) projectStacks(ctx context.Context) (map[string]string, error) {
	summaries, err := s.opts.Stacks.List(ctx)
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(summaries))
	for _, sum := range summaries {
		if sum.Project != "" {
			m[sum.Project] = sum.Name
		}
	}
	return m, nil
}

// inspectContainer validates the id and inspects it, writing the error
// response itself (404 bad/unknown id, 503 unavailable, 500 otherwise).
func (s *server) inspectContainer(w http.ResponseWriter, r *http.Request) (engine.ContainerInspect, bool) {
	id := r.PathValue("id")
	if !containerIDPattern.MatchString(id) {
		writeError(w, http.StatusNotFound, "container not found")
		return engine.ContainerInspect{}, false
	}
	insp, err := s.opts.ContainerEngine.InspectContainer(r.Context(), id)
	var actionErr *engine.ActionError
	if errors.As(err, &actionErr) && actionErr.Status == http.StatusBadRequest {
		// Docker's answer for an ambiguous id prefix: nothing is addressable by it.
		writeError(w, http.StatusNotFound, "container not found")
		return engine.ContainerInspect{}, false
	}
	if err != nil {
		s.writeEngineError(w, r, err)
		return engine.ContainerInspect{}, false
	}
	// Docker also resolves names; the API is only ever addressed by id.
	if !strings.HasPrefix(insp.ID, id) {
		writeError(w, http.StatusNotFound, "container not found")
		return engine.ContainerInspect{}, false
	}
	return insp, true
}

// writeEngineError maps an engine error to the API's status codes.
func (s *server) writeEngineError(w http.ResponseWriter, r *http.Request, err error) {
	var actionErr *engine.ActionError
	switch {
	case r.Context().Err() != nil:
		// The client went away; nobody is left to answer.
	case errors.Is(err, engine.ErrNotFound):
		writeError(w, http.StatusNotFound, "container not found")
	case errors.As(err, &actionErr):
		writeError(w, http.StatusBadGateway, actionErr.Message)
	case errors.Is(err, engine.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, "docker engine is unavailable")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusServiceUnavailable, "Docker did not answer in time")
	default:
		s.internalError(w, r, err)
	}
}

// liveStatus is Docker's human status text ("Up 3 seconds") from the state
// cache snapshot when the container is there and current; inspect only has State.Status.
func (s *server) liveStatus(ctx context.Context, insp engine.ContainerInspect) (status string, created time.Time) {
	list, err := s.opts.Observe.Containers(ctx)
	if err == nil {
		for _, c := range list {
			if c.ID == insp.ID {
				// A snapshot that predates an action still says "Up ..."; only
				// trust its text when it agrees with Docker's state.
				if c.State == insp.State {
					return c.Status, c.Created
				}
				return insp.Status, c.Created
			}
		}
	}
	return insp.Status, time.Time{}
}

// exitCodeOf is the exit code for containers that have stopped, else nil.
func exitCodeOf(insp engine.ContainerInspect) *int {
	if insp.State == "exited" || insp.State == "dead" {
		code := insp.ExitCode
		return &code
	}
	return nil
}

func timeOrNil(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	t = t.UTC()
	return &t
}

func (s *server) handleContainerDetails(w http.ResponseWriter, r *http.Request) {
	insp, ok := s.inspectContainer(w, r)
	if !ok {
		return
	}
	projects, err := s.projectStacks(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	owner := engine.Container{Labels: insp.Labels}
	managed, stack := containerManaged(owner, projects)
	status, _ := s.liveStatus(r.Context(), insp)
	var restarts int
	if s.opts.Restarts != nil {
		restarts = s.opts.Restarts.RestartCounts()[insp.ID]
	}
	d := apitypes.ContainerDetail{
		ID: insp.ID, Name: insp.Name, Image: insp.Image, State: insp.State, Status: status, Health: insp.Health,
		StartedAt: timeOrNil(insp.StartedAt), FinishedAt: timeOrNil(insp.FinishedAt), ExitCode: exitCodeOf(insp),
		RestartPolicy: insp.RestartPolicy, Restarts24h: restarts,
		Project: owner.Project(), Service: owner.ComposeService(), Stack: stack, Managed: managed,
		Ports: formatPorts(insp.Ports), Networks: map[string]string{}, Mounts: []apitypes.ContainerMount{},
		Labels: map[string]string{},
	}
	for k, v := range insp.Networks {
		d.Networks[k] = v
	}
	for k, v := range insp.Labels {
		d.Labels[k] = v
	}
	for _, m := range insp.Mounts {
		d.Mounts = append(d.Mounts, apitypes.ContainerMount{Type: m.Type, Source: m.Source, Destination: m.Destination, ReadOnly: m.ReadOnly})
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *server) handleContainerAction(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	if action != "start" && action != "stop" && action != "restart" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	var body struct{}
	if !decodeJSON(w, r, &body) {
		return
	}
	insp, ok := s.inspectContainer(w, r)
	if !ok {
		return
	}
	projects, err := s.projectStacks(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if managed, stack := containerManaged(engine.Container{Labels: insp.Labels}, projects); managed == managedStack {
		s.opts.Logger.Info("container action refused", "container", insp.Name, "action", action, "outcome", "refused")
		writeError(w, http.StatusConflict, fmt.Sprintf("%s belongs to stack %s; use the stack's actions", insp.Name, stack))
		return
	}

	actx, cancel := context.WithTimeout(r.Context(), containerActionTimeout)
	defer cancel()
	if err := s.opts.ContainerEngine.ContainerAction(actx, insp.ID, action, containerStopGrace); err != nil {
		s.opts.Logger.Info("container action failed", "container", insp.Name, "action", action, "outcome", "failed", "err", err)
		s.writeEngineError(w, r, err)
		return
	}
	s.opts.Logger.Info("container action", "container", insp.Name, "action", action)

	// Docker's answer, not the cache, says what the container looks like now.
	after, err := s.opts.ContainerEngine.InspectContainer(r.Context(), insp.ID)
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.actionRow(r.Context(), after))
}

// actionRow is the §4.1 row for a freshly inspected container.
func (s *server) actionRow(ctx context.Context, insp engine.ContainerInspect) apitypes.ContainerRow {
	owner := engine.Container{Labels: insp.Labels}
	managed, stack := managedNone, ""
	if projects, err := s.projectStacks(ctx); err == nil {
		managed, stack = containerManaged(owner, projects)
	}
	status, created := s.liveStatus(ctx, insp)
	row := apitypes.ContainerRow{
		ID: shortID(insp.ID), Name: insp.Name, Image: insp.Image, State: insp.State, Status: status,
		Health: insp.Health, ExitCode: exitCodeOf(insp), CreatedAt: created.UTC(),
		Project: owner.Project(), Service: owner.ComposeService(), Stack: stack, Managed: managed,
		Ports: formatPorts(insp.Ports), Networks: map[string]string{},
	}
	for name, ip := range insp.Networks {
		row.Networks[name] = ip
	}
	if s.opts.Restarts != nil {
		row.Restarts24h = s.opts.Restarts.RestartCounts()[insp.ID]
	}
	if insp.State == "running" {
		for _, m := range s.opts.Observe.Latest() {
			if m.Container == insp.Name {
				cpu, mem, limit := m.CPU, m.Mem, m.MemLimit
				row.CPU, row.Mem, row.MemLimit = &cpu, &mem, &limit
				break
			}
		}
	}
	return row
}

// handleContainerLogs streams one container's logs with the same events,
// cursors and resume rules as the stack route (see handleLogs). The container
// is found in the state cache by id prefix; an id matching none or several is
// a 404.
func (s *server) handleContainerLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !containerIDPattern.MatchString(id) {
		writeError(w, http.StatusNotFound, "container not found")
		return
	}
	opts, ok := parseLogOptions(w, r)
	if !ok {
		return
	}
	all, err := s.opts.Observe.Containers(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "docker engine is unavailable")
		return
	}
	var matches []engine.Container
	for _, c := range all {
		if strings.HasPrefix(c.ID, id) {
			matches = append(matches, c)
		}
	}
	if len(matches) != 1 {
		writeError(w, http.StatusNotFound, "container not found")
		return
	}
	s.streamContainerLogs(w, r, matches, opts)
}
