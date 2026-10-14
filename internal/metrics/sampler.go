package metrics

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/rcss-labs/stackstead/internal/engine"
)

const (
	maxStatsConcurrency = 4
	maxStatsTimeout     = 5 * time.Second
	minStatsTimeout     = 3 * time.Second // Docker needs about 1s to compute the CPU delta
	pruneEvery          = time.Hour
	warnEvery           = time.Minute
	subscriberBuffer    = 256
)

// StatsSource takes one stats reading of a container.
type StatsSource interface {
	Stats(ctx context.Context, id string) (engine.Stats, error)
}

// ContainerLister returns the known containers (the state cache).
type ContainerLister interface {
	Containers(ctx context.Context) ([]engine.Container, error)
}

type sampleStore interface {
	InsertBatch(ctx context.Context, samples []Sample) error
	Prune(ctx context.Context, olderThan time.Time) (int64, error)
}

type subscriber struct{ ch chan Sample }

// Sampler periodically reads stats of running compose containers, stores them
// in one batch per tick and fans them out to live subscribers. Store failures
// only raise the Degraded flag; they never affect anything else.
type Sampler struct {
	store     sampleStore
	lister    ContainerLister
	stats     StatsSource
	interval  time.Duration
	retention time.Duration
	log       *slog.Logger
	now       func() time.Time

	tickOverride <-chan time.Time // tests: manual tick trigger
	tickDone     func()           // tests: called after each tick body

	mu        sync.Mutex
	degraded  bool
	latest    []ExpositionSample
	subs      map[string]map[*subscriber]struct{}
	lastWarn  time.Time
	lastPrune time.Time
}

func NewSampler(st *Store, lister ContainerLister, stats StatsSource, interval, retention time.Duration, logger *slog.Logger, now func() time.Time) *Sampler {
	if now == nil {
		now = time.Now
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Sampler{
		store: st, lister: lister, stats: stats, interval: interval, retention: retention,
		log: logger, now: now, subs: map[string]map[*subscriber]struct{}{},
	}
}

// Run samples until ctx is cancelled. The tick body runs synchronously, so
// ticks never overlap and ticks missed during an overrun are dropped.
func (s *Sampler) Run(ctx context.Context) {
	ticks := s.tickOverride
	if ticks == nil {
		t := time.NewTicker(s.interval)
		defer t.Stop()
		ticks = t.C
	}
	s.prune(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			s.tick(ctx)
			if s.tickDone != nil {
				s.tickDone()
			}
		}
	}
}

// Degraded reports whether the last metrics write failed.
func (s *Sampler) Degraded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.degraded
}

// Latest returns a copy of the last tick's values.
func (s *Sampler) Latest() []ExpositionSample {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ExpositionSample(nil), s.latest...)
}

// Subscribe returns a channel of live samples for project. A subscriber that
// cannot keep up is dropped and its channel closed. The cancel func is
// idempotent.
func (s *Sampler) Subscribe(project string) (<-chan Sample, func()) {
	sub := &subscriber{ch: make(chan Sample, subscriberBuffer)}
	s.mu.Lock()
	if s.subs[project] == nil {
		s.subs[project] = map[*subscriber]struct{}{}
	}
	s.subs[project][sub] = struct{}{}
	s.mu.Unlock()
	return sub.ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.dropLocked(project, sub)
	}
}

func (s *Sampler) dropLocked(project string, sub *subscriber) {
	if set, ok := s.subs[project]; ok {
		if _, ok := set[sub]; ok {
			delete(set, sub)
			close(sub.ch)
			if len(set) == 0 {
				delete(s.subs, project)
			}
		}
	}
}

func (s *Sampler) tick(ctx context.Context) {
	now := s.now()
	if now.Sub(s.lastPrune) >= pruneEvery {
		s.prune(ctx)
	}
	containers, err := s.lister.Containers(ctx)
	if err != nil {
		s.mu.Lock()
		s.latest = nil // don't serve stale values as current
		s.mu.Unlock()
		if ctx.Err() == nil {
			s.log.Warn("metrics: list containers", "err", err)
		}
		return
	}
	var targets []engine.Container
	for _, c := range containers {
		if c.State == "running" && c.Project() != "" {
			targets = append(targets, c)
		}
	}

	timeout := statsTimeout(s.interval)
	results := make([]*Sample, len(targets))
	sem := make(chan struct{}, maxStatsConcurrency)
	var wg sync.WaitGroup
	for i, c := range targets {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			cctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			st, err := s.stats.Stats(cctx, c.ID)
			if err != nil {
				if ctx.Err() == nil {
					s.log.Warn("metrics: stats failed, skipping container", "container", c.Name, "err", err)
				}
				return
			}
			if st.Read.IsZero() { // Docker answers 200 with zeroed stats for a stopped container
				return
			}
			results[i] = &Sample{
				TS: now, Project: c.Project(), Service: c.ComposeService(), Container: c.Name,
				CPU: st.CPUPercent, Mem: st.MemUsage, MemLimit: st.MemLimit,
				NetRx: st.NetRx, NetTx: st.NetTx, BlkRead: st.BlkRead, BlkWrite: st.BlkWrite,
			}
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return
	}

	var batch []Sample
	var latest []ExpositionSample
	for _, r := range results {
		if r == nil {
			continue
		}
		batch = append(batch, *r)
		latest = append(latest, ExpositionSample{
			Project: r.Project, Service: r.Service, Container: r.Container, CPU: r.CPU,
			Mem: r.Mem, MemLimit: r.MemLimit, NetRx: r.NetRx, NetTx: r.NetTx, BlkRead: r.BlkRead, BlkWrite: r.BlkWrite,
		})
	}

	var werr error
	if len(batch) > 0 {
		werr = s.store.InsertBatch(ctx, batch)
	}
	failed := werr != nil && ctx.Err() == nil // a write cut short by shutdown is not a failure
	s.mu.Lock()
	s.latest = latest
	if len(batch) > 0 && (werr == nil || failed) {
		s.degraded = failed
	}
	warn := failed && now.Sub(s.lastWarn) >= warnEvery
	if warn {
		s.lastWarn = now
	}
	perProject := map[string]int{}
	for _, m := range batch {
		perProject[m.Project]++
	}
	// A subscriber is dropped only when it has no room for this tick's whole
	// burst, i.e. it did not drain what earlier ticks sent.
	for project, n := range perProject {
		for sub := range s.subs[project] {
			if cap(sub.ch)-len(sub.ch) < n {
				s.dropLocked(project, sub)
			}
		}
	}
	for _, m := range batch {
		for sub := range s.subs[m.Project] {
			sub.ch <- m
		}
	}
	s.mu.Unlock()
	if warn {
		s.log.Error("metrics: write failed, dropping samples", "err", werr)
	}
}

func (s *Sampler) prune(ctx context.Context) {
	now := s.now()
	s.lastPrune = now
	if _, err := s.store.Prune(ctx, now.Add(-s.retention)); err != nil && ctx.Err() == nil {
		s.log.Warn("metrics: prune failed", "err", err)
	}
}

// statsTimeout bounds one stats call: the interval, but within [3s, 5s].
func statsTimeout(interval time.Duration) time.Duration {
	return max(minStatsTimeout, min(maxStatsTimeout, interval))
}
