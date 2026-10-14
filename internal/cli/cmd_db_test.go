package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
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

func TestRestoreConfirmationUsesSharedPromptOnStderr(t *testing.T) {
	h := newLocalHost(t, true)
	snap := snapshot(t, h)
	env := newTestEnv(t, nil)
	env.Stdin = strings.NewReader("n\n")
	env.IsTerminal = func(uintptr) bool { return true }
	wantCode(t, runLocal(env, "db", "restore", snap, "--config", h.cfgPath), ExitGeneral)
	mustContain(t, "stderr", env.Err.String(), "[y/N]")
	if env.Out.String() != "" {
		t.Errorf("the question must not pollute stdout: %q", env.Out.String())
	}
}

func copyTestFile(t *testing.T, from, to string) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The restore source may hold committed data only in its -wal file; the
// restored database must contain it.
func TestRestoreIncludesDataOnlyInSourceWAL(t *testing.T) {
	h := newLocalHost(t, true)
	srcDir := t.TempDir()
	work := filepath.Join(srcDir, "work.db")
	live, err := store.Open(work) // stays open: no checkpoint on close
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	svc, err := auth.NewService(live, newTestEnv(t, nil).Now, auth.WithFastHashingForTests())
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.CreateAdmin(context.Background(), "walonly", "a-long-enough-password"); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(srcDir, "copy.db")
	copyTestFile(t, work, src)
	copyTestFile(t, work+"-wal", src+"-wal")
	if info, err := os.Stat(src + "-wal"); err != nil || info.Size() == 0 {
		t.Fatal("setup: expected a non-empty -wal")
	}

	wantCode(t, runLocal(newTestEnv(t, nil), "db", "restore", src, "--yes", "--config", h.cfgPath), ExitOK)
	if n := countUsers(t, h.dbPath); n != 1 {
		t.Errorf("restored database has %d users, want 1 (data in the source WAL was lost)", n)
	}
}

func TestBackupRefusesLiveDestinations(t *testing.T) {
	h := newLocalHost(t, true)
	lockPath := filepath.Join(h.dataDir, "stackstead.lock")
	lock, err := lockfile.Acquire(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	symlink := filepath.Join(h.dir, "link.db")
	if err := os.Symlink(h.dbPath, symlink); err != nil {
		t.Fatal(err)
	}
	hardlink := filepath.Join(h.dir, "hard.db")
	if err := os.Link(h.dbPath, hardlink); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(h.dbPath+"-wal", []byte("wal-bytes"), 0o600)
	dbBefore, lockBefore, walBefore := readFile(t, h.dbPath), readFile(t, lockPath), readFile(t, h.dbPath+"-wal")

	dests := []string{h.dbPath, h.dbPath + "-wal", h.dbPath + "-shm", lockPath, symlink, hardlink,
		filepath.Join(h.dataDir, "..", "data", "stackstead.db")}
	for _, dest := range dests {
		for _, force := range []bool{true, false} {
			args := []string{"db", "backup", dest, "--config", h.cfgPath}
			if force {
				args = append(args, "--force")
			}
			err := runLocal(newTestEnv(t, nil), args...)
			if codeFor(err) == ExitOK {
				t.Errorf("backup to %s (force=%v) succeeded", dest, force)
			}
		}
	}
	if readFile(t, h.dbPath) != dbBefore || readFile(t, lockPath) != lockBefore || readFile(t, h.dbPath+"-wal") != walBefore {
		t.Error("a refused backup modified a live file")
	}
	if _, err := os.Stat(h.dbPath + "-shm"); err == nil {
		t.Error("a -shm file was created")
	}
}

func TestBackupVerificationFailureLeavesNothing(t *testing.T) {
	h := newLocalHost(t, true)
	orig := verifyDatabase
	verifyDatabase = func(context.Context, string) (int, int64, error) { return 0, 0, errors.New("verify failed") }
	t.Cleanup(func() { verifyDatabase = orig })

	dest := filepath.Join(h.dir, "out.db")
	wantCode(t, runLocal(newTestEnv(t, nil), "db", "backup", dest, "--config", h.cfgPath), ExitGeneral)
	if _, err := os.Stat(dest); err == nil {
		t.Error("an unverified backup was left in place")
	}

	os.WriteFile(dest, []byte("previous good backup"), 0o600)
	wantCode(t, runLocal(newTestEnv(t, nil), "db", "backup", dest, "--force", "--config", h.cfgPath), ExitGeneral)
	if readFile(t, dest) != "previous good backup" {
		t.Error("--force replaced the old backup with an unverified one")
	}
	entries, _ := os.ReadDir(h.dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".new-") {
			t.Errorf("leftover %s", e.Name())
		}
	}
}

func TestRestoreRollsBackWhenFinalRenameFails(t *testing.T) {
	h := newLocalHost(t, true)
	snap := snapshot(t, h)
	addAdmin(t, h.dbPath, "admin")
	// The pre-restore checkpoint folds any WAL into the main file, so the
	// database is compared after it has settled.
	wantCode(t, runLocal(newTestEnv(t, nil), "db", "check", "--config", h.cfgPath), ExitOK)
	dbBefore := readFile(t, h.dbPath)

	orig := renameFile
	renameFile = func(from, to string) error {
		if to == h.dbPath && strings.HasSuffix(from, ".restore-tmp") {
			return errors.New("injected rename failure")
		}
		return orig(from, to)
	}
	t.Cleanup(func() { renameFile = orig })

	err := runLocal(newTestEnv(t, nil), "db", "restore", snap, "--yes", "--config", h.cfgPath)
	if codeFor(err) == ExitOK {
		t.Fatal("restore reported success")
	}
	if readFile(t, h.dbPath) != dbBefore {
		t.Error("the current database changed")
	}
	for _, pattern := range []string{".restore-tmp", ".bak-*"} {
		if m, _ := filepath.Glob(h.dbPath + pattern); len(m) != 0 {
			t.Errorf("leftover %v", m)
		}
	}
}

func TestRestoreRejectsPageCorruptedSource(t *testing.T) {
	h := newLocalHost(t, true)
	db, err := store.OpenExisting(h.dbPath, store.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	blob := strings.Repeat("x", 1000)
	for i := range 300 {
		if _, err := db.Exec(`INSERT INTO settings(key, value) VALUES (?, ?)`, "k"+string(rune('a'+i%26))+string(rune('a'+i/26)), blob); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	snap := snapshot(t, h)
	data := []byte(readFile(t, snap))
	for i := 4096 * 20; i < 4096*22 && i < len(data); i++ {
		data[i] = 0xFF
	}
	bad := filepath.Join(h.dir, "bad.db")
	os.WriteFile(bad, data, 0o600)
	before := readFile(t, h.dbPath)

	err = runLocal(newTestEnv(t, nil), "db", "restore", bad, "--yes", "--config", h.cfgPath)
	wantCode(t, err, ExitGeneral)
	if readFile(t, h.dbPath) != before {
		t.Error("a corrupt source replaced the database")
	}
	if m, _ := filepath.Glob(h.dbPath + ".bak-*"); len(m) != 0 {
		t.Error("a .bak- was created")
	}
}

func TestRestoreRefusesRootOverForeignOwner(t *testing.T) {
	h := newLocalHost(t, true)
	snap := snapshot(t, h)
	geteuid = func() int { return 0 }
	fileOwner = func(string) (int, error) { return 1001, nil }
	t.Cleanup(func() { fileOwner = realFileOwner })
	err := runLocal(newTestEnv(t, nil), "db", "restore", snap, "--yes", "--config", h.cfgPath)
	wantCode(t, err, ExitAuth)
	mustContain(t, "error", errMessage(err), "sudo -u")
	if _, serr := os.Stat(filepath.Join(h.dataDir, "stackstead.lock")); serr == nil {
		t.Error("a lock file was created before the ownership check")
	}
}

func TestOwnershipFallsBackToDataDir(t *testing.T) {
	h := newLocalHost(t, false) // no database yet
	geteuid = func() int { return 0 }
	fileOwner = func(path string) (int, error) {
		if path == h.dbPath {
			return 0, &fs.PathError{Op: "stat", Path: path, Err: fs.ErrNotExist}
		}
		return 1001, nil
	}
	t.Cleanup(func() { fileOwner = realFileOwner })
	err := runLocal(newTestEnv(t, nil), "db", "init", "--config", h.cfgPath)
	wantCode(t, err, ExitAuth)
	mustContain(t, "error", errMessage(err), "sudo -u")
}

func TestDBInitAsRootRefusesMissingDataDir(t *testing.T) {
	h := newLocalHost(t, false)
	os.RemoveAll(h.dataDir)
	geteuid = func() int { return 0 }
	err := runLocal(newTestEnv(t, nil), "db", "init", "--config", h.cfgPath)
	wantCode(t, err, ExitAuth)
	mustContain(t, "error", errMessage(err), "sudo -u")
	if _, serr := os.Stat(h.dataDir); serr == nil {
		t.Error("root created the data dir")
	}
}

func TestRestoreKeepsCommittedWalDataInBak(t *testing.T) {
	h := newLocalHost(t, true)
	snap := snapshot(t, h) // no users

	// Reproduce a crashed server: a user committed only to the -wal.
	live, err := store.OpenExisting(h.dbPath, store.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := live.Exec("PRAGMA wal_autocheckpoint = 0"); err != nil {
		t.Fatal(err)
	}
	svc, err := auth.NewService(live, newTestEnv(t, nil).Now, auth.WithFastHashingForTests())
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.CreateAdmin(context.Background(), "admin", "a-long-enough-password"); err != nil {
		t.Fatal(err)
	}
	mainBytes, walBytes := readFile(t, h.dbPath), readFile(t, h.dbPath+"-wal")
	live.Close() // checkpoints and removes the -wal; put the crashed state back
	if len(walBytes) == 0 {
		t.Fatal("setup: no WAL content")
	}
	os.Remove(h.dbPath + "-shm")
	os.WriteFile(h.dbPath, []byte(mainBytes), 0o600)
	os.WriteFile(h.dbPath+"-wal", []byte(walBytes), 0o600)

	wantCode(t, runLocal(newTestEnv(t, nil), "db", "restore", snap, "--yes", "--config", h.cfgPath), ExitOK)
	baks, _ := filepath.Glob(h.dbPath + ".bak-*")
	var bak string
	for _, b := range baks {
		if !strings.HasSuffix(b, "-wal") {
			bak = b
		}
	}
	if bak == "" {
		t.Fatal("no .bak- file kept")
	}
	alone := filepath.Join(t.TempDir(), "alone.db") // without any -wal beside it
	if err := os.WriteFile(alone, []byte(readFile(t, bak)), 0o600); err != nil {
		t.Fatal(err)
	}
	if n := countUsers(t, alone); n != 1 {
		t.Errorf("the kept database alone holds %d users, want 1 (committed WAL data was orphaned)", n)
	}
}

func TestRestoreVerifiesTheTempCopy(t *testing.T) {
	h := newLocalHost(t, true)
	snap := snapshot(t, h)
	addAdmin(t, h.dbPath, "admin")
	dbBefore := readFile(t, h.dbPath)

	orig := verifyDatabase
	verifyDatabase = func(ctx context.Context, path string) (int, int64, error) {
		if strings.HasSuffix(path, ".restore-tmp") {
			return 0, 0, errors.New("quick_check failed (injected)")
		}
		return orig(ctx, path)
	}
	t.Cleanup(func() { verifyDatabase = orig })

	err := runLocal(newTestEnv(t, nil), "db", "restore", snap, "--yes", "--config", h.cfgPath)
	if codeFor(err) == ExitOK {
		t.Fatal("restore reported success with an unverified copy")
	}
	if readFile(t, h.dbPath) != dbBefore || countUsers(t, h.dbPath) != 1 {
		t.Error("the current database was replaced")
	}
	for _, pattern := range []string{".restore-tmp", ".bak-*"} {
		if m, _ := filepath.Glob(h.dbPath + pattern); len(m) != 0 {
			t.Errorf("leftover %v", m)
		}
	}
}

func TestRestoreAbortsWhenWalCannotBeMoved(t *testing.T) {
	h := newLocalHost(t, true)
	snap := snapshot(t, h)
	addAdmin(t, h.dbPath, "admin")
	dbBefore := readFile(t, h.dbPath)

	orig := renameFile
	renameFile = func(from, to string) error {
		if from == h.dbPath+"-wal" {
			return errors.New("injected -wal move failure")
		}
		return orig(from, to)
	}
	t.Cleanup(func() { renameFile = orig })

	err := runLocal(newTestEnv(t, nil), "db", "restore", snap, "--yes", "--config", h.cfgPath)
	if codeFor(err) == ExitOK {
		t.Fatal("restore reported success although the -wal could not be moved")
	}
	if readFile(t, h.dbPath) != dbBefore || countUsers(t, h.dbPath) != 1 {
		t.Error("the current database was replaced")
	}
	for _, pattern := range []string{".restore-tmp", ".bak-*"} {
		if m, _ := filepath.Glob(h.dbPath + pattern); len(m) != 0 {
			t.Errorf("leftover %v", m)
		}
	}
}

func bakFiles(h *localHost) []string {
	all, _ := filepath.Glob(h.dbPath + ".bak-*")
	var out []string
	for _, b := range all {
		if !strings.HasSuffix(b, "-wal") {
			out = append(out, b)
		}
	}
	return out
}

func TestRestoreOverGarbageCurrentDatabase(t *testing.T) {
	h := newLocalHost(t, true)
	snap := snapshot(t, h)
	os.WriteFile(h.dbPath, []byte(strings.Repeat("not a database ", 500)), 0o600)
	os.Remove(h.dbPath + "-wal")
	os.Remove(h.dbPath + "-shm")

	wantCode(t, runLocal(newTestEnv(t, nil), "db", "restore", snap, "--yes", "--config", h.cfgPath), ExitOK)
	if n := countUsers(t, h.dbPath); n != 0 {
		t.Errorf("restored db has %d users, want 0", n)
	}
	if len(bakFiles(h)) != 1 {
		t.Errorf("want the broken database kept as one .bak, got %v", bakFiles(h))
	}
}

func TestRestoreOverNewerSchemaCurrentDatabase(t *testing.T) {
	h := newLocalHost(t, true)
	snap := snapshot(t, h)
	db, err := store.OpenExisting(h.dbPath, store.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	db.Exec("PRAGMA user_version = 999")
	db.Close()

	wantCode(t, runLocal(newTestEnv(t, nil), "db", "restore", snap, "--yes", "--config", h.cfgPath), ExitOK)
	if len(bakFiles(h)) != 1 {
		t.Errorf("want the newer-schema database kept as one .bak, got %v", bakFiles(h))
	}
}

func TestRestoreRollbackPutsWalBackWhenCheckpointFailed(t *testing.T) {
	h := newLocalHost(t, true)
	snap := snapshot(t, h)
	os.WriteFile(h.dbPath, []byte(strings.Repeat("not a database ", 500)), 0o600)
	os.WriteFile(h.dbPath+"-wal", []byte("stale-wal"), 0o600)
	dbBefore := readFile(t, h.dbPath)

	// SQLite itself may consume a bad -wal while failing to open, so the
	// "checkpoint failed, -wal still there" state is simulated.
	origCP := checkpointDatabase
	checkpointDatabase = func(context.Context, *Env, string) error { return nil }
	t.Cleanup(func() { checkpointDatabase = origCP })
	orig := renameFile
	renameFile = func(from, to string) error {
		if to == h.dbPath && strings.HasSuffix(from, ".restore-tmp") {
			return errors.New("injected rename failure")
		}
		return orig(from, to)
	}
	t.Cleanup(func() { renameFile = orig })

	err := runLocal(newTestEnv(t, nil), "db", "restore", snap, "--yes", "--config", h.cfgPath)
	if codeFor(err) == ExitOK {
		t.Fatal("restore reported success")
	}
	if readFile(t, h.dbPath) != dbBefore {
		t.Error("the current database changed")
	}
	if readFile(t, h.dbPath+"-wal") != "stale-wal" {
		t.Error("the -wal was not put back")
	}
}

func TestRestoreRefusesWhenCheckpointIsBusy(t *testing.T) {
	h := newLocalHost(t, true)
	snap := snapshot(t, h)
	addAdmin(t, h.dbPath, "admin")
	dbBefore := readFile(t, h.dbPath)
	origCP := checkpointDatabase
	checkpointDatabase = func(context.Context, *Env, string) error { return errors.New("database is busy") }
	t.Cleanup(func() { checkpointDatabase = origCP })

	err := runLocal(newTestEnv(t, nil), "db", "restore", snap, "--yes", "--config", h.cfgPath)
	if codeFor(err) == ExitOK {
		t.Fatal("restore reported success")
	}
	if readFile(t, h.dbPath) != dbBefore || len(bakFiles(h)) != 0 {
		t.Error("a refused restore changed things")
	}
}

func TestRestoreWarnsAndProceedsOverGarbageDatabaseAndWal(t *testing.T) {
	h := newLocalHost(t, true)
	snap := snapshot(t, h)
	os.WriteFile(h.dbPath, []byte(strings.Repeat("not a database ", 500)), 0o600)
	os.WriteFile(h.dbPath+"-wal", []byte("garbage wal"), 0o600)

	env := newTestEnv(t, nil)
	wantCode(t, runLocal(env, "db", "restore", snap, "--yes", "--config", h.cfgPath), ExitOK)
	mustContain(t, "stderr", env.Err.String(), "warning", "any -wal is kept next to the backup")
	if len(bakFiles(h)) != 1 {
		t.Errorf("want the broken database kept as one .bak, got %v", bakFiles(h))
	}
	if n := countUsers(t, h.dbPath); n != 0 {
		t.Errorf("restored db has %d users, want 0", n)
	}
}

func TestIsBusyCheckpointError(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	_, syntaxErr := db.Exec("NOT SQL")
	if isBusyError(syntaxErr) {
		t.Error("a syntax error is not busy")
	}
	if isBusyError(errors.New("database is locked")) != true || isBusyError(errors.New("database table is locked")) != true {
		t.Error("locked/busy messages must count as busy")
	}
	if isBusyError(nil) {
		t.Error("nil is not busy")
	}
	// A real SQLITE_BUSY: a second connection cannot checkpoint while a reader pins the WAL.
	path := filepath.Join(t.TempDir(), "b.db")
	a, _ := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(0)&_pragma=journal_mode(WAL)")
	a.SetMaxOpenConns(1)
	defer a.Close()
	a.Exec("CREATE TABLE t(x)")
	b, _ := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(0)&_pragma=journal_mode(WAL)")
	defer b.Close()
	tx, err := b.Begin()
	if err != nil {
		t.Fatal(err)
	}
	tx.Exec("INSERT INTO t VALUES (1)") // write lock held by b
	_, busyErr := a.Exec("INSERT INTO t VALUES (2)")
	tx.Rollback()
	if busyErr == nil {
		t.Skip("could not provoke SQLITE_BUSY")
	}
	if !isBusyError(busyErr) {
		t.Errorf("real busy error not classified: %v", busyErr)
	}
}
