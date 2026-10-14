package api_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

const newAdminPassword = "another sturdy passphrase"

func passwordBody(current, next string) string {
	return fmt.Sprintf(`{"current":%q,"new":%q}`, current, next)
}

func TestChangePasswordRequiresSession(t *testing.T) {
	env := newTestEnv(t, "")
	env.createAdmin(t)
	rec := env.do(t, http.MethodPost, "/api/password", passwordBody(adminPassword, newAdminPassword))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestChangePasswordKeepsCurrentSessionDropsOthers(t *testing.T) {
	env := newTestEnv(t, "")
	env.createAdmin(t)
	current := env.login(t)
	other := env.login(t)

	rec := env.do(t, http.MethodPost, "/api/password", passwordBody(adminPassword, newAdminPassword), withCookie(current))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if body := rec.Body.String(); strings.Contains(body, adminPassword) || strings.Contains(body, newAdminPassword) {
		t.Errorf("response echoes a password: %q", body)
	}
	if rec := env.do(t, http.MethodGet, "/api/me", "", withCookie(current)); rec.Code != http.StatusOK {
		t.Errorf("current session /api/me = %d, want 200", rec.Code)
	}
	if rec := env.do(t, http.MethodGet, "/api/me", "", withCookie(other)); rec.Code != http.StatusUnauthorized {
		t.Errorf("other session /api/me = %d, want 401", rec.Code)
	}
	oldLogin := env.do(t, http.MethodPost, "/api/login", fmt.Sprintf(`{"username":"admin","password":%q}`, adminPassword))
	if oldLogin.Code != http.StatusUnauthorized {
		t.Errorf("login with old password = %d, want 401", oldLogin.Code)
	}
	newLogin := env.do(t, http.MethodPost, "/api/login", fmt.Sprintf(`{"username":"admin","password":%q}`, newAdminPassword))
	if newLogin.Code != http.StatusNoContent {
		t.Errorf("login with new password = %d, want 204", newLogin.Code)
	}
}

func TestChangePasswordWrongCurrentIsForbidden(t *testing.T) {
	env := newTestEnv(t, "")
	env.createAdmin(t)
	cookie := env.login(t)
	rec := env.do(t, http.MethodPost, "/api/password", passwordBody("not the password", newAdminPassword), withCookie(cookie))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "current password is incorrect") {
		t.Errorf("body = %q", rec.Body)
	}
	if rec := env.do(t, http.MethodGet, "/api/me", "", withCookie(cookie)); rec.Code != http.StatusOK {
		t.Errorf("session after a wrong attempt = %d, want 200", rec.Code)
	}
}

func TestChangePasswordRejectsWeakAndSame(t *testing.T) {
	env := newTestEnv(t, "")
	env.createAdmin(t)
	cookie := env.login(t)
	weak := env.do(t, http.MethodPost, "/api/password", passwordBody(adminPassword, "short"), withCookie(cookie))
	if weak.Code != http.StatusBadRequest || !strings.Contains(weak.Body.String(), "at least 12 characters") {
		t.Errorf("weak: status %d body %q", weak.Code, weak.Body)
	}
	same := env.do(t, http.MethodPost, "/api/password", passwordBody(adminPassword, adminPassword), withCookie(cookie))
	if same.Code != http.StatusBadRequest || !strings.Contains(same.Body.String(), "must differ") {
		t.Errorf("same: status %d body %q", same.Code, same.Body)
	}
}

func TestChangePasswordRequiresJSONContentType(t *testing.T) {
	env := newTestEnv(t, "")
	env.createAdmin(t)
	cookie := env.login(t)
	rec := env.do(t, http.MethodPost, "/api/password", passwordBody(adminPassword, newAdminPassword), withCookie(cookie),
		withHeader("Content-Type", "text/plain"))
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("status = %d, want 415", rec.Code)
	}
}

func TestChangePasswordIsRateLimited(t *testing.T) {
	env := newTestEnv(t, "")
	env.createAdmin(t)
	cookie := env.login(t)
	for i := range 5 {
		rec := env.do(t, http.MethodPost, "/api/password", passwordBody("not the password", newAdminPassword), withCookie(cookie))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("attempt %d = %d, want 403", i+1, rec.Code)
		}
	}
	rec := env.do(t, http.MethodPost, "/api/password", passwordBody(adminPassword, newAdminPassword), withCookie(cookie))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("6th attempt with the right password = %d, want 429", rec.Code)
	}

	// A success resets the counter.
	env.clock.Advance(16 * time.Minute)
	if rec := env.do(t, http.MethodPost, "/api/password", passwordBody("not the password", newAdminPassword), withCookie(cookie)); rec.Code != http.StatusForbidden {
		t.Fatalf("after window = %d, want 403", rec.Code)
	}
	if rec := env.do(t, http.MethodPost, "/api/password", passwordBody(adminPassword, newAdminPassword), withCookie(cookie)); rec.Code != http.StatusNoContent {
		t.Fatalf("success = %d, want 204", rec.Code)
	}
	for i := range 5 {
		rec := env.do(t, http.MethodPost, "/api/password", passwordBody("not the password", adminPassword), withCookie(cookie))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("post-reset attempt %d = %d, want 403 (counter not reset)", i+1, rec.Code)
		}
	}
}

// A verified current password with a rejected new one is not a guessing attempt.
func TestChangePasswordPolicyFailuresDoNotCountAgainstLimiter(t *testing.T) {
	env := newTestEnv(t, "")
	env.createAdmin(t)
	cookie := env.login(t)
	for i := range 6 {
		rec := env.do(t, http.MethodPost, "/api/password", passwordBody(adminPassword, adminPassword), withCookie(cookie))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("same-password attempt %d = %d, want 400", i+1, rec.Code)
		}
	}
	login := env.do(t, http.MethodPost, "/api/login", fmt.Sprintf(`{"username":"admin","password":%q}`, adminPassword))
	if login.Code != http.StatusNoContent {
		t.Fatalf("login after policy failures = %d, want 204", login.Code)
	}
}
