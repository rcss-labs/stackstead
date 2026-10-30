package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/actionlog"
	"github.com/rcss-labs/stackstead/internal/api"
	"github.com/rcss-labs/stackstead/internal/auth"
	"github.com/rcss-labs/stackstead/internal/backup"
	"github.com/rcss-labs/stackstead/internal/config"
	"github.com/rcss-labs/stackstead/internal/lockfile"
	"github.com/rcss-labs/stackstead/internal/stacks"
	"github.com/rcss-labs/stackstead/internal/store"
	"github.com/rcss-labs/stackstead/internal/version"
)

func TestBuildRunnerAlwaysPinsDockerHostToConfiguredSocket(t *testing.T) {
	for _, sock := range []string{config.Defaults().DockerSocket, "/run/user/1000/docker.sock"} {
		cfg := config.Defaults()
		cfg.DockerSocket = sock
		r := buildRunner(cfg)
		want := "DOCKER_HOST=unix://" + sock
		found := false
		for _, e := range r.Env {
			if e == want {
				found = true
			}
		}
		if !found {
			t.Errorf("socket %s: Env = %v, want %s", sock, r.Env, want)
		}
	}
}

func TestBuildRunnerGraceFitsInsideShutdownGrace(t *testing.T) {
	cases := []struct{ shutdown, want time.Duration }{
		{30 * time.Second, 10 * time.Second},
		{20 * time.Second, 10 * time.Second},
		{10 * time.Second, 5 * time.Second},
		{1 * time.Second, 500 * time.Millisecond},
		{100 * time.Millisecond, 500 * time.Millisecond},
		{0, 500 * time.Millisecond},
	}
	for _, c := range cases {
		cfg := config.Defaults()
		cfg.ShutdownGrace = c.shutdown
		if got := buildRunner(cfg).Grace; got != c.want {
			t.Errorf("ShutdownGrace %s: Grace = %s, want %s", c.shutdown, got, c.want)
		}
	}
}

func TestBackgroundStopCancelsAndWaitsForRunners(t *testing.T) {
	var finished atomic.Int32
	runner := func(ctx context.Context) {
		<-ctx.Done()
		time.Sleep(20 * time.Millisecond)
		finished.Add(1)
	}
	bg := startBackground(context.Background(), runner, runner)
	if !bg.stop(time.Second) {
		t.Fatal("stop reported a timeout")
	}
	if got := finished.Load(); got != 2 {
		t.Errorf("finished runners = %d, want 2 (stop must wait for them)", got)
	}
}

func TestBackgroundStopIsBoundedByGrace(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	bg := startBackground(context.Background(), func(context.Context) { <-release })
	start := time.Now()
	if bg.stop(50 * time.Millisecond) {
		t.Fatal("stop reported success although a runner is stuck")
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("stop took %s, want about the 50ms grace", d)
	}
}

func TestShutdownOrder(t *testing.T) {
	var order []string
	bg := startBackground(context.Background(), func(ctx context.Context) {
		<-ctx.Done()
		order = append(order, "background")
	})
	shutdownServices(time.Second,
		func() error { order = append(order, "http"); return nil },
		func(context.Context) { order = append(order, "ops") },
		bg)
	want := []string{"http", "ops", "background"}
	if !slices.Equal(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
}

func TestServeLogsVersion(t *testing.T) {
	dir := t.TempDir()
	origConfig := defaultConfigPath
	defaultConfigPath = dir + "/no-such-config.yaml"
	t.Cleanup(func() { defaultConfigPath = origConfig })
	te := newTestEnv(t, map[string]string{
		"STACKSTEAD_LISTEN":        "127.0.0.1:0",
		"STACKSTEAD_DATA_DIR":      dir + "/data",
		"STACKSTEAD_STACKS_DIR":    dir + "/stacks",
		"STACKSTEAD_DOCKER_SOCKET": dir + "/nonexistent.sock",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	serveCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- serve(serveCtx, te.Env, nil) }()

	deadline := time.After(15 * time.Second)
	for !strings.Contains(te.Err.String(), "stackstead listening") {
		select {
		case err := <-done:
			t.Fatalf("serve exited early: %v\n%s", err, te.Err.String())
		case <-deadline:
			t.Fatalf("no listening line:\n%s", te.Err.String())
		case <-time.After(20 * time.Millisecond):
		}
	}
	stop()
	if err := <-done; err != nil {
		t.Errorf("serve returned %v after cancel", err)
	}
	if want := "version=" + version.Get().Version; !strings.Contains(te.Err.String(), want) {
		t.Errorf("startup log lacks %s:\n%s", want, te.Err.String())
	}
}

func serveTestEnv(t *testing.T) (*testEnv, string) {
	t.Helper()
	dir := t.TempDir()
	orig := defaultConfigPath
	defaultConfigPath = dir + "/no-such-config.yaml"
	t.Cleanup(func() { defaultConfigPath = orig })
	te := newTestEnv(t, map[string]string{
		"STACKSTEAD_LISTEN":        "127.0.0.1:0",
		"STACKSTEAD_DATA_DIR":      dir + "/data",
		"STACKSTEAD_STACKS_DIR":    dir + "/stacks",
		"STACKSTEAD_DOCKER_SOCKET": dir + "/nonexistent.sock",
	})
	return te, dir + "/data"
}

func TestServeRefusesWhileAnotherServerHoldsTheLock(t *testing.T) {
	te, data := serveTestEnv(t)
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	held, err := lockfile.Acquire(filepath.Join(data, "stackstead.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = serve(ctx, te.Env, nil)
	if codeFor(err) != ExitConflict {
		t.Fatalf("serve returned %v (code %d), want exit 5", err, codeFor(err))
	}
	if !strings.Contains(err.Error(), "another stackstead server is running (pid ") {
		t.Errorf("message = %q", err)
	}
	if _, serr := os.Stat(filepath.Join(data, "stackstead.db")); serr == nil {
		t.Error("a refused serve opened (and created) the database before taking the lock")
	}
}

func TestServeReleasesTheLockWhenItReturns(t *testing.T) {
	te, data := serveTestEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, te.Env, nil) }()
	lockPath := filepath.Join(data, "stackstead.lock")
	deadline := time.After(15 * time.Second)
	for !strings.Contains(te.Err.String(), "stackstead listening") {
		select {
		case err := <-done:
			t.Fatalf("serve exited early: %v", err)
		case <-deadline:
			t.Fatal("serve did not start")
		case <-time.After(20 * time.Millisecond):
		}
	}
	if l, err := lockfile.Acquire(lockPath); err == nil {
		l.Release()
		t.Fatal("the lock was free while serve was running")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	l, err := lockfile.Acquire(lockPath)
	if err != nil {
		t.Fatalf("the lock was not released: %v", err)
	}
	l.Release()
}

type fakeDockerProbe struct {
	version string
	verErr  error
	pingErr error
}

func (f fakeDockerProbe) Version(context.Context) (string, error) { return f.version, f.verErr }
func (f fakeDockerProbe) Ping(context.Context) error              { return f.pingErr }

type fakeComposeProbe struct {
	version string
	err     error
}

func (f fakeComposeProbe) Version(context.Context) (string, error) { return f.version, f.err }

func TestStatusProbeFallsBackToPing(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name    string
		docker  fakeDockerProbe
		compose fakeComposeProbe
		want    api.StatusReport
	}{
		{"version ok", fakeDockerProbe{version: "27.1.1"}, fakeComposeProbe{version: "v2.30.0"},
			api.StatusReport{Docker: "ok", DockerVersion: "27.1.1", Compose: "v2.30.0"}},
		{"version fails, ping ok", fakeDockerProbe{verErr: boom}, fakeComposeProbe{version: "v2.30.0"},
			api.StatusReport{Docker: "ok", Compose: "v2.30.0"}},
		{"both fail", fakeDockerProbe{verErr: boom, pingErr: boom}, fakeComposeProbe{err: boom},
			api.StatusReport{Docker: "unavailable", Compose: "unavailable"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := probeStatus(context.Background(), tc.docker, tc.compose); got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestBackgroundAddRunnerIsStoppedWithTheRest(t *testing.T) {
	var finished atomic.Int32
	bg := startBackground(context.Background())
	bg.add(func(ctx context.Context) {
		<-ctx.Done()
		finished.Add(1)
	})
	if !bg.stop(time.Second) {
		t.Fatal("stop reported a timeout")
	}
	if finished.Load() != 1 {
		t.Error("added runner was not stopped and awaited")
	}
}

// stageHostRestore leaves what a staged host restore leaves in data: a
// restored database (with an admin, a scheduled backup and stack "web") and
// the pending marker that asks to start "web".
func stageHostRestore(t *testing.T, data, stacksDir string) {
	t.Helper()
	dir := filepath.Join(data, "restore")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(dir, "stackstead.db")
	db, err := store.Open(staged)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	authSvc, err := auth.NewService(db, time.Now, auth.WithFastHashingForTests())
	if err != nil {
		t.Fatal(err)
	}
	if err := authSvc.CreateAdmin(ctx, "olduser", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	compose := filepath.Join(stacksDir, "web", "compose.yaml")
	if err := os.MkdirAll(filepath.Dir(compose), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(compose, []byte("services: {app: {image: nginx}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := stacks.NewRegistry(db, time.Now).Add(ctx, stacks.Entry{Name: "web", ComposePath: compose, Origin: stacks.OriginManaged, Project: "web"}); err != nil {
		t.Fatal(err)
	}
	cs := backup.NewConfigStore(db)
	cfg, err := cs.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Schedule.Enabled = true
	cfg.Local = backup.LocalDest{Enabled: true, Path: "/backups", KeepLast: 7}
	if err := cs.Save(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	marker := fmt.Sprintf(`{"stagedDb":%q,"startStacks":["web"],"pauseSchedule":true}`, staged)
	if err := os.WriteFile(filepath.Join(dir, "pending.json"), []byte(marker), 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitForLog(t *testing.T, te *testEnv, done <-chan error, want string) {
	t.Helper()
	deadline := time.After(15 * time.Second)
	for !strings.Contains(te.Err.String(), want) {
		select {
		case err := <-done:
			t.Fatalf("serve exited before logging %q: %v\n%s", want, err, te.Err.String())
		case <-deadline:
			t.Fatalf("no %q in the log:\n%s", want, te.Err.String())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestServeAppliesPendingBeforeOpen(t *testing.T) {
	te, data := serveTestEnv(t)
	stageHostRestore(t, data, filepath.Join(filepath.Dir(data), "stacks"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serve(ctx, te.Env, nil) }()
	waitForLog(t, te, done, "stackstead listening")
	// Docker is unavailable here, so starting the restored stack fails and is logged.
	waitForLog(t, te, done, "start restored stack")
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	if strings.Contains(te.Err.String(), "no admin account yet") {
		t.Error("serve opened the old database: the restored admin was not there")
	}
	for _, marker := range []string{"pending.json", "post-apply.json"} {
		if _, err := os.Stat(filepath.Join(data, "restore", marker)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s is still there", marker)
		}
	}
	db, err := store.Open(filepath.Join(data, "stackstead.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg, err := backup.NewConfigStore(db).Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Schedule.Enabled {
		t.Error("the backup schedule of the restored database was not paused")
	}
	entries, err := actionlog.NewStore(db, time.Now).Recent(context.Background(), actionlog.Filter{KindPrefix: "backup.restore"})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Scope != "web" || entries[0].Username != "system" || entries[0].Result != "failed" {
		t.Errorf("action log = %+v, want one failed backup.restore entry for web by system", entries)
	}
}

func TestServeRefusesToStartOnAHalfAppliedRestore(t *testing.T) {
	te, data := serveTestEnv(t)
	dir := filepath.Join(data, "restore")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pending.json"), []byte(`{"stagedDb":"/elsewhere/x.db"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := serve(ctx, te.Env, nil)
	if err == nil || !strings.Contains(err.Error(), "pending host restore") {
		t.Fatalf("serve returned %v, want a pending host restore error", err)
	}
	if _, serr := os.Stat(filepath.Join(data, "stackstead.db")); serr == nil {
		t.Error("serve opened a database although the restore could not be applied")
	}
}

func TestServeRestartShutsDownThenReexecs(t *testing.T) {
	te, data := serveTestEnv(t)
	var opts api.Options
	origHandler, origReexec := newAPIHandler, reexec
	t.Cleanup(func() { newAPIHandler, reexec = origHandler, origReexec })
	captured := make(chan struct{})
	newAPIHandler = func(o api.Options) http.Handler {
		opts = o
		close(captured)
		return origHandler(o)
	}
	var lockFreeAtExec atomic.Bool
	var execs atomic.Int32
	reexec = func() error {
		execs.Add(1)
		if l, err := lockfile.Acquire(filepath.Join(data, "stackstead.lock")); err == nil {
			lockFreeAtExec.Store(true)
			l.Release()
		}
		return nil
	}

	done := make(chan error, 1)
	go func() { done <- serve(context.Background(), te.Env, nil) }()
	waitForLog(t, te, done, "stackstead listening")
	<-captured
	if opts.Restart == nil || opts.HostRestore == nil || opts.RestoreOps == nil {
		t.Fatal("serve does not wire the setup restore API")
	}
	opts.Restart()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not shut down after Restart")
	}
	if execs.Load() != 1 {
		t.Fatalf("re-exec ran %d times, want once", execs.Load())
	}
	if !lockFreeAtExec.Load() {
		t.Error("the server lock was still held when the new process image would start")
	}
}

// serveUntil runs serve until every wanted line is logged, then stops it.
func serveUntil(t *testing.T, te *testEnv, wants ...string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serve(ctx, te.Env, nil) }()
	for _, want := range wants {
		waitForLog(t, te, done, want)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func scheduleEnabled(t *testing.T, data string) bool {
	t.Helper()
	db, err := store.Open(filepath.Join(data, "stackstead.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg, err := backup.NewConfigStore(db).Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Schedule.Enabled
}

func TestServeRetriesAFailedSchedulePause(t *testing.T) {
	te, data := serveTestEnv(t)
	stageHostRestore(t, data, filepath.Join(filepath.Dir(data), "stacks"))
	orig := pauseSchedule
	t.Cleanup(func() { pauseSchedule = orig })
	pauseSchedule = func(context.Context, *sql.DB) error { return errors.New("database is locked") }
	origStart := startBackupScheduler
	t.Cleanup(func() { startBackupScheduler = origStart })
	var schedulerStarts atomic.Int32
	startBackupScheduler = func(bg *background, s *backup.Scheduler) {
		schedulerStarts.Add(1)
		origStart(bg, s)
	}

	serveUntil(t, te, "stackstead listening", "start restored stack")
	if n := schedulerStarts.Load(); n != 0 {
		t.Errorf("the backup scheduler started %d times while the restored schedule was not paused", n)
	}
	if !strings.Contains(te.Err.String(), "backup scheduler not started") {
		t.Errorf("no reason logged for holding the scheduler:\n%s", te.Err.String())
	}
	post, err := backup.ReadPostApply(data)
	if err != nil || post == nil || !post.PauseSchedule || len(post.StartStacks) != 0 {
		t.Fatalf("after a failed pause: post-apply = %+v, %v; want only the pause left", post, err)
	}

	pauseSchedule = orig
	te.Err.Reset()
	serveUntil(t, te, "stackstead listening", "paused the restored backup schedule")
	if scheduleEnabled(t, data) {
		t.Error("the pause was not retried on the next start")
	}
	if n := schedulerStarts.Load(); n != 1 {
		t.Errorf("the backup scheduler started %d times once paused, want 1", n)
	}
	if post, _ := backup.ReadPostApply(data); post != nil {
		t.Errorf("post-apply = %+v after everything was done", post)
	}
}

func TestServeFinishesPostApplyWorkAfterACrash(t *testing.T) {
	te, data := serveTestEnv(t)
	stageHostRestore(t, data, filepath.Join(filepath.Dir(data), "stacks"))
	// The previous start installed the database and died before doing anything else.
	if _, err := backup.ApplyPending(data, time.Now()); err != nil {
		t.Fatal(err)
	}

	serveUntil(t, te, "stackstead listening", "start restored stack")
	if scheduleEnabled(t, data) {
		t.Error("the schedule was not paused")
	}
	if post, _ := backup.ReadPostApply(data); post != nil {
		t.Errorf("post-apply = %+v, want it gone", post)
	}
}

func TestServeRefusesToStartOnUnreadableRestoreWork(t *testing.T) {
	te, data := serveTestEnv(t)
	dir := filepath.Join(data, "restore")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "post-apply.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := serve(ctx, te.Env, nil)
	if err == nil || !strings.Contains(err.Error(), "finish the pending host restore") {
		t.Fatalf("serve returned %v, want a pending host restore error", err)
	}
}

type cancellingStarter struct{ cancel context.CancelFunc }

func (s cancellingStarter) ActWait(context.Context, string, string, func(string)) error {
	s.cancel()
	return errors.New("docker is gone")
}

type discardActions struct{}

func (discardActions) Append(context.Context, actionlog.Entry) error { return nil }

// A shutdown that lands after the last stack was attempted must not mark the
// work unfinished, or the next start repeats it.
func TestStartRestoredStacksCountsAShutdownAfterTheLastStackAsDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := slog.New(slog.DiscardHandler)
	if !startRestoredStacks(ctx, cancellingStarter{cancel}, discardActions{}, []string{"web"}, logger) {
		t.Error("every stack was attempted, but the work was reported cut short")
	}
}

// A backup carries the sessions and connection tokens of the moment it was
// taken, so a host restore must sign everyone out before the server serves:
// otherwise a session revoked after the backup would work again.
func TestServeRevokesSessionsAndTokensAfterHostRestore(t *testing.T) {
	te, data := serveTestEnv(t)
	stageHostRestore(t, data, filepath.Join(filepath.Dir(data), "stacks"))
	staged := filepath.Join(data, "restore", "stackstead.db")
	ctx := context.Background()
	sdb, err := store.Open(staged)
	if err != nil {
		t.Fatal(err)
	}
	authSvc, err := auth.NewService(sdb, time.Now, auth.WithFastHashingForTests())
	if err != nil {
		t.Fatal(err)
	}
	oldToken, err := authSvc.Login(ctx, "olduser", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	for _, q := range []string{
		fmt.Sprintf(`INSERT INTO connection_tokens(hash, created_at, expires_at) VALUES('tok', %d, %d)`, now, now+600),
		fmt.Sprintf(`INSERT INTO peers(instance_id, name, slug, url, in_hash, created_at, state) VALUES('pending-1', 'p', 'p', 'http://p', 'h1', %d, 'pending')`, now),
		fmt.Sprintf(`INSERT INTO peers(instance_id, name, slug, url, out_credential, in_hash, created_at, state) VALUES('peer-1', 'q', 'q', 'http://q', 'cred', 'h2', %d, 'active')`, now),
	} {
		if _, err := sdb.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := sdb.Close(); err != nil {
		t.Fatal(err)
	}

	serveUntil(t, te, "stackstead listening", "start restored stack")

	db, err := store.Open(filepath.Join(data, "stackstead.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for table, want := range map[string]int{"sessions": 0, "connection_tokens": 0, "peers": 1} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Errorf("%s has %d rows after the restore, want %d", table, n, want)
		}
	}
	var cred string
	if err := db.QueryRow(`SELECT out_credential FROM peers WHERE state = 'active'`).Scan(&cred); err != nil || cred != "cred" {
		t.Errorf("the active peer did not survive: %q, %v", cred, err)
	}
	svc, err := auth.NewService(db, time.Now, auth.WithFastHashingForTests())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, oldToken); err == nil {
		t.Error("a session from the backup still authenticates")
	}
}
