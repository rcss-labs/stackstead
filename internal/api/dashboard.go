package api

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/rcss-labs/stackstead/internal/alerts"
	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/host"
	"github.com/rcss-labs/stackstead/internal/metrics"
)

// HostAPI is satisfied by *host.Collector, which caches for 5 s itself.
type HostAPI interface {
	Snapshot(ctx context.Context) host.Snapshot
}

// InterfacesAPI is satisfied by host.InterfaceLister.
type InterfacesAPI interface {
	Interfaces() ([]host.Interface, error)
}

// AlertsAPI is satisfied by *alerts.Engine, which caches for 5 s itself.
type AlertsAPI interface {
	Alerts(ctx context.Context) ([]alerts.Alert, error)
}

// SummaryAPI is the metrics store's all-projects query plus the sampler's
// latest per-container readings.
type SummaryAPI interface {
	QueryAll(ctx context.Context, from, to time.Time, step time.Duration) ([]metrics.ProjectBucket, error)
	Latest() []metrics.ExpositionSample
}

// SummarySources adapts the store and sampler to SummaryAPI.
type SummarySources struct {
	Store   *metrics.Store
	Sampler *metrics.Sampler
}

var _ SummaryAPI = SummarySources{}

func (s SummarySources) QueryAll(ctx context.Context, from, to time.Time, step time.Duration) ([]metrics.ProjectBucket, error) {
	return s.Store.QueryAll(ctx, from, to, step)
}
func (s SummarySources) Latest() []metrics.ExpositionSample { return s.Sampler.Latest() }

// summaryEntry is the single-flight cache slot of one window. mu guards the
// fields; the query itself runs outside it, shared through inflight.
type summaryEntry struct {
	mu       sync.Mutex
	at       time.Time
	body     apitypes.MetricsSummary
	ok       bool
	inflight *summaryFlight
}

type summaryFlight struct {
	done chan struct{}
	body apitypes.MetricsSummary
	err  error
}

const (
	summaryBuckets  = 30
	summaryCacheTTL = 10 * time.Second
	// summaryQueryTimeout bounds the shared store query.
	summaryQueryTimeout = 10 * time.Second
	summaryTopN         = 5
)

func (s *server) registerDashboardRoutes(mux *http.ServeMux) {
	if s.opts.Interfaces != nil {
		mux.Handle("GET /api/host/interfaces", s.requireSession(s.handleHostInterfaces))
	}
	if s.opts.Host != nil {
		mux.Handle("GET /api/host", s.requireSession(s.handleHost))
	}
	if s.opts.Alerts != nil {
		mux.Handle("GET /api/alerts", s.requireSession(s.handleAlerts))
	}
	if s.opts.Summary != nil {
		mux.Handle("GET /api/metrics/summary", s.requireSession(s.handleMetricsSummary))
	}
}

func (s *server) handleHost(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, hostDTO(s.opts.Host.Snapshot(r.Context())))
}

func (s *server) handleHostInterfaces(w http.ResponseWriter, r *http.Request) {
	list, err := s.opts.Interfaces.Interfaces()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if list == nil {
		list = []host.Interface{}
	}
	writeJSON(w, http.StatusOK, list)
}

// hostDTO maps section by section so a field added to host.Snapshot cannot
// reach the wire without a deliberate change here.
func hostDTO(h host.Snapshot) apitypes.HostSnapshot {
	out := apitypes.HostSnapshot{Hostname: h.Hostname, OS: h.OS, Source: h.Source, SampledAt: h.SampledAt.UTC()}
	if c := h.Containers; c != nil {
		out.Containers = &apitypes.HostContainers{Total: c.Total, Running: c.Running, Exited: c.Exited, Bad: c.Bad}
	}
	if c := h.CPU; c != nil {
		out.CPU = &apitypes.HostCPU{Percent: c.Percent, Cores: c.Cores}
	}
	if m := h.Memory; m != nil {
		out.Memory = &apitypes.HostMemory{Used: m.Used, Total: m.Total, SwapUsed: m.SwapUsed, SwapTotal: m.SwapTotal}
	}
	if d := h.Disk; d != nil {
		out.Disk = &apitypes.HostDisk{Path: d.Path, Free: d.Free, Total: d.Total}
	}
	if n := h.Network; n != nil {
		out.Network = &apitypes.HostNetwork{RxBytesPerSec: n.RxBytesPerSec, TxBytesPerSec: n.TxBytesPerSec, Interface: n.Interface}
	}
	if d := h.DiskIO; d != nil {
		out.DiskIO = &apitypes.HostDiskIO{ReadBytesPerSec: d.ReadBytesPerSec, WriteBytesPerSec: d.WriteBytesPerSec}
	}
	if v := h.Volumes; v != nil {
		out.Volumes = &apitypes.HostVolumes{Count: v.Count, Size: v.Size, Dangling: v.Dangling}
	}
	if u := h.Uptime; u != nil {
		out.Uptime = &apitypes.HostUptime{Seconds: u.Seconds, Load1: u.Load1, Load5: u.Load5, Load15: u.Load15}
	}
	if rs := h.Restarts24h; rs != nil {
		top := make([]apitypes.HostRestartTop, 0, len(rs.Top))
		for _, t := range rs.Top {
			top = append(top, apitypes.HostRestartTop{Container: t.Container, Service: t.Service, Project: t.Project, Count: t.Count})
		}
		out.Restarts24h = &apitypes.HostRestarts{Total: rs.Total, Top: top}
	}
	return out
}

func (s *server) handleAlerts(w http.ResponseWriter, r *http.Request) {
	list, err := s.opts.Alerts.Alerts(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]apitypes.Alert, 0, len(list)) // [] rather than null when empty
	for _, a := range list {
		out = append(out, apitypes.Alert{Kind: a.Kind, Severity: a.Severity, Stack: a.Stack,
			Service: a.Service, Container: a.Container, Message: a.Message, Since: a.Since.UTC()})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) handleMetricsSummary(w http.ResponseWriter, r *http.Request) {
	window, err := metrics.ParseSummaryWindow(r.URL.Query().Get("window"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	body, err := s.summary(r.Context(), window)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, body)
}

// summary returns the window's body from the 10 s cache, computing it under
// the entry lock so concurrent requests share one query. Errors are not cached.
func (s *server) summary(ctx context.Context, window time.Duration) (apitypes.MetricsSummary, error) {
	s.summaryMu.Lock()
	e := s.summaryCache[window]
	if e == nil {
		e = &summaryEntry{}
		s.summaryCache[window] = e
	}
	s.summaryMu.Unlock()

	e.mu.Lock()
	now := s.opts.Now()
	if e.ok && now.Sub(e.at) < summaryCacheTTL {
		body := e.body
		e.mu.Unlock()
		return body, nil
	}
	f := e.inflight
	if f == nil {
		f = &summaryFlight{done: make(chan struct{})}
		e.inflight = f
		go s.runSummary(ctx, e, f, window, now)
	}
	e.mu.Unlock()

	// Every caller, the one that started the query included, waits on its own
	// context, so a cancelled request neither blocks nor fails the others.
	select {
	case <-f.done:
		return f.body, f.err
	case <-ctx.Done():
		return apitypes.MetricsSummary{}, ctx.Err()
	}
}

// runSummary runs the shared query detached from any request (bounded by
// summaryQueryTimeout) and publishes the result. Errors are not cached.
func (s *server) runSummary(ctx context.Context, e *summaryEntry, f *summaryFlight, window time.Duration, now time.Time) {
	qctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), summaryQueryTimeout)
	defer cancel()
	// A panicking query is an error for this flight, never a crashed server or
	// a stranded waiter; it is not cached.
	defer func() {
		if r := recover(); r != nil {
			f.body, f.err = apitypes.MetricsSummary{}, fmt.Errorf("summary query panicked: %v", r)
		}
		e.mu.Lock()
		e.inflight = nil
		if f.err == nil {
			e.body, e.at, e.ok = f.body, now, true
		}
		e.mu.Unlock()
		close(f.done)
	}()
	f.body, f.err = s.computeSummary(qctx, window, now)
}

func (s *server) computeSummary(ctx context.Context, window time.Duration, now time.Time) (apitypes.MetricsSummary, error) {
	step := window / summaryBuckets
	// QueryAll buckets are epoch-aligned, so the range must be too: it ends
	// with the bucket that contains now and starts window earlier.
	to := now.Truncate(step).Add(step)
	from := to.Add(-window)
	rows, err := s.opts.Summary.QueryAll(ctx, from, to, step)
	if err != nil {
		return apitypes.MetricsSummary{}, err
	}
	stacks := map[string]apitypes.ProjectSummary{}
	for project, series := range metrics.AggregateProjects(rows, from, step, summaryBuckets) {
		stacks[project] = apitypes.ProjectSummary{CPU: series.CPU, Mem: series.Mem,
			CPUNow: series.CPUNow, MemNow: series.MemNow, NetRxBytesPerSec: series.NetRx, NetTxBytesPerSec: series.NetTx}
	}
	latest := s.opts.Summary.Latest()
	return apitypes.MetricsSummary{
		Window: windowLabel(window), Step: int(step / time.Second), From: from.UTC(), To: to.UTC(), Stacks: stacks,
		Top: apitypes.SummaryTop{
			CPU: topEntries(latest, func(m metrics.ExpositionSample) float64 { return m.CPU }),
			Mem: topEntries(latest, func(m metrics.ExpositionSample) float64 { return float64(m.Mem) }),
		},
	}, nil
}

func windowLabel(window time.Duration) string {
	switch window {
	case 5 * time.Minute:
		return "5m"
	case time.Hour:
		return "1h"
	}
	return "15m"
}

// topEntries returns the highest summaryTopN containers by value, ties
// broken by container name so the order is stable.
func topEntries(samples []metrics.ExpositionSample, value func(metrics.ExpositionSample) float64) []apitypes.TopEntry {
	sorted := append([]metrics.ExpositionSample(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool {
		if vi, vj := value(sorted[i]), value(sorted[j]); vi != vj {
			return vi > vj
		}
		return sorted[i].Container < sorted[j].Container
	})
	if len(sorted) > summaryTopN {
		sorted = sorted[:summaryTopN]
	}
	out := make([]apitypes.TopEntry, 0, len(sorted))
	for _, m := range sorted {
		out = append(out, apitypes.TopEntry{Container: m.Container, Service: m.Service, Project: m.Project, Value: value(m)})
	}
	return out
}
