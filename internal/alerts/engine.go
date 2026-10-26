package alerts

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// cacheTTL bounds how often alerts are recomputed.
const cacheTTL = 5 * time.Second

// Engine computes alerts on request, caching the result and remembering when
// each alert was first seen. Nothing is persisted, so "since" restarts with
// the process.
type Engine struct {
	stacks     StackDetails
	containers ContainerSource
	restarts   RestartCounts
	conn       Connectivity
	metrics    MetricsHealth
	backups    BackupHealth // nil: no backup alerts
	now        func() time.Time

	mu       sync.Mutex
	cached   []Alert
	expires  time.Time
	inflight *flight
	seen     map[string]seenEntry // alert key -> first observation
}

type seenEntry struct {
	since         time.Time
	fromContainer bool
}

// computeTimeout bounds the shared computation, which runs detached from any
// one request.
const computeTimeout = 10 * time.Second

type flight struct {
	done   chan struct{}
	alerts []Alert
	err    error
}

func NewEngine(st StackDetails, cs ContainerSource, rc RestartCounts, conn Connectivity, mh MetricsHealth, now func() time.Time, bh BackupHealth) *Engine {
	return &Engine{stacks: st, containers: cs, restarts: rc, conn: conn, metrics: mh, backups: bh, now: now, seen: map[string]seenEntry{}}
}

// Alerts returns the current alerts, most urgent first. Concurrent calls share
// one computation and the result is reused for 5 s. It fails only when the
// stack registry cannot be read; failures are not cached.
func (e *Engine) Alerts(ctx context.Context) (out []Alert, err error) {
	e.mu.Lock()
	if e.cached != nil && e.now().Before(e.expires) {
		out := append([]Alert{}, e.cached...)
		e.mu.Unlock()
		return out, nil
	}
	if f := e.inflight; f != nil {
		e.mu.Unlock()
		select {
		case <-f.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if f.err != nil {
			return nil, f.err
		}
		return append([]Alert{}, f.alerts...), nil
	}
	f := &flight{done: make(chan struct{})}
	e.inflight = f
	e.mu.Unlock()

	// Detached so a cancelled leader request cannot fail the joiners.
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), computeTimeout)
	defer cancel()

	// Always release the flight, even if compute panics: a recovered panic is
	// an error for this computation, never cached, and joiners are not stranded.
	defer func() {
		if r := recover(); r != nil {
			f.alerts, f.err = nil, fmt.Errorf("alerts computation panicked: %v", r)
			out, err = nil, f.err
		}
		e.mu.Lock()
		e.inflight = nil
		if f.err == nil {
			e.cached, e.expires = f.alerts, e.now().Add(cacheTTL)
		}
		e.mu.Unlock()
		close(f.done)
	}()
	f.alerts, f.err = e.compute(cctx)
	if f.err != nil {
		return nil, f.err
	}
	return append([]Alert{}, f.alerts...), nil
}

func (e *Engine) compute(ctx context.Context) ([]Alert, error) {
	details, err := e.stacks.Details(ctx)
	if err != nil {
		return nil, err
	}
	containers, contErr := e.containers.Containers(ctx)
	if contErr != nil {
		containers = nil // never judge container rules on a failed listing
	}
	var restarts map[string]int
	if e.restarts != nil {
		restarts = e.restarts.RestartCounts()
	}
	items := evaluate(details, containers, restarts, e.conn.Connected(), e.metrics.Degraded())
	items = append(items, e.backupAlerts(ctx)...)

	now := e.now()
	e.mu.Lock()
	defer e.mu.Unlock()
	current := make(map[string]seenEntry, len(items))
	out := make([]Alert, len(items))
	for i, it := range items {
		k := it.key()
		since := now
		if old, ok := e.seen[k]; ok {
			since = old.since
		}
		if !it.startedAt.IsZero() {
			since = it.startedAt
		}
		current[k] = seenEntry{since, it.fromContainer}
		out[i] = it.Alert
		out[i].Since = since
	}
	if contErr != nil {
		// Without a listing the container-derived alerts cannot be judged;
		// keep their first-seen times so a disconnect does not reset them.
		for k, old := range e.seen {
			if _, ok := current[k]; !ok && old.fromContainer {
				current[k] = old
			}
		}
	}
	e.seen = current // otherwise keys absent from this computation are forgotten
	sortAlerts(out)
	return out, nil
}

// sortKey is only a tie-breaker; Message separates alerts that share it.
func sortKey(a Alert) string {
	return strings.Join([]string{a.Kind, a.Stack, a.Service, a.Container}, "|")
}

func severityRank(s string) int {
	switch s {
	case SeverityDanger:
		return 0
	case SeverityWarning:
		return 1
	}
	return 2
}

// sortAlerts orders by severity, then newest first, then key and message so
// the order is stable between computations.
func sortAlerts(as []Alert) {
	sort.SliceStable(as, func(i, j int) bool {
		a, b := as[i], as[j]
		if ra, rb := severityRank(a.Severity), severityRank(b.Severity); ra != rb {
			return ra < rb
		}
		if !a.Since.Equal(b.Since) {
			return a.Since.After(b.Since)
		}
		if ka, kb := sortKey(a), sortKey(b); ka != kb {
			return ka < kb
		}
		return a.Message < b.Message
	})
}
