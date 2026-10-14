package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rcss-labs/stackstead/internal/auth"
	"github.com/rcss-labs/stackstead/internal/lockfile"
	"github.com/rcss-labs/stackstead/internal/store"
)

func countUsers(t *testing.T, dbPath string) int {
	t.Helper()
	db, err := store.OpenExisting(dbPath, store.OpenOptions{ReadOnly: true, AllowOlderSchema: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func addAdmin(t *testing.T, dbPath, name string) {
	t.Helper()
	db, err := store.OpenExisting(dbPath, store.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc, err := auth.NewService(db, newTestEnv(t, nil).Now, auth.WithFastHashingForTests())
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.CreateAdmin(context.Background(), name, "a-long-enough-password"); err != nil {
		t.Fatal(err)
	}
}

func TestDBInitCreatesAndIsIdempotent(t *testing.T) {
	h := newLocalHost(t, false)
	for range 2 {
		env := newTestEnv(t, nil)
		wantCode(t, runLocal(env, "db", "init", "--config", h.cfgPath), ExitOK)
		mustContain(t, "stdout", env.Out.String(), h.dbPath, "schema")
	}
	if _, err := os.Stat(h.dbPath); err != nil {
		t.Fatal(err)
	}
}

func TestDBInitAsRootRefusesForeignDataDir(t *testing.T) {
	h := newLocalHost(t, false)
	geteuid = func() int { return 0 }
	fileOwner = func(string) (int, error) { return 1001, nil }
	t.Cleanup(func() { fileOwner = realFileOwner })
	err := runLocal(newTestEnv(t, nil), "db", "init", "--config", h.cfgPath)
	wantCode(t, err, ExitAuth)
	mustContain(t, "error", errMessage(err), "sudo -u")
	if _, serr := os.Stat(h.dbPath); serr == nil {
		t.Error("root created a database in a foreign data dir")
	}
}

func TestDBCheckJSON(t *testing.T) {
	h := newLocalHost(t, true)
	addAdmin(t, h.dbPath, "admin")
	env := newTestEnv(t, nil)
	wantCode(t, runLocal(env, "db", "check", "--config", h.cfgPath, "-o", "json"), ExitOK)
	var got struct {
		Path            string
		Schema          int
		SupportedSchema int
		SizeBytes       int64
		WalBytes        int64
		OK              bool
		Problems        []string
		Counts          map[string]int64
	}
	if err := json.Unmarshal([]byte(env.Out.String()), &got); err != nil {
		t.Fatalf("json: %v\n%s", err, env.Out.String())
	}
	if got.Path != h.dbPath || !got.OK || got.Schema != store.SupportedSchema() || got.SupportedSchema != got.Schema || got.SizeBytes <= 0 {
		t.Errorf("unexpected report: %+v", got)
	}
	if got.Counts["users"] != 1 {
		t.Errorf("users count = %d, want 1 (%v)", got.Counts["users"], got.Counts)
	}
	for _, k := range []string{"sessions", "stacks", "metric_samples", "settings"} {
		if _, ok := got.Counts[k]; !ok {
			t.Errorf("counts lack %q", k)
		}
	}
}

func TestDBCheckMissingDatabaseExits4(t *testing.T) {
	h := newLocalHost(t, false)
	err := runLocal(newTestEnv(t, nil), "db", "check", "--config", h.cfgPath)
	wantCode(t, err, ExitNotFound)
	mustContain(t, "error", errMessage(err), h.dbPath, "db init")
}

func TestDBBackupProducesValidCopy(t *testing.T) {
	h := newLocalHost(t, true)
	addAdmin(t, h.dbPath, "admin")
	dest := filepath.Join(h.dir, "backup.db")

	env := newTestEnv(t, nil)
	wantCode(t, runLocal(env, "db", "backup", dest, "--config", h.cfgPath), ExitOK)
	mustContain(t, "stdout", env.Out.String(), dest)
	if n := countUsers(t, dest); n != 1 {
		t.Errorf("backup has %d users, want 1", n)
	}

	// Refuses an existing destination and leaves it alone.
	before, _ := os.ReadFile(dest)
	err := runLocal(newTestEnv(t, nil), "db", "backup", dest, "--config", h.cfgPath)
	wantCode(t, err, ExitConflict)
	if after, _ := os.ReadFile(dest); string(after) != string(before) {
		t.Error("existing backup was modified")
	}

	// --force replaces it atomically and leaves no temp file behind.
	wantCode(t, runLocal(newTestEnv(t, nil), "db", "backup", dest, "--force", "--config", h.cfgPath), ExitOK)
	entries, _ := os.ReadDir(h.dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") || strings.Contains(e.Name(), ".new") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
	if n := countUsers(t, dest); n != 1 {
		t.Errorf("forced backup has %d users, want 1", n)
	}
}

func TestDBBackupWhileServerLockHeld(t *testing.T) {
	h := newLocalHost(t, true)
	lock, err := lockfile.Acquire(filepath.Join(h.dataDir, "stackstead.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	dest := filepath.Join(h.dir, "online.db")
	wantCode(t, runLocal(newTestEnv(t, nil), "db", "backup", dest, "--config", h.cfgPath), ExitOK)
}

// snapshot copies the database so a later restore has something older to bring back.
func snapshot(t *testing.T, h *localHost) string {
	t.Helper()
	dest := filepath.Join(h.dir, "snap.db")
	wantCode(t, runLocal(newTestEnv(t, nil), "db", "backup", dest, "--config", h.cfgPath), ExitOK)
	return dest
}

func TestRestoreRefusedWhileLocked(t *testing.T) {
	h := newLocalHost(t, true)
	snap := snapshot(t, h)
	addAdmin(t, h.dbPath, "admin")
	before, _ := os.ReadFile(h.dbPath)

	lock, err := lockfile.Acquire(filepath.Join(h.dataDir, "stackstead.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	err = runLocal(newTestEnv(t, nil), "db", "restore", snap, "--yes", "--config", h.cfgPath)
	wantCode(t, err, ExitConflict)
	mustContain(t, "error", errMessage(err), "server is running")
	if after, _ := os.ReadFile(h.dbPath); string(after) != string(before) {
		t.Error("database changed although the lock was held")
	}
}

func TestRestoreReplacesDBAndKeepsBak(t *testing.T) {
	h := newLocalHost(t, true)
	snap := snapshot(t, h) // no users
	addAdmin(t, h.dbPath, "admin")
	// A stale WAL left by a crashed server must not survive the restore.
	for _, suffix := range []string{"-wal", "-shm"} {
		os.WriteFile(h.dbPath+suffix, []byte("stale"), 0o600)
	}
	if countUsers(t, h.dbPath) != 1 {
		t.Fatal("setup: expected one user")
	}

	env := newTestEnv(t, nil)
	wantCode(t, runLocal(env, "db", "restore", snap, "--yes", "--config", h.cfgPath), ExitOK)
	if n := countUsers(t, h.dbPath); n != 0 {
		t.Errorf("restored db has %d users, want 0", n)
	}
	baks, _ := filepath.Glob(h.dbPath + ".bak-*")
	if len(baks) == 0 {
		t.Fatal("no .bak- file kept")
	}
	mustContain(t, "stdout", env.Out.String(), baks[0], "schema")
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(h.dbPath + suffix); err == nil {
			t.Errorf("stale %s remains", suffix)
		}
	}
	if _, err := os.Stat(h.dbPath + ".restore-tmp"); err == nil {
		t.Error("restore temp file remains")
	}
	if countUsers(t, baks[0]) != 1 {
		t.Error("the kept backup does not hold the replaced database")
	}
}

func TestRestoreRejectsNewerSchemaAndCorruptSource(t *testing.T) {
	h := newLocalHost(t, true)
	before, _ := os.ReadFile(h.dbPath)

	newer := filepath.Join(h.dir, "newer.db")
	snap := snapshot(t, h)
	data, _ := os.ReadFile(snap)
	os.WriteFile(newer, data, 0o600)
	db, err := store.OpenExisting(newer, store.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = 999"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	err = runLocal(newTestEnv(t, nil), "db", "restore", newer, "--yes", "--config", h.cfgPath)
	wantCode(t, err, ExitValidation)

	corrupt := filepath.Join(h.dir, "corrupt.db")
	os.WriteFile(corrupt, []byte(strings.Repeat("not a database ", 100)), 0o600)
	err = runLocal(newTestEnv(t, nil), "db", "restore", corrupt, "--yes", "--config", h.cfgPath)
	wantCode(t, err, ExitGeneral)

	missing := filepath.Join(h.dir, "missing.db")
	wantCode(t, runLocal(newTestEnv(t, nil), "db", "restore", missing, "--yes", "--config", h.cfgPath), ExitNotFound)

	if after, _ := os.ReadFile(h.dbPath); string(after) != string(before) {
		t.Error("a rejected restore touched the database")
	}
	if baks, _ := filepath.Glob(h.dbPath + ".bak-*"); len(baks) != 0 {
		t.Error("a rejected restore left a .bak-")
	}
}

func TestRestoreNeedsYesWithoutTTY(t *testing.T) {
	h := newLocalHost(t, true)
	snap := snapshot(t, h)
	addAdmin(t, h.dbPath, "admin")
	err := runLocal(newTestEnv(t, nil), "db", "restore", snap, "--config", h.cfgPath)
	wantCode(t, err, ExitUsage)
	mustContain(t, "error", errMessage(err), "--yes")
	if countUsers(t, h.dbPath) != 1 {
		t.Error("restore ran without confirmation")
	}
}

func TestRestoreAsksOnTTY(t *testing.T) {
	h := newLocalHost(t, true)
	snap := snapshot(t, h)
	addAdmin(t, h.dbPath, "admin")

	env := newTestEnv(t, nil)
	env.Stdin = strings.NewReader("n\n")
	env.IsTerminal = func(uintptr) bool { return true }
	wantCode(t, runLocal(env, "db", "restore", snap, "--config", h.cfgPath), ExitGeneral)
	mustContain(t, "prompt", env.Out.String()+env.Err.String(), h.dbPath, snap)
	if countUsers(t, h.dbPath) != 1 {
		t.Error("restore ran after the user declined")
	}

	env = newTestEnv(t, nil)
	env.Stdin = strings.NewReader("y\n")
	env.IsTerminal = func(uintptr) bool { return true }
	wantCode(t, runLocal(env, "db", "restore", snap, "--config", h.cfgPath), ExitOK)
	if countUsers(t, h.dbPath) != 0 {
		t.Error("restore did not run after confirmation")
	}
}
