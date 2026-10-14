package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func parseAll(t *testing.T, chunks ...string) (events []Event, comments int) {
	t.Helper()
	var p sseParser
	for _, ch := range chunks {
		err := p.feed([]byte(ch), func(e Event) error { events = append(events, e); return nil }, func() { comments++ })
		if err != nil {
			t.Fatal(err)
		}
	}
	return events, comments
}

const fixture = ": stream open\n\nid: 0\nevent: line\ndata: \"a\"\n\nid: 1\nevent: line\ndata: \"b\"\n\nevent: done\ndata: {\"id\":\"op1\"}\n\n"

var fixtureEvents = []Event{
	{Name: "line", ID: "0", Data: []byte(`"a"`)},
	{Name: "line", ID: "1", Data: []byte(`"b"`)},
	{Name: "done", Data: []byte(`{"id":"op1"}`)},
}

func TestParserTable(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		want     []Event
		comments int
	}{
		{"multi-line data joined", "event: x\ndata: a\ndata: b\n\n", []Event{{Name: "x", Data: []byte("a\nb")}}, 0},
		{"comment is liveness", ": ping\n\n", nil, 1},
		{"CRLF", "id: 5\r\nevent: line\r\ndata: 1\r\n\r\n", []Event{{Name: "line", ID: "5", Data: []byte("1")}}, 0},
		{"default name is message", "data: 1\n\n", []Event{{Name: "message", Data: []byte("1")}}, 0},
		{"no space after colon", "event:x\ndata:y\n\n", []Event{{Name: "x", Data: []byte("y")}}, 0},
		{"retry and unknown fields ignored", "retry: 100\nfoo: bar\nevent: x\ndata: 1\n\n", []Event{{Name: "x", Data: []byte("1")}}, 0},
		{"id does not stick to the next event", "id: 1\ndata: a\n\ndata: b\n\n", []Event{{Name: "message", ID: "1", Data: []byte("a")}, {Name: "message", Data: []byte("b")}}, 0},
		{"blank line without data dispatches nothing", "event: x\n\n", nil, 0},
		{"partial trailing event is not dispatched", "event: x\ndata: 1\n", nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, comments := parseAll(t, tc.in)
			if !reflect.DeepEqual(got, tc.want) || comments != tc.comments {
				t.Errorf("got %+v (%d comments), want %+v (%d)", got, comments, tc.want, tc.comments)
			}
		})
	}
}

func TestParserSplitAtEveryByteBoundary(t *testing.T) {
	for i := 0; i <= len(fixture); i++ {
		got, comments := parseAll(t, fixture[:i], fixture[i:])
		if !reflect.DeepEqual(got, fixtureEvents) || comments != 1 {
			t.Fatalf("split at %d: got %+v (%d comments)", i, got, comments)
		}
	}
	// and one byte at a time
	var chunks []string
	for i := range fixture {
		chunks = append(chunks, fixture[i:i+1])
	}
	if got, _ := parseAll(t, chunks...); !reflect.DeepEqual(got, fixtureEvents) {
		t.Fatalf("byte-wise: got %+v", got)
	}
}

// sseServer answers each request with the next scripted handler and records
// the Last-Event-ID header of every request.
type sseServer struct {
	mu       sync.Mutex
	lastIDs  []string
	requests int
	script   []http.HandlerFunc
}

func (s *sseServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		i := s.requests
		s.requests++
		s.lastIDs = append(s.lastIDs, r.Header.Get("Last-Event-ID"))
		var h http.HandlerFunc
		if i < len(s.script) {
			h = s.script[i]
		} else {
			h = s.script[len(s.script)-1]
		}
		s.mu.Unlock()
		h(w, r)
	}
}

func (s *sseServer) ids() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.lastIDs...)
}

func writeSSE(w http.ResponseWriter, s string) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, s)
	w.(http.Flusher).Flush()
}

func streamClient(t *testing.T, s *sseServer) *Client {
	t.Helper()
	srv := httptest.NewServer(s.handler())
	t.Cleanup(srv.Close)
	c := newClientFor(t, srv.URL, "sess")
	c.sleep = func(ctx context.Context, d time.Duration) error { return ctx.Err() }
	return c
}

func fastOpts() StreamOptions {
	o := StreamOptions{HeartbeatTimeout: 2 * time.Second}
	o.Backoff.Min, o.Backoff.Max = time.Millisecond, 2*time.Millisecond
	return o
}

func TestStreamReconnectsWithLastEventID(t *testing.T) {
	s := &sseServer{script: []http.HandlerFunc{
		func(w http.ResponseWriter, r *http.Request) {
			writeSSE(w, ": stream open\n\nid: 0\nevent: line\ndata: \"a\"\n\nid: 1\nevent: line\ndata: \"b\"\n\nid: 2\nevent: line\ndata: \"c\"\n\n")
		},
		func(w http.ResponseWriter, r *http.Request) {
			writeSSE(w, "id: 3\nevent: line\ndata: \"d\"\n\nevent: done\ndata: {}\n\n")
		},
	}}
	c := streamClient(t, s)
	var names []string
	reconnects := 0
	opts := fastOpts()
	opts.OnReconnect = func(int, error) { reconnects++ }
	err := c.StreamOperation(context.Background(), "op1", opts, func(e Event) error {
		names = append(names, e.Name+":"+e.ID)
		if e.Name == "done" {
			return ErrStreamDone
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"line:0", "line:1", "line:2", "line:3", "done:"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("events = %v", names)
	}
	if ids := s.ids(); !reflect.DeepEqual(ids, []string{"", "2"}) {
		t.Errorf("Last-Event-ID per request = %q, want [\"\" \"2\"]", ids)
	}
	if reconnects != 1 {
		t.Errorf("reconnects = %d", reconnects)
	}
}

func TestStreamAsksCallerForLastEventIDEveryConnect(t *testing.T) {
	s := &sseServer{script: []http.HandlerFunc{
		func(w http.ResponseWriter, r *http.Request) { writeSSE(w, "id: a:1:1\nevent: log\ndata: {}\n\n") },
		func(w http.ResponseWriter, r *http.Request) { writeSSE(w, "event: end\ndata: {}\n\n") },
	}}
	c := streamClient(t, s)
	opts := fastOpts()
	opts.LastEventID = func() string { return "a:1:1,b:2:1" }
	err := c.Stream(context.Background(), "/api/stacks/web/logs", opts, func(e Event) error {
		if e.Name == "end" {
			return ErrStreamDone
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if ids := s.ids(); !reflect.DeepEqual(ids, []string{"a:1:1,b:2:1", "a:1:1,b:2:1"}) {
		t.Errorf("ids = %q", ids)
	}
}

func TestServerEOFWithoutDoneReconnects(t *testing.T) {
	s := &sseServer{script: []http.HandlerFunc{
		func(w http.ResponseWriter, r *http.Request) { writeSSE(w, ": stream open\n\n") },
		func(w http.ResponseWriter, r *http.Request) { writeSSE(w, "event: done\ndata: {}\n\n") },
	}}
	c := streamClient(t, s)
	err := c.Stream(context.Background(), "/api/events", fastOpts(), func(e Event) error { return ErrStreamDone })
	if err != nil || len(s.ids()) != 2 {
		t.Fatalf("err = %v, requests = %d", err, len(s.ids()))
	}
}

func TestStreamStopsOnAuthAndMissing(t *testing.T) {
	for _, status := range []int{401, 403, 404, 400} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			s := &sseServer{script: []http.HandlerFunc{jsonReply(status, map[string]string{"error": "nope"})}}
			c := streamClient(t, s)
			err := c.Stream(context.Background(), "/api/events", fastOpts(), func(Event) error { return nil })
			var apiErr *Error
			if !errors.As(err, &apiErr) || apiErr.Status != status || apiErr.Message != "nope" {
				t.Fatalf("err = %v", err)
			}
			if n := len(s.ids()); n != 1 {
				t.Errorf("%d requests, want 1 (no reconnect)", n)
			}
		})
	}
}

func TestStreamStopsOn401MidStream(t *testing.T) {
	s := &sseServer{script: []http.HandlerFunc{
		func(w http.ResponseWriter, r *http.Request) { writeSSE(w, "id: 0\nevent: line\ndata: 1\n\n") },
		jsonReply(401, map[string]string{"error": "authentication required"}),
	}}
	c := streamClient(t, s)
	err := c.Stream(context.Background(), "/api/events", fastOpts(), func(Event) error { return nil })
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v", err)
	}
}

func TestStreamReconnectsOn5xx(t *testing.T) {
	s := &sseServer{script: []http.HandlerFunc{
		jsonReply(503, map[string]string{"error": "docker unavailable"}),
		func(w http.ResponseWriter, r *http.Request) { writeSSE(w, "event: done\ndata: {}\n\n") },
	}}
	c := streamClient(t, s)
	var causes []error
	opts := fastOpts()
	opts.OnReconnect = func(_ int, err error) { causes = append(causes, err) }
	if err := c.Stream(context.Background(), "/x", opts, func(Event) error { return ErrStreamDone }); err != nil {
		t.Fatal(err)
	}
	if len(causes) != 1 || !errors.Is(causes[0], ErrUnavailable) {
		t.Errorf("causes = %v", causes)
	}
}

func TestStreamFirstConnectNetworkErrorIsReturned(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	c := newClientFor(t, url, "s")
	err := c.Stream(context.Background(), "/x", fastOpts(), func(Event) error { return nil })
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestStreamHandlerErrorStopsWithoutReconnect(t *testing.T) {
	s := &sseServer{script: []http.HandlerFunc{func(w http.ResponseWriter, r *http.Request) { writeSSE(w, "data: 1\n\n") }}}
	c := streamClient(t, s)
	boom := errors.New("boom")
	if err := c.Stream(context.Background(), "/x", fastOpts(), func(Event) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if n := len(s.ids()); n != 1 {
		t.Errorf("%d requests", n)
	}
}

func TestStreamHeartbeatTimeout(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	s := &sseServer{script: []http.HandlerFunc{
		func(w http.ResponseWriter, r *http.Request) {
			writeSSE(w, ": stream open\n\n")
			select { // silent: no heartbeat
			case <-r.Context().Done():
			case <-release:
			}
		},
		func(w http.ResponseWriter, r *http.Request) { writeSSE(w, "event: done\ndata: {}\n\n") },
	}}
	c := streamClient(t, s)
	opts := fastOpts()
	opts.HeartbeatTimeout = 200 * time.Millisecond
	var causes []error
	opts.OnReconnect = func(_ int, err error) { causes = append(causes, err) }
	start := time.Now()
	if err := c.Stream(context.Background(), "/x", opts, func(Event) error { return ErrStreamDone }); err != nil {
		t.Fatal(err)
	}
	if len(causes) != 1 || !errors.Is(causes[0], errHeartbeat) {
		t.Errorf("causes = %v", causes)
	}
	if d := time.Since(start); d < 200*time.Millisecond || d > 3*time.Second {
		t.Errorf("took %v", d)
	}
}

func TestCommentsKeepTheStreamAlive(t *testing.T) {
	s := &sseServer{script: []http.HandlerFunc{func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 6; i++ {
			writeSSE(w, ": ping\n\n")
			time.Sleep(60 * time.Millisecond)
		}
		writeSSE(w, "event: done\ndata: {}\n\n")
	}}}
	c := streamClient(t, s)
	opts := fastOpts()
	opts.HeartbeatTimeout = 200 * time.Millisecond
	if err := c.Stream(context.Background(), "/x", opts, func(Event) error { return ErrStreamDone }); err != nil {
		t.Fatal(err)
	}
	if n := len(s.ids()); n != 1 {
		t.Errorf("%d requests, want 1 (pings must reset the deadline)", n)
	}
}

func TestDroppedReconnectsImmediately(t *testing.T) {
	s := &sseServer{script: []http.HandlerFunc{
		func(w http.ResponseWriter, r *http.Request) {
			writeSSE(w, "id: 0\nevent: line\ndata: \"a\"\n\nevent: dropped\ndata: {}\n\n")
		},
		func(w http.ResponseWriter, r *http.Request) { writeSSE(w, "event: done\ndata: {}\n\n") },
	}}
	c := streamClient(t, s)
	var slept []time.Duration
	c.sleep = func(ctx context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	var names []string
	err := c.Stream(context.Background(), "/x", fastOpts(), func(e Event) error {
		names = append(names, e.Name)
		if e.Name == "done" {
			return ErrStreamDone
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(names, []string{"line", "dropped", "done"}) {
		t.Errorf("events = %v", names)
	}
	if len(slept) != 0 {
		t.Errorf("slept %v before reconnecting after dropped", slept)
	}
	if ids := s.ids(); !reflect.DeepEqual(ids, []string{"", "0"}) {
		t.Errorf("ids = %q", ids)
	}
}

func TestBackoffGrowsAndResetsAfterProgress(t *testing.T) {
	s := &sseServer{script: []http.HandlerFunc{
		jsonReply(500, map[string]string{"error": "x"}),
		jsonReply(500, map[string]string{"error": "x"}),
		jsonReply(500, map[string]string{"error": "x"}),
		func(w http.ResponseWriter, r *http.Request) { writeSSE(w, ": hi\n\n") }, // progress, then EOF
		func(w http.ResponseWriter, r *http.Request) { writeSSE(w, "event: done\ndata: {}\n\n") },
	}}
	c := streamClient(t, s)
	var attempts []int
	var slept []time.Duration
	c.sleep = func(ctx context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	opts := StreamOptions{HeartbeatTimeout: time.Second}
	opts.Backoff.Min, opts.Backoff.Max = 100*time.Millisecond, 250*time.Millisecond
	opts.OnReconnect = func(a int, _ error) { attempts = append(attempts, a) }
	if err := c.Stream(context.Background(), "/x", opts, func(Event) error { return ErrStreamDone }); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(attempts, []int{1, 2, 3, 1}) {
		t.Errorf("attempts = %v", attempts)
	}
	for i, d := range slept {
		if d < opts.Backoff.Min || d > opts.Backoff.Max {
			t.Errorf("sleep %d = %v outside [min,max]", i, d)
		}
	}
	if slept[0] != opts.Backoff.Min {
		t.Errorf("first backoff = %v, want Min", slept[0])
	}
}

func TestStreamCtxCancelReturnsCanceled(t *testing.T) {
	base := runtime.NumGoroutine()
	release := make(chan struct{})
	s := &sseServer{script: []http.HandlerFunc{func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w, ": stream open\n\n")
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}}}
	srv := httptest.NewServer(s.handler())
	c := newClientFor(t, srv.URL, "s")
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		errc <- c.Stream(ctx, "/x", fastOpts(), func(Event) error { return nil })
	}()
	// A comment does not reach fn, so wait for the request itself.
	deadline := time.Now().Add(2 * time.Second)
	for len(s.ids()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	t0 := time.Now()
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v", err)
		}
		if d := time.Since(t0); d > 100*time.Millisecond {
			t.Errorf("returned after %v", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stream did not return after cancel")
	}
	close(release)
	srv.Close()
	c.unary.CloseIdleConnections()
	c.stream.CloseIdleConnections()
	var n int
	for i := 0; i < 50; i++ {
		if n = runtime.NumGoroutine(); n <= base+1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("goroutines = %d, baseline %d", n, base)
}

func TestStreamSendsSessionAndAcceptHeaders(t *testing.T) {
	var hdr atomic.Value
	s := &sseServer{script: []http.HandlerFunc{func(w http.ResponseWriter, r *http.Request) {
		hdr.Store(r.Header.Clone())
		writeSSE(w, "event: done\ndata: {}\n\n")
	}}}
	c := streamClient(t, s)
	_ = c.Stream(context.Background(), "/x", fastOpts(), func(Event) error { return ErrStreamDone })
	h := hdr.Load().(http.Header)
	if h.Get("Cookie") != "stackstead_session=sess" || h.Get("Accept") != "text/event-stream" {
		t.Errorf("headers = %v", h)
	}
}
