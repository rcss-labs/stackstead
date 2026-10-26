package backup

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/rcss-labs/stackstead/internal/stacks"
)

// configRetryDelay is how long the scheduler waits before retrying a
// configuration it could not read.
const configRetryDelay = time.Minute

const scheduleTrigger = "schedule"

// Scheduler starts a backup at the configured time. It rereads the
// configuration at startup and whenever Reload is signalled.
type Scheduler struct {
	Runner *Runner
	Config *ConfigStore
	// Now and After default to time.Now and time.After; tests replace them.
	Now    func() time.Time
	After  func(time.Duration) <-chan time.Time
	Loc    *time.Location
	Logger *slog.Logger

	reloadOnce sync.Once
	reload     chan struct{}
}

func (s *Scheduler) reloadCh() chan struct{} {
	s.reloadOnce.Do(func() { s.reload = make(chan struct{}, 1) })
	return s.reload
}

// Reload tells Run to reread the configuration. It never blocks, and signals
// sent before Run looks again collapse into one.
func (s *Scheduler) Reload() {
	select {
	case s.reloadCh() <- struct{}{}:
	default:
	}
}

// Run schedules backups until ctx ends.
func (s *Scheduler) Run(ctx context.Context) {
	now, after, loc, logger := s.Now, s.After, s.Loc, s.Logger
	if now == nil {
		now = time.Now
	}
	if after == nil {
		after = time.After
	}
	if loc == nil {
		loc = time.Local
	}
	if logger == nil {
		logger = slog.Default()
	}
	reload := s.reloadCh()

	// cursor keeps a run that fires early (or a clock that has not advanced)
	// from being scheduled a second time for the same slot.
	var cursor time.Time
	for {
		cfg, err := s.Config.Load(ctx)
		var wait <-chan time.Time // nil: wait for a reload
		var next time.Time
		switch {
		case err != nil:
			logger.Error("backup schedule: read configuration", "err", err)
			wait = after(configRetryDelay)
		default:
			from := now()
			if cursor.After(from) {
				from = cursor
			}
			var ok bool
			if next, ok = NextRun(cfg.Schedule, from, loc); ok {
				wait = after(next.Sub(now()))
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-reload:
			cursor = time.Time{}
		case <-wait:
			if err != nil {
				continue
			}
			cursor = next
			s.fire(ctx, logger)
		}
	}
}

func (s *Scheduler) fire(ctx context.Context, logger *slog.Logger) {
	_, err := s.Runner.Start(ctx, scheduleTrigger, "")
	switch {
	case err == nil:
	case errors.Is(err, stacks.ErrBusy):
		logger.Warn("backup skipped: previous run still in progress")
	default:
		logger.Error("scheduled backup could not start", "err", err)
	}
}
