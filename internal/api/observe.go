package api

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
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
	QueryContainers(ctx context.Context, project, service string, from, to time.Time, step time.Duration) ([]metrics.ContainerBucket, error)
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
func (o ObserveSources) QueryContainers(ctx context.Context, project, service string, from, to time.Time, step time.Duration) ([]metrics.ContainerBucket, error) {
	return o.Store.QueryContainers(ctx, project, service, from, to, step)
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
	route("GET /api/settings/metrics-token", s.handleMetricsTokenStatus)
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

type (
	logEventDTO    = apitypes.LogEvent
	logErrorDTO    = apitypes.LogError
	metricPointDTO = apitypes.MetricPoint
	sampleDTO      = apitypes.Sample
	statusFlagsDTO = apitypes.StatusFlags
	changeDTO      = apitypes.Change
)

type logItem struct {
	id        string // SSE event id (a logCursor), empty when the line has no usable timestamp
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

// logCursor is a resume point in one container's log: the timestamp of the
// last line delivered and how many lines with exactly that timestamp have been
// delivered (1 for the first). Its string form is the SSE event id, so it is
// newline-free by construction; a client sends the latest cursor of every
// container, comma-separated, as Last-Event-ID.
type logCursor struct {
	Container string // first 12 hex digits of the container id
	TS        int64  // unix nanoseconds
	Ordinal   int
}

func (c logCursor) String() string {
	return c.Container + ":" + strconv.FormatInt(c.TS, 10) + ":" + strconv.Itoa(c.Ordinal)
}

const cursorIDLen = 12

func cursorContainer(id string) string {
	if len(id) > cursorIDLen {
		return id[:cursorIDLen]
	}
	return id
}

func isLowerHex(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return s != ""
}

// parseLogCursors reads a Last-Event-ID header. Entries that are not
// "<1-12 lowercase hex>:<positive ts>:<ordinal in 1..maxCursorOrdinal>" are skipped (the
// header is client-controlled, and an operation-stream id may arrive here
// from a shared client); for a repeated container the later entry wins.
// maxCursorOrdinal bounds the client-supplied ordinal: it is a count of lines
// at one timestamp, so anything larger is forged, and capping it keeps later
// arithmetic on it far from overflow.
const maxCursorOrdinal = 1_000_000_000

func parseLogCursors(header string) map[string]logCursor {
	out := map[string]logCursor{}
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(strings.TrimSpace(part), ":")
		if len(fields) != 3 || len(fields[0]) > cursorIDLen || !isLowerHex(fields[0]) {
			continue
		}
		ts, err1 := strconv.ParseInt(fields[1], 10, 64)
		ord, err2 := strconv.Atoi(fields[2])
		if err1 != nil || err2 != nil || ts <= 0 || ord <= 0 || ord > maxCursorOrdinal {
			continue
		}
		out[fields[0]] = logCursor{Container: fields[0], TS: ts, Ordinal: ord}
	}
	return out
}

// handleLogs streams the merged logs of a stack's containers.
//
// Events: "log" {service, container, stream, time, text} per line; "error"
// {service, container, error} when one container's stream failed (the others
// continue); "end" once every stream has finished, which for a follow request
// means every container's stream ended on its own. A stack with no containers
// (or none matching service) gets just "end". At most 50 containers (by name)
// are read. Closing the request cancels every Docker stream.
//
// Every "log" event has an id (a logCursor). With a Last-Event-ID header, each
// container that has a cursor is read from that timestamp with tail=all and
// its first Ordinal lines at that exact timestamp are dropped, so lines are
// neither repeated nor skipped; other containers use tail/since as usual.
// Limits are Docker's: lines already rotated away cannot be replayed. A resume
// re-reads the container's log from the start (tail=all plus since) and skips
// forward, so it costs a full read. Docker may also store stdout and stderr
// lines slightly out of timestamp order, so in that race a resume can lose or
// repeat a line.
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

	cursors := parseLogCursors(r.Header.Get("Last-Event-ID"))
	merged := make(chan logItem)
	var wg sync.WaitGroup
	for _, c := range cs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			copts := opts
			cur := logCursor{Container: cursorContainer(c.ID)}
			skip := 0
			if resume, ok := cursors[cur.Container]; ok {
				copts.Tail, copts.Since = -1, time.Unix(0, resume.TS)
				cur.TS, cur.Ordinal, skip = resume.TS, resume.Ordinal, resume.Ordinal
			}
			lines, errs := s.opts.Observe.Logs(ctx, c, copts)
			for l := range lines {
				item := logItem{container: c, line: l}
				if !l.Time.IsZero() { // a line without a timestamp cannot be a cursor
					ts := l.Time.UnixNano()
					if skip > 0 && ts == cur.TS {
						skip--
						continue
					}
					skip = 0
					if ts == cur.TS {
						cur.Ordinal++
					} else {
						cur.TS, cur.Ordinal = ts, 1
					}
					item.id = cur.String()
				}
				select {
				case merged <- item:
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
			return false, sw.Event("log", it.id, logEventDTO{Service: it.container.ComposeService(), Container: it.container.Name,
				Stream: it.line.Stream, Time: it.line.Time, Text: it.line.Text})
		}
	})
}

// ---- metrics history ----

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

// handleMetricsHistory returns {points}: per service and time bucket the
// gauges (cpu, mem, memLimit), averaged across the service's replicas, and
// network and block-I/O rates, which are TOTALS across replicas. Rates are
// computed per container from its cumulative counters between its own
// consecutive buckets (a drop, i.e. a restart, counts as 0; a container's
// first bucket, or the first after a gap of more than 3 buckets, adds
// nothing) and then summed; see metrics.AggregateServices.
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
	rows, err := s.opts.Observe.QueryContainers(r.Context(), detail.Project, r.URL.Query().Get("service"), from, to, step)
	if err != nil {
		s.writeStackError(w, r, err)
		return
	}
	pts := metrics.AggregateServices(rows, step)
	out := make([]metricPointDTO, 0, len(pts))
	for _, p := range pts {
		out = append(out, metricPointDTO{Time: p.TS.UTC().Format(time.RFC3339), Service: p.Service, CPU: p.CPU, Mem: p.Mem, MemLimit: p.MemLimit,
			NetRx: p.NetRx, NetTx: p.NetTx, BlkRead: p.BlkRead, BlkWrite: p.BlkWrite})
	}
	writeJSON(w, http.StatusOK, map[string]any{"points": out})
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

// tokenTTL bounds how stale the cached token hash may be.
const tokenTTL = 5 * time.Second

// tokenHash returns the stored token hash from a cache with a 5 s TTL, checked
// on every call: /metrics is unauthenticated, so request volume must not turn
// into database reads (at most one read per TTL), yet a token rotated or
// cleared offline by `stackstead admin metrics-token` stops working within the
// TTL even while requests keep matching. The token endpoints write through the
// cache. If a refresh fails, the last cached hash keeps being used (and the
// failure is logged at most once per TTL); only a failed first load is an error.
func (s *server) tokenHash(ctx context.Context) (string, error) {
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	if s.tokenLoaded && s.opts.Now().Sub(s.tokenLoadAt) < tokenTTL {
		return s.tokenCache, nil
	}
	h, err := s.opts.Observe.TokenHash(ctx)
	if err != nil {
		if !s.tokenLoaded {
			return "", err
		}
		s.tokenLoadAt = s.opts.Now() // back off: retry after the next TTL
		s.opts.Logger.Warn("metrics token refresh failed, using cached hash", "err", err)
		return s.tokenCache, nil
	}
	s.tokenCache, s.tokenLoaded, s.tokenLoadAt = h, true, s.opts.Now()
	return s.tokenCache, nil
}

func (s *server) setTokenHash(ctx context.Context, hash string) error {
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	if err := s.opts.Observe.SetTokenHash(ctx, hash); err != nil {
		return err
	}
	s.tokenCache, s.tokenLoaded, s.tokenLoadAt = hash, true, s.opts.Now()
	return nil
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
	if !bearerMatches(r, stored) {
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
	token, hash, err := metrics.NewToken()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := s.setTokenHash(r.Context(), hash); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

// bearerMatches reports whether the request carries a bearer token whose
// SHA-256 equals the stored hex hash, compared in constant time.
func bearerMatches(r *http.Request, storedHex string) bool {
	scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
	want, err := hex.DecodeString(storedHex)
	token = strings.TrimSpace(token)
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" || err != nil {
		return false
	}
	got, _ := hex.DecodeString(metrics.HashToken(token))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// handleMetricsTokenStatus reports only whether a token hash is stored, from
// the same in-memory cache /metrics uses; it never exposes the hash.
func (s *server) handleMetricsTokenStatus(w http.ResponseWriter, r *http.Request) {
	hash, err := s.tokenHash(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]bool{"configured": hash != ""})
}

func (s *server) handleDeleteMetricsToken(w http.ResponseWriter, r *http.Request) {
	if err := s.setTokenHash(r.Context(), ""); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
