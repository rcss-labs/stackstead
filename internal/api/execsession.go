package api

import (
	"bytes"
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
	// WriteTimeout is how long (real time) the session waits for the writer
	// to flush the terminal frame before closing anyway. Pass the same value
	// the ws adapter uses for its per-write deadline. Default 60s.
	WriteTimeout time.Duration

	Now   func() time.Time
	After func(d time.Duration) <-chan time.Time

	// MissingExecutableMessage, when set, enables detection of a command Docker
	// could not start. Real Docker still answers 101 and streams its "OCI
	// runtime exec failed" text, then exits 126/127; the session then sends this
	// as the error frame instead of the raw text and an exit frame.
	MissingExecutableMessage string

	// StillAuthorized is called on every ping tick; false ends the session.
	StillAuthorized func(ctx context.Context) bool
	// Closing is closed when the server shuts down.
	Closing <-chan struct{}
}

const (
	idleWarningLead  = time.Minute
	exitInspectTries = 20
	exitInspectWait  = 50 * time.Millisecond
	defaultFlushWait = 60 * time.Second
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

	// One writer goroutine owns every ws write. Control frames always go
	// before data; data is unbuffered so stdout backpressure reaches the
	// stream reader, not the socket.
	control      chan controlMsg
	data         chan []byte
	pingPending  atomic.Bool
	terminalSent atomic.Bool
	stopWriter   chan struct{}
	stopOnce     sync.Once
	writerDone   chan struct{}

	stateMu      sync.Mutex
	lastActivity time.Time
	lastPong     time.Time

	eofOnce sync.Once
	ends    chan sessionEnd

	stdinSeen atomic.Bool // any client input reached the process
	// heldOutput is the output withheld while it might be Docker's "could not
	// start" text. holdMu guards it (readStream, readClient, finishExit).
	heldOutput  []heldChunk
	holdMu      sync.Mutex  // guards heldOutput and holdChecked
	holdChecked bool        // the first output chunk has been classified
	holdDone    atomic.Bool // holding is settled and flushed; skip holdMu for good
}

type heldChunk struct {
	kind byte
	p    []byte
}

// runSession pumps bytes between the WebSocket and the exec stream until the
// session ends, and returns the audit result: "exit N", "idle",
// "session_expired", "shutdown", "disconnected", "detached" or "error".
func runSession(ctx context.Context, ws wsConn, stream engine.ExecStream, be execBackend, cfg sessionConfig) string {
	s := &session{ctx: ctx, ws: ws, stream: stream, be: be, cfg: cfg, ends: make(chan sessionEnd, 2),
		control: make(chan controlMsg, 4), data: make(chan []byte), stopWriter: make(chan struct{}), writerDone: make(chan struct{})}
	if s.cfg.WriteTimeout <= 0 {
		s.cfg.WriteTimeout = defaultFlushWait
	}
	start := cfg.Now()
	s.lastActivity, s.lastPong = start, start

	go s.writeLoop()
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
			s.enqueuePing()
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
					s.enqueue(controlMsg{Type: "idle_warning", Seconds: remaining})
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
		s.stdinSeen.Store(true)
		s.releaseHeldOutput()
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

// maxHeldOutput caps how much output is withheld while checking for a start failure.
const maxHeldOutput = 4096

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
		var ok bool
		if s.holdDone.Load() {
			ok = s.forward(kind, p) // settled: nothing held, no lock
		} else {
			s.holdMu.Lock()
			ok = s.routeOutput(kind, p)
			if s.holdChecked && len(s.heldOutput) == 0 {
				s.holdDone.Store(true)
			}
			s.holdMu.Unlock()
		}
		if !ok {
			return
		}
	}
}

// routeOutput holds or forwards one chunk; holdMu must be held. Output is only
// withheld while it might still be Docker's single short "could not start"
// message: the first chunk, looking like one, before any stdin. A second
// chunk means a real program is running, so everything held is released first.
func (s *session) routeOutput(kind byte, p []byte) bool {
	if !s.holdChecked {
		s.holdChecked = true
		if s.cfg.MissingExecutableMessage != "" && !s.stdinSeen.Load() && len(p) <= maxHeldOutput && looksLikeStartFailure(p) {
			s.heldOutput = append(s.heldOutput, heldChunk{kind, append([]byte(nil), p...)})
			return true // finishExit, stdin or the next chunk decides
		}
	}
	if !s.flushHeldLocked() {
		return false
	}
	return s.forward(kind, p)
}

// flushHeldLocked forwards the withheld chunks in order and stops holding for
// good; holdMu must be held. It reports false when the writer is gone.
func (s *session) flushHeldLocked() bool {
	held := s.heldOutput
	s.heldOutput = nil
	return s.forwardChunks(held)
}

func (s *session) forwardChunks(chunks []heldChunk) bool {
	for _, h := range chunks {
		if !s.forward(h.kind, h.p) {
			return false
		}
	}
	return true
}

// releaseHeldOutput is called once client input arrives: the process is
// clearly running, so output held back as a possible start failure is sent now.
func (s *session) releaseHeldOutput() {
	if s.holdDone.Load() {
		return // settled: no lock, so stdin never waits behind output backpressure
	}
	s.holdMu.Lock()
	s.holdChecked = true
	_ = s.flushHeldLocked() // a false result means the writer is gone; the session is ending anyway
	s.holdDone.Store(true)
	s.holdMu.Unlock()
}

// looksLikeStartFailure reports whether the output is the text Docker writes
// when it cannot start the command.
func looksLikeStartFailure(p []byte) bool {
	return bytes.HasPrefix(p, []byte("OCI runtime exec failed")) || bytes.HasPrefix(p, []byte("exec failed"))
}

// forward queues one output chunk for the writer. It reports false when the
// terminal frame was sent or the client is gone.
func (s *session) forward(kind byte, p []byte) bool {
	frame := make([]byte, 0, len(p)+1)
	frame = append(append(frame, kind), p...)
	select {
	case s.data <- frame:
		return true
	case <-s.writerDone:
		return false
	}
}

func isTerminal(m controlMsg) bool {
	return m.Type == "exit" || m.Type == "closed" || m.Type == "error"
}

// writeLoop is the only goroutine that writes to the WebSocket. Control
// frames win over data. The terminal frame is its last write; data queued
// behind it is dropped. A write error means the client is gone.
func (s *session) writeLoop() {
	defer close(s.writerDone)
	writeControl := func(m controlMsg) (stop bool) {
		err := s.ws.WriteText(m)
		if m.Type == "ping" {
			s.pingPending.Store(false)
		}
		if isTerminal(m) {
			return true
		}
		if err != nil {
			s.signal(endClientGone)
			return true
		}
		return false
	}
	for {
		select { // control first, without waiting for data
		case m := <-s.control:
			if writeControl(m) {
				return
			}
			continue
		default:
		}
		select {
		case m := <-s.control:
			if writeControl(m) {
				return
			}
		case p := <-s.data:
			if err := s.ws.WriteBinary(p); err != nil {
				s.signal(endClientGone)
				return
			}
		case <-s.stopWriter:
			return
		}
	}
}

// enqueue queues a control frame without ever blocking the control loop; if
// the queue is full the frame is dropped.
func (s *session) enqueue(m controlMsg) {
	select {
	case s.control <- m:
	default:
	}
}

// enqueuePing queues a ping unless one is already waiting to be written.
func (s *session) enqueuePing() {
	if !s.pingPending.CompareAndSwap(false, true) {
		return
	}
	select {
	case s.control <- controlMsg{Type: "ping"}:
	default:
		s.pingPending.Store(false)
	}
}

// sendTerminal queues the single final frame and waits (bounded) for the
// writer to flush it.
func (s *session) sendTerminal(m controlMsg) {
	if !s.terminalSent.CompareAndSwap(false, true) {
		return
	}
	select {
	case s.control <- m:
	case <-s.writerDone:
		return
	}
	select {
	case <-s.writerDone:
	case <-time.After(s.cfg.WriteTimeout):
	}
}

// seal stops the writer; no further frames are written.
func (s *session) seal() { s.stopOnce.Do(func() { close(s.stopWriter) }) }

func (s *session) shutdownConns() {
	_ = s.stream.Close()
	// Last: x/net Close waits behind an in-flight write, bounded by the
	// per-write deadline.
	_ = s.ws.Close()
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
	// Take the held output in one critical section so a late keystroke cannot
	// flush it between the decision and the action.
	s.holdMu.Lock()
	held := s.heldOutput
	s.heldOutput = nil
	s.holdChecked = true
	s.holdDone.Store(true)
	s.holdMu.Unlock()
	if len(held) > 0 {
		if code == 126 || code == 127 {
			s.sendTerminal(controlMsg{Type: "error", Message: s.cfg.MissingExecutableMessage})
			s.shutdownConns()
			return "error"
		}
		s.forwardChunks(held) // it was ordinary output after all
	}
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
	<-s.cfg.After(s.cfg.DetachCheck)
	// A fresh context: a cancelled request must still be able to report
	// "detached".
	ictx, cancel := context.WithTimeout(context.Background(), s.cfg.DetachCheck+2*time.Second)
	defer cancel()
	info, err := s.be.Inspect(ictx)
	if err == nil && info.Running {
		return "detached"
	}
	return "disconnected"
}
