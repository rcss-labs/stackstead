package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/rcss-labs/stackstead/internal/composefile"
)

func (f *fakeStacks) HasBackup(context.Context, string) bool { return f.hasBackup }
func (f *fakeStacks) RestoreBackup(_ context.Context, _, hash string) (string, error) {
	f.gotHash = hash
	return "h3", f.err
}

func TestRestoreBackupRequiresSession(t *testing.T) {
	env, _ := newStacksEnv(t, newFakeStacks())
	rec := env.do(t, "POST", "/api/stacks/web/compose/restore-backup", `{"hash":"h1"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestRestoreBackupRoute(t *testing.T) {
	fake := newFakeStacks()
	env, cookie := newStacksEnv(t, fake)
	rec := env.do(t, "POST", "/api/stacks/web/compose/restore-backup", `{"hash":"h1"}`, withCookie(cookie))
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"hash":"h3"}` || fake.gotHash != "h1" {
		t.Fatalf("restore = %d %s (hash %q)", rec.Code, rec.Body, fake.gotHash)
	}
	fake.err = composefile.ErrConflict
	if rec := env.do(t, "POST", "/api/stacks/web/compose/restore-backup", `{"hash":"old"}`, withCookie(cookie)); rec.Code != 409 {
		t.Errorf("stale = %d, want 409", rec.Code)
	}
	fake.err = composefile.ErrNoBackup
	if rec := env.do(t, "POST", "/api/stacks/web/compose/restore-backup", `{"hash":"h1"}`, withCookie(cookie)); rec.Code != 404 {
		t.Errorf("missing = %d, want 404", rec.Code)
	}
}

func TestRawComposeReportsBackup(t *testing.T) {
	fake := newFakeStacks()
	env, cookie := newStacksEnv(t, fake)
	for _, has := range []bool{false, true} {
		fake.hasBackup = has
		rec := env.do(t, "GET", "/api/stacks/web/compose", "", withCookie(cookie))
		want := `"hasBackup":false`
		if has {
			want = `"hasBackup":true`
		}
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("has=%v: %d %s", has, rec.Code, rec.Body)
		}
	}
}
