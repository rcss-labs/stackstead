package cli

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rcss-labs/stackstead/internal/auth"
	"github.com/rcss-labs/stackstead/internal/lockfile"
	"github.com/rcss-labs/stackstead/internal/store"
)

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

func TestConfigInitDoesNotReplaceDanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.Symlink(filepath.Join(dir, "elsewhere"), path); err != nil {
		t.Fatal(err)
	}
	err := runLocal(newTestEnv(t, nil), "config", "init", "--path", path)
	wantCode(t, err, ExitConflict)
	if target, err := os.Readlink(path); err != nil || target != filepath.Join(dir, "elsewhere") {
		t.Errorf("the symlink was replaced (%q, %v)", target, err)
	}
}

func TestLoadConfigMapsIOErrorsAwayFromValidation(t *testing.T) {
	dir := t.TempDir()
	err := runLocal(newTestEnv(t, nil), "config", "validate", "--config", dir) // a directory
	wantCode(t, err, ExitGeneral)

	if os.Geteuid() != 0 {
		unreadable := filepath.Join(dir, "secret.yaml")
		os.WriteFile(unreadable, []byte("listen: 127.0.0.1:1\n"), 0o000)
		wantCode(t, runLocal(newTestEnv(t, nil), "config", "validate", "--config", unreadable), ExitAuth)
	}
	// Parse and value problems stay validation errors.
	bad := filepath.Join(dir, "bad.yaml")
	os.WriteFile(bad, []byte("listen: [\n"), 0o600)
	wantCode(t, runLocal(newTestEnv(t, nil), "config", "validate", "--config", bad), ExitValidation)
}

func TestSetPasswordRejectsHugeInput(t *testing.T) {
	h := newLocalHost(t, true)
	env := newTestEnv(t, nil)
	env.Stdin = io.MultiReader(strings.NewReader(strings.Repeat("p", 200<<10)), strings.NewReader("\n"))
	err := runLocal(env, "admin", "set-password", "--password-stdin", "--config", h.cfgPath)
	wantCode(t, err, ExitValidation)
	mustContain(t, "error", errMessage(err), "too long")
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
