package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"mime"
	"net/http"
	"strings"
	"time"
)

// Event is one parsed server-sent event. ID is the id of this event only
// (empty when it carried none), not a sticky last-event-id.
type Event struct {
	Name string
	ID   string
	Data json.RawMessage
}

// StreamOptions tunes Stream.
type StreamOptions struct {
	// LastEventID is asked on every (re)connect; "" sends no header. When nil
	// the client resumes from the last event id it saw itself.
	LastEventID      func() string
	HeartbeatTimeout time.Duration // 0 -> 45s
	Backoff          struct{ Min, Max time.Duration }
	// OnReconnect is called before each reconnect with the consecutive
	// attempt number and the cause (nil for a clean close or "dropped").
	OnReconnect func(attempt int, err error)
}

// ErrStreamDone is returned by an event handler to stop the stream cleanly.
var ErrStreamDone = errors.New("stream done")

var errHeartbeat = errors.New("no data from server (heartbeat timeout)")

// sseParser turns raw bytes into events; partial lines are buffered across
// feeds. Comments (": ping") are reported as liveness.
type sseParser struct {
	buf     []byte
	name    string
	id      string
	data    []byte
	hasData bool
}

func (p *sseParser) feed(chunk []byte, onEvent func(Event) error, onComment func()) error {
	p.buf = append(p.buf, chunk...)
	for {
		i := bytes.IndexByte(p.buf, '\n')
		if i < 0 {
			if len(p.buf) > maxEventBytes {
				return fatalError{errEventTooLarge}
			}
			return nil
		}
		line := bytes.TrimSuffix(p.buf[:i], []byte("\r"))
		err := p.line(line, onEvent, onComment)
		p.buf = p.buf[i+1:]
		if err != nil {
			return err
		}
	}
}

func (p *sseParser) line(line []byte, onEvent func(Event) error, onComment func()) error {
	if len(line) == 0 {
		var err error
		if p.hasData {
			name := p.name
			if name == "" {
				name = "message"
			}
			err = onEvent(Event{Name: name, ID: p.id, Data: json.RawMessage(append([]byte(nil), p.data...))})
		}
		p.name, p.id, p.data, p.hasData = "", "", nil, false
		return err
	}
	if line[0] == ':' {
		onComment()
		return nil
	}
	field, value, _ := strings.Cut(string(line), ":")
	value = strings.TrimPrefix(value, " ")
	switch field {
	case "event":
		p.name = value
	case "data":
		if p.hasData {
			p.data = append(p.data, '\n')
		}
		p.data = append(p.data, value...)
		p.hasData = true
		if len(p.data) > maxEventBytes {
			return fatalError{errEventTooLarge}
		}
	case "id":
		if !strings.Contains(value, "\x00") {
			p.id = value
		}
	}
	return nil
}

type chunk struct {
	data []byte
	err  error
}

// readChunks reads body in a goroutine so the caller can apply a timeout.
// The goroutine ends when body is closed (the caller always does).
func readChunks(body io.Reader, stop <-chan struct{}) <-chan chunk {
	ch := make(chan chunk)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := body.Read(buf)
			if n > 0 {
				select {
				case ch <- chunk{data: append([]byte(nil), buf[:n]...)}:
				case <-stop:
					return
				}
			}
			if err != nil {
				select {
				case ch <- chunk{err: err}:
				case <-stop:
				}
				return
			}
		}
	}()
	return ch
}

type streamState struct {
	lastID     string // last id this call saw (default resume point)
	everOpened bool
	progressed bool // current connection delivered an event or comment
}

// Stream connects to path (query already encoded), parses server-sent events
// and calls fn for each. It reconnects on EOF, heartbeat timeout, a 5xx, and
// (once it has connected before) network errors. It stops with *Error on
// other statuses such as 401/403/404, with ctx's error when ctx ends, and
// with nil when fn returns ErrStreamDone. An "event: dropped" is passed to fn
// and then reconnects immediately, without backoff.
func (c *Client) Stream(ctx context.Context, path string, opts StreamOptions, fn func(Event) error) error {
	if c.cfg.Session == "" {
		return ErrNoSession
	}
	hb, minB, maxB := streamDefaults(opts)
	st := &streamState{}
	attempt := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		st.progressed = false
		immediate, err := c.streamOnce(ctx, path, opts, hb, st, fn)
		if err != nil {
			if errors.Is(err, ErrStreamDone) {
				return nil
			}
			var fatal fatalError
			if errors.As(err, &fatal) {
				return fatal.err
			}
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if st.progressed {
			attempt = 0
		}
		attempt++
		if opts.OnReconnect != nil {
			opts.OnReconnect(attempt, err)
		}
		if immediate {
			continue
		}
		if serr := c.sleep(ctx, backoffDelay(attempt, minB, maxB)); serr != nil {
			return serr
		}
	}
}

// fatalError marks an error that must end Stream instead of reconnecting.
type fatalError struct{ err error }

func (f fatalError) Error() string { return f.err.Error() }
func (f fatalError) Unwrap() error { return f.err }

func backoffDelay(attempt int, minD, maxD time.Duration) time.Duration {
	capD := minD
	for i := 1; i < attempt && capD < maxD; i++ {
		capD *= 2
	}
	if capD > maxD {
		capD = maxD
	}
	if capD <= minD {
		return minD
	}
	return minD + time.Duration(rand.Int64N(int64(capD-minD)+1)) // full jitter
}

func (c *Client) streamOnce(ctx context.Context, path string, opts StreamOptions, hb time.Duration, st *streamState, fn func(Event) error) (immediate bool, err error) {
	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	req, err := c.newRequest(reqCtx, http.MethodGet, path, nil)
	if err != nil {
		return false, fatalError{err}
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	var last string
	if opts.LastEventID != nil {
		last = opts.LastEventID()
	} else {
		last = st.lastID
	}
	if last != "" {
		req.Header.Set("Last-Event-ID", last)
	}

	resp, err := c.stream.Do(req)
	if err != nil {
		err = wrapNetwork(c.base.Host, err)
		if isFatalConnectError(err) || !st.everOpened {
			return false, fatalError{err}
		}
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		apiErr := readError(resp)
		if resp.StatusCode >= 500 && st.everOpened {
			return false, apiErr
		}
		return false, fatalError{apiErr}
	}
	if mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mt != "text/event-stream" {
		notStream := fmt.Errorf("server did not answer with an event stream (Content-Type %q)", resp.Header.Get("Content-Type"))
		if !st.everOpened {
			return false, fatalError{notStream}
		}
		return false, notStream
	}
	st.everOpened = true

	stop := make(chan struct{})
	defer close(stop)
	chunks := readChunks(resp.Body, stop)
	timer := time.NewTimer(hb)
	defer timer.Stop()

	var parser sseParser
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-timer.C:
			return false, errHeartbeat
		case ch := <-chunks:
			if ch.err != nil {
				if errors.Is(ch.err, io.EOF) {
					return false, nil
				}
				return false, ch.err
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(hb)
			dropped := false
			err := parser.feed(ch.data, func(ev Event) error {
				st.progressed = true
				if ev.ID != "" {
					st.lastID = ev.ID
				}
				if err := fn(ev); err != nil {
					return fatalError{err} // a handler's error ends the stream
				}
				if ev.Name == "dropped" {
					dropped = true
				}
				return nil
			}, func() { st.progressed = true })
			if err != nil {
				return false, err // ErrStreamDone or the handler's own error
			}
			if dropped {
				return true, nil
			}
		}
	}
}

// errEventTooLarge is returned when a single SSE line or event exceeds maxEventBytes.
var errEventTooLarge = errors.New("server-sent event too large")

const maxEventBytes = 4 << 20

// streamDefaults resolves zero StreamOptions values.
func streamDefaults(opts StreamOptions) (hb, minB, maxB time.Duration) {
	hb, minB, maxB = opts.HeartbeatTimeout, opts.Backoff.Min, opts.Backoff.Max
	if hb <= 0 {
		hb = 45 * time.Second
	}
	if minB <= 0 {
		minB = time.Second
	}
	if maxB <= 0 {
		maxB = 30 * time.Second
	}
	if maxB < minB {
		maxB = minB
	}
	return hb, minB, maxB
}

// isFatalConnectError reports whether a connect error must end Stream:
// certificate verification failures and refused redirects never fix themselves.
func isFatalConnectError(err error) bool {
	var certErr *tls.CertificateVerificationError
	return errors.Is(err, errRedirectHost) || errors.As(err, &certErr)
}
