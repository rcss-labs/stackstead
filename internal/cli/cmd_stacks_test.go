package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
)

const t8Session = "T8-SESSION-VALUE-0123456789"

func t8Root() *Command {
	return &Command{Name: "stackstead", Sub: []*Command{stacksCommand(), opsCommand(), networksCommand()}}
}

// t8Setup returns an env logged in to a stub API serving routes ("METHOD /path").
func t8Setup(t *testing.T, routes map[string]http.HandlerFunc) *testEnv {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, ok := routes[r.Method+" "+r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	te := newTestEnv(t, nil)
	if err := newContextStore(te.Env).Save(Contexts{Version: 1, Current: "lab", Contexts: map[string]Context{
		"lab": {URL: srv.URL, Username: "admin", Session: t8Session, ExpiresAt: te.Now().Add(time.Hour)},
	}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { opStreamBackoff.Min, opStreamBackoff.Max = 0, 0 })
	opStreamBackoff.Min, opStreamBackoff.Max = 5*time.Millisecond, 10*time.Millisecond
	return te
}

func t8Run(te *testEnv, args ...string) error {
	return Run(context.Background(), t8Root(), args, te.Env)
}

func t8JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type t8Event struct{ id, name, data string }

func t8Line(id int, text string) t8Event {
	b, _ := json.Marshal(text)
	return t8Event{fmt.Sprint(id), "line", string(b)}
}

func t8Done(op apitypes.Operation) t8Event {
	b, _ := json.Marshal(op)
	return t8Event{"", "done", string(b)}
}

// t8Stream scripts an SSE endpoint: request n (0-based) is answered with
// scripts[n] and then closed. It records each request's Last-Event-ID.
type t8Stream struct {
	mu      sync.Mutex
	scripts [][]t8Event
	lastIDs []string
}

func (s *t8Stream) handler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	n := len(s.lastIDs)
	s.lastIDs = append(s.lastIDs, r.Header.Get("Last-Event-ID"))
	s.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	fl := w.(http.Flusher)
	if n >= len(s.scripts) {
		return
	}
	for _, ev := range s.scripts[n] {
		if ev.id != "" {
			fmt.Fprintf(w, "id: %s\n", ev.id)
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.name, ev.data)
		fl.Flush()
	}
}

func (s *t8Stream) ids() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.lastIDs...)
}

func t8Op(state string) apitypes.Operation {
	return apitypes.Operation{ID: "op1", Stack: "web", Action: "up", State: state, StartedAt: time.Date(2026, 10, 14, 11, 59, 0, 0, time.UTC)}
}

// t8UpRoutes serves POST up -> 202 op1 and op1's stream from st.
func t8UpRoutes(st *t8Stream) map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"POST /api/stacks/web/actions/up": func(w http.ResponseWriter, r *http.Request) { t8JSON(w, 202, t8Op("running")) },
		"GET /api/operations/op1/stream":  st.handler,
	}
}

func t8Code(err error) int { return codeFor(err) }

func TestStacksLsTableAndJSON(t *testing.T) {
	stacks := []apitypes.StackSummary{
		{Name: "api", Path: "/opt/stacks/api", Project: "api", Origin: "managed", Status: "running", ServiceCount: 2, Running: 2},
		{Name: "web", Path: "/opt/stacks/web", Project: "web", Origin: "imported", Status: "stopped", ServiceCount: 1, Running: 0},
	}
	te := t8Setup(t, map[string]http.HandlerFunc{"GET /api/stacks": func(w http.ResponseWriter, r *http.Request) { t8JSON(w, 200, stacks) }})
	if err := t8Run(te, "stacks", "ls"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(te.Out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("stdout = %q, want header + 2 rows", te.Out.String())
	}
	if got := strings.Fields(lines[0]); strings.Join(got, " ") != "NAME STATUS SERVICES RUNNING ORIGIN PATH" {
		t.Errorf("header = %v", got)
	}
	if got := strings.Join(strings.Fields(lines[1]), " "); got != "api running 2 2 managed /opt/stacks/api" {
		t.Errorf("row 1 = %q", got)
	}
	if got := strings.Join(strings.Fields(lines[2]), " "); got != "web stopped 1 0 imported /opt/stacks/web" {
		t.Errorf("row 2 = %q", got)
	}

	te.Out.buf.Reset()
	if err := t8Run(te, "stacks", "ls", "-o", "json"); err != nil {
		t.Fatal(err)
	}
	var got []apitypes.StackSummary
	if err := json.Unmarshal([]byte(te.Out.String()), &got); err != nil || len(got) != 2 || got[1].Name != "web" {
		t.Errorf("json = %q (%v)", te.Out.String(), err)
	}
}

func TestStacksLsEmpty(t *testing.T) {
	te := t8Setup(t, map[string]http.HandlerFunc{"GET /api/stacks": func(w http.ResponseWriter, r *http.Request) { t8JSON(w, 200, []apitypes.StackSummary{}) }})
	if err := t8Run(te, "stacks", "ls"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(te.Err.String(), "no stacks") {
		t.Errorf("stderr = %q, want a 'no stacks' note", te.Err.String())
	}
	if !strings.HasPrefix(te.Out.String(), "NAME") || strings.Count(te.Out.String(), "\n") != 1 {
		t.Errorf("stdout = %q, want only the header", te.Out.String())
	}
	te.Out.buf.Reset()
	if err := t8Run(te, "stacks", "ls", "-o", "json"); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(te.Out.String()) != "[]" {
		t.Errorf("json = %q, want []", te.Out.String())
	}
}

func TestStacksShowRendersServicesAndContainers(t *testing.T) {
	detail := apitypes.StackDetail{
		Name: "web", Path: "/opt/stacks/web", Project: "web-proj", Origin: "managed", Status: "running", Hash: "abc123",
		Services: []apitypes.ServiceDetail{{
			Name:     "app",
			Config:   apitypes.ServiceConfig{Image: "nginx:1.27", Ports: []string{"8080:80"}, Limits: apitypes.Limits{CPUs: "0.5", Memory: "256m"}},
			ReadOnly: []string{"healthcheck"},
			Containers: []apitypes.ContainerInfo{
				{ID: "c1", Name: "web-app-1", Service: "app", State: "running", Status: "Up 2 minutes"},
			},
		}, {
			Name:   "builder",
			Config: apitypes.ServiceConfig{Build: &apitypes.Build{Context: "./b"}},
		}},
	}
	te := t8Setup(t, map[string]http.HandlerFunc{"GET /api/stacks/web": func(w http.ResponseWriter, r *http.Request) { t8JSON(w, 200, detail) }})
	if err := t8Run(te, "stacks", "show", "web"); err != nil {
		t.Fatal(err)
	}
	out := te.Out.String()
	for _, want := range []string{"web-proj", "managed", "/opt/stacks/web", "running", "abc123",
		"SERVICE", "IMAGE/BUILD", "PORTS", "LIMITS", "READ-ONLY", "nginx:1.27", "8080:80", "healthcheck", "256m",
		"build: ./b", "CONTAINER", "web-app-1", "Up 2 minutes"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	te.Out.buf.Reset()
	if err := t8Run(te, "stacks", "show", "web", "-o", "json"); err != nil {
		t.Fatal(err)
	}
	var got apitypes.StackDetail
	if err := json.Unmarshal([]byte(te.Out.String()), &got); err != nil || got.Hash != "abc123" {
		t.Errorf("json = %q (%v)", te.Out.String(), err)
	}
}

func TestStacksShowPrintsParseError(t *testing.T) {
	te := t8Setup(t, map[string]http.HandlerFunc{"GET /api/stacks/web": func(w http.ResponseWriter, r *http.Request) {
		t8JSON(w, 200, apitypes.StackDetail{Name: "web", ParseError: "yaml: line 3: mapping values are not allowed"})
	}})
	if err := t8Run(te, "stacks", "show", "web"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(te.Out.String(), "yaml: line 3") {
		t.Errorf("output lacks the parse error:\n%s", te.Out.String())
	}
}

func TestUpStreamsAndExits0(t *testing.T) {
	done := t8Op("succeeded")
	st := &t8Stream{scripts: [][]t8Event{{t8Line(0, "pulling"), t8Line(1, "creating"), t8Line(2, "started"), t8Done(done)}}}
	te := t8Setup(t, t8UpRoutes(st))
	if err := t8Run(te, "stacks", "up", "web"); err != nil {
		t.Fatal(err)
	}
	if te.Out.String() != "pulling\ncreating\nstarted\n" {
		t.Errorf("stdout = %q", te.Out.String())
	}
	if !strings.Contains(te.Err.String(), "up web succeeded") {
		t.Errorf("stderr = %q, want a summary", te.Err.String())
	}
	if strings.Contains(te.Out.String()+te.Err.String(), t8Session) {
		t.Error("session leaked")
	}
}

func TestUpFailedOperationExits9(t *testing.T) {
	failed := t8Op("failed")
	failed.Error = "exit status 1"
	st := &t8Stream{scripts: [][]t8Event{{t8Line(0, "boom"), t8Done(failed)}}}
	te := t8Setup(t, t8UpRoutes(st))
	err := t8Run(te, "stacks", "up", "web")
	if t8Code(err) != ExitOperationFailed {
		t.Fatalf("code = %d (%v), want 9", t8Code(err), err)
	}
	if err == nil || err.Error() != "up web failed: exit status 1" {
		t.Errorf("error = %v", err)
	}
	if te.Out.String() != "boom\n" {
		t.Errorf("stdout = %q", te.Out.String())
	}
}

func TestUpDetachPrintsID(t *testing.T) {
	// No stream route: a request to it would 404 and fail the command.
	te := t8Setup(t, map[string]http.HandlerFunc{
		"POST /api/stacks/web/actions/up": func(w http.ResponseWriter, r *http.Request) { t8JSON(w, 202, t8Op("running")) },
	})
	if err := t8Run(te, "stacks", "up", "web", "--detach"); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(te.Out.String()) != "operation op1 started" {
		t.Errorf("stdout = %q", te.Out.String())
	}
	te.Out.buf.Reset()
	if err := t8Run(te, "stacks", "up", "web", "--detach", "-o", "json"); err != nil {
		t.Fatal(err)
	}
	var op apitypes.Operation
	if err := json.Unmarshal([]byte(te.Out.String()), &op); err != nil || op.ID != "op1" {
		t.Errorf("json = %q (%v)", te.Out.String(), err)
	}
}

func TestUpBusyExits5(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"no message", `{}`, "stack web is busy: another operation is running"},
		{"server message", `{"error":"stack web is being removed"}`, "stack web is being removed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			te := t8Setup(t, map[string]http.HandlerFunc{"POST /api/stacks/web/actions/up": func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(409)
				io.WriteString(w, tc.body)
			}})
			err := t8Run(te, "stacks", "up", "web")
			if t8Code(err) != ExitConflict || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("code = %d, err = %v, want 5 containing %q", t8Code(err), err, tc.want)
			}
		})
	}
}

func TestUpUnavailableExits7AndMissingExits4(t *testing.T) {
	for status, want := range map[int]int{503: ExitUnavailable, 404: ExitNotFound} {
		te := t8Setup(t, map[string]http.HandlerFunc{"POST /api/stacks/web/actions/up": func(w http.ResponseWriter, r *http.Request) {
			t8JSON(w, status, apitypes.ErrorBody{Error: "nope"})
		}})
		if err := t8Run(te, "stacks", "up", "web"); t8Code(err) != want {
			t.Errorf("status %d: code = %d (%v), want %d", status, t8Code(err), err, want)
		}
	}
}

func TestUpUnauthorizedExits3(t *testing.T) {
	te := t8Setup(t, map[string]http.HandlerFunc{"POST /api/stacks/web/actions/up": func(w http.ResponseWriter, r *http.Request) {
		t8JSON(w, 401, apitypes.ErrorBody{Error: "unauthorized"})
	}})
	if err := t8Run(te, "stacks", "up", "web"); t8Code(err) != ExitAuth {
		t.Errorf("code = %d (%v), want 3", t8Code(err), err)
	}
}

func TestUpWaitsAcrossDisconnectWithoutDuplicateLines(t *testing.T) {
	st := &t8Stream{scripts: [][]t8Event{
		{t8Line(0, "l0"), t8Line(1, "l1")}, // server closes after line 2 (id 1)
		{t8Line(2, "l2"), t8Line(3, "l3"), t8Line(4, "l4"), t8Done(t8Op("succeeded"))},
	}}
	te := t8Setup(t, t8UpRoutes(st))
	if err := t8Run(te, "stacks", "up", "web"); err != nil {
		t.Fatal(err)
	}
	if te.Out.String() != "l0\nl1\nl2\nl3\nl4\n" {
		t.Errorf("stdout = %q, want lines 0-4 once", te.Out.String())
	}
	if ids := st.ids(); len(ids) != 2 || ids[0] != "" || ids[1] != "1" {
		t.Errorf("Last-Event-ID per request = %q, want [\"\" \"1\"]", ids)
	}
	if !strings.Contains(te.Err.String(), "reconnect") {
		t.Errorf("stderr = %q, want a reconnect note", te.Err.String())
	}
}

func TestUpDroppedReconnectsAndReplays(t *testing.T) {
	note := t8Event{"", "line", `"... 3 earlier lines dropped"`}
	st := &t8Stream{scripts: [][]t8Event{
		{note, t8Line(3, "l3"), {"", "dropped", "{}"}},
		{t8Line(4, "l4"), t8Done(t8Op("succeeded"))},
	}}
	te := t8Setup(t, t8UpRoutes(st))
	if err := t8Run(te, "stacks", "up", "web"); err != nil {
		t.Fatal(err)
	}
	if te.Out.String() != "... 3 earlier lines dropped\nl3\nl4\n" {
		t.Errorf("stdout = %q", te.Out.String())
	}
	if ids := st.ids(); len(ids) != 2 || ids[1] != "3" {
		t.Errorf("Last-Event-ID per request = %q, want second = 3", ids)
	}
}

func TestStreamStripsControlCharacters(t *testing.T) {
	st := &t8Stream{scripts: [][]t8Event{{t8Line(0, "evil\x1b[2J\x1b]0;pwned\x07 text\r"), t8Done(t8Op("succeeded"))}}}
	te := t8Setup(t, t8UpRoutes(st))
	if err := t8Run(te, "stacks", "up", "web"); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(te.Out.String(), "\x1b\x07\r") {
		t.Errorf("stdout still has control characters: %q", te.Out.String())
	}
	if !strings.Contains(te.Out.String(), "text") {
		t.Errorf("stdout = %q, lost the text", te.Out.String())
	}
}

func TestUpJSONIsNDJSON(t *testing.T) {
	st := &t8Stream{scripts: [][]t8Event{{t8Line(0, "a"), t8Line(1, "b"), t8Done(t8Op("succeeded"))}}}
	te := t8Setup(t, t8UpRoutes(st))
	if err := t8Run(te, "stacks", "up", "web", "-o", "json"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(te.Out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("stdout = %q, want 3 NDJSON lines", te.Out.String())
	}
	var first struct {
		Event string `json:"event"`
		ID    string `json:"id"`
		Data  string `json:"data"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &first); err != nil || first.Event != "line" || first.ID != "1" || first.Data != "b" {
		t.Errorf("line 2 = %q (%v)", lines[1], err)
	}
	var last struct {
		Event string             `json:"event"`
		Data  apitypes.Operation `json:"data"`
	}
	if err := json.Unmarshal([]byte(lines[2]), &last); err != nil || last.Event != "done" || last.Data.State != "succeeded" {
		t.Errorf("last = %q (%v)", lines[2], err)
	}
}

func TestRmNeedsConfirmation(t *testing.T) {
	var mu sync.Mutex
	var deletes []string
	st := &t8Stream{scripts: [][]t8Event{{t8Line(0, "removing"), t8Done(t8Op("succeeded"))}}}
	routes := map[string]http.HandlerFunc{
		"DELETE /api/stacks/web": func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			deletes = append(deletes, r.URL.RawQuery)
			mu.Unlock()
			t8JSON(w, 202, t8Op("running"))
		},
		"GET /api/operations/op1/stream": st.handler,
	}
	te := t8Setup(t, routes)
	err := t8Run(te, "stacks", "rm", "web", "--volumes")
	if t8Code(err) != ExitUsage {
		t.Fatalf("code = %d (%v), want 2 without --yes on a non-TTY", t8Code(err), err)
	}
	if len(deletes) != 0 {
		t.Fatalf("DELETE sent without confirmation: %v", deletes)
	}
	if err := t8Run(te, "stacks", "rm", "web", "--volumes", "--yes"); err != nil {
		t.Fatal(err)
	}
	if len(deletes) != 1 || deletes[0] != "volumes=true" {
		t.Errorf("DELETE queries = %q, want [volumes=true]", deletes)
	}
	if !strings.Contains(te.Out.String(), "removing") {
		t.Errorf("stdout = %q, want the operation output", te.Out.String())
	}
}

func TestRmPromptAndDecline(t *testing.T) {
	deleted := false
	te := t8Setup(t, map[string]http.HandlerFunc{"DELETE /api/stacks/web": func(w http.ResponseWriter, r *http.Request) {
		deleted = true
		t8JSON(w, 202, t8Op("running"))
	}})
	te.Env.IsTerminal = func(uintptr) bool { return true }
	te.Stdin = strings.NewReader("n\n")
	if err := t8Run(te, "stacks", "rm", "web"); err != nil {
		t.Fatal(err)
	}
	if deleted {
		t.Error("stack removed although the answer was no")
	}
	if !strings.Contains(te.Err.String(), "remove stack web (compose down); files stay on disk? [y/N]") {
		t.Errorf("prompt = %q", te.Err.String())
	}
}

func TestImportNotesPartialChecks(t *testing.T) {
	var gotBody apitypes.ImportRequest
	te := t8Setup(t, map[string]http.HandlerFunc{"POST /api/stacks/import": func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("X-Stackstead-Checks", "partial")
		t8JSON(w, 201, apitypes.StackSummary{Name: "legacy", Path: "/srv/legacy", Project: "legacy", Origin: "imported", Status: "stopped"})
	}})
	if err := t8Run(te, "stacks", "import", "/srv/legacy"); err != nil {
		t.Fatal(err)
	}
	if gotBody.Path != "/srv/legacy" {
		t.Errorf("request path = %q", gotBody.Path)
	}
	if !strings.Contains(te.Out.String(), "legacy") {
		t.Errorf("stdout = %q, want the stack summary", te.Out.String())
	}
	if !strings.Contains(te.Err.String(), "note: port checks were partial (Docker unavailable)") {
		t.Errorf("stderr = %q", te.Err.String())
	}
}

func TestComposeGetPrintsYAML(t *testing.T) {
	te := t8Setup(t, map[string]http.HandlerFunc{"GET /api/stacks/web/compose": func(w http.ResponseWriter, r *http.Request) {
		t8JSON(w, 200, apitypes.RawCompose{Content: "services:\n  app:\n    image: nginx\n", Hash: "h1"})
	}})
	if err := t8Run(te, "stacks", "compose", "get", "web"); err != nil {
		t.Fatal(err)
	}
	if te.Out.String() != "services:\n  app:\n    image: nginx\n" {
		t.Errorf("stdout = %q", te.Out.String())
	}
	te.Out.buf.Reset()
	if err := t8Run(te, "stacks", "compose", "get", "web", "-o", "json"); err != nil {
		t.Fatal(err)
	}
	var raw apitypes.RawCompose
	if err := json.Unmarshal([]byte(te.Out.String()), &raw); err != nil || raw.Hash != "h1" {
		t.Errorf("json = %q (%v)", te.Out.String(), err)
	}
}

func TestComposePutSendsFileAndHash(t *testing.T) {
	var got apitypes.RawCompose
	te := t8Setup(t, map[string]http.HandlerFunc{"PUT /api/stacks/web/compose": func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		t8JSON(w, 200, apitypes.HashResponse{Hash: "h2"})
	}})
	file := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(file, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := t8Run(te, "stacks", "compose", "put", "web", "-f", file, "--hash", "h1"); err != nil {
		t.Fatal(err)
	}
	if got.Content != "services: {}\n" || got.Hash != "h1" {
		t.Errorf("body = %+v", got)
	}
	if !strings.Contains(te.Out.String(), "h2") {
		t.Errorf("stdout = %q, want the new hash", te.Out.String())
	}

	// "-" reads stdin.
	te.Stdin = strings.NewReader("services: {a: {}}\n")
	if err := t8Run(te, "stacks", "compose", "put", "web", "-f", "-", "--hash", "h2"); err != nil {
		t.Fatal(err)
	}
	if got.Content != "services: {a: {}}\n" || got.Hash != "h2" {
		t.Errorf("stdin body = %+v", got)
	}
}

func TestComposePutRequiresFileAndHash(t *testing.T) {
	te := t8Setup(t, nil)
	if err := t8Run(te, "stacks", "compose", "put", "web", "--hash", "h"); t8Code(err) != ExitUsage {
		t.Errorf("missing -f: code = %d (%v), want 2", t8Code(err), err)
	}
	if err := t8Run(te, "stacks", "compose", "put", "web", "-f", "x.yaml"); t8Code(err) != ExitUsage {
		t.Errorf("missing --hash: code = %d (%v), want 2", t8Code(err), err)
	}
}

func TestComposePutStaleHashExits5(t *testing.T) {
	te := t8Setup(t, map[string]http.HandlerFunc{"PUT /api/stacks/web/compose": func(w http.ResponseWriter, r *http.Request) {
		t8JSON(w, 409, apitypes.ErrorBody{Error: "stale hash"})
	}})
	file := filepath.Join(t.TempDir(), "c.yaml")
	_ = os.WriteFile(file, []byte("x: 1\n"), 0o600)
	err := t8Run(te, "stacks", "compose", "put", "web", "-f", file, "--hash", "old")
	if t8Code(err) != ExitConflict || err == nil || !strings.Contains(err.Error(), "compose file changed on the server: run stacks compose get and retry") {
		t.Errorf("code = %d, err = %v", t8Code(err), err)
	}
}

func TestComposePutInvalidExits6WithServerMessage(t *testing.T) {
	te := t8Setup(t, map[string]http.HandlerFunc{"PUT /api/stacks/web/compose": func(w http.ResponseWriter, r *http.Request) {
		t8JSON(w, 422, apitypes.ErrorBody{Error: "services.app.image: required"})
	}})
	file := filepath.Join(t.TempDir(), "c.yaml")
	_ = os.WriteFile(file, []byte("x: 1\n"), 0o600)
	err := t8Run(te, "stacks", "compose", "put", "web", "-f", file, "--hash", "h")
	if t8Code(err) != ExitValidation || err == nil || !strings.Contains(err.Error(), "services.app.image: required") {
		t.Errorf("code = %d, err = %v", t8Code(err), err)
	}
}

// t8Editor writes an executable EDITOR script with the given body.
func t8Editor(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "editor.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func t8EditSetup(t *testing.T, editor string, put http.HandlerFunc) (*testEnv, string) {
	t.Helper()
	routes := map[string]http.HandlerFunc{"GET /api/stacks/web/compose": func(w http.ResponseWriter, r *http.Request) {
		t8JSON(w, 200, apitypes.RawCompose{Content: "services: {}\n", Hash: "h1"})
	}}
	if put != nil {
		routes["PUT /api/stacks/web/compose"] = put
	}
	te := t8Setup(t, routes)
	vars := map[string]string{"EDITOR": editor}
	te.Env.Getenv = func(k string) string { return vars[k] }
	te.Env.IsTerminal = func(uintptr) bool { return true }
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	return te, tmp
}

func t8TempFiles(t *testing.T, dir string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, "stackstead-web-*.yaml"))
	return m
}

func TestComposeEditNoChanges(t *testing.T) {
	te, tmp := t8EditSetup(t, t8Editor(t, "exit 0"), nil) // a PUT would 404 and fail
	if err := t8Run(te, "stacks", "compose", "edit", "web"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(te.Err.String()+te.Out.String(), "no changes") {
		t.Errorf("output = %q / %q, want 'no changes'", te.Out.String(), te.Err.String())
	}
	if left := t8TempFiles(t, tmp); len(left) != 0 {
		t.Errorf("temp files left: %v", left)
	}
}

func TestComposeEditSavesWithOriginalHash(t *testing.T) {
	var got apitypes.RawCompose
	te, tmp := t8EditSetup(t, t8Editor(t, `echo "# edited" >> "$1"; stat -f %Lp "$1" > "$1.mode" 2>/dev/null || stat -c %a "$1" > "$1.mode"`), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		t8JSON(w, 200, apitypes.HashResponse{Hash: "h2"})
	})
	if err := t8Run(te, "stacks", "compose", "edit", "web"); err != nil {
		t.Fatal(err)
	}
	if got.Hash != "h1" || got.Content != "services: {}\n# edited\n" {
		t.Errorf("saved = %+v", got)
	}
	if !strings.Contains(te.Out.String()+te.Err.String(), "h2") {
		t.Errorf("output = %q / %q, want the new hash", te.Out.String(), te.Err.String())
	}
	m, _ := filepath.Glob(filepath.Join(tmp, "stackstead-web-*.yaml.mode"))
	if len(m) != 1 {
		t.Fatalf("mode probe files = %v", m)
	}
	if b, _ := os.ReadFile(m[0]); strings.TrimSpace(string(b)) != "600" {
		t.Errorf("temp file mode = %q, want 600", b)
	}
	_ = os.Remove(m[0])
	if left := t8TempFiles(t, tmp); len(left) != 0 {
		t.Errorf("temp file not deleted after success: %v", left)
	}
}

func TestComposeEditKeepsFileOnValidationError(t *testing.T) {
	te, tmp := t8EditSetup(t, t8Editor(t, `echo "# edited" >> "$1"`), func(w http.ResponseWriter, r *http.Request) {
		t8JSON(w, 422, apitypes.ErrorBody{Error: "bad yaml"})
	})
	err := t8Run(te, "stacks", "compose", "edit", "web")
	if t8Code(err) != ExitValidation || err == nil || !strings.Contains(err.Error(), "bad yaml") {
		t.Fatalf("code = %d, err = %v", t8Code(err), err)
	}
	left := t8TempFiles(t, tmp)
	if len(left) != 1 || !strings.Contains(err.Error(), left[0]) {
		t.Errorf("kept files = %v; error %q should name the kept file", left, err)
	}
}

func TestComposeEditConflictKeepsFile(t *testing.T) {
	te, tmp := t8EditSetup(t, t8Editor(t, `echo "# edited" >> "$1"`), func(w http.ResponseWriter, r *http.Request) {
		t8JSON(w, 409, apitypes.ErrorBody{Error: "stale"})
	})
	err := t8Run(te, "stacks", "compose", "edit", "web")
	left := t8TempFiles(t, tmp)
	if t8Code(err) != ExitConflict || len(left) != 1 || !strings.Contains(err.Error(), left[0]) {
		t.Errorf("code = %d, err = %v, kept = %v", t8Code(err), err, left)
	}
}

func TestComposeEditNeedsEditorAndTerminal(t *testing.T) {
	te, _ := t8EditSetup(t, "", nil)
	if err := t8Run(te, "stacks", "compose", "edit", "web"); t8Code(err) != ExitUsage {
		t.Errorf("no editor: code = %d (%v), want 2", t8Code(err), err)
	}
	te, _ = t8EditSetup(t, t8Editor(t, "exit 0"), nil)
	te.Env.IsTerminal = func(uintptr) bool { return false }
	if err := t8Run(te, "stacks", "compose", "edit", "web"); t8Code(err) != ExitUsage {
		t.Errorf("no terminal: code = %d (%v), want 2", t8Code(err), err)
	}
}

func TestComposeEditPrefersVisual(t *testing.T) {
	te, _ := t8EditSetup(t, "/nonexistent-editor", nil)
	visual := t8Editor(t, "exit 0")
	te.Env.Getenv = func(k string) string {
		return map[string]string{"VISUAL": visual, "EDITOR": "/nonexistent-editor"}[k]
	}
	if err := t8Run(te, "stacks", "compose", "edit", "web"); err != nil {
		t.Fatalf("VISUAL was not preferred: %v", err)
	}
}

func TestNetworksLs(t *testing.T) {
	te := t8Setup(t, map[string]http.HandlerFunc{"GET /api/networks": func(w http.ResponseWriter, r *http.Request) {
		t8JSON(w, 200, []apitypes.Network{
			{Name: "frontend", Driver: "bridge", Subnets: []string{"172.20.0.0/16", "fd00::/64"}},
			{Name: "none", Driver: "null"},
		})
	}})
	if err := t8Run(te, "networks", "ls"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(te.Out.String()), "\n")
	if len(lines) != 3 || strings.Join(strings.Fields(lines[0]), " ") != "NAME DRIVER SUBNETS" {
		t.Fatalf("stdout = %q", te.Out.String())
	}
	if got := strings.Join(strings.Fields(lines[1]), " "); got != "frontend bridge 172.20.0.0/16,fd00::/64" {
		t.Errorf("row = %q", got)
	}
}
