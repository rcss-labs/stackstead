package api_test

import (
	"fmt"
	"net/http"
	"net/netip"
	"testing"

	"github.com/rcss-labs/stackstead/internal/api"
)

func trustLoopbackProxy(o *api.Options) {
	o.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}
}

func fromProxy(forwardedFor ...string) func(*http.Request) {
	return func(r *http.Request) {
		r.RemoteAddr = "127.0.0.1:50000"
		for _, v := range forwardedFor {
			r.Header.Add("X-Forwarded-For", v)
		}
	}
}

func fromPeer(addr string, forwardedFor ...string) func(*http.Request) {
	return func(r *http.Request) {
		r.RemoteAddr = addr + ":50000"
		for _, v := range forwardedFor {
			r.Header.Add("X-Forwarded-For", v)
		}
	}
}

func wrongLogin(t *testing.T, env *testEnv, opts ...func(*http.Request)) int {
	t.Helper()
	return env.do(t, http.MethodPost, "/api/login", `{"username":"admin","password":"wrong password!"}`, opts...).Code
}

func rightLogin(t *testing.T, env *testEnv, opts ...func(*http.Request)) int {
	t.Helper()
	body := fmt.Sprintf(`{"username":"admin","password":%q}`, adminPassword)
	return env.do(t, http.MethodPost, "/api/login", body, opts...).Code
}

func TestLoginLimiterSeparatesClientsBehindTrustedProxy(t *testing.T) {
	env := newEnvWith(t, "", trustLoopbackProxy)
	env.createAdmin(t)
	for range 5 {
		wrongLogin(t, env, fromProxy("203.0.113.1"))
	}
	if got := wrongLogin(t, env, fromProxy("203.0.113.1")); got != http.StatusTooManyRequests {
		t.Fatalf("client A after 5 failures = %d, want 429", got)
	}
	if got := rightLogin(t, env, fromProxy("203.0.113.2")); got != http.StatusNoContent {
		t.Errorf("client B correct login = %d, want 204", got)
	}
}

func TestPasswordChangeLimiterSeparatesClientsBehindTrustedProxy(t *testing.T) {
	env := newEnvWith(t, "", trustLoopbackProxy)
	env.createAdmin(t)
	cookie := env.login(t) // no forwarded header: keyed on the proxy itself
	for range 5 {
		env.do(t, http.MethodPost, "/api/password", passwordBody("not the password", newAdminPassword), withCookie(cookie), fromProxy("203.0.113.1"))
	}
	rec := env.do(t, http.MethodPost, "/api/password", passwordBody(adminPassword, newAdminPassword), withCookie(cookie), fromProxy("203.0.113.2"))
	if rec.Code != http.StatusNoContent {
		t.Errorf("client B password change = %d, want 204", rec.Code)
	}
}

func TestLoginLimiterIgnoresForwardedForFromUntrustedPeer(t *testing.T) {
	env := newEnvWith(t, "", trustLoopbackProxy)
	env.createAdmin(t)
	// A direct client rotating X-Forwarded-For must stay in one bucket.
	for i := range 5 {
		wrongLogin(t, env, fromPeer("198.51.100.7", fmt.Sprintf("203.0.113.%d", i+1)))
	}
	if got := rightLogin(t, env, fromPeer("198.51.100.7", "203.0.113.99")); got != http.StatusTooManyRequests {
		t.Errorf("spoofed header from untrusted peer = %d, want 429", got)
	}
}

func TestLoginLimiterIgnoresForwardedForWithoutTrustedProxies(t *testing.T) {
	env := newTestEnv(t, "")
	env.createAdmin(t)
	for i := range 5 {
		wrongLogin(t, env, fromProxy(fmt.Sprintf("203.0.113.%d", i+1)))
	}
	if got := rightLogin(t, env, fromProxy("203.0.113.99")); got != http.StatusTooManyRequests {
		t.Errorf("default config honoured X-Forwarded-For: %d, want 429", got)
	}
}

func TestLoginLimiterForwardedChains(t *testing.T) {
	tests := []struct {
		name string
		// header lines of the failing client and of the later client
		failing, other []string
		wantOther      int
	}{
		{"multi hop: the rightmost untrusted hop identifies the client", []string{"198.51.100.1, 203.0.113.1"}, []string{"198.51.100.1, 203.0.113.2"}, http.StatusNoContent},
		{"client-supplied left entries cannot rotate the identity", []string{"1.1.1.1, 203.0.113.1"}, []string{"2.2.2.2, 203.0.113.1"}, http.StatusTooManyRequests},
		{"trusted hops on the right are skipped", []string{"203.0.113.1, 127.0.0.2"}, []string{"203.0.113.2, 127.0.0.3"}, http.StatusNoContent},
		{"split header lines count as one chain", []string{"9.9.9.9", "203.0.113.1"}, []string{"8.8.8.8", "203.0.113.1"}, http.StatusTooManyRequests},
		{"malformed header falls back to the proxy address", []string{"not-an-ip"}, []string{"also garbage"}, http.StatusTooManyRequests},
		{"malformed entry on the right falls back to the proxy address", []string{"203.0.113.1, junk"}, []string{"203.0.113.2, junk"}, http.StatusTooManyRequests},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newEnvWith(t, "", trustLoopbackProxy)
			env.createAdmin(t)
			for range 5 {
				wrongLogin(t, env, fromProxy(tt.failing...))
			}
			if got := rightLogin(t, env, fromProxy(tt.other...)); got != tt.wantOther {
				t.Errorf("other client = %d, want %d", got, tt.wantOther)
			}
		})
	}
}
