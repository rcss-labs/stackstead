package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const (
	defaultSSEHeartbeat    = 15 * time.Second
	defaultSSEWriteTimeout = 10 * time.Second
)

// handleStreamOperation streams an operation's output as server-sent events.
//
// Contract:
//   - "event: line" with a JSON string as data, once per output line: first the
//     retained output, then live lines.
//   - "event: done" with the final Operation JSON; the stream then ends.
//   - "event: dropped" when the subscription ended without the operation
//     finishing (the client was too slow and was dropped). The stream ends and
//     the client should reconnect, which replays the retained output again
//     (clients must discard what they already showed), or GET the operation.
//   - ": ping" comments every SSEHeartbeat keep idle connections alive.
//
// The handler returns when the request context is done, and always
// unsubscribes. It holds no locks while writing: the Ops manager never blocks
// on subscribers, and a stuck client is cut by the per-write deadline.
func (s *server) handleStreamOperation(w http.ResponseWriter, r *http.Request) {
	replay, live, done, cancel, err := s.opts.Stacks.SubscribeOperation(r.PathValue("id"))
	if err != nil {
		s.writeStackError(w, r, err)
		return
	}
	defer cancel()

	heartbeat, writeTimeout := s.opts.SSEHeartbeat, s.opts.SSEWriteTimeout
	if heartbeat <= 0 {
		heartbeat = defaultSSEHeartbeat
	}
	if writeTimeout <= 0 {
		writeTimeout = defaultSSEWriteTimeout
	}

	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no") // tell nginx-style proxies not to buffer
	w.WriteHeader(http.StatusOK)

	// send writes raw bytes under a fresh deadline and flushes. SetWriteDeadline
	// may be unsupported (recorders in tests); that is not an error.
	send := func(format string, args ...any) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(writeTimeout))
		if _, err := fmt.Fprintf(w, format, args...); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	sendEvent := func(name string, payload any) bool {
		data, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		return send("event: %s\ndata: %s\n\n", name, data)
	}

	if !send(": stream open\n\n") {
		return
	}
	for _, line := range replay {
		if !sendEvent("line", line) {
			return
		}
	}

	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !send(": ping\n\n") {
				return
			}
		case line, ok := <-live:
			if ok {
				if !sendEvent("line", line) {
					return
				}
				continue
			}
			// live closed: a value on done means the operation finished.
			select {
			case <-ctx.Done():
			case op, ok := <-done:
				if ok {
					sendEvent("done", toOperation(op))
				} else {
					sendEvent("dropped", struct{}{})
				}
			}
			return
		}
	}
}
