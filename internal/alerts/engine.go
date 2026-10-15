package alerts

import (
	"context"
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
	now        func() time.Time

	mu       sync.Mutex
	cached   []Alert
	expires  time.Time
	inflight *flight
	seen     map[string]time.Time // alert key -> first observed
}

type flight struct {
	done   chan struct{}
	alerts []Alert
	err    error
}

func NewEngine(st StackDetails, cs ContainerSource, rc RestartCounts, conn Connectivity, mh MetricsHealth, now func() time.Time) *Engine {
	return &Engine{stacks: st, containers: cs, restarts: rc, conn: conn, metrics: mh, now: now, seen: map[string]time.Time{}}
}

// Alerts returns the current alerts, most urgent first. Concurrent calls share
// one computation and the result is reused for 5 s. It fails only when the
// stack registry cannot be read; failures are not cached.
func (e *Engine) Alerts(ctx context.Context) ([]Alert, error) {
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

	f.alerts, f.err = e.compute(ctx)

	e.mu.Lock()
	e.inflight = nil
	if f.err == nil {
		e.cached, e.expires = f.alerts, e.now().Add(cacheTTL)
	}
	e.mu.Unlock()
	close(f.done)
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
	// A container error leaves the container-based rules silent; the
	// disconnected alert and file-based alerts still apply.
	containers, _ := e.containers.Containers(ctx)
	var restarts map[string]int
	if e.restarts != nil {
		restarts = e.restarts.RestartCounts()
	}
	out := Evaluate(details, containers, restarts, e.conn.Connected(), e.metrics.Degraded())
	if out == nil {
		out = []Alert{}
	}

	now := e.now()
	e.mu.Lock()
	defer e.mu.Unlock()
	current := make(map[string]time.Time, len(out))
	for i := range out {
		k := key(out[i])
		since, ok := e.seen[k]
		if !ok {
			since = now
		}
		current[k] = since
		out[i].Since = since
	}
	e.seen = current // keys absent from this computation are forgotten
	sortAlerts(out)
	return out, nil
}

// key identifies an alert by names, not ids, so a recreated container keeps it.
func key(a Alert) string {
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
		if ka, kb := key(a), key(b); ka != kb {
			return ka < kb
		}
		return a.Message < b.Message
	})
}
