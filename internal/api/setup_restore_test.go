package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/api"
	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/backup"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

// fakeHostRestore stands in for the restore engine. earlyErr fails the
// restore before it is accepted; lateErr fails it afterwards.
type fakeHostRestore struct {
	upload   string
	summary  backup.HostSummary
	earlyErr error
	lateErr  error

	mu        sync.Mutex
	gotSource backup.HostSource
	gotStart  bool
	gotDigest string
	inspected []backup.HostSource
}

func (f *fakeHostRestore) UploadPath() string { return f.upload }

func (f *fakeHostRestore) Inspect(_ context.Context, src backup.HostSource) (backup.HostSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inspected = append(f.inspected, src)
	return f.summary, f.earlyErr
}

func (f *fakeHostRestore) Restore(_ context.Context, src backup.HostSource, startStacks bool, digest string, out func(string), accepted func()) (backup.HostReport, error) {
	f.mu.Lock()
	f.gotSource, f.gotStart, f.gotDigest = src, startStacks, digest
	f.mu.Unlock()
	if f.earlyErr != nil {
		return backup.HostReport{}, f.earlyErr
	}
	accepted()
	out("restoring stack web")
	out("restore staged")
	return backup.HostReport{}, f.lateErr
}

type restoreEnv struct {
	*testEnv
	fake     *fakeHostRestore
	ops      *stacks.Ops
	restarts chan struct{}
}

func newRestoreEnv(t *testing.T, setupToken string) *restoreEnv {
	t.Helper()
	fake := &fakeHostRestore{
		upload:  filepath.Join(t.TempDir(), "data", "restore", "upload.tar.gz"),
		summary: backup.HostSummary{Manifest: backup.Manifest{Host: "old"}, ConflictsDigest: "digest-1"},
	}
	env := &restoreEnv{fake: fake, ops: stacks.NewOps(0, time.Now), restarts: make(chan struct{}, 4)}
	env.testEnv = newEnvWith(t, setupToken, func(o *api.Options) {
		o.HostRestore = fake
		o.RestoreOps = env.ops
		o.Restart = func() { env.restarts <- struct{}{} }
	})
	return env
}

func startBody(token string) string {
	return fmt.Sprintf(`{"source":{"kind":"path","path":"/srv/backups/a.tar.gz"},"startStacks":true,"confirmedDigest":"digest-1","setupToken":%q}`, token)
}

func (e *restoreEnv) start(t *testing.T) apitypes.HostRestoreStarted {
	t.Helper()
	rec := e.do(t, http.MethodPost, "/api/setup/restore/start", startBody(""), fromLoopback)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start = %d %s", rec.Code, rec.Body)
	}
	var started apitypes.HostRestoreStarted
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if started.RestoreToken == "" || started.Operation.ID == "" {
		t.Fatalf("started = %+v", started)
	}
	return started
}

func (e *restoreEnv) restarted(within time.Duration) bool {
	select {
	case <-e.restarts:
		return true
	case <-time.After(within):
		return false
	}
}

func TestSetupRestoreForbiddenWithoutToken(t *testing.T) {
	env := newRestoreEnv(t, "tok-123")
	for _, c := range []struct{ path, body, ctype string }{
		{"/api/setup/restore/upload", "archive bytes", "application/gzip"},
		{"/api/setup/restore/inspect", `{"source":{"kind":"path","path":"/a"},"setupToken":"wrong"}`, "application/json"},
		{"/api/setup/restore/start", startBody("wrong"), "application/json"},
	} {
		rec := env.do(t, http.MethodPost, c.path, c.body, withHeader("Content-Type", c.ctype), withHeader("X-Stackstead-Setup-Token", "wrong"))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s from a remote client without the token = %d %s, want 403", c.path, rec.Code, rec.Body)
		}
	}
	if _, err := os.Stat(env.fake.upload); !errors.Is(err, os.ErrNotExist) {
		t.Error("a refused upload was written to disk")
	}
	if len(env.fake.inspected) != 0 || env.fake.gotDigest != "" {
		t.Error("a refused request reached the restore engine")
	}
	// The setup token from the service log opens the gate from anywhere.
	rec := env.do(t, http.MethodPost, "/api/setup/restore/inspect", `{"source":{"kind":"path","path":"/a"},"setupToken":"tok-123"}`)
	if rec.Code != http.StatusOK {
		t.Errorf("inspect with the setup token = %d %s", rec.Code, rec.Body)
	}
}

func TestSetupRestore409WhenAdminExists(t *testing.T) {
	env := newRestoreEnv(t, "")
	env.createAdmin(t)
	for _, c := range []struct{ path, body, ctype string }{
		{"/api/setup/restore/upload", "archive bytes", "application/gzip"},
		{"/api/setup/restore/inspect", `{"source":{"kind":"path","path":"/a"}}`, "application/json"},
		{"/api/setup/restore/start", startBody(""), "application/json"},
	} {
		rec := env.do(t, http.MethodPost, c.path, c.body, fromLoopback, withHeader("Content-Type", c.ctype))
		if rec.Code != http.StatusConflict {
			t.Errorf("%s with an admin = %d %s, want 409", c.path, rec.Code, rec.Body)
		}
	}
	if _, err := os.Stat(env.fake.upload); !errors.Is(err, os.ErrNotExist) {
		t.Error("an upload was written although an admin exists")
	}
}

// diskCheckingReader produces size bytes and, on every read, compares how much
// it has handed out with what is already on disk: a handler that buffers the
// body would let the gap grow to the whole body.
type diskCheckingReader struct {
	t        *testing.T
	path     string
	size     int64
	read     int64
	maxGap   int64
	readSize int
}

func (r *diskCheckingReader) Read(p []byte) (int, error) {
	if r.read >= r.size {
		return 0, io.EOF
	}
	var onDisk int64
	if info, err := os.Stat(r.path); err == nil {
		onDisk = info.Size()
	}
	r.maxGap = max(r.maxGap, r.read-onDisk)
	n := min(int64(len(p)), r.size-r.read, int64(r.readSize))
	for i := range n {
		p[i] = byte(r.read + i)
	}
	r.read += n
	return int(n), nil
}

func TestSetupRestoreUploadStreamsToDisk(t *testing.T) {
	env := newRestoreEnv(t, "")
	const size = 64 << 20
	body := &diskCheckingReader{t: t, path: env.fake.upload, size: size, readSize: 64 << 10}
	req := httptest.NewRequest(http.MethodPost, "/api/setup/restore/upload", body)
	req.Header.Set("Content-Type", "application/gzip")
	fromLoopback(req)
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload = %d %s", rec.Code, rec.Body)
	}
	var uploaded apitypes.HostRestoreUploaded
	if err := json.Unmarshal(rec.Body.Bytes(), &uploaded); err != nil || uploaded.Source.Kind != "upload" {
		t.Fatalf("body = %s (%v), want source kind upload", rec.Body, err)
	}
	if body.read != size {
		t.Fatalf("read %d bytes of the body, want %d", body.read, size)
	}
	if body.maxGap > 1<<20 {
		t.Errorf("up to %d bytes were held in memory before reaching disk, want a streamed copy", body.maxGap)
	}
	info, err := os.Stat(env.fake.upload)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != size || info.Mode().Perm() != 0o600 {
		t.Errorf("upload is %d bytes, mode %v; want %d and 0600", info.Size(), info.Mode().Perm(), size)
	}

	// A second upload replaces the first.
	rec = env.do(t, http.MethodPost, "/api/setup/restore/upload", "small", fromLoopback, withHeader("Content-Type", "application/gzip"))
	if rec.Code != http.StatusOK {
		t.Fatalf("second upload = %d %s", rec.Code, rec.Body)
	}
	if got, _ := os.ReadFile(env.fake.upload); string(got) != "small" {
		t.Errorf("second upload left %d bytes, want the new body", len(got))
	}
}

func TestSetupRestoreProgressRequiresRestoreToken(t *testing.T) {
	env := newRestoreEnv(t, "tok-123")
	if rec := env.do(t, http.MethodGet, "/api/setup/restore/progress?token=anything", "", fromLoopback); rec.Code != http.StatusForbidden {
		t.Errorf("progress with no restore running = %d, want 403", rec.Code)
	}
	started := env.start(t)
	env.ops.Wait(started.Operation.ID)
	for _, target := range []string{"/api/setup/restore/progress", "/api/setup/restore/progress?token=wrong", "/api/setup/restore/progress?token=tok-123"} {
		if rec := env.do(t, http.MethodGet, target, "", fromLoopback); rec.Code != http.StatusForbidden {
			t.Errorf("%s = %d, want 403", target, rec.Code)
		}
	}
	// The restore token works even once an admin exists (the setup gate has closed).
	env.createAdmin(t)
	rec := env.do(t, http.MethodGet, "/api/setup/restore/progress?token="+started.RestoreToken, "")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("progress with the restore token = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	stream := rec.Body.String()
	for _, want := range []string{`data: "restoring stack web"`, `data: "restore staged"`, "event: done"} {
		if !strings.Contains(stream, want) {
			t.Errorf("stream lacks %s:\n%s", want, stream)
		}
	}
}

func TestSetupRestoreStartCallsRestartAfterSuccess(t *testing.T) {
	env := newRestoreEnv(t, "")
	started := env.start(t)
	if !env.restarted(5 * time.Second) {
		t.Fatal("Restart was not called after a successful restore")
	}
	op, err := env.ops.Get(started.Operation.ID)
	if err != nil || op.State != stacks.OpSucceeded || op.Stack != "host:restore" {
		t.Errorf("operation = %+v, %v; want a succeeded host:restore operation", op, err)
	}
	if env.fake.gotSource != (backup.HostSource{Kind: "path", Path: "/srv/backups/a.tar.gz"}) || !env.fake.gotStart || env.fake.gotDigest != "digest-1" {
		t.Errorf("restore got source %+v, startStacks %v, digest %q", env.fake.gotSource, env.fake.gotStart, env.fake.gotDigest)
	}
}

func TestSetupRestoreStartUsesTheUpload(t *testing.T) {
	env := newRestoreEnv(t, "")
	if rec := env.do(t, http.MethodPost, "/api/setup/restore/upload", "archive", fromLoopback, withHeader("Content-Type", "application/gzip")); rec.Code != http.StatusOK {
		t.Fatalf("upload = %d %s", rec.Code, rec.Body)
	}
	body := `{"source":{"kind":"upload","path":"/etc/shadow"},"startStacks":false,"confirmedDigest":"digest-1"}`
	rec := env.do(t, http.MethodPost, "/api/setup/restore/start", body, fromLoopback)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start = %d %s", rec.Code, rec.Body)
	}
	if !env.restarted(5 * time.Second) {
		t.Fatal("no restart")
	}
	if want := (backup.HostSource{Kind: "upload", Path: env.fake.upload}); env.fake.gotSource != want {
		t.Errorf("source = %+v, want %+v", env.fake.gotSource, want)
	}
	if _, err := os.Stat(env.fake.upload); !errors.Is(err, os.ErrNotExist) {
		t.Error("the uploaded archive was kept after a successful restore")
	}
}

func TestSetupRestoreStartDoesNotRestartOnFailure(t *testing.T) {
	env := newRestoreEnv(t, "")
	env.fake.lateErr = errors.New("disk full")
	started := env.start(t)
	env.ops.Wait(started.Operation.ID)
	if env.restarted(200 * time.Millisecond) {
		t.Fatal("Restart was called after a failed restore")
	}
	if op, _ := env.ops.Get(started.Operation.ID); op.State != stacks.OpFailed || !strings.Contains(op.Error, "disk full") {
		t.Errorf("operation = %+v, want failed with the cause", op)
	}
	// A failed restore may be retried.
	env.fake.lateErr = nil
	env.start(t)
	if !env.restarted(5 * time.Second) {
		t.Fatal("the retry did not restart")
	}
}

func TestSetupRestoreStartRefusesChangedConflicts(t *testing.T) {
	env := newRestoreEnv(t, "")
	env.fake.earlyErr = fmt.Errorf("wrapped: %w", backup.ErrConflictsChanged)
	rec := env.do(t, http.MethodPost, "/api/setup/restore/start", startBody(""), fromLoopback)
	if rec.Code != http.StatusConflict || decodeErrorBody(t, rec).Code != "conflicts_changed" {
		t.Fatalf("start = %d %s, want 409 conflicts_changed", rec.Code, rec.Body)
	}
	if env.restarted(100 * time.Millisecond) {
		t.Error("Restart was called")
	}
}

func TestSetupRestoreOnlyOneAtATime(t *testing.T) {
	env := newRestoreEnv(t, "")
	env.drainRestarts(t)
	env.start(t)
	rec := env.do(t, http.MethodPost, "/api/setup/restore/start", startBody(""), fromLoopback)
	if rec.Code != http.StatusConflict || decodeErrorBody(t, rec).Code != "restore_running" {
		t.Fatalf("second start = %d %s, want 409 restore_running", rec.Code, rec.Body)
	}
}

// drainRestarts swallows restart signals so a test can focus on later starts.
func (e *restoreEnv) drainRestarts(t *testing.T) {
	t.Helper()
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		for {
			select {
			case <-e.restarts:
			case <-stop:
				return
			}
		}
	}()
}
