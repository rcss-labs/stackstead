package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/rcss-labs/stackstead/internal/stacks"
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

// raw writes bytes under a fresh deadline and flushes. A writer that does not
// support deadlines at all (http.ErrNotSupported, e.g. httptest recorders)
// is written to without one; any other deadline failure ends the stream,
// because continuing would leave the write unbounded.
func (s *sseWriter) raw(format string, args ...any) error {
	if err := s.rc.SetWriteDeadline(time.Now().Add(s.writeTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
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
// If handle returns (false, nil) for ok=false, in is not polled again and the
// loop just keeps heartbeating until the context ends or Closing closes.
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
			stop, err := handle(v, ok)
			if stop || err != nil {
				return
			}
			if !ok {
				in = nil // closed and handled once; a nil channel never fires
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
//     output line: first the retained output, then live lines. The index is
//     absolute (0 is the operation's first line), so it is stable across
//     subscriptions even when old lines were evicted. The "... N earlier lines
//     dropped" note, shown first once lines were evicted, has no id.
//   - A Last-Event-ID header resumes after that index: the replay is sliced
//     from the subscription's own atomic replay, so no line is lost or
//     repeated. A malformed value, or an index whose next line was already
//     evicted, replays everything; an index at or past the end sends nothing.
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
	s.streamOperation(w, r, s.opts.Operations, r.PathValue("id"))
}

// operationSubscriber is the part of an operations registry a stream needs.
type operationSubscriber interface {
	Subscribe(id string) (replay []string, first int, live <-chan string, done <-chan stacks.Operation, cancel func(), err error)
}

// streamOperation is handleStreamOperation for operation id of ops; routes
// with their own authorisation reuse it.
func (s *server) streamOperation(w http.ResponseWriter, r *http.Request, ops operationSubscriber, id string) {
	replay, first, live, done, cancel, err := ops.Subscribe(id)
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

	// Ids are absolute line indexes, stable across subscriptions. replay[0]
	// has index first; when first > 0 it is the "earlier lines dropped" note,
	// which stands in for a line that is gone and so carries no id.
	nextID := first + len(replay) // id of the next live line
	start := 0
	if last := parseLastEventID(r); last >= 0 {
		switch {
		case first > 0 && last < first:
			// The line after the client's position was evicted: replay all,
			// so the client sees the dropped note.
		case last >= nextID-1: // compare, never last+1: it may be MaxInt
			start = len(replay)
		default:
			start = last + 1 - first
		}
	}
	for i := start; i < len(replay); i++ {
		// A large replay must not outlive the request or a closing server.
		select {
		case <-r.Context().Done():
			return
		case <-s.opts.Closing:
			return
		default:
		}
		id := strconv.Itoa(first + i)
		if first > 0 && i == 0 {
			id = ""
		}
		if sw.Event("line", id, replay[i]) != nil {
			return
		}
	}
	sendLive := func(line string) error {
		err := sw.Event("line", strconv.Itoa(nextID), line)
		nextID++
		return err
	}

	runSSE(s, r, sw, live, func(line string, ok bool) (bool, error) {
		if ok {
			return false, sendLive(line)
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
