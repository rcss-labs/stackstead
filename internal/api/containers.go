package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/engine"
)

// RestartsAPI is satisfied by *host.Collector: restarts in the last 24 h by
// container id, nil before its first scan.
type RestartsAPI interface{ RestartCounts() map[string]int }

const (
	containerListTTL = 2 * time.Second
	// containerListTimeout bounds the shared list computation.
	containerListTimeout = 10 * time.Second

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
}

func (s *server) handleContainerList(w http.ResponseWriter, r *http.Request) {
	body, err := s.containerList(r.Context())
	switch {
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
		if i, ok := latest[c.Name]; ok {
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
		entries = append(entries, entry{p.PublicPort, fmt.Sprintf("%s:%d->%d/%s", p.IP, p.PublicPort, p.PrivatePort, p.Type)})
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
