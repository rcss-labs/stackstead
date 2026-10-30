package api_test

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

const hostCookieName = "__Host-stackstead_session"

func loginRequest(t *testing.T, env *testEnv, opts ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	return env.do(t, http.MethodPost, "/api/login",
		fmt.Sprintf(`{"username":"admin","password":%q}`, adminPassword), opts...)
}

func setCookies(rec *httptest.ResponseRecorder) map[string]*http.Cookie {
	out := map[string]*http.Cookie{}
	for _, c := range rec.Result().Cookies() {
		out[c.Name] = c
	}
	return out
}

func overTLS(r *http.Request) { r.TLS = &tls.ConnectionState{} }

func TestSessionCookieOverTLSUsesHostPrefix(t *testing.T) {
	env := newTestEnv(t, "")
	env.createAdmin(t)
	cookies := setCookies(loginRequest(t, env, overTLS))
	c := cookies[hostCookieName]
	if c == nil || len(cookies) != 1 {
		t.Fatalf("cookies = %v, want only %s", cookies, hostCookieName)
	}
	if !c.Secure || !c.HttpOnly || c.Path != "/" || c.Domain != "" {
		t.Errorf("flags Secure=%v HttpOnly=%v Path=%q Domain=%q; want true, true, /, empty", c.Secure, c.HttpOnly, c.Path, c.Domain)
	}
}

func TestSessionCookieBehindTrustedProxyUsesHostPrefix(t *testing.T) {
	env := newEnvWith(t, "", trustLoopbackProxy)
	env.createAdmin(t)
	rec := loginRequest(t, env, fromProxy(), withHeader("X-Forwarded-Proto", "https"))
	c := setCookies(rec)[hostCookieName]
	if c == nil || !c.Secure || c.Path != "/" || c.Domain != "" {
		t.Fatalf("cookie = %+v, want Secure %s with Path=/ and no Domain", c, hostCookieName)
	}
}

// A client spoofing the header only makes its own cookie Secure; a
// TLS-terminating proxy that is not in trusted_proxies must still get one.
func TestSessionCookieForwardedProtoFromAnyPeerUsesHostPrefix(t *testing.T) {
	env := newTestEnv(t, "")
	env.createAdmin(t)
	cookies := setCookies(loginRequest(t, env, withHeader("X-Forwarded-Proto", "https")))
	c := cookies[hostCookieName]
	if c == nil || !c.Secure || c.Path != "/" || c.Domain != "" || len(cookies) != 1 {
		t.Fatalf("cookies = %v, want only a Secure %s", cookies, hostCookieName)
	}
}

func TestSessionCookieOverPlainHTTPKeepsName(t *testing.T) {
	env := newTestEnv(t, "")
	env.createAdmin(t)
	c := setCookies(loginRequest(t, env))["stackstead_session"]
	if c == nil || c.Secure || c.Path != "/" || c.Domain != "" {
		t.Fatalf("cookie = %+v, want plain non-Secure stackstead_session", c)
	}
}

func TestSessionReadAcceptsEitherCookieName(t *testing.T) {
	env := newTestEnv(t, "")
	env.createAdmin(t)
	token := env.login(t).Value
	for _, name := range []string{"stackstead_session", hostCookieName} {
		c := &http.Cookie{Name: name, Value: token}
		if rec := env.do(t, http.MethodGet, "/api/me", "", withCookie(c), overTLS); rec.Code != http.StatusOK {
			t.Errorf("GET /api/me with %s = %d, want 200", name, rec.Code)
		}
	}
}

func TestLogoutClearsBothCookieNames(t *testing.T) {
	env := newTestEnv(t, "")
	env.createAdmin(t)
	hostCookie := &http.Cookie{Name: hostCookieName, Value: env.login(t).Value}
	rec := env.do(t, http.MethodPost, "/api/logout", "", overTLS, withCookie(hostCookie))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout = %d", rec.Code)
	}
	cookies := setCookies(rec)
	for _, name := range []string{"stackstead_session", hostCookieName} {
		if c := cookies[name]; c == nil || c.MaxAge >= 0 || c.Value != "" {
			t.Errorf("%s after logout = %+v, want expired", name, c)
		}
	}
	if c := cookies[hostCookieName]; c != nil && (!c.Secure || c.Path != "/" || c.Domain != "") {
		t.Errorf("expired %s must be Secure, Path=/, no Domain: %+v", hostCookieName, c)
	}
	if rec := env.do(t, http.MethodGet, "/api/me", "", withCookie(hostCookie)); rec.Code != http.StatusUnauthorized {
		t.Errorf("session still valid after logout: %d", rec.Code)
	}
}
