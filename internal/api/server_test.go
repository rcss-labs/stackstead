package api_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/rcss-labs/stackstead/internal/api"
	"github.com/rcss-labs/stackstead/internal/auth"
	"github.com/rcss-labs/stackstead/internal/store"
)

const adminPassword = "correct horse battery"

// fakeClock is safe for concurrent use: open streams read it while tests advance it.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type testEnv struct {
	handler http.Handler
	auth    *auth.Service
	clock   *fakeClock
	db      *sql.DB
}

func newTestEnv(t *testing.T, setupToken string) *testEnv {
	t.Helper()
	return newEnvWith(t, setupToken, nil)
}

// newEnvWith builds a test environment; tweak may adjust the Options.
func newEnvWith(t *testing.T, setupToken string, tweak func(*api.Options)) *testEnv {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	clock := &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	svc, err := auth.NewService(db, clock.Now, auth.WithFastHashingForTests(), auth.WithSessionRecheckInterval(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	static := fstest.MapFS{
		"index.html":    {Data: []byte("<html>spa</html>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
		"favicon.svg":   {Data: []byte("<svg/>")},
		".gitignore":    {Data: []byte("secret-ignore-rules")},
		"sub/.hidden":   {Data: []byte("hidden")},
	}
	opts := api.Options{
		Auth:       svc,
		Limiter:    auth.NewLoginLimiter(5, 15*time.Minute, clock.Now),
		SetupToken: setupToken,
		Static:     static,
		Logger:     slog.New(slog.DiscardHandler),
	}
	if tweak != nil {
		tweak(&opts)
	}
	h := api.New(opts)
	return &testEnv{handler: h, auth: svc, clock: clock, db: db}
}

func fromLoopback(r *http.Request) {
	r.RemoteAddr = "127.0.0.1:50000"
	r.Host = "127.0.0.1:8420"
}

func withHost(host string) func(*http.Request) {
	return func(r *http.Request) { r.Host = host }
}

func withHeader(key, value string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set(key, value) }
}

func withCookie(c *http.Cookie) func(*http.Request) {
	return func(r *http.Request) { r.AddCookie(c) }
}

// do sends a request; httptest's default RemoteAddr (192.0.2.1) is non-loopback.
func (e *testEnv) do(t *testing.T, method, target, body string, opts ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, opt := range opts {
		opt(req)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func (e *testEnv) createAdmin(t *testing.T) {
	t.Helper()
	if err := e.auth.CreateAdmin(context.Background(), "admin", adminPassword); err != nil {
		t.Fatal(err)
	}
}

func (e *testEnv) login(t *testing.T) *http.Cookie {
	t.Helper()
	rec := e.do(t, http.MethodPost, "/api/login", fmt.Sprintf(`{"username":"admin","password":%q}`, adminPassword))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("login status = %d, body %s", rec.Code, rec.Body)
	}
	return sessionCookie(t, rec)
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == "stackstead_session" {
			return c
		}
	}
	t.Fatal("no stackstead_session cookie in response")
	return nil
}

func setupBody(token string) string {
	return fmt.Sprintf(`{"username":"admin","password":%q,"setupToken":%q}`, adminPassword, token)
}

func TestSetupFromLoopbackCreatesAdmin(t *testing.T) {
	env := newTestEnv(t, "tok-123")
	if rec := env.do(t, http.MethodGet, "/api/setup/status", ""); !strings.Contains(rec.Body.String(), `"needsSetup":true`) {
		t.Fatalf("status before setup = %s", rec.Body)
	}
	if rec := env.do(t, http.MethodPost, "/api/setup", setupBody(""), fromLoopback); rec.Code != http.StatusNoContent {
		t.Fatalf("setup status = %d, body %s", rec.Code, rec.Body)
	}
	if rec := env.do(t, http.MethodGet, "/api/setup/status", ""); !strings.Contains(rec.Body.String(), `"needsSetup":false`) {
		t.Errorf("status after setup = %s", rec.Body)
	}
}

func TestSetupFromRemoteRequiresToken(t *testing.T) {
	env := newTestEnv(t, "tok-123")
	if rec := env.do(t, http.MethodPost, "/api/setup", setupBody("")); rec.Code != http.StatusForbidden {
		t.Errorf("no token: status = %d, want 403", rec.Code)
	}
	if rec := env.do(t, http.MethodPost, "/api/setup", setupBody("wrong")); rec.Code != http.StatusForbidden {
		t.Errorf("wrong token: status = %d, want 403", rec.Code)
	}
	if rec := env.do(t, http.MethodPost, "/api/setup", setupBody("tok-123")); rec.Code != http.StatusNoContent {
		t.Errorf("correct token: status = %d, want 204; body %s", rec.Code, rec.Body)
	}
}

func TestSetupThroughLocalProxyRequiresToken(t *testing.T) {
	env := newTestEnv(t, "tok-123")
	for _, header := range []string{"X-Forwarded-For", "Forwarded", "X-Real-IP"} {
		rec := env.do(t, http.MethodPost, "/api/setup", setupBody(""), fromLoopback, withHeader(header, "203.0.113.9"))
		if rec.Code != http.StatusForbidden {
			t.Errorf("loopback with %s: status = %d, want 403", header, rec.Code)
		}
	}
}

func TestSetupFromLoopbackChecksHostHeader(t *testing.T) {
	tests := []struct {
		host string
		want int
	}{
		{"evil.com", http.StatusForbidden},
		{"evil.com:8420", http.StatusForbidden},
		{"localhost:8420", http.StatusNoContent},
		{"LOCALHOST", http.StatusNoContent},
		{"[::1]:8420", http.StatusNoContent},
		{"127.0.0.1:8420", http.StatusNoContent},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			env := newTestEnv(t, "tok-123")
			rec := env.do(t, http.MethodPost, "/api/setup", setupBody(""), fromLoopback, withHost(tt.host))
			if rec.Code != tt.want {
				t.Errorf("Host %q: status = %d, want %d", tt.host, rec.Code, tt.want)
			}
		})
	}
}

func TestSetupRejectsEmptyTokenWhenServerHasNone(t *testing.T) {
	env := newTestEnv(t, "")
	if rec := env.do(t, http.MethodPost, "/api/setup", setupBody("")); rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestSetupWhenAdminExists(t *testing.T) {
	env := newTestEnv(t, "tok-123")
	env.createAdmin(t)
	if rec := env.do(t, http.MethodPost, "/api/setup", setupBody(""), fromLoopback); rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
}

func TestSetupRejectsWeakPassword(t *testing.T) {
	env := newTestEnv(t, "")
	rec := env.do(t, http.MethodPost, "/api/setup", `{"username":"admin","password":"short","setupToken":""}`, fromLoopback)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestSetupRequiresJSONContentType(t *testing.T) {
	env := newTestEnv(t, "")
	rec := env.do(t, http.MethodPost, "/api/setup", setupBody(""), fromLoopback, withHeader("Content-Type", "text/plain"))
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("status = %d, want 415", rec.Code)
	}
}

func TestLoginSetsSessionCookieAndMeReturnsUser(t *testing.T) {
	env := newTestEnv(t, "")
	env.createAdmin(t)
	cookie := env.login(t)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Secure {
		t.Errorf("cookie flags: HttpOnly=%v SameSite=%v Secure=%v; want true, Strict, false over plain HTTP",
			cookie.HttpOnly, cookie.SameSite, cookie.Secure)
	}
	rec := env.do(t, http.MethodGet, "/api/me", "", withCookie(cookie))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"username":"admin"`) {
		t.Errorf("GET /api/me = %d %s", rec.Code, rec.Body)
	}
}

func TestLoginFailuresLookIdentical(t *testing.T) {
	env := newTestEnv(t, "")
	env.createAdmin(t)
	wrongPass := env.do(t, http.MethodPost, "/api/login", `{"username":"admin","password":"wrong password!"}`)
	unknownUser := env.do(t, http.MethodPost, "/api/login", fmt.Sprintf(`{"username":"nobody","password":%q}`, adminPassword))
	if wrongPass.Code != http.StatusUnauthorized || unknownUser.Code != http.StatusUnauthorized {
		t.Fatalf("statuses = %d, %d; want 401, 401", wrongPass.Code, unknownUser.Code)
	}
	if wrongPass.Body.String() != unknownUser.Body.String() {
		t.Errorf("bodies differ: %q vs %q", wrongPass.Body, unknownUser.Body)
	}
}

func TestLoginIsRateLimited(t *testing.T) {
	env := newTestEnv(t, "")
	env.createAdmin(t)
	for range 5 {
		env.do(t, http.MethodPost, "/api/login", `{"username":"admin","password":"wrong password!"}`)
	}
	rec := env.do(t, http.MethodPost, "/api/login", fmt.Sprintf(`{"username":"admin","password":%q}`, adminPassword))
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("status after 5 failures = %d, want 429", rec.Code)
	}
}

func TestProtectedRoutesRequireSession(t *testing.T) {
	env := newTestEnv(t, "")
	for _, target := range []string{"/api/me", "/api/anything"} {
		if rec := env.do(t, http.MethodGet, target, ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without session = %d, want 401", target, rec.Code)
		}
	}
	if rec := env.do(t, http.MethodPost, "/api/logout", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("POST /api/logout without session = %d, want 401", rec.Code)
	}
}

func TestExpiredSessionIsRejected(t *testing.T) {
	env := newTestEnv(t, "")
	env.createAdmin(t)
	cookie := env.login(t)
	env.clock.Advance(auth.SessionTTL + time.Second)
	if rec := env.do(t, http.MethodGet, "/api/me", "", withCookie(cookie)); rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/me with expired session = %d, want 401", rec.Code)
	}
}

func TestLogoutInvalidatesSession(t *testing.T) {
	env := newTestEnv(t, "")
	env.createAdmin(t)
	cookie := env.login(t)
	if rec := env.do(t, http.MethodPost, "/api/logout", "", withCookie(cookie)); rec.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d", rec.Code)
	}
	if rec := env.do(t, http.MethodGet, "/api/me", "", withCookie(cookie)); rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/me after logout = %d, want 401", rec.Code)
	}
}

func TestSPAFallbackAndAPINotFound(t *testing.T) {
	env := newTestEnv(t, "")
	env.createAdmin(t)
	cookie := env.login(t)

	if rec := env.do(t, http.MethodGet, "/assets/app.js", ""); rec.Body.String() != "console.log(1)" {
		t.Errorf("asset body = %q", rec.Body)
	}
	for _, target := range []string{"/", "/stacks/web"} {
		rec := env.do(t, http.MethodGet, target, "")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "spa") {
			t.Errorf("GET %s = %d %q, want index.html", target, rec.Code, rec.Body)
		}
	}

	rec := env.do(t, http.MethodGet, "/api/nope", "", withCookie(cookie))
	var body map[string]string
	if rec.Code != http.StatusNotFound || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body["error"] == "" {
		t.Errorf("GET /api/nope = %d %q, want JSON 404", rec.Code, rec.Body)
	}
}

func TestAssetsMissingIs404(t *testing.T) {
	env := newTestEnv(t, "")
	for _, target := range []string{"/assets/nope.js", "/assets/", "/assets"} {
		rec := env.do(t, http.MethodGet, target, "")
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", target, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "spa") || strings.Contains(rec.Header().Get("Content-Type"), "html") {
			t.Errorf("GET %s served HTML: %q (%s)", target, rec.Body, rec.Header().Get("Content-Type"))
		}
	}
}

func TestAssetsAreImmutable(t *testing.T) {
	env := newTestEnv(t, "")
	rec := env.do(t, http.MethodGet, "/assets/app.js", "")
	if rec.Code != http.StatusOK || rec.Body.String() != "console.log(1)" {
		t.Fatalf("GET /assets/app.js = %d %q", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("Cache-Control = %q", got)
	}
}

func TestIndexHasSecurityHeaders(t *testing.T) {
	env := newTestEnv(t, "")
	for _, target := range []string{"/", "/stacks/web"} {
		rec := env.do(t, http.MethodGet, target, "")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "spa") {
			t.Fatalf("GET %s = %d %q", target, rec.Code, rec.Body)
		}
		h := rec.Header()
		if h.Get("Cache-Control") != "no-cache" {
			t.Errorf("%s Cache-Control = %q", target, h.Get("Cache-Control"))
		}
		if !strings.Contains(h.Get("Content-Security-Policy"), "script-src 'self'") {
			t.Errorf("%s CSP = %q", target, h.Get("Content-Security-Policy"))
		}
		if h.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s X-Content-Type-Options = %q", target, h.Get("X-Content-Type-Options"))
		}
		if h.Get("Referrer-Policy") != "same-origin" {
			t.Errorf("%s Referrer-Policy = %q", target, h.Get("Referrer-Policy"))
		}
	}
}

func TestRootFilesServedNoCache(t *testing.T) {
	env := newTestEnv(t, "")
	rec := env.do(t, http.MethodGet, "/favicon.svg", "")
	if rec.Code != http.StatusOK || rec.Body.String() != "<svg/>" {
		t.Fatalf("GET /favicon.svg = %d %q", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q", got)
	}
}

// ServeMux cleans the path and redirects; the redirect target is the cleaned
// path, never the traversed file, and nothing is served with asset caching.
func TestAssetTraversalIsRedirectedToCleanPath(t *testing.T) {
	env := newTestEnv(t, "")
	rec := env.do(t, http.MethodGet, "/assets/../index.html", "")
	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want 307", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/index.html" {
		t.Errorf("Location = %q, want /index.html", loc)
	}
	if strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("traversal served with immutable caching: %q", rec.Header().Get("Cache-Control"))
	}
}

func TestDotfilesAreNotServed(t *testing.T) {
	env := newTestEnv(t, "")
	for _, target := range []string{"/.gitignore", "/sub/.hidden", "/.git/config", "/assets/.hidden"} {
		rec := env.do(t, http.MethodGet, target, "")
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", target, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "secret-ignore-rules") || strings.Contains(rec.Body.String(), "hidden") {
			t.Errorf("GET %s leaked file content: %q", target, rec.Body)
		}
	}
}

func TestNosniffOnEveryResponse(t *testing.T) {
	env := newTestEnv(t, "")
	for _, target := range []string{"/", "/stacks/web", "/favicon.svg", "/assets/app.js", "/assets/nope.js", "/.gitignore", "/api/setup/status", "/api/me", "/api/nope", "/metrics"} {
		rec := env.do(t, http.MethodGet, target, "")
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("GET %s (%d) X-Content-Type-Options = %q, want nosniff", target, rec.Code, got)
		}
	}
}

func TestFontsServedAsFontWoff2(t *testing.T) {
	env := newEnvWith(t, "", func(o *api.Options) {
		o.Static = fstest.MapFS{
			"index.html":     {Data: []byte("<html>spa</html>")},
			"assets/x.woff2": {Data: []byte("wOF2")},
		}
	})
	rec := env.do(t, http.MethodGet, "/assets/x.woff2", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /assets/x.woff2 = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "font/woff2" {
		t.Errorf("Content-Type = %q, want font/woff2", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("Cache-Control = %q", got)
	}
}

func TestThemeScriptIsNoCache(t *testing.T) {
	env := newEnvWith(t, "", func(o *api.Options) {
		o.Static = fstest.MapFS{
			"index.html": {Data: []byte("<html>spa</html>")},
			"theme.js":   {Data: []byte("(function(){})();")},
		}
	})
	rec := env.do(t, http.MethodGet, "/theme.js", "")
	if rec.Code != http.StatusOK || rec.Body.String() != "(function(){})();" {
		t.Fatalf("GET /theme.js = %d %q", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q", got)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("Content-Type = %q", ct)
	}
}
