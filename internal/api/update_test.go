package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/api"
	"github.com/rcss-labs/stackstead/internal/apitypes"
)

type fakeUpdates struct {
	status        apitypes.UpdateStatus
	checkCalls    int
	checkCtxErr   error
	cancelRequest context.CancelFunc
}

func (f *fakeUpdates) Status() apitypes.UpdateStatus { return f.status }

func (f *fakeUpdates) CheckNow(ctx context.Context) apitypes.UpdateStatus {
	f.checkCalls++
	if f.cancelRequest != nil {
		f.cancelRequest() // the browser goes away while the check runs
	}
	f.checkCtxErr = ctx.Err()
	return f.status
}

func updatesEnv(t *testing.T, fake *fakeUpdates) (*testEnv, *http.Cookie) {
	t.Helper()
	env := newEnvWith(t, "", func(o *api.Options) {
		if fake != nil {
			o.Updates = fake
		}
	})
	env.createAdmin(t)
	return env, env.login(t)
}

func sampleUpdateStatus() apitypes.UpdateStatus {
	return apitypes.UpdateStatus{
		Current:   "1.2.0",
		Installer: "script",
		Command:   "'/usr/local/bin/stackstead' upgrade",
		State:     "available",
		Latest:    "1.3.0",
		NotesURL:  "https://github.com/rcss-labs/stackstead/releases/tag/v1.3.0",
		CheckedAt: time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC),
	}
}

func TestUpdateStatusRequiresSession(t *testing.T) {
	env, _ := updatesEnv(t, &fakeUpdates{})
	for _, rt := range []struct{ method, path string }{
		{"GET", "/api/settings/update"}, {"POST", "/api/settings/update/check"},
	} {
		if rec := env.do(t, rt.method, rt.path, "{}"); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without session = %d, want 401", rt.method, rt.path, rec.Code)
		}
	}
}

func TestUpdateStatusBody(t *testing.T) {
	fake := &fakeUpdates{status: sampleUpdateStatus()}
	env, cookie := updatesEnv(t, fake)
	rec := env.do(t, "GET", "/api/settings/update", "", withCookie(cookie))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var got apitypes.UpdateStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got != fake.status {
		t.Errorf("body = %+v, want %+v", got, fake.status)
	}

	fake.status = apitypes.UpdateStatus{Current: "1.2.0", State: "unknown"}
	rec = env.do(t, "GET", "/api/settings/update", "", withCookie(cookie))
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["checkedAt"]; ok {
		t.Errorf("checkedAt present for a zero time: %s", rec.Body)
	}
	for _, key := range []string{"current", "installer", "command", "state", "latest", "notesURL", "error"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("key %q missing: %s", key, rec.Body)
		}
	}
}

// A browser that gives up mid-check must not turn the shared check into a
// recorded "network" failure.
func TestUpdateCheckIgnoresClientCancellation(t *testing.T) {
	fake := &fakeUpdates{status: sampleUpdateStatus()}
	env, cookie := updatesEnv(t, fake)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake.cancelRequest = cancel
	env.do(t, "POST", "/api/settings/update/check", "{}", withCookie(cookie), func(r *http.Request) {
		*r = *r.WithContext(ctx)
	})
	if fake.checkCalls != 1 || fake.checkCtxErr != nil {
		t.Errorf("checkCalls = %d, ctx err = %v; the check must not see the request's cancellation", fake.checkCalls, fake.checkCtxErr)
	}
}

func TestUpdateCheckRequiresJSON(t *testing.T) {
	fake := &fakeUpdates{status: sampleUpdateStatus()}
	env, cookie := updatesEnv(t, fake)

	req := func(body string, opts ...func(*http.Request)) int {
		return env.do(t, "POST", "/api/settings/update/check", body, append(opts, withCookie(cookie))...).Code
	}
	if code := req("{}", withHeader("Content-Type", "text/plain")); code != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain = %d, want 415", code)
	}
	if fake.checkCalls != 0 {
		t.Fatalf("checkCalls after 415 = %d", fake.checkCalls)
	}
	if code := req(`{"version":"9.9.9"}`); code != http.StatusBadRequest {
		t.Errorf("unknown field = %d, want 400", code)
	}
	if fake.checkCalls != 0 {
		t.Fatalf("checkCalls after 400 = %d", fake.checkCalls)
	}
	rec := env.do(t, "POST", "/api/settings/update/check", "{}", withCookie(cookie))
	if rec.Code != http.StatusOK {
		t.Fatalf("{} = %d, body %s", rec.Code, rec.Body)
	}
	if fake.checkCalls != 1 {
		t.Errorf("checkCalls = %d, want 1", fake.checkCalls)
	}
	var got apitypes.UpdateStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got != fake.status {
		t.Errorf("body = %s (%v)", rec.Body, err)
	}
}

func TestUpdateRoutesAbsentWithoutUpdates(t *testing.T) {
	env, cookie := updatesEnv(t, nil)
	for _, rt := range []struct{ method, path string }{
		{"GET", "/api/settings/update"}, {"POST", "/api/settings/update/check"},
	} {
		rec := env.do(t, rt.method, rt.path, "{}", withCookie(cookie))
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("%s %s = %d %q, want JSON 404", rt.method, rt.path, rec.Code, rec.Body)
		}
	}
}

func TestUpdateStatusDropsUnprintableCommand(t *testing.T) {
	long := "'/" + strings.Repeat("a", 600) + "' upgrade"
	for name, cmd := range map[string]string{
		"escape": "stackstead upgrade\x1b[31m", "newline": "a\nb", "non-ascii": "stackstead upgräde", "too long": long,
	} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeUpdates{status: sampleUpdateStatus()}
			fake.status.Command = cmd
			env, cookie := updatesEnv(t, fake)
			for _, rec := range []interface{ String() string }{
				env.do(t, "GET", "/api/settings/update", "", withCookie(cookie)).Body,
				env.do(t, "POST", "/api/settings/update/check", "{}", withCookie(cookie)).Body,
			} {
				var got apitypes.UpdateStatus
				if err := json.Unmarshal([]byte(rec.String()), &got); err != nil {
					t.Fatal(err)
				}
				if got.Command != "" {
					t.Errorf("command = %q, want empty", got.Command)
				}
				if got.Latest != "1.3.0" {
					t.Errorf("other fields changed: %+v", got)
				}
			}
		})
	}
}

func TestUpdateStatusDropsForeignNotesURL(t *testing.T) {
	for name, u := range map[string]string{
		"http":        "http://github.com/rcss-labs/stackstead/releases/tag/v1.3.0",
		"host":        "https://evil.example/releases",
		"lookalike":   "https://github.com.evil.example/x",
		"userinfo":    "https://github.com@evil.example/x",
		"credentials": "https://user:pw@github.com/x",
		"javascript":  "javascript:alert(1)",
	} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeUpdates{status: sampleUpdateStatus()}
			fake.status.NotesURL = u
			env, cookie := updatesEnv(t, fake)
			var got apitypes.UpdateStatus
			rec := env.do(t, "GET", "/api/settings/update", "", withCookie(cookie))
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.NotesURL != "" {
				t.Errorf("notesURL = %q, want empty", got.NotesURL)
			}
		})
	}
}
