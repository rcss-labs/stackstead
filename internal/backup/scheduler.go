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

const (
	// maxWaitStep bounds one timer so the wall clock is rechecked often.
	maxWaitStep = time.Minute
	// missedSlotTolerance is how late a slot may be and still run.
	missedSlotTolerance = 2 * time.Minute
)

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

	for {
		cfg, err := s.Config.Load(ctx)
		if err != nil {
			logger.Error("backup schedule: read configuration", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-reload:
			case <-after(configRetryDelay):
			}
			continue
		}
		// NextRun is strictly after now, so a slot that just fired is not
		// found again.
		next, ok := NextRun(cfg.Schedule, now(), loc)
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-reload:
			}
			continue
		}
		switch s.waitUntil(ctx, next, now, after) {
		case waitCanceled:
			return
		case waitReloaded:
			continue
		}
		// A suspend or clock jump can wake us long after the slot; the
		// schedule is not caught up.
		if late := now().Sub(next); late > missedSlotTolerance {
			logger.Warn("backup slot missed", "slot", next, "late", late.Round(time.Second))
			continue
		}
		s.fire(ctx, logger)
	}
}

type waitResult int

const (
	waitDue waitResult = iota
	waitReloaded
	waitCanceled
)

// waitUntil returns when the wall clock reaches target. It waits in short
// steps and rechecks the clock each time, because one long timer does not
// follow a wall-clock jump or a suspend.
func (s *Scheduler) waitUntil(ctx context.Context, target time.Time, now func() time.Time, after func(time.Duration) <-chan time.Time) waitResult {
	for {
		remaining := target.Sub(now())
		if remaining <= 0 {
			return waitDue
		}
		select {
		case <-ctx.Done():
			return waitCanceled
		case <-s.reloadCh():
			return waitReloaded
		case <-after(min(remaining, maxWaitStep)):
		}
	}
}

func (s *Scheduler) fire(ctx context.Context, logger *slog.Logger) {
	_, err := s.Runner.Start(ctx, scheduleTrigger, "")
	switch {
	case err == nil:
	case errors.Is(err, stacks.ErrShutdown) || ctx.Err() != nil:
		// The server is stopping; nothing to report.
	case errors.Is(err, stacks.ErrBusy):
		logger.Warn("backup skipped: previous run still in progress")
	default:
		logger.Error("scheduled backup could not start", "err", err)
	}
}
