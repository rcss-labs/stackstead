package api

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rcss-labs/stackstead/internal/engine"
)

// execBackend is the part of an exec instance the session controls besides
// its byte stream.
type execBackend interface {
	Resize(ctx context.Context, cols, rows uint16) error
	Inspect(ctx context.Context) (engine.ExecInspect, error)
}

// sessionConfig carries the timings and seams of one exec session. Tests
// substitute small or fake values and a fake clock.
type sessionConfig struct {
	Ping        time.Duration // how often to ping and re-check authorization
	PongTimeout time.Duration // close if no pong arrived within this long
	IdleTimeout time.Duration // 0 disables the idle timeout
	DetachCheck time.Duration // wait after a disconnect before inspecting

	Now   func() time.Time
	After func(d time.Duration) <-chan time.Time

	// StillAuthorized is called on every ping tick; false ends the session.
	StillAuthorized func(ctx context.Context) bool
	// Closing is closed when the server shuts down.
	Closing <-chan struct{}
}

const (
	idleWarningLead  = time.Minute
	exitInspectTries = 20
	exitInspectWait  = 50 * time.Millisecond
	controlWriteWait = 250 * time.Millisecond // real time, not the injected clock
)

type sessionEnd int

const (
	endClientGone sessionEnd = iota
	endStreamDone
)

type session struct {
	ctx    context.Context
	ws     wsConn
	stream engine.ExecStream
	be     execBackend
	cfg    sessionConfig

	writeMu sync.Mutex  // serialises every ws write
	sealed  atomic.Bool // a terminal frame was sent or the client is gone

	stateMu      sync.Mutex
	lastActivity time.Time
	lastPong     time.Time

	eofOnce sync.Once
	ends    chan sessionEnd
}

// runSession pumps bytes between the WebSocket and the exec stream until the
// session ends, and returns the audit result: "exit N", "idle",
// "session_expired", "shutdown", "disconnected", "detached" or "error".
func runSession(ctx context.Context, ws wsConn, stream engine.ExecStream, be execBackend, cfg sessionConfig) string {
	s := &session{ctx: ctx, ws: ws, stream: stream, be: be, cfg: cfg, ends: make(chan sessionEnd, 2)}
	start := cfg.Now()
	s.lastActivity, s.lastPong = start, start

	go s.readClient()
	go s.readStream()

	pingCh := cfg.After(cfg.Ping)
	var idleCh <-chan time.Time
	warned := false
	warnAt := idleWarnAt(cfg.IdleTimeout)
	if cfg.IdleTimeout > 0 {
		idleCh = cfg.After(warnAt)
	}

	for {
		select {
		case end := <-s.ends:
			if end == endStreamDone {
				return s.finishExit()
			}
			return s.finishClientGone()

		case <-pingCh:
			if !cfg.StillAuthorized(ctx) {
				return s.finishClosed("session_expired")
			}
			s.stateMu.Lock()
			sincePong := cfg.Now().Sub(s.lastPong)
			s.stateMu.Unlock()
			if sincePong >= cfg.PongTimeout {
				return s.finishClientGone()
			}
			if err := s.writeText(controlMsg{Type: "ping"}); err != nil {
				return s.finishClientGone()
			}
			pingCh = cfg.After(cfg.Ping)

		case <-idleCh:
			s.stateMu.Lock()
			elapsed := cfg.Now().Sub(s.lastActivity)
			s.stateMu.Unlock()
			switch {
			case elapsed >= cfg.IdleTimeout:
				return s.finishClosed("idle")
			case elapsed >= warnAt:
				if !warned {
					warned = true
					// Round up so the client never sees 0 or a figure a second short.
					remaining := max(1, int((cfg.IdleTimeout-elapsed+time.Second-1)/time.Second))
					if err := s.writeText(controlMsg{Type: "idle_warning", Seconds: remaining}); err != nil {
						return s.finishClientGone()
					}
				}
				idleCh = cfg.After(cfg.IdleTimeout - elapsed)
			default:
				warned = false // activity cancelled the countdown
				idleCh = cfg.After(warnAt - elapsed)
			}

		case <-cfg.Closing:
			return s.finishClosed("shutdown")

		case <-ctx.Done():
			return s.finishClientGone()
		}
	}
}

// idleWarnAt is how long after the last activity the warning is sent: one
// minute before the deadline, or at half the timeout if that is later.
func idleWarnAt(idle time.Duration) time.Duration {
	return max(idle-idleWarningLead, idle/2)
}

func (s *session) signal(end sessionEnd) {
	select {
	case s.ends <- end:
	default: // an end is already pending; the first one wins
	}
}

func (s *session) touchActivity() {
	s.stateMu.Lock()
	s.lastActivity = s.cfg.Now()
	s.stateMu.Unlock()
}

// readClient pumps WebSocket frames to stdin and handles control messages.
func (s *session) readClient() {
	for {
		text, data, err := s.ws.ReadFrame()
		if err != nil {
			s.signal(endClientGone)
			return
		}
		if text {
			s.handleControl(data)
			continue
		}
		if len(data) < 2 || data[0] != streamStdin {
			continue
		}
		s.touchActivity()
		_, _ = s.stream.Write(data[1:]) // a closed stdin is the process's business
	}
}

func (s *session) handleControl(raw []byte) {
	var m controlMsg
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	switch m.Type {
	case "pong":
		s.stateMu.Lock()
		s.lastPong = s.cfg.Now()
		s.stateMu.Unlock()
	case "resize":
		if m.Cols > 0 && m.Cols <= 0xFFFF && m.Rows > 0 && m.Rows <= 0xFFFF {
			_ = s.be.Resize(s.ctx, uint16(m.Cols), uint16(m.Rows))
		}
	case "eof":
		s.eofOnce.Do(func() { _ = s.stream.CloseWrite() })
	}
}

// readStream pumps process output to the WebSocket.
func (s *session) readStream() {
	for {
		kind, p, err := s.stream.ReadFrame()
		if err != nil {
			s.signal(endStreamDone)
			return
		}
		if len(p) == 0 {
			continue
		}
		s.touchActivity()
		frame := make([]byte, 0, len(p)+1)
		frame = append(append(frame, kind), p...)
		if err := s.writeBinary(frame); err != nil {
			s.signal(endClientGone)
			return
		}
	}
}

var errSealed = fmt.Errorf("exec session: no more frames")

func (s *session) writeBinary(p []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.sealed.Load() {
		return errSealed
	}
	return s.ws.WriteBinary(p)
}

// lockWriteWithin takes the write mutex, giving up after d of real time. A
// write stuck on a dead client holds the mutex; the control loop must not
// freeze behind it.
func (s *session) lockWriteWithin(d time.Duration) bool {
	for deadline := time.Now().Add(d); ; time.Sleep(time.Millisecond) {
		if s.writeMu.TryLock() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
	}
}

// writeText sends a control frame. If a stuck write holds the connection, the
// frame is skipped and the pong timeout decides the client's fate.
func (s *session) writeText(m controlMsg) error {
	if !s.lockWriteWithin(controlWriteWait) {
		return nil
	}
	defer s.writeMu.Unlock()
	if s.sealed.Load() {
		return errSealed
	}
	return s.ws.WriteText(m)
}

// sendTerminal seals the session, then sends the single final frame if the
// connection is writable.
func (s *session) sendTerminal(m controlMsg) {
	if !s.sealed.CompareAndSwap(false, true) {
		return
	}
	if !s.lockWriteWithin(controlWriteWait) {
		return
	}
	defer s.writeMu.Unlock()
	_ = s.ws.WriteText(m)
}

func (s *session) seal() { s.sealed.Store(true) }

func (s *session) shutdownConns() {
	_ = s.stream.Close()
	_ = s.ws.Close() // last: x/net Close waits for an in-flight write
}

// finishClosed ends a session the process did not cause.
func (s *session) finishClosed(reason string) string {
	s.sendTerminal(controlMsg{Type: "closed", Reason: reason})
	s.shutdownConns()
	return reason
}

// finishExit reports the process's exit code once its output has ended.
func (s *session) finishExit() string {
	var info engine.ExecInspect
	var err error
	for i := 0; i < exitInspectTries; i++ {
		info, err = s.be.Inspect(s.ctx)
		if err != nil || !info.Running {
			break
		}
		select { // Docker can lag a moment behind the closed output
		case <-s.cfg.After(exitInspectWait):
		case <-s.ctx.Done():
		}
	}
	if err != nil || info.Running {
		s.sendTerminal(controlMsg{Type: "error", Message: "could not read the exit status"})
		s.shutdownConns()
		return "error"
	}
	code := info.ExitCode
	s.sendTerminal(controlMsg{Type: "exit", Code: &code})
	s.shutdownConns()
	return fmt.Sprintf("exit %d", code)
}

// finishClientGone handles a vanished client: the stream is closed, and after
// a short wait the exec is inspected to tell "disconnected" (the process died
// with its stdin) from "detached" (it keeps running unattended).
func (s *session) finishClientGone() string {
	s.seal()
	s.shutdownConns()
	select {
	case <-s.cfg.After(s.cfg.DetachCheck):
	case <-s.ctx.Done():
		return "disconnected"
	}
	info, err := s.be.Inspect(s.ctx)
	if err == nil && info.Running {
		return "detached"
	}
	return "disconnected"
}
