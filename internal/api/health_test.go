package api_test

import "testing"

func TestHealthIsPublic(t *testing.T) {
	env := newTestEnv(t, "")
	rec := env.do(t, "GET", "/api/health", "")
	if rec.Code != 200 {
		t.Fatalf("health = %d, body %s", rec.Code, rec.Body)
	}
	if got := rec.Body.String(); got != "{\"ok\":true,\"peering\":1}\n" && got != "{\"ok\":true,\"peering\":1}" {
		t.Errorf("body = %q", got)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q", cc)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" && ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestUnknownAPIPathStillRequiresSession(t *testing.T) {
	env := newTestEnv(t, "")
	if rec := env.do(t, "GET", "/api/healthz", ""); rec.Code != 401 {
		t.Errorf("/api/healthz without session = %d, want 401", rec.Code)
	}
}
