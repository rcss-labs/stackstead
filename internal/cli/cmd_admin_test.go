package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/auth"
	"github.com/rcss-labs/stackstead/internal/metrics"
	"github.com/rcss-labs/stackstead/internal/store"
)

const goodPassword = "correct-horse-battery"

func TestSetPasswordCreatesThenResets(t *testing.T) {
	h := newLocalHost(t, true)

	env := newTestEnv(t, nil)
	env.Stdin = strings.NewReader(goodPassword + "\n")
	wantCode(t, runLocal(env, "admin", "set-password", "--password-stdin", "--config", h.cfgPath), ExitOK)
	mustContain(t, "stdout", env.Out.String(), "admin account admin created")
	if strings.Contains(env.Out.String()+env.Err.String(), goodPassword) {
		t.Error("the password was printed")
	}

	db, err := store.OpenExisting(h.dbPath, store.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := auth.NewService(db, env.Now)
	if err != nil {
		t.Fatal(err)
	}
	token, err := svc.Login(context.Background(), "admin", goodPassword)
	if err != nil {
		t.Fatalf("login with the created password: %v", err)
	}
	db.Close()

	pwFile := filepath.Join(h.dir, "pw")
	os.WriteFile(pwFile, []byte("another-long-password\r\n"), 0o600)
	env = newTestEnv(t, nil)
	wantCode(t, runLocal(env, "admin", "set-password", "--password-file", pwFile, "--config", h.cfgPath), ExitOK)
	mustContain(t, "stdout", env.Out.String(), "password for admin updated", "signed out")

	db, err = store.OpenExisting(h.dbPath, store.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc, _ = auth.NewService(db, env.Now)
	if _, err := svc.Authenticate(context.Background(), token); err == nil {
		t.Error("old session survived the reset")
	}
	if _, err := svc.Login(context.Background(), "admin", "another-long-password"); err != nil {
		t.Errorf("new password rejected: %v", err)
	}
}

func TestSetPasswordUsernameMismatchExits6(t *testing.T) {
	h := newLocalHost(t, true)
	addAdmin(t, h.dbPath, "boss")
	env := newTestEnv(t, nil)
	env.Stdin = strings.NewReader(goodPassword + "\n")
	err := runLocal(env, "admin", "set-password", "--username", "other", "--password-stdin", "--config", h.cfgPath)
	wantCode(t, err, ExitValidation)
}

func TestSetPasswordWeakExits6AndNeedsASource(t *testing.T) {
	h := newLocalHost(t, true)
	env := newTestEnv(t, nil)
	env.Stdin = strings.NewReader("short\n")
	wantCode(t, runLocal(env, "admin", "set-password", "--password-stdin", "--config", h.cfgPath), ExitValidation)

	wantCode(t, runLocal(newTestEnv(t, nil), "admin", "set-password", "--config", h.cfgPath), ExitUsage)
	env = newTestEnv(t, nil)
	wantCode(t, runLocal(env, "admin", "set-password", "--password-stdin", "--password-file", "x", "--config", h.cfgPath), ExitUsage)
}

func TestSetPasswordNoDatabaseExits4(t *testing.T) {
	h := newLocalHost(t, false)
	env := newTestEnv(t, nil)
	env.Stdin = strings.NewReader(goodPassword + "\n")
	err := runLocal(env, "admin", "set-password", "--password-stdin", "--config", h.cfgPath)
	wantCode(t, err, ExitNotFound)
	if _, serr := os.Stat(h.dbPath); serr == nil {
		t.Error("set-password created a database")
	}
}

func TestSetPasswordSchemaMismatchExits5(t *testing.T) {
	h := newLocalHost(t, true)
	db, _ := store.OpenExisting(h.dbPath, store.OpenOptions{})
	db.Exec("PRAGMA user_version = 1")
	db.Close()
	env := newTestEnv(t, nil)
	env.Stdin = strings.NewReader(goodPassword + "\n")
	wantCode(t, runLocal(env, "admin", "set-password", "--password-stdin", "--config", h.cfgPath), ExitConflict)
}

func TestSetPasswordWhileAnotherConnectionWrites(t *testing.T) {
	h := newLocalHost(t, true)
	writer, err := store.Open(h.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				writer.Exec(`INSERT OR REPLACE INTO settings(key, value) VALUES ('busy', ?)`, i)
				// Throttled: still writing throughout the command, but without
				// starving set-password's own writer under load.
				time.Sleep(2 * time.Millisecond)
			}
		}
	}()
	env := newTestEnv(t, nil)
	env.Stdin = strings.NewReader(goodPassword + "\n")
	err = runLocal(env, "admin", "set-password", "--password-stdin", "--config", h.cfgPath)
	close(stop)
	wg.Wait()
	writer.Close()
	wantCode(t, err, ExitOK)
}

func TestAdminRefusesRootOverForeignOwner(t *testing.T) {
	h := newLocalHost(t, true)
	geteuid = func() int { return 0 }
	fileOwner = func(string) (int, error) { return 1001, nil }
	t.Cleanup(func() { fileOwner = realFileOwner })
	for _, args := range [][]string{
		{"admin", "set-password", "--password-stdin"},
		{"admin", "revoke-sessions"},
		{"admin", "metrics-token", "rotate"},
		{"db", "check"},
		{"db", "backup", filepath.Join(h.dir, "b.db")},
	} {
		env := newTestEnv(t, nil)
		env.Stdin = strings.NewReader(goodPassword + "\n")
		err := runLocal(env, append(args, "--config", h.cfgPath)...)
		wantCode(t, err, ExitAuth)
		mustContain(t, strings.Join(args, " ")+" error", errMessage(err), "sudo -u")
	}
	if _, err := os.Stat(filepath.Join(h.dataDir, "stackstead.lock")); err == nil {
		t.Error("a lock file was created before the ownership check")
	}
}

func TestRootOverRootOwnedDatabaseIsAllowed(t *testing.T) {
	h := newLocalHost(t, true)
	geteuid = func() int { return 0 }
	fileOwner = func(string) (int, error) { return 0, nil }
	t.Cleanup(func() { fileOwner = realFileOwner })
	wantCode(t, runLocal(newTestEnv(t, nil), "admin", "revoke-sessions", "--config", h.cfgPath), ExitOK)
}

func TestMetricsTokenRotatePrintsOnceAndStoresHash(t *testing.T) {
	h := newLocalHost(t, true)
	env := newTestEnv(t, nil)
	wantCode(t, runLocal(env, "admin", "metrics-token", "rotate", "--config", h.cfgPath, "-o", "json"), ExitOK)
	var got struct{ Token string }
	if err := json.Unmarshal([]byte(env.Out.String()), &got); err != nil || got.Token == "" {
		t.Fatalf("json: %v\n%q", err, env.Out.String())
	}
	mustContain(t, "stderr", env.Err.String(), "5 s")
	if strings.Contains(env.Err.String(), got.Token) {
		t.Error("token printed on stderr as well")
	}

	stored := func() string {
		db, err := store.OpenExisting(h.dbPath, store.OpenOptions{})
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		hash, err := metrics.NewStore(db, env.Now).TokenHash(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return hash
	}
	if stored() != metrics.HashToken(got.Token) {
		t.Error("stored hash does not match the printed token")
	}

	st := newTestEnv(t, nil)
	wantCode(t, runLocal(st, "admin", "metrics-token", "status", "--config", h.cfgPath, "-o", "json"), ExitOK)
	mustContain(t, "status", st.Out.String(), `"configured": true`)
	if strings.Contains(st.Out.String(), got.Token) {
		t.Error("status printed the token")
	}

	wantCode(t, runLocal(newTestEnv(t, nil), "admin", "metrics-token", "clear", "--config", h.cfgPath), ExitOK)
	if stored() != "" {
		t.Error("clear left a hash")
	}
	st = newTestEnv(t, nil)
	wantCode(t, runLocal(st, "admin", "metrics-token", "status", "--config", h.cfgPath), ExitOK)
	mustContain(t, "status", st.Out.String(), "not configured")
}

func TestMetricsTokenRotateTablePrintsToken(t *testing.T) {
	h := newLocalHost(t, true)
	env := newTestEnv(t, nil)
	wantCode(t, runLocal(env, "admin", "metrics-token", "rotate", "--config", h.cfgPath), ExitOK)
	if len(strings.TrimSpace(env.Out.String())) < 20 {
		t.Errorf("token not printed: %q", env.Out.String())
	}
}

func TestRevokeSessionsCount(t *testing.T) {
	h := newLocalHost(t, true)
	addAdmin(t, h.dbPath, "admin")
	db, _ := store.OpenExisting(h.dbPath, store.OpenOptions{})
	svc, _ := auth.NewService(db, newTestEnv(t, nil).Now, auth.WithFastHashingForTests())
	for range 3 {
		if _, err := svc.Login(context.Background(), "admin", "a-long-enough-password"); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	env := newTestEnv(t, nil)
	wantCode(t, runLocal(env, "admin", "revoke-sessions", "--config", h.cfgPath), ExitOK)
	mustContain(t, "stdout", env.Out.String(), "3")
}

func TestSetPasswordPromptsWithConfirmOnTTY(t *testing.T) {
	h := newLocalHost(t, true)
	env := newTestEnv(t, nil)
	env.IsTerminal = func(uintptr) bool { return true }
	stubPasswords(t, goodPassword, goodPassword)
	wantCode(t, runLocal(env, "admin", "set-password", "--config", h.cfgPath), ExitOK)
	mustContain(t, "stdout", env.Out.String(), "created")
	mustContain(t, "prompt", env.Err.String(), "Password", "Confirm")
}

func TestSetPasswordTTYMismatchExits6AndChangesNothing(t *testing.T) {
	h := newLocalHost(t, true)
	env := newTestEnv(t, nil)
	env.IsTerminal = func(uintptr) bool { return true }
	stubPasswords(t, goodPassword, goodPassword+"x")
	wantCode(t, runLocal(env, "admin", "set-password", "--config", h.cfgPath), ExitValidation)
	if countUsers(t, h.dbPath) != 0 {
		t.Error("a mismatched confirmation still created the admin")
	}
}

func TestSetPasswordMissingFileIsUsageError(t *testing.T) {
	h := newLocalHost(t, true)
	err := runLocal(newTestEnv(t, nil), "admin", "set-password", "--password-file", filepath.Join(t.TempDir(), "nope"), "--config", h.cfgPath)
	wantCode(t, err, ExitUsage)
}
