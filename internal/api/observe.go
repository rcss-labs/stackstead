package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/metrics"
	"github.com/rcss-labs/stackstead/internal/stacks"
	"github.com/rcss-labs/stackstead/internal/state"
)

// ObserveAPI is everything the observability routes need from the status
// cache, the metrics sampler and store, and the Docker log source. Method
// names are distinct (the cache and the sampler both have Subscribe), so one
// value, ObserveSources in production, implements all of it.
type ObserveAPI interface {
	// Containers has the contract of state.Cache.Containers: an error
	// wrapping engine.ErrUnavailable while the daemon is unreachable.
	Containers(ctx context.Context) ([]engine.Container, error)
	Connected() bool
	// SubscribeChanges has the contract of state.Cache.Subscribe.
	SubscribeChanges() (<-chan state.Change, func())
	// Logs has the contract of engine.Client.Logs. The container is passed
	// whole so an implementation can choose the TTY mode when it knows it.
	Logs(ctx context.Context, c engine.Container, opts engine.LogOptions) (<-chan engine.LogLine, <-chan error)
	// SubscribeSamples has the contract of metrics.Sampler.Subscribe.
	SubscribeSamples(project string) (<-chan metrics.Sample, func())
	Latest() []metrics.ExpositionSample
	Degraded() bool
	Query(ctx context.Context, project, service string, from, to time.Time, step time.Duration) ([]metrics.Point, error)
	TokenHash(ctx context.Context) (string, error)
	SetTokenHash(ctx context.Context, hash string) error
}

// LogSource is the part of *engine.Client that streams logs.
type LogSource interface {
	Logs(ctx context.Context, containerID string, opts engine.LogOptions) (<-chan engine.LogLine, <-chan error)
}

// ObserveSources adapts the production components to ObserveAPI.
type ObserveSources struct {
	Cache   *state.Cache
	Sampler *metrics.Sampler
	Store   *metrics.Store
	Engine  LogSource
}

var _ ObserveAPI = ObserveSources{}

func (o ObserveSources) Containers(ctx context.Context) ([]engine.Container, error) {
	return o.Cache.Containers(ctx)
}
func (o ObserveSources) Connected() bool { return o.Cache.Connected() }
func (o ObserveSources) SubscribeChanges() (<-chan state.Change, func()) {
	return o.Cache.Subscribe()
}

// Logs requests multiplexed framing because the cache does not record whether
// a container has a TTY. engine.Logs recognises a raw TTY stream from its first
// bytes and reads it raw, so both kinds work without an inspect call.
func (o ObserveSources) Logs(ctx context.Context, c engine.Container, opts engine.LogOptions) (<-chan engine.LogLine, <-chan error) {
	opts.TTY = false
	return o.Engine.Logs(ctx, c.ID, opts)
}
func (o ObserveSources) SubscribeSamples(project string) (<-chan metrics.Sample, func()) {
	return o.Sampler.Subscribe(project)
}
func (o ObserveSources) Latest() []metrics.ExpositionSample { return o.Sampler.Latest() }
func (o ObserveSources) Degraded() bool                     { return o.Sampler.Degraded() }
func (o ObserveSources) Query(ctx context.Context, project, service string, from, to time.Time, step time.Duration) ([]metrics.Point, error) {
	return o.Store.Query(ctx, project, service, from, to, step)
}
func (o ObserveSources) TokenHash(ctx context.Context) (string, error) {
	return o.Store.TokenHash(ctx)
}
func (o ObserveSources) SetTokenHash(ctx context.Context, hash string) error {
	return o.Store.SetTokenHash(ctx, hash)
}

const (
	maxLogContainers   = 50
	defaultLogTail     = 200
	defaultMetricsSpan = time.Hour
	defaultMaxPoints   = 700  // default step targets at most this many buckets (+1 for alignment)
	maxMetricsPoints   = 5000 // upper bound on points per service, whatever step is requested
	maxStatusPoll      = 2 * time.Second
	maxStepSeconds     = 1e9 // about 31 years; anything larger is a typo
)

// registerObserveRoutes registers the routes that need only Observe, plus
// the stack-scoped ones when the stack service is available too. /metrics is
// deliberately outside session auth: it has its own bearer token.
func (s *server) registerObserveRoutes(mux *http.ServeMux) {
	route := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.requireSession(h)) }
	mux.HandleFunc("GET /metrics", s.handlePrometheus)
	route("GET /api/events", s.handleEvents)
	route("POST /api/settings/metrics-token", s.handleCreateMetricsToken)
	route("DELETE /api/settings/metrics-token", s.handleDeleteMetricsToken)
	if s.opts.Stacks != nil {
		route("GET /api/stacks/{name}/logs", s.handleLogs)
		route("GET /api/stacks/{name}/metrics", s.handleMetricsHistory)
		route("GET /api/stacks/{name}/metrics/live", s.handleMetricsLive)
	}
}

func (s *server) sseWriteTimeout() time.Duration {
	if s.opts.SSEWriteTimeout > 0 {
		return s.opts.SSEWriteTimeout
	}
	return defaultSSEWriteTimeout
}

// ---- logs ----

type logEventDTO struct {
	Service   string    `json:"service"`
	Container string    `json:"container"`
	Stream    string    `json:"stream"`
	Time      time.Time `json:"time"`
	Text      string    `json:"text"`
}

type logErrorDTO struct {
	Service   string `json:"service"`
	Container string `json:"container"`
	Error     string `json:"error"`
}

type logItem struct {
	container engine.Container
	line      engine.LogLine
	err       error
}

// parseTime accepts RFC3339 or Unix seconds.
func parseTime(v string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t, nil
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
		return time.Unix(n, 0), nil
	}
	return time.Time{}, errors.New("not an RFC3339 time or Unix timestamp")
}

// parseLogOptions reads tail, since and follow. since also accepts a Go
// duration ("10m") meaning that long ago.
func parseLogOptions(w http.ResponseWriter, r *http.Request) (engine.LogOptions, bool) {
	q := r.URL.Query()
	opts := engine.LogOptions{Tail: defaultLogTail}
	if v := q.Get("tail"); v != "" {
		if v == "all" {
			opts.Tail = -1
		} else if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			opts.Tail = n
		} else {
			writeError(w, http.StatusBadRequest, "tail must be a non-negative number or \"all\"")
			return opts, false
		}
	}
	if v := q.Get("since"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			opts.Since = time.Now().Add(-d)
		} else if t, err := parseTime(v); err == nil {
			opts.Since = t
		} else {
			writeError(w, http.StatusBadRequest, "since must be an RFC3339 time, a Unix timestamp or a duration like 10m")
			return opts, false
		}
	}
	if v := q.Get("follow"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "follow must be true or false")
			return opts, false
		}
		opts.Follow = b
	}
	return opts, true
}

// handleLogs streams the merged logs of a stack's containers.
//
// Events: "log" {service, container, stream, time, text} per line; "error"
// {service, container, error} when one container's stream failed (the others
// continue); "end" once every stream has finished, which for a follow request
// means every container's stream ended on its own. A stack with no containers
// (or none matching service) gets just "end". At most 50 containers (by name)
// are read. Closing the request cancels every Docker stream.
func (s *server) handleLogs(w http.ResponseWriter, r *http.Request) {
	opts, ok := parseLogOptions(w, r)
	if !ok {
		return
	}
	detail, err := s.opts.Stacks.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		s.writeStackError(w, r, err)
		return
	}
	all, err := s.opts.Observe.Containers(r.Context())
	if err != nil {
		s.writeStackError(w, r, err)
		return
	}
	service := r.URL.Query().Get("service")
	var cs []engine.Container
	for _, c := range all {
		// An empty project would otherwise match every non-compose container.
		if detail.Project != "" && c.Project() == detail.Project && (service == "" || c.ComposeService() == service) {
			cs = append(cs, c)
		}
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].Name < cs[j].Name })
	if len(cs) > maxLogContainers {
		cs = cs[:maxLogContainers]
	}

	sw, err := startSSE(w, s.sseWriteTimeout())
	if err != nil {
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel() // cancels every Docker stream and stops the pumps

	merged := make(chan logItem)
	var wg sync.WaitGroup
	for _, c := range cs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lines, errs := s.opts.Observe.Logs(ctx, c, opts)
			for l := range lines {
				select {
				case merged <- logItem{container: c, line: l}:
				case <-ctx.Done():
					return
				}
			}
			select {
			case err := <-errs: // nil once the channel is closed without an error
				if err != nil {
					select {
					case merged <- logItem{container: c, err: err}:
					case <-ctx.Done():
					}
				}
			case <-ctx.Done():
			}
		}()
	}
	go func() { wg.Wait(); close(merged) }()

	runSSE(s, r.WithContext(ctx), sw, merged, func(it logItem, ok bool) (bool, error) {
		switch {
		case !ok:
			_ = sw.Event("end", "", struct{}{})
			return true, nil
		case it.err != nil:
			msg := "log stream failed"
			if errors.Is(it.err, engine.ErrUnavailable) {
				msg = "docker engine is unavailable"
			} else {
				s.opts.Logger.Warn("log stream failed", "container", it.container.Name, "err", it.err)
			}
			return false, sw.Event("error", "", logErrorDTO{Service: it.container.ComposeService(), Container: it.container.Name, Error: msg})
		default:
			return false, sw.Event("log", "", logEventDTO{Service: it.container.ComposeService(), Container: it.container.Name,
				Stream: it.line.Stream, Time: it.line.Time, Text: it.line.Text})
		}
	})
}

// ---- metrics history ----

type metricPointDTO struct {
	Time     string  `json:"time"`
	Service  string  `json:"service"`
	CPU      float64 `json:"cpu"`
	Mem      uint64  `json:"mem"`
	MemLimit uint64  `json:"memLimit"`
	// Rates are bytes per second between this point and the service's
	// previous one; absent on a service's first point.
	NetRx    *float64 `json:"netRxBytesPerSec,omitempty"`
	NetTx    *float64 `json:"netTxBytesPerSec,omitempty"`
	BlkRead  *float64 `json:"blkReadBytesPerSec,omitempty"`
	BlkWrite *float64 `json:"blkWriteBytesPerSec,omitempty"`
}

func ceilSeconds(d time.Duration) time.Duration {
	return ((d + time.Second - 1) / time.Second) * time.Second
}

// parseStep accepts a Go duration or a bare number of seconds.
func parseStep(v string) (time.Duration, error) {
	if n, err := strconv.ParseFloat(v, 64); err == nil {
		if math.IsNaN(n) || math.IsInf(n, 0) || math.Abs(n) > maxStepSeconds {
			return 0, errors.New("step out of range")
		}
		return time.Duration(n * float64(time.Second)), nil
	}
	return time.ParseDuration(v)
}

// metricsWindow resolves from, to and step from the query. The step is always
// positive (so the store aggregates), a whole number of seconds, and large
// enough that a service gets at most maxMetricsPoints points.
func metricsWindow(r *http.Request) (from, to time.Time, step time.Duration, err error) {
	q := r.URL.Query()
	to = time.Now()
	if v := q.Get("to"); v != "" {
		if to, err = parseTime(v); err != nil {
			return from, to, 0, errors.New("to must be an RFC3339 time or Unix timestamp")
		}
	}
	from = to.Add(-defaultMetricsSpan)
	if v := q.Get("from"); v != "" {
		if from, err = parseTime(v); err != nil {
			return from, to, 0, errors.New("from must be an RFC3339 time or Unix timestamp")
		}
	}
	if !from.Before(to) {
		return from, to, 0, errors.New("from must be before to")
	}
	span := to.Sub(from)
	step = ceilSeconds(span / defaultMaxPoints)
	if v := q.Get("step"); v != "" {
		d, perr := parseStep(v)
		if perr != nil {
			return from, to, 0, errors.New("step must be a duration like 30s or a number of seconds")
		}
		step = d - d%time.Second
	}
	if minStep := ceilSeconds(span / (maxMetricsPoints - 1)); step < minStep {
		step = minStep
	}
	if step < time.Second {
		step = time.Second
	}
	return from, to, step, nil
}

func rate(cur, prev uint64, seconds float64) *float64 {
	var v float64
	if cur > prev { // a drop means the container restarted and its counter reset
		v = float64(cur-prev) / seconds
	}
	return &v
}

// handleMetricsHistory returns {points}: per service and time bucket the
// averaged gauges (cpu, mem, memLimit) and counter rates. The store keeps
// cumulative counters and buckets them with MAX across a service's replicas,
// so rates reflect the busiest replica, not the sum. A negative delta
// (container restart) is reported as 0.
func (s *server) handleMetricsHistory(w http.ResponseWriter, r *http.Request) {
	from, to, step, err := metricsWindow(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	detail, err := s.opts.Stacks.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		s.writeStackError(w, r, err)
		return
	}
	pts, err := s.opts.Observe.Query(r.Context(), detail.Project, r.URL.Query().Get("service"), from, to, step)
	if err != nil {
		s.writeStackError(w, r, err)
		return
	}
	out := make([]metricPointDTO, 0, len(pts))
	prev := map[string]metrics.Point{}
	for _, p := range pts {
		dto := metricPointDTO{Time: p.TS.UTC().Format(time.RFC3339), Service: p.Service, CPU: p.CPU, Mem: p.Mem, MemLimit: p.MemLimit}
		if last, ok := prev[p.Service]; ok {
			if dt := p.TS.Sub(last.TS).Seconds(); dt > 0 {
				dto.NetRx, dto.NetTx = rate(p.NetRx, last.NetRx, dt), rate(p.NetTx, last.NetTx, dt)
				dto.BlkRead, dto.BlkWrite = rate(p.BlkRead, last.BlkRead, dt), rate(p.BlkWrite, last.BlkWrite, dt)
			}
		}
		prev[p.Service] = p
		out = append(out, dto)
	}
	writeJSON(w, http.StatusOK, map[string]any{"points": out})
}

type sampleDTO struct {
	Time      string  `json:"time"`
	Service   string  `json:"service"`
	Container string  `json:"container"`
	CPU       float64 `json:"cpu"`
	Mem       uint64  `json:"mem"`
	MemLimit  uint64  `json:"memLimit"`
	NetRx     uint64  `json:"netRx"`
	NetTx     uint64  `json:"netTx"`
	BlkRead   uint64  `json:"blkRead"`
	BlkWrite  uint64  `json:"blkWrite"`
}

// handleMetricsLive streams "sample" events (raw per-container readings with
// cumulative counters) for the stack's project. A subscriber the sampler
// dropped for being slow gets "dropped" and the stream ends; the client
// should reconnect.
func (s *server) handleMetricsLive(w http.ResponseWriter, r *http.Request) {
	detail, err := s.opts.Stacks.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		s.writeStackError(w, r, err)
		return
	}
	samples, cancel := s.opts.Observe.SubscribeSamples(detail.Project)
	defer cancel()
	sw, err := startSSE(w, s.sseWriteTimeout())
	if err != nil {
		return
	}
	runSSE(s, r, sw, samples, func(m metrics.Sample, ok bool) (bool, error) {
		if !ok {
			_ = sw.Event("dropped", "", struct{}{})
			return true, nil
		}
		return false, sw.Event("sample", "", sampleDTO{Time: m.TS.UTC().Format(time.RFC3339), Service: m.Service, Container: m.Container, CPU: m.CPU,
			Mem: m.Mem, MemLimit: m.MemLimit, NetRx: m.NetRx, NetTx: m.NetTx, BlkRead: m.BlkRead, BlkWrite: m.BlkWrite})
	})
}

// ---- status events ----

type statusFlagsDTO struct {
	Events          string `json:"events"`
	MetricsDegraded bool   `json:"metricsDegraded"`
}

func (s *server) statusFlags() statusFlagsDTO {
	flags := statusFlagsDTO{Events: "disconnected"}
	if o := s.opts.Observe; o != nil {
		if o.Connected() {
			flags.Events = "connected"
		}
		flags.MetricsDegraded = o.Degraded()
	}
	return flags
}

type changeDTO struct {
	Project string `json:"project"`
	Seq     uint64 `json:"seq"`
}

type eventItem struct {
	change  state.Change
	poll    bool
	dropped bool
}

// handleEvents streams the status cache: "status" {events, metricsDegraded}
// once on connect and whenever either flag changes, and "change" {project, seq}
// when a project's containers changed. The cache signals connection changes
// with an empty project, which only triggers the flag check. The degraded flag
// has no notification, so it is polled (at most every 2s). A subscriber the
// cache dropped for being slow gets "dropped" and the stream ends; the client
// should reconnect and refetch.
func (s *server) handleEvents(w http.ResponseWriter, r *http.Request) {
	changes, cancel := s.opts.Observe.SubscribeChanges()
	defer cancel()
	sw, err := startSSE(w, s.sseWriteTimeout())
	if err != nil {
		return
	}
	last := s.statusFlags()
	if sw.Event("status", "", last) != nil {
		return
	}

	pollEvery := s.opts.SSEHeartbeat
	if pollEvery <= 0 || pollEvery > maxStatusPoll {
		pollEvery = maxStatusPoll
	}
	ctx, stop := context.WithCancel(r.Context())
	defer stop()
	items := make(chan eventItem)
	send := func(it eventItem) bool {
		select {
		case items <- it:
			return true
		case <-ctx.Done():
			return false
		}
	}
	go func() {
		ticker := time.NewTicker(pollEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !send(eventItem{poll: true}) {
					return
				}
			case c, ok := <-changes:
				if !ok {
					send(eventItem{dropped: true})
					return
				}
				if !send(eventItem{change: c}) {
					return
				}
			}
		}
	}()

	runSSE(s, r.WithContext(ctx), sw, items, func(it eventItem, _ bool) (bool, error) {
		if it.dropped {
			_ = sw.Event("dropped", "", struct{}{})
			return true, nil
		}
		if !it.poll && it.change.Project != "" {
			if err := sw.Event("change", "", changeDTO{Project: it.change.Project, Seq: it.change.Seq}); err != nil {
				return true, err
			}
		}
		if now := s.statusFlags(); now != last {
			last = now
			return false, sw.Event("status", "", now)
		}
		return false, nil
	})
}

// ---- running operations ----

func validOpState(v string) bool {
	switch stacks.OpState(v) {
	case stacks.OpRunning, stacks.OpSucceeded, stacks.OpFailed, stacks.OpCancelled, stacks.OpTimedOut:
		return true
	}
	return false
}

// handleListOperations lists retained operations across all stacks, newest
// first, optionally only those in ?state=.
func (s *server) handleListOperations(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	if state != "" && !validOpState(state) {
		writeError(w, http.StatusBadRequest, "unknown operation state")
		return
	}
	var ops []stacks.Operation
	for _, op := range s.opts.Operations.All() {
		if state == "" || string(op.State) == state {
			ops = append(ops, op)
		}
	}
	writeJSON(w, http.StatusOK, toOperations(ops))
}

// ---- /metrics and its token ----

// tokenHash returns the stored token hash, reading the store once and then
// serving it from memory: /metrics is unauthenticated, so a flood of bad
// requests must not turn into database reads. The token endpoints update the
// cache, and they are the only writers.
func (s *server) tokenHash(ctx context.Context) (string, error) {
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	if !s.tokenLoaded {
		h, err := s.opts.Observe.TokenHash(ctx)
		if err != nil {
			return "", err
		}
		s.tokenCache, s.tokenLoaded = h, true
	}
	return s.tokenCache, nil
}

func (s *server) setTokenHash(ctx context.Context, hash string) error {
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	if err := s.opts.Observe.SetTokenHash(ctx, hash); err != nil {
		return err
	}
	s.tokenCache, s.tokenLoaded = hash, true
	return nil
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// handlePrometheus serves the exposition format behind a bearer token. With no
// token configured the endpoint does not exist (404). Digests of the presented
// and stored tokens are compared in constant time.
func (s *server) handlePrometheus(w http.ResponseWriter, r *http.Request) {
	stored, err := s.tokenHash(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if stored == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
	want, decodeErr := hex.DecodeString(stored)
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" || decodeErr != nil ||
		subtle.ConstantTimeCompare(hashToken(strings.TrimSpace(token)), want) != 1 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="stackstead"`)
		writeError(w, http.StatusUnauthorized, "invalid or missing bearer token")
		return
	}
	var buf bytes.Buffer
	if err := metrics.WriteExposition(&buf, s.opts.Observe.Latest(), s.opts.Observe.Degraded()); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

// handleCreateMetricsToken creates or rotates the token. The plaintext is in
// this response only; just its SHA-256 is stored.
func (s *server) handleCreateMetricsToken(w http.ResponseWriter, r *http.Request) {
	var empty struct{}
	if !decodeJSON(w, r, &empty) {
		return
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		s.internalError(w, r, fmt.Errorf("generate metrics token: %w", err))
		return
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	if err := s.setTokenHash(r.Context(), hex.EncodeToString(hashToken(token))); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

func (s *server) handleDeleteMetricsToken(w http.ResponseWriter, r *http.Request) {
	if err := s.setTokenHash(r.Context(), ""); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
