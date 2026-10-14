package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

const (
	defaultSSEHeartbeat    = 15 * time.Second
	defaultSSEWriteTimeout = 10 * time.Second
)

// sseWriter writes server-sent events with a fresh write deadline per write,
// so a stalled client is cut instead of blocking its handler forever.
type sseWriter struct {
	w            http.ResponseWriter
	rc           *http.ResponseController
	writeTimeout time.Duration
}

// startSSE sets the event-stream headers, sends 200 and the ": stream open"
// comment. The returned error is that first write's; callers should return on it.
func startSSE(w http.ResponseWriter, writeTimeout time.Duration) (*sseWriter, error) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no") // tell nginx-style proxies not to buffer
	w.WriteHeader(http.StatusOK)
	sw := &sseWriter{w: w, rc: http.NewResponseController(w), writeTimeout: writeTimeout}
	return sw, sw.Comment("stream open")
}

// raw writes bytes under a fresh deadline and flushes. SetWriteDeadline may be
// unsupported (recorders in tests); that is not an error.
func (s *sseWriter) raw(format string, args ...any) error {
	_ = s.rc.SetWriteDeadline(time.Now().Add(s.writeTimeout))
	if _, err := fmt.Fprintf(s.w, format, args...); err != nil {
		return err
	}
	return s.rc.Flush()
}

// Event writes one event with JSON-encoded data. id is omitted when empty.
func (s *sseWriter) Event(name, id string, data any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if id != "" {
		return s.raw("id: %s\nevent: %s\ndata: %s\n\n", id, name, payload)
	}
	return s.raw("event: %s\ndata: %s\n\n", name, payload)
}

// Comment writes an SSE comment line (used for the open marker and pings).
func (s *sseWriter) Comment(text string) error { return s.raw(": %s\n\n", text) }

// runSSE is the one place that owns an SSE stream's loop: it sends heartbeat
// pings every Options.SSEHeartbeat and returns when the request context is
// done, Options.Closing closes, a write fails, or handle stops it.
//
// handle is called for every value received from in, and once with ok=false
// when in is closed. It returns stop=true to end the stream, or an error
// (which also ends it). It must not block for long; writes go through sw.
func runSSE[T any](s *server, r *http.Request, sw *sseWriter, in <-chan T, handle func(v T, ok bool) (stop bool, err error)) {
	heartbeat := s.opts.SSEHeartbeat
	if heartbeat <= 0 {
		heartbeat = defaultSSEHeartbeat
	}
	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.opts.Closing: // nil channel (never ready) when unset
			return
		case <-ticker.C:
			if sw.Comment("ping") != nil {
				return
			}
		case v, ok := <-in:
			if stop, err := handle(v, ok); stop || err != nil {
				return
			}
		}
	}
}

// parseLastEventID returns the index from a Last-Event-ID header, or -1 when
// absent or malformed (which means: replay everything).
func parseLastEventID(r *http.Request) int {
	n, err := strconv.Atoi(r.Header.Get("Last-Event-ID"))
	if err != nil || n < 0 {
		return -1
	}
	return n
}

// handleStreamOperation streams an operation's output as server-sent events.
//
// Contract:
//   - "event: line" with a JSON string as data and "id: <line index>", once per
//     output line: first the retained output, then live lines. The index counts
//     from the start of the retained output.
//   - A Last-Event-ID header resumes after that index: the replay is sliced
//     from the subscription's own atomic replay, so no line is lost or
//     repeated. A malformed value replays everything.
//   - "event: done" with the final Operation JSON; the stream then ends.
//   - "event: dropped" when the subscription ended without the operation
//     finishing (the client was too slow and was dropped). The stream ends and
//     the client should reconnect (sending Last-Event-ID), or GET the operation.
//   - ": ping" comments every SSEHeartbeat keep idle connections alive.
//
// The handler returns when the request context is done, and always
// unsubscribes. It holds no locks while writing: the Ops manager never blocks
// on subscribers, and a stuck client is cut by the per-write deadline.
func (s *server) handleStreamOperation(w http.ResponseWriter, r *http.Request) {
	replay, live, done, cancel, err := s.opts.Operations.Subscribe(r.PathValue("id"))
	if err != nil {
		s.writeStackError(w, r, err)
		return
	}
	defer cancel()

	writeTimeout := s.opts.SSEWriteTimeout
	if writeTimeout <= 0 {
		writeTimeout = defaultSSEWriteTimeout
	}
	sw, err := startSSE(w, writeTimeout)
	if err != nil {
		return
	}

	next := 0 // index of the next line to send
	if last := parseLastEventID(r); last >= 0 {
		next = min(last+1, len(replay))
		replay = replay[next:]
	}
	sendLine := func(line string) error {
		err := sw.Event("line", strconv.Itoa(next), line)
		next++
		return err
	}
	for _, line := range replay {
		if sendLine(line) != nil {
			return
		}
	}

	runSSE(s, r, sw, live, func(line string, ok bool) (bool, error) {
		if ok {
			return false, sendLine(line)
		}
		// live closed: a value on done means the operation finished.
		select {
		case <-r.Context().Done():
		case op, ok := <-done:
			if ok {
				_ = sw.Event("done", "", toOperation(op))
			} else {
				_ = sw.Event("dropped", "", struct{}{})
			}
		}
		return true, nil
	})
}
