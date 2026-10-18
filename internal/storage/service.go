package storage

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/stacks"
	"github.com/rcss-labs/stackstead/internal/stale"
)

var dfKinds = []engine.DFKind{engine.DFImages, engine.DFContainers, engine.DFVolumes, engine.DFBuildCache}

// InventoryEngine contains only the cheap lists and bounded disk-usage calls.
type InventoryEngine interface {
	Images(context.Context) ([]engine.Image, error)
	Volumes(context.Context) ([]engine.Volume, error)
	Containers(context.Context) ([]engine.Container, error)
	DiskUsage(context.Context, engine.DFKind) (engine.DFSnapshot, error)
}
type StackRegistry interface {
	List(context.Context) ([]stacks.Entry, error)
}
type DeclarationSource interface {
	Get(context.Context, stacks.Entry) StackDeclarations
}

// ServiceConfig wires one shared inventory owner. Timeouts default to ten
// seconds for lists and sixty seconds for df. Now defaults to time.Now.
type ServiceConfig struct {
	Engine                 InventoryEngine
	Registry               StackRegistry
	Declarations           DeclarationSource
	Now                    func() time.Time
	ListTimeout, DFTimeout time.Duration
}
type dfFlight struct {
	done     chan struct{}
	snapshot engine.DFSnapshot
	err      error
}
type dfSlot struct {
	reading            DFReading
	checked            time.Time
	attempted, invalid bool
	flight             *dfFlight
}

// Service shares cheap lists and per-kind disk usage across all consumers.
// Returned inputs and snapshots are shared read-only values.
type Service struct {
	cfg        ServiceConfig
	lists      *stale.Value[Inputs]
	mu         sync.Mutex
	df         map[engine.DFKind]*dfSlot
	discovered bool
	full       bool
	bootstrap  *dfFlight
	pending    map[engine.DFKind]bool
	refreshAt  time.Time
	refreshed  bool
}

func NewService(cfg ServiceConfig) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.ListTimeout <= 0 {
		cfg.ListTimeout = 10 * time.Second
	}
	if cfg.DFTimeout <= 0 {
		cfg.DFTimeout = 60 * time.Second
	}
	s := &Service{cfg: cfg, lists: stale.New[Inputs](cfg.Now), df: map[engine.DFKind]*dfSlot{}, pending: map[engine.DFKind]bool{}}
	for _, k := range dfKinds {
		s.df[k] = &dfSlot{}
	}
	return s
}

// Inputs returns shared cheap lists, waiting only for their detached flight.
// Neither caller cancellation nor subprocess termination grace extends the
// request's deadline or the shared list budget.
func (s *Service) Inputs(ctx context.Context) (Inputs, error) {
	result := make(chan struct {
		in  Inputs
		err error
	}, 1)
	go func() {
		in, err := s.lists.Get(context.Background(), 2*time.Second, func(context.Context) (Inputs, error) {
			loadCtx, cancel := context.WithTimeout(context.Background(), s.cfg.ListTimeout)
			defer cancel()
			done := make(chan struct {
				in  Inputs
				err error
			}, 1)
			go func() {
				in, err := s.loadLists(loadCtx)
				done <- struct {
					in  Inputs
					err error
				}{in, err}
			}()
			select {
			case v := <-done:
				return v.in, v.err
			case <-loadCtx.Done():
				return Inputs{}, loadCtx.Err()
			}
		})
		result <- struct {
			in  Inputs
			err error
		}{in, err}
	}()
	select {
	case r := <-result:
		return r.in, r.err
	case <-ctx.Done():
		return Inputs{}, ctx.Err()
	}
}
func (s *Service) loadLists(ctx context.Context) (Inputs, error) {
	var in Inputs
	var err error
	if in.Images, err = s.cfg.Engine.Images(ctx); err != nil {
		return in, err
	}
	if in.Volumes, err = s.cfg.Engine.Volumes(ctx); err != nil {
		return in, err
	}
	if in.Containers, err = s.cfg.Engine.Containers(ctx); err != nil {
		return in, err
	}
	in.RegisteredProjects = map[string]string{}
	in.Stacks = []StackDeclarations{}
	if s.cfg.Registry == nil {
		return in, nil
	}
	entries, err := s.cfg.Registry.List(ctx)
	if err != nil {
		return in, err
	}
	for _, e := range entries {
		in.RegisteredProjects[e.Project] = e.Name
		d := StackDeclarations{Name: e.Name, Project: e.Project}
		if s.cfg.Declarations != nil {
			d = s.cfg.Declarations.Get(ctx, e)
		}
		// The declaration cache deliberately does not retain live state.
		d.State = "stopped"
		for _, c := range in.Containers {
			if c.Project() == e.Project && c.State == "running" {
				d.State = "running"
				break
			}
		}
		in.Stacks = append(in.Stacks, d)
	}
	return in, nil
}
func (s *Service) Inventory(ctx context.Context) (Inventory, error) {
	in, err := s.Inputs(ctx)
	if err != nil {
		return Inventory{}, err
	}
	in.DF = s.DFReadings()
	return Classify(in), nil
}

// DFReadings returns immediately, starting background loads for due kinds.
func (s *Service) DFReadings() map[engine.DFKind]DFReading {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range dfKinds {
		s.ensureLocked(k, false)
	}
	out := map[engine.DFKind]DFReading{}
	for _, k := range dfKinds {
		r := s.df[k].reading
		r.Refreshing = s.df[k].flight != nil || (s.bootstrap != nil)
		out[k] = r
	}
	return out
}
func (s *Service) ensureLocked(kind engine.DFKind, fresh bool) *dfFlight {
	slot := s.df[kind]
	if slot.flight != nil {
		return slot.flight
	}
	if !fresh && !slot.invalid && slot.attempted && s.cfg.Now().Sub(slot.checked) < 5*time.Minute {
		return nil
	}
	if s.full {
		if s.bootstrap != nil {
			return s.bootstrap
		}
		for _, target := range s.df {
			target.invalid = false
		}
		f := s.startLocked(engine.DFImages)
		s.bootstrap = f
		return f
	}
	if !s.discovered {
		if !fresh && s.bootstrap == nil {
			first := s.df[engine.DFImages]
			if !slot.invalid && first.attempted && s.cfg.Now().Sub(first.checked) < 5*time.Minute {
				return nil
			}
		}
		s.pending[kind] = true
		if s.bootstrap != nil {
			return s.bootstrap
		}
		for _, target := range s.df {
			target.invalid = false
		}
		f := s.startLocked(engine.DFImages)
		s.bootstrap = f
		return f
	}
	return s.startLocked(kind)
}
func (s *Service) startLocked(kind engine.DFKind) *dfFlight {
	f := &dfFlight{done: make(chan struct{})}
	slot := s.df[kind]
	slot.flight = f
	slot.invalid = false
	go s.loadDF(kind, f)
	return f
}
func (s *Service) loadDF(kind engine.DFKind, f *dfFlight) {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.DFTimeout)
	defer cancel()
	snap, err := s.cfg.Engine.DiskUsage(ctx, kind)
	if err == nil {
		found := false
		for _, k := range snap.Kinds {
			found = found || k == kind
		}
		if !found {
			err = errors.New("disk usage response missing requested kind")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f.snapshot, f.err = snap, err
	at := s.cfg.Now()
	slot := s.df[kind]
	slot.flight = nil
	slot.checked = at
	slot.attempted = true
	if err != nil {
		slot.reading.Error = err.Error()
		if s.bootstrap == f {
			for _, target := range s.df {
				target.reading.Error = err.Error()
				target.checked = at
				target.attempted = true
			}
		}
	} else {
		for _, k := range snap.Kinds {
			if target := s.df[k]; target != nil {
				target.reading = DFReading{Snapshot: snap, LoadedAt: at}
				target.checked = at
				target.attempted = true
			}
		}
	}
	if s.bootstrap == f {
		s.bootstrap = nil
		if err == nil {
			s.discovered = true
			s.full = len(snap.Kinds) == len(dfKinds)
			if !s.full {
				// A failed discovery reported an error for every kind. Those kinds
				// have not had their own typed attempt yet.
				for k, target := range s.df {
					if k != kind {
						target.attempted = false
					}
				}
			}
		}
		pending := s.pending
		s.pending = map[engine.DFKind]bool{}
		if s.discovered {
			for k := range pending {
				s.ensureLocked(k, false)
			}
		}
	}
	close(f.done)
}

// FreshDF shares an existing load or starts a fresh sixty-second reading. A
// canceled waiter leaves the shared load running, and successful results are
// published before waiters return.
func (s *Service) FreshDF(ctx context.Context, kind engine.DFKind) (engine.DFSnapshot, error) {
	s.mu.Lock()
	if s.df[kind] == nil {
		s.mu.Unlock()
		return engine.DFSnapshot{}, errors.New("invalid disk usage kind")
	}
	f := s.ensureLocked(kind, true)
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return engine.DFSnapshot{}, ctx.Err()
	case <-f.done:
	}
	if f.err != nil {
		return engine.DFSnapshot{}, f.err
	}
	for _, k := range f.snapshot.Kinds {
		if k == kind {
			return f.snapshot, nil
		}
	}
	// Typed discovery may have already started or finished this category.
	s.mu.Lock()
	next := s.ensureLocked(kind, false)
	r := s.df[kind].reading
	s.mu.Unlock()
	if next == nil {
		if r.Error != "" {
			return engine.DFSnapshot{}, errors.New(r.Error)
		}
		return r.Snapshot, nil
	}
	select {
	case <-ctx.Done():
		return engine.DFSnapshot{}, ctx.Err()
	case <-next.done:
		return next.snapshot, next.err
	}
}

// Refresh invalidates every usage kind. A nonzero duration is the remaining
// wait until another manual refresh is accepted. It does not wait for df.
func (s *Service) Refresh() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.cfg.Now()
	if s.refreshed && now.Sub(s.refreshAt) < time.Minute {
		return time.Minute - now.Sub(s.refreshAt)
	}
	s.refreshAt = now
	s.refreshed = true
	for _, slot := range s.df {
		slot.invalid = true
	}
	return 0
}

// VolumeSummary uses exactly the volume cache used by inventory and plans.
// The first call may also start image discovery before the volume load.
func (s *Service) VolumeSummary() (engine.VolumeUsage, bool) {
	s.mu.Lock()
	s.ensureLocked(engine.DFVolumes, false)
	r := s.df[engine.DFVolumes].reading
	s.mu.Unlock()
	if r.LoadedAt.IsZero() {
		return engine.VolumeUsage{}, false
	}
	u := engine.VolumeUsage{Count: len(r.Snapshot.Volumes)}
	for _, v := range r.Snapshot.Volumes {
		if v.UsageData != nil {
			if v.UsageData.Size >= 0 {
				u.Size += uint64(v.UsageData.Size)
			}
			if v.UsageData.RefCount == 0 {
				u.Dangling++
			}
		}
	}
	return u, true
}
