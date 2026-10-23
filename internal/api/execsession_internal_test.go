package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/engine"
	"golang.org/x/net/websocket"
)

// --- fakes ---

type fakeWS struct {
	in     chan wsInbound
	closed chan struct{}
	once   sync.Once

	mu    sync.Mutex
	bins  [][]byte
	texts []controlMsg

	blockBinary atomic.Bool  // WriteBinary hangs until Close, like a dead client
	slowBinary  atomic.Int64 // nanoseconds each WriteBinary takes
	order       []string     // every frame written, in order
}

type wsInbound struct {
	text bool
	data []byte
}

func newFakeWS() *fakeWS {
	return &fakeWS{in: make(chan wsInbound, 64), closed: make(chan struct{})}
}

func (f *fakeWS) ReadFrame() (bool, []byte, error) {
	select {
	case m, ok := <-f.in:
		if !ok {
			return false, nil, io.EOF
		}
		return m.text, m.data, nil
	case <-f.closed:
		return false, nil, io.ErrClosedPipe
	}
}

func (f *fakeWS) WriteBinary(p []byte) error {
	if f.blockBinary.Load() {
		<-f.closed
		return io.ErrClosedPipe
	}
	time.Sleep(time.Duration(f.slowBinary.Load()))
	f.mu.Lock()
	defer f.mu.Unlock()
	select {
	case <-f.closed:
		return io.ErrClosedPipe
	default:
	}
	f.order = append(f.order, "bin")
	f.bins = append(f.bins, append([]byte(nil), p...))
	return nil
}

func (f *fakeWS) WriteText(v controlMsg) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	select {
	case <-f.closed:
		return io.ErrClosedPipe
	default:
	}
	f.order = append(f.order, v.Type)
	f.texts = append(f.texts, v)
	return nil
}

func (f *fakeWS) Close() error {
	f.once.Do(func() { close(f.closed) })
	return nil
}

func (f *fakeWS) sendBinary(p ...byte) { f.in <- wsInbound{data: p} }
func (f *fakeWS) sendText(s string)    { f.in <- wsInbound{text: true, data: []byte(s)} }

func (f *fakeWS) textsExceptPing() []controlMsg {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []controlMsg
	for _, m := range f.texts {
		if m.Type != "ping" {
			out = append(out, m)
		}
	}
	return out
}

func (f *fakeWS) pingCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, m := range f.texts {
		if m.Type == "ping" {
			n++
		}
	}
	return n
}

func (f *fakeWS) binaries() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.bins...)
}

func (f *fakeWS) isClosed() bool {
	select {
	case <-f.closed:
		return true
	default:
		return false
	}
}

type outFrame struct {
	stream byte
	p      []byte
}

type fakeStream struct {
	frames chan outFrame
	eof    chan struct{}
	closed chan struct{}
	once   sync.Once
	finish sync.Once

	mu          sync.Mutex
	stdin       bytes.Buffer
	closeWrites int
	echo        bool
}

func newFakeStream() *fakeStream {
	return &fakeStream{frames: make(chan outFrame), eof: make(chan struct{}), closed: make(chan struct{})}
}

func (s *fakeStream) ReadFrame() (byte, []byte, error) {
	select {
	case f := <-s.frames:
		return f.stream, f.p, nil
	case <-s.eof:
		return 0, nil, io.EOF
	case <-s.closed:
		return 0, nil, io.ErrClosedPipe
	}
}

func (s *fakeStream) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.stdin.Write(p)
	echo := s.echo
	s.mu.Unlock()
	if echo {
		select {
		case s.frames <- outFrame{1, append([]byte(nil), p...)}:
		case <-s.closed:
			return 0, io.ErrClosedPipe
		}
	}
	return len(p), nil
}

func (s *fakeStream) CloseWrite() error {
	s.mu.Lock()
	s.closeWrites++
	s.mu.Unlock()
	return nil
}

func (s *fakeStream) Close() error { s.once.Do(func() { close(s.closed) }); return nil }

func (s *fakeStream) out(stream byte, text string) {
	s.frames <- outFrame{stream, []byte(text)}
}
func (s *fakeStream) endOutput() { s.finish.Do(func() { close(s.eof) }) }

func (s *fakeStream) stdinBytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.stdin.Bytes()...)
}

func (s *fakeStream) closeWriteCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeWrites
}

func (s *fakeStream) isClosed() bool {
	select {
	case <-s.closed:
		return true
	default:
		return false
	}
}

type fakeBackend struct {
	mu      sync.Mutex
	resizes [][2]uint16
	inspect engine.ExecInspect
	err     error
}

func (b *fakeBackend) Resize(_ context.Context, cols, rows uint16) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.resizes = append(b.resizes, [2]uint16{cols, rows})
	return nil
}

func (b *fakeBackend) Inspect(context.Context) (engine.ExecInspect, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.inspect, b.err
}

func (b *fakeBackend) setInspect(i engine.ExecInspect) {
	b.mu.Lock()
	b.inspect = i
	b.mu.Unlock()
}

func (b *fakeBackend) resizeList() [][2]uint16 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([][2]uint16(nil), b.resizes...)
}

// fakeClock is a manual clock: timers fire only from Advance.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
	late   time.Duration // timers are observed this long after they were due
}

type fakeTimer struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(1_000_000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{at: c.now.Add(d), ch: make(chan time.Time, 1)}
	c.timers = append(c.timers, t)
	return t.ch
}

// Advance moves time forward, firing due timers in order and giving the
// session loop a moment to react (and re-arm) after each one.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	target := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		idx := -1
		for i, t := range c.timers {
			if !t.at.After(target) && (idx < 0 || t.at.Before(c.timers[idx].at)) {
				idx = i
			}
		}
		if idx < 0 {
			c.now = target
			c.mu.Unlock()
			return
		}
		t := c.timers[idx]
		c.timers = append(c.timers[:idx], c.timers[idx+1:]...)
		c.now = t.at.Add(c.late)
		armedBefore := len(c.timers)
		c.mu.Unlock()
		t.ch <- t.at
		// The loop handles the tick and then re-arms; wait for that so the
		// next timer is created at the right fake time. A tick that ends the
		// session never re-arms, so give up after a bound.
		for end := time.Now().Add(400 * time.Millisecond); c.pending() <= armedBefore && time.Now().Before(end); {
			time.Sleep(100 * time.Microsecond)
		}
	}
}

func (c *fakeClock) pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers)
}

// --- harness ---

type harness struct {
	t       *testing.T
	ws      *fakeWS
	st      *fakeStream
	be      *fakeBackend
	clk     *fakeClock
	closing chan struct{}
	result  chan string
}

func startSession(t *testing.T, mutate func(*sessionConfig)) *harness {
	t.Helper()
	h := &harness{t: t, ws: newFakeWS(), st: newFakeStream(), be: &fakeBackend{}, clk: newFakeClock(), closing: make(chan struct{}), result: make(chan string, 1)}
	cfg := sessionConfig{
		Ping:            20 * time.Second,
		PongTimeout:     1000 * time.Hour, // tests that don't care never time out
		DetachCheck:     2 * time.Second,
		WriteTimeout:    2 * time.Second,
		Now:             h.clk.Now,
		After:           h.clk.After,
		StillAuthorized: func(context.Context) bool { return true },
		Closing:         h.closing,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	go func() { h.result <- runSession(context.Background(), h.ws, h.st, h.be, cfg) }()
	// Let the loop arm its timers before a test advances the fake clock. A
	// test on the real clock never registers any, so this wait is best effort.
	want := 1
	if cfg.IdleTimeout > 0 {
		want = 2
	}
	for end := time.Now().Add(200 * time.Millisecond); h.clk.pending() < want && time.Now().Before(end); {
		time.Sleep(time.Millisecond)
	}
	t.Cleanup(func() { h.st.Close(); h.ws.Close() })
	return h
}

func (h *harness) waitResult() string {
	h.t.Helper()
	select {
	case r := <-h.result:
		return r
	case <-time.After(5 * time.Second):
		h.t.Fatal("session did not end")
		return ""
	}
}

func (h *harness) expectNoResult() {
	h.t.Helper()
	select {
	case r := <-h.result:
		h.t.Fatalf("session ended unexpectedly with %q", r)
	case <-time.After(50 * time.Millisecond):
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// --- tests ---

func TestSessionPumpsStdinStdout(t *testing.T) {
	h := startSession(t, nil)
	h.ws.sendBinary(0, 'l', 's')
	eventually(t, "stdin", func() bool { return string(h.st.stdinBytes()) == "ls" })
	h.st.out(1, "out")
	eventually(t, "stdout frame", func() bool { return len(h.ws.binaries()) == 1 })
	if got := h.ws.binaries()[0]; !bytes.Equal(got, []byte{1, 'o', 'u', 't'}) {
		t.Fatalf("frame = %v", got)
	}
}

func TestSessionStderrFrame(t *testing.T) {
	h := startSession(t, nil)
	h.st.out(2, "e")
	eventually(t, "stderr frame", func() bool { return len(h.ws.binaries()) == 1 })
	if got := h.ws.binaries()[0]; !bytes.Equal(got, []byte{2, 'e'}) {
		t.Fatalf("frame = %v", got)
	}
}

func TestSessionResize(t *testing.T) {
	h := startSession(t, nil)
	h.ws.sendText(`{"type":"resize","cols":120,"rows":40}`)
	eventually(t, "resize", func() bool { return len(h.be.resizeList()) == 1 })
	if got := h.be.resizeList()[0]; got != [2]uint16{120, 40} {
		t.Fatalf("resize = %v", got)
	}
}

func TestSessionEOF(t *testing.T) {
	h := startSession(t, nil)
	h.ws.sendText(`{"type":"eof"}`)
	h.ws.sendText(`{"type":"eof"}`)
	eventually(t, "closeWrite", func() bool { return h.st.closeWriteCount() >= 1 })
	time.Sleep(20 * time.Millisecond)
	if n := h.st.closeWriteCount(); n != 1 {
		t.Fatalf("CloseWrite called %d times", n)
	}
}

func TestSessionExitSendsCode(t *testing.T) {
	h := startSession(t, nil)
	h.be.setInspect(engine.ExecInspect{Running: false, ExitCode: 3})
	h.st.endOutput()
	if r := h.waitResult(); r != "exit 3" {
		t.Fatalf("result = %q", r)
	}
	got := h.ws.textsExceptPing()
	if len(got) != 1 || got[0].Type != "exit" || got[0].Code == nil || *got[0].Code != 3 {
		t.Fatalf("texts = %+v", got)
	}
	if !h.ws.isClosed() {
		t.Fatal("ws not closed")
	}
}

// Real Docker answers a missing binary with a started stream carrying the OCI
// error text, then exit code 127.
const ociMissing = "OCI runtime exec failed: exec failed: unable to start container process: exec: \"/bin/bash\": stat /bin/bash: no such file or directory: unknown\r\n"

func TestSessionMissingExecutableReportsError(t *testing.T) {
	for _, tc := range []struct{ name, msg string }{
		{"auto shell", "no shell found in this container; pass a command"},
		{"custom command", "command not found in this container"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := startSession(t, func(c *sessionConfig) { c.MissingExecutableMessage = tc.msg })
			h.be.setInspect(engine.ExecInspect{Running: false, ExitCode: 127})
			h.st.out(1, ociMissing)
			h.st.endOutput()
			if r := h.waitResult(); r != "error" {
				t.Fatalf("result = %q", r)
			}
			got := h.ws.textsExceptPing()
			if len(got) != 1 || got[0].Type != "error" || got[0].Message != tc.msg {
				t.Fatalf("texts = %+v", got)
			}
			if b := h.ws.binaries(); len(b) != 0 {
				t.Fatalf("the raw OCI text was forwarded: %q", b)
			}
		})
	}
}

func TestSessionOCITextAfterStdinIsOrdinaryOutput(t *testing.T) {
	h := startSession(t, func(c *sessionConfig) { c.MissingExecutableMessage = "no shell" })
	h.ws.sendBinary(0, 'x')
	eventually(t, "stdin", func() bool { return string(h.st.stdinBytes()) == "x" })
	h.be.setInspect(engine.ExecInspect{Running: false, ExitCode: 127})
	h.st.out(1, ociMissing)
	eventually(t, "forwarded", func() bool { return len(h.ws.binaries()) == 1 })
	h.st.endOutput()
	if r := h.waitResult(); r != "exit 127" {
		t.Fatalf("result = %q", r)
	}
}

func TestSessionHeldOCITextIsDeliveredWhenExitIsOrdinary(t *testing.T) {
	h := startSession(t, func(c *sessionConfig) { c.MissingExecutableMessage = "no shell" })
	h.be.setInspect(engine.ExecInspect{Running: false, ExitCode: 1})
	h.st.out(1, ociMissing)
	h.st.endOutput()
	if r := h.waitResult(); r != "exit 1" {
		t.Fatalf("result = %q", r)
	}
	if b := h.ws.binaries(); len(b) != 1 || string(b[0][1:]) != ociMissing {
		t.Fatalf("binaries = %q", b)
	}
}

func TestSessionOrdinaryFirstChunkIsNotHeldBack(t *testing.T) {
	h := startSession(t, func(c *sessionConfig) { c.MissingExecutableMessage = "no shell" })
	h.st.out(1, "$ ")
	eventually(t, "prompt forwarded", func() bool { return len(h.ws.binaries()) == 1 })
}

func TestSessionExitZeroCodeIsSent(t *testing.T) {
	h := startSession(t, nil)
	h.st.endOutput()
	if r := h.waitResult(); r != "exit 0" {
		t.Fatalf("result = %q", r)
	}
	raw, _ := json.Marshal(h.ws.textsExceptPing()[0])
	if string(raw) != `{"type":"exit","code":0}` {
		t.Fatalf("json = %s", raw)
	}
}

func TestSessionOutputDeliveredBeforeExit(t *testing.T) {
	h := startSession(t, nil)
	h.st.out(1, "bye")
	h.st.endOutput()
	h.waitResult()
	if len(h.ws.binaries()) != 1 {
		t.Fatalf("binaries = %v", h.ws.binaries())
	}
}

func TestSessionClientGoneDisconnected(t *testing.T) {
	h := startSession(t, nil)
	close(h.ws.in)
	eventually(t, "stream closed", h.st.isClosed)
	h.expectNoResult() // still waiting for the detach check
	h.clk.Advance(2 * time.Second)
	if r := h.waitResult(); r != "disconnected" {
		t.Fatalf("result = %q", r)
	}
	if n := len(h.ws.textsExceptPing()); n != 0 {
		t.Fatalf("no terminal frame expected when client vanished, got %d", n)
	}
}

func TestSessionClientGoneDetached(t *testing.T) {
	h := startSession(t, nil)
	h.be.setInspect(engine.ExecInspect{Running: true})
	close(h.ws.in)
	eventually(t, "stream closed", h.st.isClosed)
	h.clk.Advance(2 * time.Second)
	if r := h.waitResult(); r != "detached" {
		t.Fatalf("result = %q", r)
	}
}

func TestSessionPingPongTimeout(t *testing.T) {
	h := startSession(t, func(c *sessionConfig) { c.PongTimeout = 60 * time.Second })
	h.clk.Advance(60 * time.Second)
	h.clk.Advance(2 * time.Second) // detach check
	if r := h.waitResult(); r != "disconnected" {
		t.Fatalf("result = %q", r)
	}
	if !h.ws.isClosed() {
		t.Fatal("ws not closed")
	}
}

func TestSessionPongTimeoutStillRunningIsDetached(t *testing.T) {
	h := startSession(t, func(c *sessionConfig) { c.PongTimeout = 60 * time.Second })
	h.be.setInspect(engine.ExecInspect{Running: true})
	h.clk.Advance(60 * time.Second)
	h.clk.Advance(2 * time.Second)
	if r := h.waitResult(); r != "detached" {
		t.Fatalf("result = %q", r)
	}
}

// A write stuck on a vanished client must not freeze ping, pong timeout or
// shutdown handling.
func TestSessionStuckWriteDoesNotFreezeSession(t *testing.T) {
	h := startSession(t, func(c *sessionConfig) { c.PongTimeout = 60 * time.Second })
	h.ws.blockBinary.Store(true)
	go h.st.out(1, "stuck") // reader goroutine now hangs in WriteBinary
	time.Sleep(20 * time.Millisecond)
	h.clk.Advance(60 * time.Second) // pings skipped, pong timeout fires
	h.clk.Advance(2 * time.Second)
	if r := h.waitResult(); r != "disconnected" {
		t.Fatalf("result = %q", r)
	}
}

func TestSessionStuckWriteShutdownStillDelivers(t *testing.T) {
	h := startSession(t, func(c *sessionConfig) { c.WriteTimeout = 300 * time.Millisecond })
	h.ws.blockBinary.Store(true)
	go h.st.out(1, "stuck")
	time.Sleep(20 * time.Millisecond)
	close(h.closing)
	if r := h.waitResult(); r != "shutdown" {
		t.Fatalf("result = %q", r)
	}
}

func TestSessionIdleWarningRoundsUp(t *testing.T) {
	h := startSession(t, func(c *sessionConfig) { c.IdleTimeout = 5 * time.Minute })
	// The timer is handled 300 ms late, so 59.7 s remain; truncation says 59.
	h.clk.late = 300 * time.Millisecond
	h.clk.Advance(4*time.Minute + 300*time.Millisecond)
	got := h.ws.textsExceptPing()
	if len(got) != 1 || got[0].Seconds != 60 {
		t.Fatalf("texts = %+v", got)
	}
}

// slowOutput keeps output flowing until the test ends.
func (h *harness) slowOutput() {
	h.ws.slowBinary.Store(int64(300 * time.Millisecond))
	stop := make(chan struct{})
	h.t.Cleanup(func() { close(stop) })
	go func() {
		for {
			select {
			case h.st.frames <- outFrame{1, []byte("tail")}:
			case <-stop:
				return
			case <-h.st.closed:
				return
			}
		}
	}()
}

func TestSessionSlowWriterStillGetsPings(t *testing.T) {
	h := startSession(t, func(c *sessionConfig) { c.PongTimeout = 60 * time.Second })
	h.slowOutput()
	for i := 1; i <= 5; i++ {
		h.clk.Advance(20 * time.Second)
		want := i
		eventually(t, "ping written", func() bool { return h.ws.pingCount() >= want })
		h.ws.sendText(`{"type":"pong"}`)
	}
	h.expectNoResult()
}

func TestSessionSlowWriterShutdownDeliversClosed(t *testing.T) {
	h := startSession(t, nil)
	h.slowOutput()
	eventually(t, "output flowing", func() bool { return len(h.ws.binaries()) >= 2 })
	close(h.closing)
	if r := h.waitResult(); r != "shutdown" {
		t.Fatalf("result = %q", r)
	}
	h.ws.mu.Lock()
	defer h.ws.mu.Unlock()
	if n := len(h.ws.order); n == 0 || h.ws.order[n-1] != "closed" {
		t.Fatalf("last frames = %v", h.ws.order)
	}
}

func TestSessionPingsAreSentAndPongKeepsOpen(t *testing.T) {
	h := startSession(t, func(c *sessionConfig) { c.PongTimeout = 60 * time.Second })
	for i := 0; i < 6; i++ {
		h.clk.Advance(20 * time.Second)
		h.ws.sendText(`{"type":"pong"}`)
		time.Sleep(5 * time.Millisecond)
	}
	h.expectNoResult()
	if n := h.ws.pingCount(); n < 5 {
		t.Fatalf("pings = %d", n)
	}
}

func TestSessionIdleWarningThenClose(t *testing.T) {
	h := startSession(t, func(c *sessionConfig) { c.IdleTimeout = 5 * time.Minute })
	h.clk.Advance(4 * time.Minute)
	got := h.ws.textsExceptPing()
	if len(got) != 1 || got[0].Type != "idle_warning" || got[0].Seconds != 60 {
		t.Fatalf("texts = %+v", got)
	}
	h.clk.Advance(time.Minute)
	if r := h.waitResult(); r != "idle" {
		t.Fatalf("result = %q", r)
	}
	got = h.ws.textsExceptPing()
	if len(got) != 2 || got[1].Type != "closed" || got[1].Reason != "idle" {
		t.Fatalf("texts = %+v", got)
	}
}

func TestSessionIdleResetByOutput(t *testing.T) {
	h := startSession(t, func(c *sessionConfig) { c.IdleTimeout = 5 * time.Minute })
	h.clk.Advance(4*time.Minute + 30*time.Second)
	h.st.out(1, "x")
	eventually(t, "frame", func() bool { return len(h.ws.binaries()) == 1 })
	h.clk.Advance(30 * time.Second) // 5m total, only 30s since output
	h.expectNoResult()
	for _, m := range h.ws.textsExceptPing() {
		if m.Type == "closed" {
			t.Fatalf("unexpected close: %+v", m)
		}
	}
	h.clk.Advance(5 * time.Minute)
	if r := h.waitResult(); r != "idle" {
		t.Fatalf("result = %q", r)
	}
}

func TestSessionIdleResetByStdin(t *testing.T) {
	h := startSession(t, func(c *sessionConfig) { c.IdleTimeout = 5 * time.Minute })
	h.clk.Advance(4*time.Minute + 30*time.Second)
	h.ws.sendBinary(0, 'a')
	eventually(t, "stdin", func() bool { return len(h.st.stdinBytes()) == 1 })
	h.clk.Advance(time.Minute)
	h.expectNoResult()
}

func TestSessionPongAndResizeAreNotActivity(t *testing.T) {
	h := startSession(t, func(c *sessionConfig) { c.IdleTimeout = 5 * time.Minute })
	h.clk.Advance(4*time.Minute + 30*time.Second)
	h.ws.sendText(`{"type":"pong"}`)
	h.ws.sendText(`{"type":"resize","cols":80,"rows":24}`)
	time.Sleep(10 * time.Millisecond)
	h.clk.Advance(30 * time.Second)
	if r := h.waitResult(); r != "idle" {
		t.Fatalf("result = %q", r)
	}
}

func TestSessionIdleShortTimeoutWarnsAtHalf(t *testing.T) {
	h := startSession(t, func(c *sessionConfig) { c.IdleTimeout = 90 * time.Second })
	h.clk.Advance(44 * time.Second)
	if n := len(h.ws.textsExceptPing()); n != 0 {
		t.Fatalf("warned too early: %d", n)
	}
	h.clk.Advance(time.Second)
	got := h.ws.textsExceptPing()
	if len(got) != 1 || got[0].Type != "idle_warning" || got[0].Seconds != 45 {
		t.Fatalf("texts = %+v", got)
	}
}

func TestSessionIdleDisabled(t *testing.T) {
	h := startSession(t, nil)
	h.clk.Advance(2 * time.Hour)
	h.expectNoResult()
	if n := len(h.ws.textsExceptPing()); n != 0 {
		t.Fatalf("idle frames sent: %d", n)
	}
}

func TestSessionRecheckExpired(t *testing.T) {
	var mu sync.Mutex
	authorized := true
	h := startSession(t, func(c *sessionConfig) {
		c.StillAuthorized = func(context.Context) bool { mu.Lock(); defer mu.Unlock(); return authorized }
	})
	h.clk.Advance(20 * time.Second)
	h.expectNoResult()
	mu.Lock()
	authorized = false
	mu.Unlock()
	h.clk.Advance(20 * time.Second)
	if r := h.waitResult(); r != "session_expired" {
		t.Fatalf("result = %q", r)
	}
	got := h.ws.textsExceptPing()
	if len(got) != 1 || got[0].Type != "closed" || got[0].Reason != "session_expired" {
		t.Fatalf("texts = %+v", got)
	}
}

func TestSessionShutdown(t *testing.T) {
	h := startSession(t, nil)
	close(h.closing)
	if r := h.waitResult(); r != "shutdown" {
		t.Fatalf("result = %q", r)
	}
	got := h.ws.textsExceptPing()
	if len(got) != 1 || got[0].Type != "closed" || got[0].Reason != "shutdown" {
		t.Fatalf("texts = %+v", got)
	}
	if !h.st.isClosed() {
		t.Fatal("stream not closed")
	}
}

func TestSessionLargeTransfer(t *testing.T) {
	h := startSession(t, func(c *sessionConfig) {
		c.After = time.After
		c.Now = time.Now
	})
	h.st.mu.Lock()
	h.st.echo = true
	h.st.mu.Unlock()

	const chunk, total = 32 << 10, 5 << 20
	want := make([]byte, total)
	for i := range want {
		want[i] = byte(i * 7)
	}
	go func() {
		for off := 0; off < total; off += chunk {
			h.ws.in <- wsInbound{data: append([]byte{0}, want[off:off+chunk]...)}
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var got []byte
		for _, b := range h.ws.binaries() {
			if b[0] != 1 {
				t.Fatalf("unexpected stream id %d", b[0])
			}
			got = append(got, b[1:]...)
		}
		if len(got) >= total {
			if !bytes.Equal(got, want) {
				t.Fatal("echoed data differs")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d bytes in 5s", len(got), total)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSessionUnknownControlIgnored(t *testing.T) {
	h := startSession(t, nil)
	h.ws.sendText(`{"type":"bogus"}`)
	h.ws.sendText(`not json`)
	h.ws.sendBinary(9, 'x')
	h.ws.sendBinary()
	time.Sleep(20 * time.Millisecond)
	h.expectNoResult()
	h.st.out(1, "still alive")
	eventually(t, "frame", func() bool { return len(h.ws.binaries()) == 1 })
}

func TestSessionInspectFailureReportsError(t *testing.T) {
	h := startSession(t, nil)
	h.be.mu.Lock()
	h.be.err = errors.New("boom")
	h.be.mu.Unlock()
	h.st.endOutput()
	if r := h.waitResult(); r != "error" {
		t.Fatalf("result = %q", r)
	}
	got := h.ws.textsExceptPing()
	if len(got) != 1 || got[0].Type != "error" {
		t.Fatalf("texts = %+v", got)
	}
}

func TestFrameCodecRoundTripOverWebSocket(t *testing.T) {
	got := make(chan string, 2)
	srv := httptest.NewServer(websocket.Handler(func(c *websocket.Conn) {
		conn := newWSConn(c, 0)
		for i := 0; i < 2; i++ {
			text, data, err := conn.ReadFrame()
			if err != nil {
				return
			}
			got <- fmt.Sprintf("%v:%s", text, data)
		}
		code := 0
		_ = conn.WriteText(controlMsg{Type: "exit", Code: &code})
		_ = conn.WriteBinary([]byte{1, 'x'})
	}))
	defer srv.Close()

	c, err := websocket.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), "", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := frameCodec.Send(c, []byte{0, 'a'}); err != nil {
		t.Fatal(err)
	}
	if err := frameCodec.Send(c, controlMsg{Type: "pong"}); err != nil {
		t.Fatal(err)
	}
	if a, b := <-got, <-got; a != "false:\x00a" || b != `true:{"type":"pong"}` {
		t.Fatalf("server saw %q, %q", a, b)
	}
	var f inboundFrame
	if err := frameCodec.Receive(c, &f); err != nil || !f.text || string(f.data) != `{"type":"exit","code":0}` {
		t.Fatalf("text frame = %+v, %v", f, err)
	}
	if err := frameCodec.Receive(c, &f); err != nil || f.text || !bytes.Equal(f.data, []byte{1, 'x'}) {
		t.Fatalf("binary frame = %+v, %v", f, err)
	}
}

// A write to a client that stopped reading must fail at the deadline instead
// of blocking forever.
func TestXNetConnWriteDeadline(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(websocket.Handler(func(c *websocket.Conn) { <-release }))
	defer srv.Close()
	defer close(release)

	c, err := websocket.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), "", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	conn := newWSConn(c, 100*time.Millisecond)

	done := make(chan error, 1)
	go func() {
		chunk := make([]byte, 1<<20)
		for i := 0; i < 1024; i++ {
			if err := conn.WriteBinary(chunk); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a write deadline error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("write blocked past its deadline")
	}
}

const ociLike = "exec failed: something\n"

func TestSessionHeldOutputFlushedOnSecondChunk(t *testing.T) {
	h := startSession(t, func(c *sessionConfig) { c.MissingExecutableMessage = "no shell" })
	h.st.out(1, ociLike)
	h.st.out(1, "more\n")
	eventually(t, "both chunks forwarded before exit", func() bool { return len(h.ws.binaries()) == 2 })
	b := h.ws.binaries()
	if string(b[0][1:]) != ociLike || string(b[1][1:]) != "more\n" {
		t.Fatalf("binaries = %q", b)
	}
	h.be.setInspect(engine.ExecInspect{Running: false, ExitCode: 0})
	h.st.endOutput()
	if r := h.waitResult(); r != "exit 0" {
		t.Fatalf("result = %q", r)
	}
}

func TestSessionHeldOutputFlushedOnStdin(t *testing.T) {
	h := startSession(t, func(c *sessionConfig) { c.MissingExecutableMessage = "no shell" })
	h.st.out(1, ociLike)
	h.expectNoResult()
	if n := len(h.ws.binaries()); n != 0 {
		t.Fatalf("held chunk forwarded too early: %d", n)
	}
	h.ws.sendBinary(0, 'x')
	eventually(t, "held chunk forwarded after stdin", func() bool { return len(h.ws.binaries()) == 1 })
	if b := h.ws.binaries(); string(b[0][1:]) != ociLike {
		t.Fatalf("binaries = %q", b)
	}
}

func TestSessionHeldOutputFlushedOverCap(t *testing.T) {
	h := startSession(t, func(c *sessionConfig) { c.MissingExecutableMessage = "no shell" })
	h.st.out(1, ociLike+strings.Repeat("x", maxHeldOutput))
	eventually(t, "oversized chunk forwarded", func() bool { return len(h.ws.binaries()) == 1 })
}

// A client blocked on output backpressure must still have its stdin and pongs
// read; a false pong timeout would drop a live client.
func TestSessionStdinAndPongReadWhileOutputIsBackpressured(t *testing.T) {
	h := startSession(t, func(c *sessionConfig) {
		c.MissingExecutableMessage = "no shell"
		c.PongTimeout = 20 * time.Second
	})
	h.ws.blockBinary.Store(true)
	h.st.out(1, "$ ")   // the writer blocks on this one
	h.st.out(1, "more") // readStream blocks handing this to the writer
	h.expectNoResult()
	h.clk.Advance(time.Second)
	h.ws.sendBinary(0, 'x')
	h.ws.sendText(`{"type":"pong"}`)
	time.Sleep(100 * time.Millisecond) // let readClient reach the pong
	h.clk.Advance(19 * time.Second)    // ping tick at t=20: 19s since the pong, 20s since start
	time.Sleep(100 * time.Millisecond)
	if h.ws.isClosed() {
		t.Fatal("session dropped a live client: the pong was never read")
	}
}
