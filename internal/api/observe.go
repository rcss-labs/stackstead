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
	// SubscribeEvents has the contract of state.Cache.SubscribeEvents.
	SubscribeEvents() (<-chan engine.Event, func())
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

func (o ObserveSources) SubscribeEvents() (<-chan engine.Event, func()) {
	return o.Cache.SubscribeEvents()
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
	end       bool // follow mode: nothing is left to follow
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
// continue); "end" once every stream has finished (without follow), or, with
// follow, once the stack (or the service named in service) has no listed
// container left. A follow stream outlives restarts and recreation: the status
// cache's change events trigger a reconcile that re-attaches containers that
// started again (resumed from their cursor, so nothing repeats), attaches
// replacements that appeared with a new id, and closes the streams of
// containers that are gone. A stack with no containers (or none matching
// service) gets just "end". At most 50 containers (by name) are read. Closing
// the request cancels every Docker stream.
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
	belongs := func(c engine.Container) bool {
		// An empty project would otherwise match every non-compose container.
		return detail.Project != "" && c.Project() == detail.Project && (service == "" || c.ComposeService() == service)
	}
	var cs []engine.Container
	for _, c := range all {
		if belongs(c) {
			cs = append(cs, c)
		}
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].Name < cs[j].Name })
	if len(cs) > maxLogContainers {
		cs = cs[:maxLogContainers]
	}

	s.streamContainerLogs(w, r, cs, opts, detail.Project, belongs)
}

// streamContainerLogs is the SSE core shared by the stack and single-container
// log routes: it starts the stream, merges the containers' lines (resuming
// from Last-Event-ID cursors) and ends with "end". See handleLogs for the
// event contract. cs are the containers to read now; project and match say
// which containers belong to the request (used only when following, to
// re-attach after restarts and recreation).
func (s *server) streamContainerLogs(w http.ResponseWriter, r *http.Request, cs []engine.Container, opts engine.LogOptions,
	project string, match func(engine.Container) bool) {
	sw, err := startSSE(w, s.sseWriteTimeout())
	if err != nil {
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel() // cancels every Docker stream and stops the pumps

	cursors := parseLogCursors(r.Header.Get("Last-Event-ID"))
	merged := make(chan logItem)
	if opts.Follow {
		f := &logFollower{s: s, ctx: ctx, opts: opts, header: cursors, project: project, match: match,
			merged: merged, done: make(chan pumpDone), pumps: map[string]*logPump{}}
		go f.run(cs)
	} else {
		var wg sync.WaitGroup
		for _, c := range cs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				cur, skip := resumePoint(cursors, c)
				copts := opts
				if skip > 0 {
					copts.Tail, copts.Since = -1, time.Unix(0, cur.TS)
				}
				s.pumpContainer(ctx, c, copts, cur, skip, merged, nil)
			}()
		}
		go func() { wg.Wait(); close(merged) }()
	}

	runSSE(s, r.WithContext(ctx), sw, merged, func(it logItem, ok bool) (bool, error) {
		switch {
		case !ok || it.end:
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

// resumePoint returns the cursor a container's stream starts from and how many
// lines at the cursor's exact timestamp to drop: the Last-Event-ID cursor when
// the client has one, otherwise an empty cursor and no skip.
func resumePoint(cursors map[string]logCursor, c engine.Container) (logCursor, int) {
	cur := logCursor{Container: cursorContainer(c.ID)}
	if resume, ok := cursors[cur.Container]; ok {
		return resume, resume.Ordinal
	}
	return cur, 0
}

// pumpContainer reads one container's Docker log stream into merged until it
// ends, the context is cancelled, or the stream fails (reported as an error
// item). It drops the first skip lines stamped exactly cur.TS (already
// delivered), gives every other timestamped line a cursor id, and calls record
// (when set) with the cursor of each line it delivers. It returns the number of
// lines delivered and whether the stream ended with an error.
func (s *server) pumpContainer(ctx context.Context, c engine.Container, copts engine.LogOptions, cur logCursor, skip int,
	merged chan<- logItem, record func(logCursor)) (delivered int, failed bool) {
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
			return delivered, false
		}
		delivered++
		if record != nil && item.id != "" {
			record(cur)
		}
	}
	select {
	case err := <-errs: // nil once the channel is closed without an error
		if err != nil {
			failed = true
			select {
			case merged <- logItem{container: c, err: err}:
			case <-ctx.Done():
			}
		}
	case <-ctx.Done():
	}
	return delivered, failed
}

// logPump is one container's follow stream. All fields are guarded by
// logFollower.mu.
type logPump struct {
	c           engine.Container // the listed container, never one built from an event
	cursor      logCursor        // last line delivered; TS 0 when none yet
	active      bool
	wait        pumpWait // what a retry waits for after the last stream came to nothing
	idleRetries int      // poll retries that ended empty without an error since the last line or change
	fromPumpEnd bool     // the running stream was started as a retry (another stream ended)
	sawChange   bool     // a change arrived while the stream was running, so a retry has fresh information
	cancel      context.CancelFunc
	attachedAt  time.Time // when the stream was last attached; re-attach reads from here when no line has been delivered
	listing     string    // identity and state of the last listing entry, to spot a real restart
	goneAt      time.Time // when the container dropped out of the listing; zero while listed
}

// pumpWait says when a container whose last retry came to nothing may be
// attached again.
type pumpWait int

const (
	// waitNone: attach whenever a reconcile finds the container running.
	waitNone pumpWait = iota
	// waitPoll: the retry ended empty without an error, which is what a
	// container stopped for a moment (a quick restart) looks like while the
	// listing still says running. The next poll tick tries again; a stream end
	// does not, so this cannot spin.
	waitPoll
	// waitChange: the retry failed (for example a container without a
	// readable log), or waitPoll ran out of tries. Only a change event or a
	// restart visible in the listing attaches it again.
	waitChange
)

// maxIdleRetries bounds the poll retries of a container whose stream keeps
// ending empty while it is listed as running.
const maxIdleRetries = 5

// reconcileTrigger is why reconcile runs.
type reconcileTrigger int

const (
	triggerChange  reconcileTrigger = iota // a change event, or the catch-up after subscribing
	triggerPumpEnd                         // a stream ended
	triggerPoll                            // the project-less poll ticked
)

type pumpDone struct {
	id        string
	delivered int
	failed    bool // the stream ended with an error
}

// logFollower keeps a follow-mode log stream attached to the containers that
// belong to the request as they restart and are recreated. A single goroutine
// (run) owns the decisions; the pumps only read Docker and report back.
type logFollower struct {
	s       *server
	ctx     context.Context
	opts    engine.LogOptions
	header  map[string]logCursor // Last-Event-ID cursors, by short container id
	project string               // changes for other projects are ignored; "" reacts to all
	match   func(engine.Container) bool
	merged  chan logItem
	done    chan pumpDone

	mu    sync.Mutex
	pumps map[string]*logPump // by full container id
}

// resubscribeDelay spaces out resubscriptions so a subscription that keeps
// closing cannot spin the loop.
const resubscribeDelay = 50 * time.Millisecond

func (f *logFollower) run(initial []engine.Container) {
	changes, cancelSub := f.s.opts.Observe.SubscribeChanges()
	defer func() { cancelSub() }()
	for _, c := range initial {
		f.start(c, false)
	}
	if len(initial) == 0 {
		f.sendEnd()
		return
	}
	// A container created after the caller's snapshot but before the
	// subscription went live is in neither; catch it now.
	if f.reconcile(triggerChange) {
		return
	}
	// The cache publishes no change for containers outside a compose project,
	// so a request for one (the single-container route) polls the cached
	// listing instead; it is cheap and bounded by the interval.
	var poll <-chan time.Time
	if f.project == "" {
		ticker := time.NewTicker(f.s.statusPollInterval())
		defer ticker.Stop()
		poll = ticker.C
	}
	for {
		select {
		case <-f.ctx.Done():
			return
		case <-poll:
			if f.reconcile(triggerPoll) {
				return
			}
		case d := <-f.done:
			f.pumpEnded(d)
			if f.reconcile(triggerPumpEnd) {
				return
			}
		case ch, ok := <-changes:
			if !ok { // the cache dropped this subscriber: resubscribe and catch up
				select {
				case <-f.ctx.Done():
					return
				case <-time.After(resubscribeDelay):
				}
				cancelSub()
				changes, cancelSub = f.s.opts.Observe.SubscribeChanges()
			} else if f.project != "" && ch.Project != "" && ch.Project != f.project {
				continue
			}
			f.noteChange()
			if f.reconcile(triggerChange) {
				return
			}
		}
	}
}

func (f *logFollower) sendEnd() {
	select {
	case f.merged <- logItem{end: true}:
	case <-f.ctx.Done():
	}
}

// start attaches a stream to c: from its cursor when one is known (Last-Event-ID
// for the first attach, the last delivered line afterwards), otherwise with the
// request's own options.
func (f *logFollower) start(c engine.Container, fromPumpEnd bool) {
	f.mu.Lock()
	p := f.pumps[c.ID]
	if p == nil {
		p = &logPump{}
		f.pumps[c.ID] = p
	}
	if p.cursor.TS == 0 {
		if resume, ok := f.header[cursorContainer(c.ID)]; ok {
			p.cursor = resume
		}
	}
	pumpCtx, cancel := context.WithCancel(f.ctx)
	p.c, p.active, p.fromPumpEnd, p.sawChange, p.cancel, p.goneAt = c, true, fromPumpEnd, false, cancel, time.Time{}
	cur, previousAttach := p.cursor, p.attachedAt
	p.listing = c.ID + "|" + c.State + "|" + c.Created.String()
	p.attachedAt = time.Now()
	f.mu.Unlock()

	copts, skip := f.opts, 0
	switch {
	case cur.TS != 0:
		copts.Tail, copts.Since, skip = -1, time.Unix(0, cur.TS), cur.Ordinal
	case !previousAttach.IsZero():
		// Nothing was delivered before (for example tail=0): everything written
		// since the stream was first attached is new, so do not fall back to tail.
		copts.Tail, copts.Since = -1, previousAttach
	}
	cur.Container = cursorContainer(c.ID)
	record := func(cur logCursor) {
		f.mu.Lock()
		p.cursor = cur
		f.mu.Unlock()
	}
	go func() {
		defer cancel()
		n, failed := f.s.pumpContainer(pumpCtx, c, copts, cur, skip, f.merged, record)
		select {
		case f.done <- pumpDone{id: c.ID, delivered: n, failed: failed}:
		case <-f.ctx.Done():
		}
	}()
}

// pumpEnded decides what a retry of the ended stream waits for. A stream that
// delivered lines, or that saw a change while it ran, is retried at once; so is
// a first attach, once. A retry that came to nothing waits: for the next poll
// tick when it ended cleanly (the container may be mid-restart), for a change
// when it failed, so a failing stream is not retried on every tick.
func (f *logFollower) pumpEnded(d pumpDone) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.pumps[d.id]
	if p == nil {
		return
	}
	p.active = false
	if d.delivered > 0 {
		p.idleRetries = 0
	}
	switch {
	case !p.fromPumpEnd || d.delivered > 0 || p.sawChange:
		p.wait = waitNone
	case d.failed:
		p.wait = waitChange
	case p.idleRetries < maxIdleRetries:
		p.wait = waitPoll
	default:
		p.wait = waitChange
	}
}

// Cursors of containers that left the listing are kept for keptCursorTTL, and
// at most maxKeptCursors of them (the longest gone are dropped first).
const (
	keptCursorTTL  = 10 * time.Minute
	maxKeptCursors = 256
)

// pruneGone drops the entries of containers that have been gone too long.
// The caller holds f.mu.
func (f *logFollower) pruneGone(now time.Time) {
	var gone []string
	for id, p := range f.pumps {
		if p.goneAt.IsZero() || p.active {
			continue
		}
		if now.Sub(p.goneAt) > keptCursorTTL {
			delete(f.pumps, id)
			continue
		}
		gone = append(gone, id)
	}
	if len(gone) <= maxKeptCursors {
		return
	}
	sort.Slice(gone, func(i, j int) bool { return f.pumps[gone[i]].goneAt.Before(f.pumps[gone[j]].goneAt) })
	for _, id := range gone[:len(gone)-maxKeptCursors] {
		delete(f.pumps, id)
	}
}

// noteChange records that the cache published a change: every stream that gave
// up waiting may retry, and a running one will not be treated as stale when it
// ends, because the change may already describe the container it is about to
// become.
func (f *logFollower) noteChange() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.pumps {
		p.wait, p.idleRetries = waitNone, 0
		p.sawChange = p.active
	}
}

// reconcile attaches streams to listed running containers that have none,
// closes the streams of containers that are gone, and reports whether it sent
// "end" (nothing left to follow). A container whose last retry came to nothing
// is attached again only as its pumpWait allows, or on a restart visible in
// the listing, which bounds a stale listing to one retry per change (and at
// most maxIdleRetries poll retries).
func (f *logFollower) reconcile(trigger reconcileTrigger) (ended bool) {
	all, err := f.s.opts.Observe.Containers(f.ctx)
	if err != nil {
		return false // wait for the next change
	}
	var listed []engine.Container
	for _, c := range all {
		if f.match(c) {
			listed = append(listed, c)
		}
	}
	sort.Slice(listed, func(i, j int) bool { return listed[i].Name < listed[j].Name })
	if len(listed) > maxLogContainers {
		listed = listed[:maxLogContainers]
	}
	present := make(map[string]bool, len(listed))
	for _, c := range listed {
		present[c.ID] = true
	}

	f.mu.Lock()
	now := time.Now()
	for id, p := range f.pumps {
		switch {
		case present[id]:
			p.goneAt = time.Time{}
		case p.active:
			p.cancel() // the container is gone; its pump then reports back
		default:
			// Keep the cursor for a while: the same id can reappear in the listing
			// and must not be replayed from tail.
			if p.goneAt.IsZero() {
				p.goneAt = now
			}
		}
	}
	f.pruneGone(now)
	type attachment struct {
		c     engine.Container
		retry bool // a retry of a stream that ended: if it comes to nothing, it waits
	}
	var attach []attachment
	for _, c := range listed {
		p := f.pumps[c.ID]
		if p == nil {
			if c.State == "running" {
				attach = append(attach, attachment{c: c, retry: trigger == triggerPumpEnd})
			}
			continue
		}
		// The listing carries no start time, so a restart that the listing shows
		// is a change of state (or of creation time).
		listing := c.ID + "|" + c.State + "|" + c.Created.String()
		if p.listing != listing {
			p.listing, p.wait, p.idleRetries = listing, waitNone, 0
		}
		if c.State != "running" || p.active {
			continue
		}
		switch {
		case p.wait == waitNone:
			attach = append(attach, attachment{c: c, retry: trigger == triggerPumpEnd})
		case p.wait == waitPoll && trigger == triggerPoll:
			p.idleRetries++
			attach = append(attach, attachment{c: c, retry: true})
		}
	}
	f.mu.Unlock()
	for _, a := range attach {
		f.start(a.c, a.retry)
	}

	f.mu.Lock()
	active := 0
	for _, p := range f.pumps {
		if p.active {
			active++
		}
	}
	f.mu.Unlock()
	if active == 0 && len(listed) == 0 {
		f.sendEnd()
		return true
	}
	return false
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
	// An empty project would otherwise match every non-compose container; keep
	// the stream open (heartbeats only) without subscribing.
	var samples <-chan metrics.Sample
	if detail.Project != "" {
		var cancel func()
		samples, cancel = s.opts.Observe.SubscribeSamples(detail.Project)
		defer cancel()
	}
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
	event   engine.Event
	isEvent bool
	poll    bool
	dropped bool
}

// eventMessage maps an engine event to its wire form. The compose labels give
// project and service; the exit code and health are passed on only for the
// actions that define them.
func eventMessage(ev engine.Event) apitypes.EventMessage {
	msg := apitypes.EventMessage{
		Action:    ev.Action,
		Container: ev.Name,
		Project:   ev.Labels["com.docker.compose.project"],
		Service:   ev.Labels["com.docker.compose.service"],
		Image:     ev.Image,
		Time:      ev.Time.UTC(),
	}
	switch ev.Action {
	case "die":
		msg.ExitCode = ev.ExitCode
	case "health_status":
		msg.Health = ev.Health
	}
	return msg
}

// statusPollInterval is how often handlers that poll a cheap in-memory state
// do so: the heartbeat, capped at maxStatusPoll.
func (s *server) statusPollInterval() time.Duration {
	every := s.opts.SSEHeartbeat
	if every <= 0 || every > maxStatusPoll {
		every = maxStatusPoll
	}
	return every
}

// handleEvents streams the status cache: "status" {events, metricsDegraded}
// once on connect and whenever either flag changes, and "change" {project, seq}
// when a project's containers changed, and "event" {action, container, ...} for
// each Docker container event (no ids, no replay). The cache signals connection changes
// with an empty project, which only triggers the flag check. The degraded flag
// has no notification, so it is polled (at most every 2s). A subscriber the
// cache dropped for being slow, on either channel, gets "dropped" and the stream
// ends; the client should reconnect and refetch.
func (s *server) handleEvents(w http.ResponseWriter, r *http.Request) {
	changes, cancel := s.opts.Observe.SubscribeChanges()
	defer cancel()
	events, cancelEvents := s.opts.Observe.SubscribeEvents()
	defer cancelEvents()
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
			case ev, ok := <-events:
				if !ok {
					send(eventItem{dropped: true})
					return
				}
				if !send(eventItem{event: ev, isEvent: true}) {
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
		if it.isEvent {
			return false, sw.Event("event", "", eventMessage(it.event))
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
