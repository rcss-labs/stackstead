package cli

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/client"
	"github.com/rcss-labs/stackstead/internal/version"
)

const t6Session = "SESSION-VALUE-0123456789abcdef"

func t6Root() *Command {
	return &Command{Name: "stackstead", Sub: []*Command{
		loginCommand(), logoutCommand(), contextCommand(), whoamiCommand(), statusCommand(), settingsCommand(),
	}}
}

func t6Run(te *testEnv, args ...string) error {
	return Run(context.Background(), t6Root(), args, te.Env)
}

func t6Code(err error) int { return codeFor(err) }

// t6API serves a stub Stackstead API; handlers are keyed "METHOD /path".
func t6API(t *testing.T, routes map[string]http.HandlerFunc) *httptest.Server {
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
	return srv
}

func t6JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func t6LoginOK(w http.ResponseWriter, r *http.Request) {
	var req apitypes.LoginRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Username != "admin" || req.Password != "hunter2-pw" {
		t6JSON(w, 401, apitypes.ErrorBody{Error: "invalid credentials"})
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "stackstead_session", Value: t6Session, MaxAge: 3600, Path: "/"})
	w.WriteHeader(204)
}

// t6Seed writes a contexts file with one logged-in context named "lab".
func t6Seed(t *testing.T, te *testEnv, url string) *ContextStore {
	t.Helper()
	st := newContextStore(te.Env)
	if err := st.Save(Contexts{Version: 1, Current: "lab", Contexts: map[string]Context{
		"lab": {URL: url, Username: "admin", Session: t6Session, ExpiresAt: te.Now().Add(time.Hour)},
	}}); err != nil {
		t.Fatal(err)
	}
	return st
}

func t6NoLeak(t *testing.T, te *testEnv) {
	t.Helper()
	if strings.Contains(te.Out.String()+te.Err.String(), t6Session) {
		t.Errorf("session value leaked into output:\nstdout=%q\nstderr=%q", te.Out.String(), te.Err.String())
	}
	if strings.Contains(te.Out.String()+te.Err.String(), "hunter2-pw") {
		t.Error("password leaked into output")
	}
}

func TestLoginStoresContextAndSetsCurrent(t *testing.T) {
	srv := t6API(t, map[string]http.HandlerFunc{"POST /api/login": t6LoginOK})
	te := newTestEnv(t, nil)
	te.Stdin = strings.NewReader("hunter2-pw\n")
	if err := t6Run(te, "login", srv.URL, "--context", "lab", "--password-stdin"); err != nil {
		t.Fatal(err)
	}
	c, err := newContextStore(te.Env).Load()
	if err != nil {
		t.Fatal(err)
	}
	cx := c.Contexts["lab"]
	if c.Current != "lab" || cx.URL != srv.URL || cx.Username != "admin" || cx.Session != t6Session {
		t.Errorf("stored = current %q, %+v", c.Current, cx)
	}
	if cx.ExpiresAt.Before(time.Now().Add(30*time.Minute)) || cx.ExpiresAt.After(time.Now().Add(2*time.Hour)) {
		t.Errorf("expiresAt = %v, want about one hour ahead", cx.ExpiresAt)
	}
	out := te.Out.String() + te.Err.String()
	if !strings.Contains(out, "logged in to lab") || !strings.Contains(out, "as admin") {
		t.Errorf("message = %q", out)
	}
	t6NoLeak(t, te)
}

func TestLoginJSONHasNoSession(t *testing.T) {
	srv := t6API(t, map[string]http.HandlerFunc{"POST /api/login": t6LoginOK})
	te := newTestEnv(t, nil)
	te.Stdin = strings.NewReader("hunter2-pw\n")
	if err := t6Run(te, "login", srv.URL, "--context", "lab", "--password-stdin", "-o", "json"); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(te.Out.String()), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v: %q", err, te.Out.String())
	}
	if len(got) != 4 || got["context"] != "lab" || got["url"] != srv.URL || got["username"] != "admin" || got["expiresAt"] == nil {
		t.Errorf("json = %v, want exactly context,url,username,expiresAt", got)
	}
	t6NoLeak(t, te)
}

func TestLoginDefaultsContextNameFromHost(t *testing.T) {
	srv := t6API(t, map[string]http.HandlerFunc{"POST /api/login": t6LoginOK})
	te := newTestEnv(t, nil)
	te.Stdin = strings.NewReader("hunter2-pw\n")
	if err := t6Run(te, "login", srv.URL, "--password-stdin"); err != nil {
		t.Fatal(err)
	}
	c, _ := newContextStore(te.Env).Load()
	if _, ok := c.Contexts["127-0-0-1"]; !ok || c.Current != "127-0-0-1" {
		t.Errorf("contexts = %+v current %q, want 127-0-0-1", c.Contexts, c.Current)
	}
}

func TestLoginRejectsBadInput(t *testing.T) {
	for _, args := range [][]string{
		{"login", "ftp://host", "--password-stdin"},
		{"login", "not a url", "--password-stdin"},
		{"login", "https://user:pw@host", "--password-stdin"},
		{"login", "https://host", "--context", "Bad Name", "--password-stdin"},
	} {
		te := newTestEnv(t, nil)
		te.Stdin = strings.NewReader("x\n")
		if err := t6Run(te, args...); t6Code(err) != ExitUsage {
			t.Errorf("%v: code = %d (%v), want 2", args, t6Code(err), err)
		}
	}
}

func TestLoginWarnsOnPlainHTTPToRemoteHost(t *testing.T) {
	te := newTestEnv(t, nil)
	te.Stdin = strings.NewReader("") // stops at the empty password, before any network
	_ = t6Run(te, "login", "http://stackstead.invalid:8420", "--password-stdin")
	if !strings.Contains(te.Err.String(), "is not TLS") {
		t.Errorf("stderr = %q, want the clear-text warning", te.Err.String())
	}

	srv := t6API(t, map[string]http.HandlerFunc{"POST /api/login": t6LoginOK})
	te = newTestEnv(t, nil)
	te.Stdin = strings.NewReader("hunter2-pw\n")
	if err := t6Run(te, "login", srv.URL, "--password-stdin"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(te.Err.String(), "not TLS") {
		t.Errorf("loopback http must not warn: %q", te.Err.String())
	}
}

func TestLoginWrongPasswordExits3(t *testing.T) {
	srv := t6API(t, map[string]http.HandlerFunc{"POST /api/login": t6LoginOK})
	te := newTestEnv(t, nil)
	te.Stdin = strings.NewReader("wrong\n")
	err := t6Run(te, "login", srv.URL, "--password-stdin")
	if t6Code(err) != ExitAuth || err == nil || !strings.Contains(err.Error(), "invalid username or password") {
		t.Errorf("err = %v code %d, want exit 3 invalid username or password", err, t6Code(err))
	}
	if _, statErr := os.Stat(newContextStore(te.Env).Path); statErr == nil {
		t.Error("a failed login must not create the contexts file")
	}
}

func TestLoginRateLimitedExits8(t *testing.T) {
	srv := t6API(t, map[string]http.HandlerFunc{"POST /api/login": func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "42")
		t6JSON(w, 429, apitypes.ErrorBody{Error: "too many attempts"})
	}})
	te := newTestEnv(t, nil)
	te.Stdin = strings.NewReader("x\n")
	err := t6Run(te, "login", srv.URL, "--password-stdin")
	if t6Code(err) != ExitRateLimited || !strings.Contains(err.Error(), "too many attempts") || !strings.Contains(err.Error(), "42s") {
		t.Errorf("err = %v code %d, want exit 8 mentioning the message and 42s", err, t6Code(err))
	}
}

func TestLoginCAFileMustExist(t *testing.T) {
	te := newTestEnv(t, nil)
	te.Stdin = strings.NewReader("x\n")
	err := t6Run(te, "login", "https://host.example", "--password-stdin", "--ca-file", filepath.Join(t.TempDir(), "missing.pem"))
	if t6Code(err) != ExitUsage {
		t.Errorf("code = %d (%v), want 2", t6Code(err), err)
	}
}

func TestLoginStoresAbsoluteCAFile(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t6LoginOK(w, r) }))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(filepath.Join(dir, "ca.pem"), pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	te := newTestEnv(t, nil)
	te.Stdin = strings.NewReader("hunter2-pw\n")
	if err := t6Run(te, "login", srv.URL, "--context", "lab", "--password-stdin", "--ca-file", "ca.pem"); err != nil {
		t.Fatal(err)
	}
	c, _ := newContextStore(te.Env).Load()
	got := c.Contexts["lab"].CAFile
	if !filepath.IsAbs(got) || filepath.Base(got) != "ca.pem" {
		t.Errorf("stored CAFile = %q, want an absolute path to ca.pem", got)
	}
}

func TestLoginStoresInsecureAndWarns(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t6LoginOK(w, r) }))
	t.Cleanup(srv.Close)
	te := newTestEnv(t, nil)
	te.Stdin = strings.NewReader("hunter2-pw\n")
	if err := t6Run(te, "login", srv.URL, "--context", "lab", "--password-stdin", "--insecure"); err != nil {
		t.Fatal(err)
	}
	c, _ := newContextStore(te.Env).Load()
	if !c.Contexts["lab"].Insecure {
		t.Error("--insecure not stored")
	}
	if !strings.Contains(te.Err.String(), "TLS verification is disabled") {
		t.Errorf("stderr = %q, want an insecure warning at login", te.Err.String())
	}
}

func TestLoginRejectsInsecureCombinations(t *testing.T) {
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"login", "https://host.example", "--insecure", "--ca-file", ca, "--password-stdin"},
		{"login", "http://127.0.0.1:1", "--insecure", "--password-stdin"},
	} {
		te := newTestEnv(t, nil)
		te.Stdin = strings.NewReader("x\n")
		if err := t6Run(te, args...); t6Code(err) != ExitUsage {
			t.Errorf("%v: code = %d (%v), want 2", args, t6Code(err), err)
		}
	}
}

func TestLoginRefusesToOverwriteContextOfAnotherServer(t *testing.T) {
	srv := t6API(t, map[string]http.HandlerFunc{"POST /api/login": t6LoginOK})
	te := newTestEnv(t, map[string]string{"STACKSTEAD_CONTEXT": "home"})
	st := newContextStore(te.Env)
	home := Context{URL: "https://home.example", Username: "admin", Session: "HOME-SESSION"}
	if err := st.Save(Contexts{Version: 1, Current: "home", Contexts: map[string]Context{"home": home}}); err != nil {
		t.Fatal(err)
	}
	te.Stdin = strings.NewReader("hunter2-pw\n")
	err := t6Run(te, "login", srv.URL, "--password-stdin")
	if t6Code(err) != ExitConflict || err == nil || !strings.Contains(err.Error(), "already points at https://home.example") {
		t.Fatalf("err = %v code %d, want exit 5 naming the existing URL", err, t6Code(err))
	}
	if c, _ := st.Load(); c.Contexts["home"] != home {
		t.Errorf("home context was modified: %+v", c.Contexts["home"])
	}

	// Same host, different port: the derived name collides and must be refused too.
	te = newTestEnv(t, nil)
	te.ConfigDir = filepath.Dir(st.Path)
	if err := st.Save(Contexts{Version: 1, Current: "127-0-0-1", Contexts: map[string]Context{"127-0-0-1": {URL: "http://127.0.0.1:1", Session: "S"}}}); err != nil {
		t.Fatal(err)
	}
	te.Stdin = strings.NewReader("hunter2-pw\n")
	if err := t6Run(te, "login", srv.URL, "--password-stdin"); t6Code(err) != ExitConflict {
		t.Errorf("same host other port: code %d (%v), want 5", t6Code(err), err)
	}

	// Re-login to the same URL replaces the session.
	if err := st.Save(Contexts{Version: 1, Current: "lab", Contexts: map[string]Context{"lab": {URL: srv.URL, Session: "OLD"}}}); err != nil {
		t.Fatal(err)
	}
	te.Stdin = strings.NewReader("hunter2-pw\n")
	if err := t6Run(te, "login", srv.URL, "--context", "lab", "--password-stdin"); err != nil {
		t.Fatal(err)
	}
	if c, _ := st.Load(); c.Contexts["lab"].Session != t6Session {
		t.Error("re-login to the same URL must replace the session")
	}
}

func TestUnauthorizedClearsSessionAndExits3(t *testing.T) {
	srv := t6API(t, map[string]http.HandlerFunc{"GET /api/me": func(w http.ResponseWriter, r *http.Request) {
		t6JSON(w, 401, apitypes.ErrorBody{Error: "unauthorized"})
	}})
	te := newTestEnv(t, nil)
	st := t6Seed(t, te, srv.URL)
	if err := st.Update(func(c *Contexts) error {
		c.Contexts["other"] = Context{URL: "https://other.example", Session: "OTHER-SESSION"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	err := t6Run(te, "whoami")
	if c, _ := st.Load(); c.Contexts["other"].Session != "OTHER-SESSION" {
		t.Error("another context's session must survive")
	}
	if t6Code(err) != ExitAuth {
		t.Fatalf("code = %d (%v), want 3", t6Code(err), err)
	}
	if !strings.Contains(err.Error(), "lab") || !strings.Contains(err.Error(), srv.URL) || !strings.Contains(err.Error(), "stackstead login") {
		t.Errorf("message = %q, want context name, url and the login hint", err.Error())
	}
	c, _ := st.Load()
	if c.Contexts["lab"].Session != "" {
		t.Error("session was not cleared")
	}
	if c.Contexts["lab"].URL != srv.URL || c.Current != "lab" {
		t.Errorf("context lost beyond the session: %+v", c)
	}
}

func TestNoSessionExits3WithoutRequest(t *testing.T) {
	te := newTestEnv(t, nil)
	if err := newContextStore(te.Env).Save(Contexts{Version: 1, Current: "lab", Contexts: map[string]Context{"lab": {URL: "http://127.0.0.1:1"}}}); err != nil {
		t.Fatal(err)
	}
	if err := t6Run(te, "whoami"); t6Code(err) != ExitAuth {
		t.Errorf("code = %d (%v), want 3", t6Code(err), err)
	}
	if err := t6Run(newTestEnv(t, nil), "whoami"); t6Code(err) != ExitAuth {
		t.Errorf("no contexts: code = %d, want 3", t6Code(err))
	}
}

func TestWhoami(t *testing.T) {
	srv := t6API(t, map[string]http.HandlerFunc{"GET /api/me": func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("stackstead_session"); err != nil || c.Value != t6Session {
			t6JSON(w, 401, apitypes.ErrorBody{Error: "unauthorized"})
			return
		}
		t6JSON(w, 200, apitypes.Me{Username: "admin"})
	}})
	te := newTestEnv(t, nil)
	t6Seed(t, te, srv.URL)
	if err := t6Run(te, "whoami"); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(te.Out.String()), "admin @ "+srv.URL+" (context lab)"; got != want {
		t.Errorf("whoami = %q, want %q", got, want)
	}
	te2 := newTestEnv(t, nil)
	te2.ConfigDir = te.ConfigDir
	if err := t6Run(te2, "whoami", "-o", "json"); err != nil {
		t.Fatal(err)
	}
	var me apitypes.Me
	if err := json.Unmarshal([]byte(te2.Out.String()), &me); err != nil || me.Username != "admin" {
		t.Errorf("json = %q err %v", te2.Out.String(), err)
	}
	t6NoLeak(t, te)
	t6NoLeak(t, te2)
}

func TestWhoamiUsesContextFlagAndEnv(t *testing.T) {
	srv := t6API(t, map[string]http.HandlerFunc{"GET /api/me": func(w http.ResponseWriter, r *http.Request) {
		t6JSON(w, 200, apitypes.Me{Username: "admin"})
	}})
	te := newTestEnv(t, map[string]string{"STACKSTEAD_CONTEXT": "other"})
	if err := newContextStore(te.Env).Save(Contexts{Version: 1, Current: "lab", Contexts: map[string]Context{
		"lab":   {URL: "http://127.0.0.1:1", Session: "x"},
		"other": {URL: srv.URL, Session: t6Session},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := t6Run(te, "whoami"); err != nil || !strings.Contains(te.Out.String(), "context other") {
		t.Errorf("env context: err %v out %q", err, te.Out.String())
	}
	if err := t6Run(te, "whoami", "--context", "nope"); t6Code(err) != ExitNotFound {
		t.Errorf("unknown context: code %d, want 4", t6Code(err))
	}
}

func TestInsecureContextWarnsEveryCommand(t *testing.T) {
	srv := t6API(t, map[string]http.HandlerFunc{"GET /api/me": func(w http.ResponseWriter, r *http.Request) {
		t6JSON(w, 200, apitypes.Me{Username: "admin"})
	}})
	te := newTestEnv(t, nil)
	if err := newContextStore(te.Env).Save(Contexts{Version: 1, Current: "lab", Contexts: map[string]Context{
		"lab": {URL: srv.URL, Session: t6Session, Insecure: true},
	}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		te.Err.mu.Lock()
		te.Err.buf.Reset()
		te.Err.mu.Unlock()
		if err := t6Run(te, "whoami"); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(te.Err.String(), "warning: TLS verification is disabled for context lab") {
			t.Errorf("run %d stderr = %q", i, te.Err.String())
		}
	}
}

func TestLooseContextsPermsWarn(t *testing.T) {
	srv := t6API(t, map[string]http.HandlerFunc{"GET /api/me": func(w http.ResponseWriter, r *http.Request) {
		t6JSON(w, 200, apitypes.Me{Username: "admin"})
	}})
	te := newTestEnv(t, nil)
	st := t6Seed(t, te, srv.URL)
	if err := os.Chmod(st.Path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := t6Run(te, "whoami"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(te.Err.String(), "warning: contexts file") || !strings.Contains(te.Err.String(), "readable by others") {
		t.Errorf("stderr = %q", te.Err.String())
	}
}

func TestServerErrorTextIsSanitised(t *testing.T) {
	srv := t6API(t, map[string]http.HandlerFunc{"GET /api/me": func(w http.ResponseWriter, r *http.Request) {
		t6JSON(w, 400, apitypes.ErrorBody{Error: "bad\x1b[31m red\rbell\x07", Fields: []apitypes.FieldError{{Field: "name", Message: "evil\x1b]0;title\x07"}}})
	}})
	te := newTestEnv(t, nil)
	t6Seed(t, te, srv.URL)
	err := t6Run(te, "whoami")
	if err == nil {
		t.Fatal("want an error")
	}
	msg := err.Error()
	for _, r := range msg {
		if r < 0x20 && r != '\n' && r != '\t' || r == 0x7f {
			t.Fatalf("control character %q in %q", r, msg)
		}
	}
	if !strings.Contains(msg, "bad") || !strings.Contains(msg, "name") || !strings.Contains(msg, "evil") {
		t.Errorf("message lost its content: %q", msg)
	}
	if t6Code(err) != ExitValidation {
		t.Errorf("400 with fields: code = %d, want 6", t6Code(err))
	}
}

func TestAPIErrorKeepsStatusSentinel(t *testing.T) {
	srv := t6API(t, map[string]http.HandlerFunc{"GET /api/me": func(w http.ResponseWriter, r *http.Request) {
		t6JSON(w, 404, apitypes.ErrorBody{Error: "nope"})
	}})
	te := newTestEnv(t, nil)
	t6Seed(t, te, srv.URL)
	err := t6Run(te, "whoami")
	if t6Code(err) != ExitNotFound || !errors.Is(err, client.ErrNotFound) {
		t.Errorf("code = %d, Is(ErrNotFound) = %v, want 4 and true", t6Code(err), errors.Is(err, client.ErrNotFound))
	}
}

func TestLogoutClearsEvenWhen401(t *testing.T) {
	srv := t6API(t, map[string]http.HandlerFunc{"POST /api/logout": func(w http.ResponseWriter, r *http.Request) {
		t6JSON(w, 401, apitypes.ErrorBody{Error: "unauthorized"})
	}})
	te := newTestEnv(t, nil)
	st := t6Seed(t, te, srv.URL)
	if err := t6Run(te, "logout"); err != nil {
		t.Fatalf("logout = %v, want nil", err)
	}
	c, _ := st.Load()
	if c.Contexts["lab"].Session != "" || c.Contexts["lab"].URL != srv.URL {
		t.Errorf("context after logout = %+v", c.Contexts["lab"])
	}
	if te.Out.String() != "" || !strings.Contains(te.Err.String(), "lab") {
		t.Errorf("stdout %q stderr %q: message belongs on stderr", te.Out.String(), te.Err.String())
	}
}

func TestLogoutClearsWhenServerUnreachable(t *testing.T) {
	te := newTestEnv(t, nil)
	st := t6Seed(t, te, "http://127.0.0.1:1")
	if err := t6Run(te, "logout"); err != nil {
		t.Fatalf("logout = %v, want nil", err)
	}
	if c, _ := st.Load(); c.Contexts["lab"].Session != "" {
		t.Error("session not cleared locally")
	}
}

func TestLogoutSendsPostAndNeverLeaks(t *testing.T) {
	var got bool
	srv := t6API(t, map[string]http.HandlerFunc{"POST /api/logout": func(w http.ResponseWriter, r *http.Request) {
		got = true
		w.WriteHeader(204)
	}})
	te := newTestEnv(t, nil)
	t6Seed(t, te, srv.URL)
	if err := t6Run(te, "logout"); err != nil || !got {
		t.Errorf("err %v, server saw logout = %v", err, got)
	}
	t6NoLeak(t, te)
}

func TestContextLsUseRm(t *testing.T) {
	te := newTestEnv(t, nil)
	st := newContextStore(te.Env)
	now := te.Now()
	if err := st.Save(Contexts{Version: 1, Current: "lab", Contexts: map[string]Context{
		"lab":  {URL: "https://lab.example", Username: "admin", Session: t6Session, ExpiresAt: now.Add(48 * time.Hour)},
		"old":  {URL: "http://127.0.0.1:8420", Username: "root", Session: t6Session, ExpiresAt: now.Add(-time.Hour), CAFile: "/etc/ca.pem"},
		"anon": {URL: "https://anon.example", Username: "admin", Insecure: true},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := t6Run(te, "context", "ls"); err != nil {
		t.Fatal(err)
	}
	out := te.Out.String()
	t6NoLeak(t, te)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 {
		t.Fatalf("lines = %q", lines)
	}
	find := func(name string) string {
		for _, l := range lines {
			if strings.Contains(l, name) {
				return l
			}
		}
		t.Fatalf("no line for %s in %q", name, out)
		return ""
	}
	if l := find("https://lab.example"); !strings.HasPrefix(strings.TrimSpace(l), "*") || !strings.Contains(l, "valid until") || !strings.Contains(l, "system") {
		t.Errorf("lab line = %q", l)
	}
	if l := find("127.0.0.1:8420"); strings.Contains(l, "*") || !strings.Contains(l, "expired") || !strings.Contains(l, "ca:/etc/ca.pem") {
		t.Errorf("old line = %q", l)
	}
	if l := find("anon.example"); !strings.Contains(l, "none") || !strings.Contains(l, "INSECURE") {
		t.Errorf("anon line = %q", l)
	}

	te = newTestEnv(t, nil)
	te.ConfigDir = filepath.Dir(st.Path)
	if err := t6Run(te, "context", "ls", "-o", "json"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(te.Out.String(), t6Session) || !json.Valid([]byte(te.Out.String())) || !strings.Contains(te.Out.String(), "sessionState") {
		t.Errorf("json = %q", te.Out.String())
	}

	if err := t6Run(te, "context", "current"); err != nil || strings.TrimSpace(te.Out.String()) == "" {
		t.Errorf("current: %v", err)
	}
	if err := t6Run(te, "context", "use", "nope"); t6Code(err) != ExitNotFound {
		t.Errorf("use unknown: code %d, want 4", t6Code(err))
	}
	if err := t6Run(te, "context", "use", "old"); err != nil {
		t.Fatal(err)
	}
	if c, _ := st.Load(); c.Current != "old" {
		t.Errorf("current = %q, want old", c.Current)
	}
	if err := t6Run(te, "context", "rm", "old"); err != nil {
		t.Fatal(err)
	}
	if c, _ := st.Load(); c.Current != "" || len(c.Contexts) != 2 {
		t.Errorf("after rm: %+v", c)
	}
	if err := t6Run(te, "context", "rm", "old"); t6Code(err) != ExitNotFound {
		t.Errorf("rm unknown: code %d, want 4", t6Code(err))
	}
	te = newTestEnv(t, nil)
	te.ConfigDir = filepath.Dir(st.Path)
	if err := t6Run(te, "context", "current"); t6Code(err) != ExitNotFound {
		t.Errorf("current with none: code %d, want 4", t6Code(err))
	}
}

func TestStatusTableAndJSON(t *testing.T) {
	want := apitypes.Status{Docker: "available", Compose: "unavailable", Events: "disconnected", MetricsDegraded: true, Version: version.Get().Version}
	srv := t6API(t, map[string]http.HandlerFunc{"GET /api/status": func(w http.ResponseWriter, r *http.Request) { t6JSON(w, 200, want) }})
	te := newTestEnv(t, nil)
	st := t6Seed(t, te, srv.URL)
	if err := t6Run(te, "status"); err != nil {
		t.Fatal(err)
	}
	out := te.Out.String()
	for _, w := range []string{"docker", "available", "compose", "unavailable", "events", "disconnected", "metrics", "degraded", "server version", "client version", version.Get().Version} {
		if !strings.Contains(out, w) {
			t.Errorf("table lacks %q:\n%s", w, out)
		}
	}
	if te.Err.String() != "" {
		t.Errorf("matching versions must print no note: %q", te.Err.String())
	}

	te = newTestEnv(t, nil)
	te.ConfigDir = filepath.Dir(st.Path)
	if err := t6Run(te, "status", "-o", "json"); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(te.Out.String()), &got); err != nil {
		t.Fatal(err)
	}
	if got["docker"] != "available" || got["metricsDegraded"] != true || got["clientVersion"] != nil {
		t.Errorf("json = %v, want the server's Status verbatim", got)
	}
}

func TestStatusNotesVersionMismatch(t *testing.T) {
	srv := t6API(t, map[string]http.HandlerFunc{"GET /api/status": func(w http.ResponseWriter, r *http.Request) {
		t6JSON(w, 200, apitypes.Status{Docker: "available", Compose: "available", Events: "connected", Version: "9.9.9-other"})
	}})
	te := newTestEnv(t, nil)
	t6Seed(t, te, srv.URL)
	if err := t6Run(te, "status"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(te.Err.String(), "9.9.9-other") || !strings.Contains(te.Err.String(), "note:") {
		t.Errorf("stderr = %q, want a version mismatch note", te.Err.String())
	}
	te2 := newTestEnv(t, nil)
	te2.ConfigDir = te.ConfigDir
	if err := t6Run(te2, "status", "-o", "json"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(te2.Err.String(), "note:") || strings.Contains(te2.Out.String(), "note") {
		t.Errorf("note must stay on stderr: out %q err %q", te2.Out.String(), te2.Err.String())
	}
}

func TestUnauthorizedKeepsSessionSavedByConcurrentLogin(t *testing.T) {
	var te *testEnv
	var st *ContextStore
	srv := t6API(t, map[string]http.HandlerFunc{"GET /api/me": func(w http.ResponseWriter, r *http.Request) {
		// another terminal logs in again while this request is in flight
		_ = st.Update(func(c *Contexts) error {
			cx := c.Contexts["lab"]
			cx.Session = "FRESH-SESSION"
			c.Contexts["lab"] = cx
			return nil
		})
		t6JSON(w, 401, apitypes.ErrorBody{Error: "unauthorized"})
	}})
	te = newTestEnv(t, nil)
	st = t6Seed(t, te, srv.URL)
	if err := t6Run(te, "whoami"); t6Code(err) != ExitAuth {
		t.Errorf("code = %d, want 3", t6Code(err))
	}
	if c, _ := st.Load(); c.Contexts["lab"].Session != "FRESH-SESSION" {
		t.Errorf("session = %q, the fresh login was wiped", c.Contexts["lab"].Session)
	}
}

func TestSingleLineMessagesCannotForgeLines(t *testing.T) {
	srv := t6API(t, map[string]http.HandlerFunc{"GET /api/me": func(w http.ResponseWriter, r *http.Request) {
		t6JSON(w, 500, apitypes.ErrorBody{Error: "boom\nstackstead: fake line"})
	}})
	te := newTestEnv(t, nil)
	t6Seed(t, te, srv.URL)
	err := t6Run(te, "whoami")
	if err == nil || strings.Contains(err.Error(), "\n") {
		t.Errorf("err = %q, want a single line", err)
	}
}

func TestLoginRefusesContextOverwriteBeforePromptingOrContactingServer(t *testing.T) {
	var hits int
	srv := t6API(t, map[string]http.HandlerFunc{"POST /api/login": func(w http.ResponseWriter, r *http.Request) { hits++; w.WriteHeader(204) }})
	te := newTestEnv(t, nil)
	if err := newContextStore(te.Env).Save(Contexts{Version: 1, Current: "home", Contexts: map[string]Context{
		"home": {URL: "https://home.example", Session: "S"},
	}}); err != nil {
		t.Fatal(err)
	}
	te.IsTerminal = func(uintptr) bool { return true }
	prompts := 0
	orig := readPassword
	readPassword = func(int) ([]byte, error) { prompts++; return []byte("pw"), nil }
	t.Cleanup(func() { readPassword = orig })
	err := t6Run(te, "login", srv.URL, "--context", "home")
	if t6Code(err) != ExitConflict {
		t.Fatalf("code %d (%v), want 5", t6Code(err), err)
	}
	if prompts != 0 || hits != 0 {
		t.Errorf("password prompted %d times, server hit %d times; the conflict must be reported first", prompts, hits)
	}
}

func TestLoginNonTTYErrorDoesNotMentionPasswordFile(t *testing.T) {
	te := newTestEnv(t, nil)
	err := t6Run(te, "login", "http://127.0.0.1:1")
	if t6Code(err) != ExitUsage || err == nil || !strings.Contains(err.Error(), "--password-stdin") || strings.Contains(err.Error(), "password-file") {
		t.Errorf("err = %v", err)
	}
}
