package history

import (
	"context"
	"log/slog"
	"time"

	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/state"
)

// warnEvery rate-limits insert-failure warnings, like the metrics sampler.
const warnEvery = time.Minute

const (
	labelComposeProject = "com.docker.compose.project"
	labelComposeService = "com.docker.compose.service"
)

// Source is the container state cache as the recorder uses it; satisfied by *state.Cache.
type Source interface {
	Containers(ctx context.Context) ([]engine.Container, error)
	Connected() bool
	Subscribe() (<-chan state.Change, func())
	SubscribeEvents() (<-chan engine.Event, func())
}

// Journal persists rows and reports each live container's last stored state;
// satisfied by *Store.
type Journal interface {
	Insert(ctx context.Context, rows []Row) error
	Latest(ctx context.Context) ([]Row, error)
}

// known is what the recorder last wrote for one container.
type known struct {
	project, service, id, state string
}

// Recorder turns the cache's change notifications into stored transitions.
type Recorder struct {
	src    Source
	w      Journal
	log    *slog.Logger
	now    func() time.Time
	last   map[string]known     // container id -> last written state
	dying  map[string]bool      // container id -> saw "die", waiting for "start"
	lastRS map[string]time.Time // container id -> event time of the last restart recorded
	warned time.Time
	seeded bool // last has been seeded from the journal
}

func NewRecorder(src Source, w Journal, logger *slog.Logger, now func() time.Time) *Recorder {
	if logger == nil {
		logger = slog.Default()
	}
	if now == nil {
		now = time.Now
	}
	return &Recorder{src: src, w: w, log: logger, now: now,
		last: map[string]known{}, dying: map[string]bool{}, lastRS: map[string]time.Time{}}
}

// DeriveState maps a container's engine state and status text to a stored state.
func DeriveState(c engine.Container) (string, *int) {
	switch c.State {
	case "running":
		switch c.Health() {
		case "healthy":
			return StateHealthy, nil
		case "unhealthy":
			return StateUnhealthy, nil
		case "starting":
			return StateStarting, nil
		}
		return StateRunning, nil
	case "restarting":
		return StateRestarting, nil
	case "created":
		return StateStarting, nil
	case "paused":
		return StateRunning, nil
	default: // exited, dead
		if code, ok := c.ExitCode(); ok {
			return StateExited, &code
		}
		return StateExited, nil
	}
}

// Run records until ctx is done. State lives in this goroutine only.
func (r *Recorder) Run(ctx context.Context) {
	changes, cancelChanges := r.src.Subscribe()
	events, cancelEvents := r.src.SubscribeEvents()
	defer func() { cancelChanges(); cancelEvents() }()

	r.connect(ctx) // baseline for an already-connected cache
	for {
		select {
		case <-ctx.Done():
			return
		case ch, ok := <-changes:
			if !ok {
				cancelChanges()
				changes, cancelChanges = r.src.Subscribe()
				r.connect(ctx)
				continue
			}
			if ch.Project == "" {
				r.connect(ctx)
			} else {
				r.project(ctx, ch.Project)
			}
		case ev, ok := <-events:
			if !ok {
				cancelEvents()
				events, cancelEvents = r.src.SubscribeEvents()
				continue
			}
			r.event(ctx, ev)
		}
	}
}

// connect writes a baseline row for every compose container when connected.
func (r *Recorder) connect(ctx context.Context) {
	if !r.src.Connected() {
		return
	}
	cs, err := r.src.Containers(ctx)
	if err != nil {
		return
	}
	r.seed(ctx)
	ts := r.now()
	var rows []Row
	next := map[string]known{}
	for _, c := range cs {
		if c.Project() == "" {
			continue
		}
		st, code := DeriveState(c)
		k := known{c.Project(), c.ComposeService(), c.ID, st}
		next[c.ID] = k
		rows = append(rows, rowFor(ts, k, code))
	}
	for id, k := range r.last {
		if _, ok := next[id]; !ok {
			rows = append(rows, rowFor(ts, known{k.project, k.service, k.id, StateRemoved}, nil))
		}
	}
	if !r.insert(ctx, rows) {
		return // keep the old map so the next change retries
	}
	r.last = next
}

// seed loads, once, the containers the journal last saw alive, so the first
// baseline marks removed those that vanished while Stackstead was down. On
// error it warns and carries on with what this run has seen.
func (r *Recorder) seed(ctx context.Context) {
	if r.seeded {
		return
	}
	r.seeded = true
	rows, err := r.w.Latest(ctx)
	if err != nil {
		r.warn(ctx, "history: load last container states", err)
		return
	}
	for _, row := range rows {
		if _, ok := r.last[row.Container]; !ok {
			r.last[row.Container] = known{row.Project, row.Service, row.Container, row.State}
		}
	}
}

// project records what changed in one project since the last-known states.
func (r *Recorder) project(ctx context.Context, project string) {
	cs, err := r.src.Containers(ctx)
	if err != nil {
		return
	}
	ts := r.now()
	var rows []Row
	updates := map[string]known{}
	seen := map[string]bool{}
	for _, c := range cs {
		if c.Project() != project {
			continue
		}
		seen[c.ID] = true
		st, code := DeriveState(c)
		k := known{project, c.ComposeService(), c.ID, st}
		if prev, ok := r.last[c.ID]; !ok || prev.state != st {
			rows = append(rows, rowFor(ts, k, code))
			updates[c.ID] = k
		}
	}
	var gone []string
	for id, k := range r.last {
		if k.project == project && !seen[id] {
			rows = append(rows, rowFor(ts, known{k.project, k.service, k.id, StateRemoved}, nil))
			gone = append(gone, id)
		}
	}
	if !r.insert(ctx, rows) {
		return
	}
	for id, k := range updates {
		r.last[id] = k
	}
	for _, id := range gone {
		delete(r.last, id)
	}
}

// event counts a die followed by a start as one restart. A destroyed
// container can never restart, so its bookkeeping is dropped.
func (r *Recorder) event(ctx context.Context, ev engine.Event) {
	project := ev.Labels[labelComposeProject]
	if project == "" {
		return
	}
	switch ev.Action {
	case "die":
		r.dying[ev.ContainerID] = true
	case "destroy":
		delete(r.dying, ev.ContainerID)
		delete(r.lastRS, ev.ContainerID)
	case "start":
		if !r.dying[ev.ContainerID] {
			return
		}
		delete(r.dying, ev.ContainerID)
		ts := r.now()
		// A replayed start carries its original event time; a new restart, even
		// within the same second, does not. Events without a time fall back to now.
		eventTime := ev.Time
		if eventTime.IsZero() {
			eventTime = ts
		}
		if prev, ok := r.lastRS[ev.ContainerID]; ok && prev.Equal(eventTime) {
			return
		}
		row := Row{TS: ts, Project: project, Service: ev.Labels[labelComposeService],
			Container: ev.ContainerID, State: StateRestart}
		if r.insert(ctx, []Row{row}) {
			r.lastRS[ev.ContainerID] = eventTime
		}
	}
}

func rowFor(ts time.Time, k known, code *int) Row {
	return Row{TS: ts, Project: k.project, Service: k.service, Container: k.id, State: k.state, ExitCode: code}
}

// insert writes rows in one call; failures are logged through warn.
func (r *Recorder) insert(ctx context.Context, rows []Row) bool {
	if len(rows) == 0 {
		return true
	}
	err := r.w.Insert(ctx, rows)
	if err == nil {
		return true
	}
	r.warn(ctx, "history: record transitions", err)
	return false
}

// warn logs at most once per warnEvery, and not at all once ctx is done.
func (r *Recorder) warn(ctx context.Context, msg string, err error) {
	if ctx.Err() != nil {
		return
	}
	if now := r.now(); now.Sub(r.warned) >= warnEvery {
		r.warned = now
		r.log.Warn(msg, "err", err)
	}
}
