package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
)

const t9Session = "T9-SESSION-VALUE-0123456789"

func init() { streamBackoff.Min, streamBackoff.Max = time.Millisecond, 2*time.Millisecond }

func t9Root() *Command {
	return &Command{Name: "stackstead", Sub: []*Command{logsCommand(), metricsCommand(), eventsCommand()}}
}

func t9Run(ctx context.Context, te *testEnv, args ...string) error {
	return Run(ctx, t9Root(), args, te.Env)
}

// t9Ev renders one server-sent event frame.
func t9Ev(name, id string, data any) string {
	b, _ := json.Marshal(data)
	s := "event: " + name + "\n"
	if id != "" {
		s += "id: " + id + "\n"
	}
	return s + "data: " + string(b) + "\n\n"
}

// t9Conn scripts one connection: frames are sent, then the connection closes
// (or, with Hold, stays open until the client goes away). Status != 0 answers
// with that status and a JSON error instead.
type t9Conn struct {
	Frames []string
	Hold   bool
	Status int
}

type t9Req struct {
	Path   string
	Query  url.Values
	LastID string
}

type t9Stub struct {
	*httptest.Server
	mu    sync.Mutex
	reqs  []t9Req
	conns []t9Conn
}

func (s *t9Stub) Requests() []t9Req {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.reqs)
}

// t9Serve answers every request with the next scripted connection; once the
// script is used up, connections are held open.
func t9Serve(t *testing.T, conns ...t9Conn) *t9Stub {
	t.Helper()
	s := &t9Stub{conns: conns}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		n := len(s.reqs)
		s.reqs = append(s.reqs, t9Req{r.URL.Path, r.URL.Query(), r.Header.Get("Last-Event-ID")})
		c := t9Conn{Hold: true}
		if n < len(s.conns) {
			c = s.conns[n]
		}
		s.mu.Unlock()
		if c.Status != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(c.Status)
			fmt.Fprint(w, `{"error":"scripted failure"}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl := w.(http.Flusher)
		for _, f := range c.Frames {
			fmt.Fprint(w, f)
			fl.Flush()
		}
		if c.Hold {
			<-r.Context().Done()
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func t9Seed(t *testing.T, te *testEnv, url string) *ContextStore {
	t.Helper()
	st := newContextStore(te.Env)
	if err := st.Save(Contexts{Version: 1, Current: "lab", Contexts: map[string]Context{
		"lab": {URL: url, Username: "admin", Session: t9Session, ExpiresAt: te.Now().Add(time.Hour)},
	}}); err != nil {
		t.Fatal(err)
	}
	return st
}

func t9Log(service, container, stream, text string) apitypes.LogEvent {
	return apitypes.LogEvent{Service: service, Container: container, Stream: stream,
		Time: time.Date(2026, 10, 14, 11, 59, 30, 0, time.UTC), Text: text}
}

func TestLogsPrintsServicePrefixedLines(t *testing.T) {
	srv := t9Serve(t, t9Conn{Frames: []string{
		t9Ev("log", "aaaaaaaaaaaa:1:1", t9Log("web", "aaaaaaaaaaaa", "stdout", "hello")),
		t9Ev("log", "bbbbbbbbbbbb:2:1", t9Log("db", "bbbbbbbbbbbb", "stderr", "world")),
		t9Ev("end", "", struct{}{}),
	}})
	te := newTestEnv(t, nil)
	t9Seed(t, te, srv.URL)
	if err := t9Run(context.Background(), te, "logs", "demo"); err != nil {
		t.Fatal(err)
	}
	if got, want := te.Out.String(), "[web] hello\n[db] world\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if te.Err.String() != "" {
		t.Errorf("stderr = %q", te.Err.String())
	}
	if reqs := srv.Requests(); len(reqs) != 1 || reqs[0].Path != "/api/stacks/demo/logs" {
		t.Errorf("requests = %+v", reqs)
	}
}

func TestLogsStderrLinesAreDimRedWithColour(t *testing.T) {
	srv := t9Serve(t, t9Conn{Frames: []string{
		t9Ev("log", "", t9Log("web", "aaaaaaaaaaaa", "stdout", "fine")),
		t9Ev("log", "", t9Log("web", "aaaaaaaaaaaa", "stderr", "oops")),
		t9Ev("end", "", struct{}{}),
	}})
	te := newTestEnv(t, nil)
	t9Seed(t, te, srv.URL)
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	te.Stdout = f
	te.IsTerminal = func(uintptr) bool { return true }
	if err := t9Run(context.Background(), te, "logs", "demo"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(f.Name())
	var fine, oops string
	for _, l := range strings.Split(string(b), "\n") {
		switch {
		case strings.Contains(l, "fine"):
			fine = l
		case strings.Contains(l, "oops"):
			oops = l
		}
	}
	if !strings.Contains(oops, "\x1b[2;31m") {
		t.Errorf("stderr line not dim red: %q", oops)
	}
	if strings.Contains(fine, "\x1b[2;31m") || !strings.Contains(fine, "\x1b[") {
		t.Errorf("stdout line should have a coloured prefix only: %q", fine)
	}
	if stripANSI(fine) != "[web] fine" {
		t.Errorf("stripped = %q", stripANSI(fine))
	}
}

func TestLogsTimestamps(t *testing.T) {
	srv := t9Serve(t, t9Conn{Frames: []string{
		t9Ev("log", "", t9Log("web", "aaaaaaaaaaaa", "stdout", "hello")),
		t9Ev("end", "", struct{}{}),
	}})
	te := newTestEnv(t, nil)
	t9Seed(t, te, srv.URL)
	if err := t9Run(context.Background(), te, "logs", "demo", "--timestamps"); err != nil {
		t.Fatal(err)
	}
	if got, want := te.Out.String(), "2026-10-14T11:59:30.000000000Z [web] hello\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestLogsPassesQueryThrough(t *testing.T) {
	srv := t9Serve(t, t9Conn{Frames: []string{t9Ev("end", "", struct{}{})}})
	te := newTestEnv(t, nil)
	t9Seed(t, te, srv.URL)
	if err := t9Run(context.Background(), te, "logs", "demo", "--tail", "all", "--since", "10m", "--service", "web"); err != nil {
		t.Fatal(err)
	}
	reqs := srv.Requests()
	if len(reqs) == 0 {
		t.Fatal("no request made")
	}
	q := reqs[0].Query
	if q.Get("tail") != "all" || q.Get("since") != "10m" || q.Get("service") != "web" || q.Get("follow") != "" {
		t.Errorf("query = %v", q)
	}
}

func TestLogsRejectsBadTail(t *testing.T) {
	for _, tail := range []string{"abc", "-1", "1.5"} {
		te := newTestEnv(t, nil)
		t9Seed(t, te, "http://127.0.0.1:1")
		err := t9Run(context.Background(), te, "logs", "demo", "--tail", tail)
		if codeFor(err) != ExitUsage {
			t.Errorf("--tail %q: code = %d, err = %v", tail, codeFor(err), err)
		}
	}
}

func TestLogsEndsOnEndWithoutFollow(t *testing.T) {
	srv := t9Serve(t, t9Conn{Frames: []string{
		t9Ev("log", "aaaaaaaaaaaa:1:1", t9Log("web", "aaaaaaaaaaaa", "stdout", "one")),
		t9Ev("end", "", struct{}{}),
	}, Hold: true})
	te := newTestEnv(t, nil)
	t9Seed(t, te, srv.URL)
	done := make(chan error, 1)
	go func() { done <- t9Run(context.Background(), te, "logs", "demo") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("logs did not stop on the end event")
	}
	if n := len(srv.Requests()); n != 1 {
		t.Errorf("requests = %d, want 1", n)
	}
}

func TestLogsFollowResumesWithAllCursors(t *testing.T) {
	a, b := "aaaaaaaaaaaa", "bbbbbbbbbbbb"
	srv := t9Serve(t,
		t9Conn{Frames: []string{
			t9Ev("log", a+":ts1:1", t9Log("web", a, "stdout", "a1")),
			t9Ev("log", b+":ts2:1", t9Log("db", b, "stdout", "b1")),
			t9Ev("log", a+":ts2:2", t9Log("web", a, "stdout", "a2")),
		}},
		t9Conn{Frames: []string{
			t9Ev("log", a+":ts3:1", t9Log("web", a, "stdout", "a3")),
			t9Ev("end", "", struct{}{}),
		}},
	)
	te := newTestEnv(t, nil)
	t9Seed(t, te, srv.URL)
	if err := t9Run(context.Background(), te, "logs", "demo", "-f"); err != nil {
		t.Fatal(err)
	}
	reqs := srv.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	if reqs[0].LastID != "" || reqs[0].Query.Get("follow") != "true" {
		t.Errorf("first request = %+v", reqs[0])
	}
	got := strings.Split(reqs[1].LastID, ",")
	slices.Sort(got)
	if want := []string{a + ":ts2:2", b + ":ts2:1"}; !slices.Equal(got, want) {
		t.Errorf("Last-Event-ID = %q, want exactly %v", reqs[1].LastID, want)
	}
	if !strings.Contains(te.Err.String(), "reconnect") {
		t.Errorf("no reconnect note on stderr: %q", te.Err.String())
	}
	if want := "[web] a1\n[db] b1\n[web] a2\n[web] a3\n"; te.Out.String() != want {
		t.Errorf("stdout = %q, want %q", te.Out.String(), want)
	}
}

func TestLogCursorsKeepLatestIDPerContainer(t *testing.T) {
	var c logCursors
	for _, id := range []string{"aaaaaaaaaaaa:1:1", "bbbbbbbbbbbb:2:1", "aaaaaaaaaaaa:2:2", "malformed", ""} {
		c.Set(id)
	}
	got := strings.Split(c.String(), ",")
	slices.Sort(got)
	if want := []string{"aaaaaaaaaaaa:2:2", "bbbbbbbbbbbb:2:1"}; !slices.Equal(got, want) {
		t.Errorf("String() = %q, want %v", c.String(), want)
	}
	var empty logCursors
	if empty.String() != "" {
		t.Errorf("empty String() = %q", empty.String())
	}
}

func TestLogsErrorEventGoesToStderr(t *testing.T) {
	srv := t9Serve(t, t9Conn{Frames: []string{
		t9Ev("error", "", apitypes.LogError{Service: "web", Container: "aaaaaaaaaaaa", Error: "container gone\x1b[2J"}),
		t9Ev("log", "", t9Log("db", "bbbbbbbbbbbb", "stdout", "still here")),
		t9Ev("end", "", struct{}{}),
	}})
	te := newTestEnv(t, nil)
	t9Seed(t, te, srv.URL)
	if err := t9Run(context.Background(), te, "logs", "demo"); err != nil {
		t.Fatal(err)
	}
	if got, want := te.Err.String(), "log stream error for aaaaaaaaaaaa: container gone\n"; got != want {
		t.Errorf("stderr = %q, want %q (escape sequences stripped)", got, want)
	}
	if te.Out.String() != "[db] still here\n" {
		t.Errorf("stdout = %q", te.Out.String())
	}
}

func TestLogsJSONNDJSON(t *testing.T) {
	srv := t9Serve(t, t9Conn{Frames: []string{
		t9Ev("log", "aaaaaaaaaaaa:1:1", t9Log("web", "aaaaaaaaaaaa", "stdout", "hi\x1b[31m")),
		t9Ev("error", "", apitypes.LogError{Container: "bbbbbbbbbbbb", Error: "boom"}),
		t9Ev("end", "", struct{}{}),
	}})
	te := newTestEnv(t, nil)
	t9Seed(t, te, srv.URL)
	if err := t9Run(context.Background(), te, "logs", "demo", "-o", "json"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(te.Out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %q", lines)
	}
	var names []string
	for _, l := range lines {
		var v struct {
			Event string          `json:"event"`
			ID    string          `json:"id"`
			Data  json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal([]byte(l), &v); err != nil {
			t.Fatalf("line %q: %v", l, err)
		}
		names = append(names, v.Event)
		if v.Event == "log" {
			var le apitypes.LogEvent
			if err := json.Unmarshal(v.Data, &le); err != nil || le.Text != "hi\x1b[31m" || v.ID != "aaaaaaaaaaaa:1:1" {
				t.Errorf("log line = %q (JSON keeps the wire text verbatim)", l)
			}
		}
	}
	if !slices.Equal(names, []string{"log", "error", "end"}) {
		t.Errorf("events = %v", names)
	}
	if te.Err.String() != "" {
		t.Errorf("stderr = %q", te.Err.String())
	}
}

func TestLogsStripsTerminalEscapesUnlessRaw(t *testing.T) {
	nasty := "ok\x1b]0;pwned\x07\x1b[31mred\x1b[0m\x1b[2J\x1b(Bz\rq\x00\u009b1m"
	frames := []string{
		t9Ev("log", "", t9Log("we\x1b[31mb", "aaaaaaaaaaaa", "stdout", nasty)),
		t9Ev("end", "", struct{}{}),
	}
	te := newTestEnv(t, nil)
	srv := t9Serve(t, t9Conn{Frames: frames})
	t9Seed(t, te, srv.URL)
	if err := t9Run(context.Background(), te, "logs", "demo"); err != nil {
		t.Fatal(err)
	}
	if got, want := te.Out.String(), "[web] okredzq\n"; got != want {
		t.Errorf("sanitised stdout = %q, want %q", got, want)
	}

	te = newTestEnv(t, nil)
	srv = t9Serve(t, t9Conn{Frames: frames})
	t9Seed(t, te, srv.URL)
	if err := t9Run(context.Background(), te, "logs", "demo", "--raw"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(te.Out.String(), "\x1b]0;pwned\x07") {
		t.Errorf("--raw must pass the text through: %q", te.Out.String())
	}
}

func TestLogs401MidStreamExits3(t *testing.T) {
	srv := t9Serve(t,
		t9Conn{Frames: []string{t9Ev("log", "aaaaaaaaaaaa:1:1", t9Log("web", "aaaaaaaaaaaa", "stdout", "one"))}},
		t9Conn{Status: 401},
	)
	te := newTestEnv(t, nil)
	st := t9Seed(t, te, srv.URL)
	err := t9Run(context.Background(), te, "logs", "demo", "-f")
	if codeFor(err) != ExitAuth {
		t.Fatalf("code = %d, err = %v", codeFor(err), err)
	}
	c, lerr := st.Load()
	if lerr != nil {
		t.Fatal(lerr)
	}
	if c.Contexts["lab"].Session != "" {
		t.Error("the dead session should be cleared")
	}
	if te.Out.String() != "[web] one\n" {
		t.Errorf("stdout = %q", te.Out.String())
	}
}

func TestLogsTimestampsAreFixedWidth(t *testing.T) {
	ev := t9Log("web", "aaaaaaaaaaaa", "stdout", "hello")
	ev.Time = time.Date(2026, 10, 14, 11, 59, 30, 500_000_000, time.UTC)
	srv := t9Serve(t, t9Conn{Frames: []string{t9Ev("log", "", ev), t9Ev("end", "", struct{}{})}})
	te := newTestEnv(t, nil)
	t9Seed(t, te, srv.URL)
	if err := t9Run(context.Background(), te, "logs", "demo", "--timestamps"); err != nil {
		t.Fatal(err)
	}
	if got, want := te.Out.String(), "2026-10-14T11:59:30.500000000Z [web] hello\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestLogsTailPlusSignIsNormalised(t *testing.T) {
	srv := t9Serve(t, t9Conn{Frames: []string{t9Ev("end", "", struct{}{})}})
	te := newTestEnv(t, nil)
	t9Seed(t, te, srv.URL)
	if err := t9Run(context.Background(), te, "logs", "demo", "--tail", "+5"); err != nil {
		t.Fatal(err)
	}
	if reqs := srv.Requests(); len(reqs) != 1 || reqs[0].Query.Get("tail") != "5" {
		t.Errorf("requests = %+v, want tail=5", reqs)
	}
}

func TestLogsFollowJSONResumesWithCursors(t *testing.T) {
	a := "aaaaaaaaaaaa"
	srv := t9Serve(t,
		t9Conn{Frames: []string{t9Ev("log", a+":ts1:1", t9Log("web", a, "stdout", "a1"))}},
		t9Conn{Frames: []string{t9Ev("end", "", struct{}{})}},
	)
	te := newTestEnv(t, nil)
	t9Seed(t, te, srv.URL)
	if err := t9Run(context.Background(), te, "logs", "demo", "-f", "-o", "json"); err != nil {
		t.Fatal(err)
	}
	reqs := srv.Requests()
	if len(reqs) != 2 || reqs[1].LastID != a+":ts1:1" {
		t.Errorf("requests = %+v, want the second to resume from the cursor", reqs)
	}
}

func TestLogsNamesCannotForgeLines(t *testing.T) {
	srv := t9Serve(t, t9Conn{Frames: []string{
		t9Ev("log", "", t9Log("web] real\n[db", "aaaaaaaaaaaa", "stdout", "x")),
		t9Ev("error", "", apitypes.LogError{Container: "c1\n[db] forged\x1b[2J", Error: "bad\nline"}),
		t9Ev("end", "", struct{}{}),
	}})
	te := newTestEnv(t, nil)
	t9Seed(t, te, srv.URL)
	if err := t9Run(context.Background(), te, "logs", "demo"); err != nil {
		t.Fatal(err)
	}
	if got, want := te.Out.String(), "[web] real [db] x\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if got, want := te.Err.String(), "log stream error for c1 [db] forged: bad line\n"; got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
}
