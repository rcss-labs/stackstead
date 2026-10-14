package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/rcss-labs/stackstead/internal/apitypes"
)

const t6Token = "metrics-token-ABCDEF123456"

func TestMetricsTokenStatus(t *testing.T) {
	for _, configured := range []bool{true, false} {
		srv := t6API(t, map[string]http.HandlerFunc{"GET /api/settings/metrics-token": func(w http.ResponseWriter, r *http.Request) {
			t6JSON(w, 200, apitypes.MetricsTokenStatus{Configured: configured})
		}})
		te := newTestEnv(t, nil)
		t6Seed(t, te, srv.URL)
		if err := t6Run(te, "settings", "metrics-token", "status"); err != nil {
			t.Fatal(err)
		}
		want := "configured: false"
		if configured {
			want = "configured: true"
		}
		if got := strings.TrimSpace(te.Out.String()); got != want {
			t.Errorf("status = %q, want %q", got, want)
		}
	}
}

func TestMetricsTokenRotatePrintsOnce(t *testing.T) {
	srv := t6API(t, map[string]http.HandlerFunc{"POST /api/settings/metrics-token": func(w http.ResponseWriter, r *http.Request) {
		t6JSON(w, 200, apitypes.MetricsToken{Token: t6Token})
	}})
	te := newTestEnv(t, nil)
	st := t6Seed(t, te, srv.URL)
	if err := t6Run(te, "settings", "metrics-token", "rotate"); err != nil {
		t.Fatal(err)
	}
	if strings.Count(te.Out.String(), t6Token) != 1 || strings.TrimSpace(te.Out.String()) != t6Token {
		t.Errorf("stdout = %q, want the token exactly once and nothing else", te.Out.String())
	}
	if strings.Contains(te.Err.String(), t6Token) || !strings.Contains(te.Err.String(), "only once") || !strings.Contains(te.Err.String(), "old token") {
		t.Errorf("stderr = %q, want a reminder without the token", te.Err.String())
	}
	t6NoLeak(t, te)

	te = newTestEnv(t, nil)
	te.ConfigDir = strings.TrimSuffix(st.Path, "/contexts.json")
	if err := t6Run(te, "settings", "metrics-token", "rotate", "-o", "json"); err != nil {
		t.Fatal(err)
	}
	var got apitypes.MetricsToken
	if err := json.Unmarshal([]byte(te.Out.String()), &got); err != nil || got.Token != t6Token {
		t.Errorf("json = %q err %v", te.Out.String(), err)
	}
}

func TestMetricsTokenClear(t *testing.T) {
	var deleted bool
	srv := t6API(t, map[string]http.HandlerFunc{"DELETE /api/settings/metrics-token": func(w http.ResponseWriter, r *http.Request) {
		deleted = true
		w.WriteHeader(204)
	}})
	te := newTestEnv(t, nil)
	t6Seed(t, te, srv.URL)
	if err := t6Run(te, "settings", "metrics-token", "clear"); err != nil || !deleted {
		t.Errorf("err %v deleted %v", err, deleted)
	}
}

func TestMetricsTokenUnauthorizedClearsSession(t *testing.T) {
	srv := t6API(t, map[string]http.HandlerFunc{"GET /api/settings/metrics-token": func(w http.ResponseWriter, r *http.Request) {
		t6JSON(w, 401, apitypes.ErrorBody{Error: "unauthorized"})
	}})
	te := newTestEnv(t, nil)
	st := t6Seed(t, te, srv.URL)
	err := t6Run(te, "settings", "metrics-token", "status")
	if t6Code(err) != ExitAuth {
		t.Errorf("code = %d, want 3", t6Code(err))
	}
	if c, _ := st.Load(); c.Contexts["lab"].Session != "" {
		t.Error("session not cleared")
	}
}

func TestSettingsPasswordStdinTwoLines(t *testing.T) {
	var got apitypes.ChangePasswordRequest
	srv := t6API(t, map[string]http.HandlerFunc{"POST /api/password": func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(204)
	}})
	te := newTestEnv(t, nil)
	t6Seed(t, te, srv.URL)
	te.Stdin = strings.NewReader("old-secret-pw\nnew-secret-pw\n")
	if err := t6Run(te, "settings", "password", "--password-stdin"); err != nil {
		t.Fatal(err)
	}
	if got.Current != "old-secret-pw" || got.New != "new-secret-pw" {
		t.Errorf("request = %+v, want current then new", got)
	}
	if !strings.Contains(te.Out.String(), "password changed; other sessions were signed out") {
		t.Errorf("stdout = %q", te.Out.String())
	}
	if strings.Contains(te.Out.String()+te.Err.String(), "secret-pw") {
		t.Error("password leaked into output")
	}
}

func TestSettingsPasswordNeedsTerminalOrStdin(t *testing.T) {
	srv := t6API(t, nil)
	te := newTestEnv(t, nil)
	t6Seed(t, te, srv.URL)
	if err := t6Run(te, "settings", "password"); t6Code(err) != ExitUsage {
		t.Errorf("code = %d, want 2", t6Code(err))
	}
}

func TestSettingsPasswordTTYPromptsWithConfirm(t *testing.T) {
	var got apitypes.ChangePasswordRequest
	srv := t6API(t, map[string]http.HandlerFunc{"POST /api/password": func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(204)
	}})
	te := newTestEnv(t, nil)
	t6Seed(t, te, srv.URL)
	te.IsTerminal = func(uintptr) bool { return true }
	answers := []string{"cur-pw-1", "new-pw-2", "new-pw-2"}
	orig := readPassword
	readPassword = func(int) ([]byte, error) { a := answers[0]; answers = answers[1:]; return []byte(a), nil }
	t.Cleanup(func() { readPassword = orig })
	if err := t6Run(te, "settings", "password"); err != nil {
		t.Fatal(err)
	}
	if got.Current != "cur-pw-1" || got.New != "new-pw-2" || len(answers) != 0 {
		t.Errorf("request %+v, unread answers %v", got, answers)
	}
}

func TestSettingsPassword403Exits3(t *testing.T) {
	srv := t6API(t, map[string]http.HandlerFunc{"POST /api/password": func(w http.ResponseWriter, r *http.Request) {
		t6JSON(w, 403, apitypes.ErrorBody{Error: "forbidden"})
	}})
	te := newTestEnv(t, nil)
	st := t6Seed(t, te, srv.URL)
	te.Stdin = strings.NewReader("old\nnew-password-1\n")
	err := t6Run(te, "settings", "password", "--password-stdin")
	if t6Code(err) != ExitAuth || err == nil || !strings.Contains(err.Error(), "current password is incorrect") {
		t.Errorf("err = %v code %d", err, t6Code(err))
	}
	if c, _ := st.Load(); c.Contexts["lab"].Session == "" {
		t.Error("a 403 must not clear the session")
	}
}

func TestSettingsPassword400Exits6And429Exits8(t *testing.T) {
	for _, c := range []struct {
		status int
		body   apitypes.ErrorBody
		code   int
		want   string
	}{
		{400, apitypes.ErrorBody{Error: "password must be at least 12 characters"}, ExitValidation, "at least 12"},
		{429, apitypes.ErrorBody{Error: "slow down"}, ExitRateLimited, "slow down"},
	} {
		srv := t6API(t, map[string]http.HandlerFunc{"POST /api/password": func(w http.ResponseWriter, r *http.Request) {
			t6JSON(w, c.status, c.body)
		}})
		te := newTestEnv(t, nil)
		t6Seed(t, te, srv.URL)
		te.Stdin = strings.NewReader("old\nshort\n")
		err := t6Run(te, "settings", "password", "--password-stdin")
		if t6Code(err) != c.code || err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("status %d: err = %v code %d, want %d containing %q", c.status, err, t6Code(err), c.code, c.want)
		}
	}
}
