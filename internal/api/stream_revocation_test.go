package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/auth"
	"github.com/rcss-labs/stackstead/internal/state"
)

// streamPaths are authenticated long-lived streams that must not outlive
// their session.
var streamPaths = []string{"/api/events", "/api/stacks/web/logs?follow=1", "/api/stacks/web/metrics/live"}

func requireStreamEnds(t *testing.T, ch <-chan sseEvent, what string) {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-timeout:
			t.Fatalf("stream stayed open after %s", what)
		}
	}
}

func secondSession(t *testing.T, env *testEnv) *http.Cookie {
	t.Helper()
	return env.login(t)
}

func TestStreamsEndWhenSessionIsRevoked(t *testing.T) {
	revocations := []struct {
		name   string
		revoke func(t *testing.T, env *testEnv, cookie *http.Cookie)
	}{
		{"logout", func(t *testing.T, env *testEnv, cookie *http.Cookie) {
			if rec := env.do(t, http.MethodPost, "/api/logout", "", withCookie(cookie)); rec.Code != http.StatusNoContent {
				t.Fatalf("logout = %d", rec.Code)
			}
		}},
		{"password change from another session", func(t *testing.T, env *testEnv, cookie *http.Cookie) {
			other := secondSession(t, env)
			rec := env.do(t, http.MethodPost, "/api/password", passwordBody(adminPassword, newAdminPassword), withCookie(other))
			if rec.Code != http.StatusNoContent {
				t.Fatalf("password change = %d %s", rec.Code, rec.Body)
			}
		}},
		{"host-side password reset", func(t *testing.T, env *testEnv, cookie *http.Cookie) {
			if _, err := env.auth.ResetPassword(context.Background(), "admin", newAdminPassword); err != nil {
				t.Fatal(err)
			}
		}},
		{"revoke all sessions", func(t *testing.T, env *testEnv, cookie *http.Cookie) {
			if _, err := env.auth.RevokeAllSessions(context.Background()); err != nil {
				t.Fatal(err)
			}
		}},
		{"expiry", func(t *testing.T, env *testEnv, cookie *http.Cookie) {
			env.clock.Advance(auth.SessionTTL + time.Second)
		}},
	}
	for _, path := range streamPaths {
		for _, rev := range revocations {
			t.Run(path+"/"+rev.name, func(t *testing.T) {
				fake := twoContainerFake()
				env, cookie := newObserveEnv(t, fake)
				_, ch := openSSE(t, env, cookie, context.Background(), path)
				if path == "/api/events" {
					nextEvent(t, ch, true) // initial status: the stream is live
				}
				rev.revoke(t, env, cookie)
				requireStreamEnds(t, ch, rev.name)
				if rec := env.do(t, http.MethodGet, "/api/me", "", withCookie(cookie)); rec.Code != http.StatusUnauthorized {
					t.Errorf("fresh request with the revoked cookie = %d, want 401", rec.Code)
				}
			})
		}
	}
}

func TestRevokedEventsStreamDeliversNothingAndUnsubscribes(t *testing.T) {
	fake := newFakeObserve()
	env, cookie := newObserveEnv(t, fake)
	_, ch := openSSE(t, env, cookie, context.Background(), "/api/events")
	nextEvent(t, ch, true)
	if rec := env.do(t, http.MethodPost, "/api/logout", "", withCookie(cookie)); rec.Code != http.StatusNoContent {
		t.Fatalf("logout = %d", rec.Code)
	}
	select {
	case <-fake.changesCancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("change subscription was not cancelled after logout")
	}
	select {
	case fake.changes <- state.Change{Project: "web", Seq: 99}:
	default:
	}
	for ev := range ch {
		if ev.name == "change" {
			t.Fatalf("delivered %+v after logout", ev)
		}
	}
}

func TestRetainedSessionStreamSurvivesPasswordChange(t *testing.T) {
	fake := newFakeObserve()
	env, cookie := newObserveEnv(t, fake)
	_, ch := openSSE(t, env, cookie, context.Background(), "/api/events")
	nextEvent(t, ch, true)
	rec := env.do(t, http.MethodPost, "/api/password", passwordBody(adminPassword, newAdminPassword), withCookie(cookie))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("password change = %d", rec.Code)
	}
	// Well past several recheck intervals.
	time.Sleep(300 * time.Millisecond)
	fake.changes <- state.Change{Project: "web", Seq: 1}
	if ev := nextEvent(t, ch, true); ev.name != "change" {
		t.Fatalf("event = %+v, want change", ev)
	}
}

func TestStreamEndsWhenSessionDeletedBehindTheServer(t *testing.T) {
	// The host-side CLI deletes sessions from another process; the stream
	// notices at its next recheck.
	fake := newFakeObserve()
	env, cookie := newObserveEnv(t, fake)
	_, ch := openSSE(t, env, cookie, context.Background(), "/api/events")
	nextEvent(t, ch, true)
	if _, err := env.db.Exec(`DELETE FROM sessions`); err != nil {
		t.Fatal(err)
	}
	requireStreamEnds(t, ch, "direct session deletion")
}
