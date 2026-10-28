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
	gotOpts   backup.HostRestoreOptions
	inspected []backup.HostSource
}

func (f *fakeHostRestore) UploadPath() string { return f.upload }

func (f *fakeHostRestore) Inspect(_ context.Context, src backup.HostSource) (backup.HostSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inspected = append(f.inspected, src)
	return f.summary, f.earlyErr
}

func (f *fakeHostRestore) Restore(_ context.Context, src backup.HostSource, opts backup.HostRestoreOptions, out func(string), accepted func()) (backup.HostReport, error) {
	f.mu.Lock()
	f.gotSource, f.gotStart, f.gotDigest, f.gotOpts = src, opts.StartStacks, opts.ConfirmedDigest, opts
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
	// The upload is written under a temporary name and renamed when complete.
	body := &diskCheckingReader{t: t, path: env.fake.upload + ".partial", size: size, readSize: 64 << 10}
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
	if rec := env.do(t, http.MethodGet, "/api/setup/restore/progress", "", fromLoopback, withHeader("X-Restore-Token", "anything")); rec.Code != http.StatusForbidden {
		t.Errorf("progress with no restore running = %d, want 403", rec.Code)
	}
	started := env.start(t)
	env.ops.Wait(started.Operation.ID)
	for name, opts := range map[string][]func(*http.Request){
		"no token":                 {fromLoopback},
		"wrong header":             {fromLoopback, withHeader("X-Restore-Token", "wrong")},
		"setup token in header":    {fromLoopback, withHeader("X-Restore-Token", "tok-123")},
		"restore token in the URL": {fromLoopback, func(r *http.Request) { r.URL.RawQuery = "token=" + started.RestoreToken }},
	} {
		if rec := env.do(t, http.MethodGet, "/api/setup/restore/progress", "", opts...); rec.Code != http.StatusForbidden {
			t.Errorf("%s = %d, want 403", name, rec.Code)
		}
	}
	// The restore token works even once an admin exists (the setup gate has closed).
	env.createAdmin(t)
	rec := env.do(t, http.MethodGet, "/api/setup/restore/progress", "", withHeader("X-Restore-Token", started.RestoreToken))
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

// startSlowUpload begins an upload whose body waits for release; it returns
// once the handler has started writing and a channel with the final response.
func (e *restoreEnv) startSlowUpload(t *testing.T) (release func(), result <-chan *httptest.ResponseRecorder) {
	t.Helper()
	pr, pw := io.Pipe()
	req := httptest.NewRequest(http.MethodPost, "/api/setup/restore/upload", pr)
	req.Header.Set("Content-Type", "application/gzip")
	fromLoopback(req)
	out := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		e.handler.ServeHTTP(rec, req)
		out <- rec
	}()
	if _, err := pw.Write([]byte("first part ")); err != nil { // returns once the handler reads it
		t.Fatal(err)
	}
	return func() {
		_, _ = pw.Write([]byte("of the first upload"))
		pw.Close()
	}, out
}

func TestSetupRestoreOverlappingUploads(t *testing.T) {
	env := newRestoreEnv(t, "")
	release, first := env.startSlowUpload(t)

	rec := env.do(t, http.MethodPost, "/api/setup/restore/upload", "second", fromLoopback, withHeader("Content-Type", "application/gzip"))
	if rec.Code != http.StatusConflict {
		t.Errorf("an upload during another = %d %s, want 409", rec.Code, rec.Body)
	}
	if _, err := os.Stat(env.fake.upload); !errors.Is(err, os.ErrNotExist) {
		t.Error("a half-written upload is visible under the final name")
	}

	release()
	if rec := <-first; rec.Code != http.StatusOK {
		t.Fatalf("first upload = %d %s", rec.Code, rec.Body)
	}
	if got, _ := os.ReadFile(env.fake.upload); string(got) != "first part of the first upload" {
		t.Errorf("upload = %q, want the first body intact", got)
	}
}

func TestSetupRestoreStartRefusesAnUploadInProgress(t *testing.T) {
	env := newRestoreEnv(t, "")
	release, first := env.startSlowUpload(t)
	rec := env.do(t, http.MethodPost, "/api/setup/restore/start", `{"source":{"kind":"upload"},"confirmedDigest":"digest-1"}`, fromLoopback)
	if rec.Code != http.StatusConflict {
		t.Errorf("start from an upload in progress = %d %s, want 409", rec.Code, rec.Body)
	}
	release()
	<-first
	if env.fake.gotDigest != "" {
		t.Error("the restore ran against a half-written upload")
	}
}

func TestSetupRemovesALeftoverUpload(t *testing.T) {
	env := newRestoreEnv(t, "")
	if rec := env.do(t, http.MethodPost, "/api/setup/restore/upload", "archive", fromLoopback, withHeader("Content-Type", "application/gzip")); rec.Code != http.StatusOK {
		t.Fatalf("upload = %d", rec.Code)
	}
	if rec := env.do(t, http.MethodPost, "/api/setup", setupBody(""), fromLoopback); rec.Code != http.StatusNoContent {
		t.Fatalf("setup = %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(env.fake.upload); !errors.Is(err, os.ErrNotExist) {
		t.Error("the uploaded archive outlived a normal setup")
	}
}

func TestSetupRemovesALeftoverPartialUpload(t *testing.T) {
	env := newRestoreEnv(t, "")
	partial := env.fake.upload + ".partial"
	if err := os.MkdirAll(filepath.Dir(partial), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(partial, []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	if rec := env.do(t, http.MethodPost, "/api/setup", setupBody(""), fromLoopback); rec.Code != http.StatusNoContent {
		t.Fatalf("setup = %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(partial); !errors.Is(err, os.ErrNotExist) {
		t.Error("a crashed upload's partial file outlived a normal setup")
	}
}

// An upload still running when setup completes is thrown away at its end.
func TestSetupDuringAnUploadDiscardsIt(t *testing.T) {
	env := newRestoreEnv(t, "")
	release, first := env.startSlowUpload(t)
	if rec := env.do(t, http.MethodPost, "/api/setup", setupBody(""), fromLoopback); rec.Code != http.StatusNoContent {
		t.Fatalf("setup = %d %s", rec.Code, rec.Body)
	}
	release()
	if rec := <-first; rec.Code != http.StatusConflict {
		t.Errorf("upload finishing after setup = %d %s, want 409", rec.Code, rec.Body)
	}
	for _, p := range []string{env.fake.upload, env.fake.upload + ".partial"} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s was kept after setup completed", filepath.Base(p))
		}
	}
}

func TestSetupRestoreInspectPassesPassphrase(t *testing.T) {
	env := newRestoreEnv(t, "")
	env.fake.summary = backup.HostSummary{Manifest: backup.Manifest{Host: "old"}, ConflictsDigest: "digest-1", Encrypted: true, KeyID: "0123456789abcdef"}
	body := `{"source":{"kind":"path","path":"/srv/backups/a.tar.gz.enc"},"passphrase":"open sesame passphrase"}`
	rec := env.do(t, http.MethodPost, "/api/setup/restore/inspect", body, fromLoopback)
	if rec.Code != http.StatusOK {
		t.Fatalf("inspect = %d %s", rec.Code, rec.Body)
	}
	if got := env.fake.inspected; len(got) != 1 || got[0].Passphrase.Reveal() != "open sesame passphrase" {
		t.Errorf("inspected = %+v, want the passphrase passed to the engine", got)
	}
	var sum apitypes.HostRestoreSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &sum); err != nil || !sum.Encrypted || sum.KeyID != "0123456789abcdef" {
		t.Errorf("summary = %s (%v)", rec.Body, err)
	}
	if strings.Contains(rec.Body.String(), "open sesame") {
		t.Errorf("the response echoes the passphrase: %s", rec.Body)
	}
}

func TestSetupRestoreStartPassesAcknowledgement(t *testing.T) {
	env := newRestoreEnv(t, "")
	body := `{"source":{"kind":"path","path":"/srv/backups/a.tar.gz"},"startStacks":false,"confirmedDigest":"digest-1","allowUnencrypted":true,"passphrase":"p-for-start"}`
	rec := env.do(t, http.MethodPost, "/api/setup/restore/start", body, fromLoopback)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start = %d %s", rec.Code, rec.Body)
	}
	env.fake.mu.Lock()
	defer env.fake.mu.Unlock()
	if !env.fake.gotOpts.AllowUnencrypted || env.fake.gotOpts.StartStacks || env.fake.gotOpts.ConfirmedDigest != "digest-1" {
		t.Errorf("options = %+v", env.fake.gotOpts)
	}
	if env.fake.gotSource.Passphrase.Reveal() != "p-for-start" {
		t.Errorf("source passphrase = %q", env.fake.gotSource.Passphrase.Reveal())
	}
}

func TestSetupRestoreErrorCodes(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{backup.ErrArchiveTampered, "archive_tampered"},
		{fmt.Errorf("damaged: %w", backup.ErrArchiveTampered), "archive_tampered"},
		{backup.ErrWrongPassphrase, "wrong_passphrase"},
		{backup.ErrPassphraseRequired, "passphrase_required"},
		{backup.ErrUnencryptedArchive, "unencrypted_archive"},
		{fmt.Errorf("%w: %w", backup.ErrArchiveDamaged, backup.ErrArchiveTampered), "archive_tampered"},
	} {
		env := newRestoreEnv(t, "")
		env.fake.earlyErr = tc.err
		for path, body := range map[string]string{
			"/api/setup/restore/inspect": `{"source":{"kind":"path","path":"/a.tar.gz"}}`,
			"/api/setup/restore/start":   `{"source":{"kind":"path","path":"/a.tar.gz"},"confirmedDigest":"digest-1"}`,
		} {
			rec := env.do(t, http.MethodPost, path, body, fromLoopback)
			var e apitypes.ErrorBody
			_ = json.Unmarshal(rec.Body.Bytes(), &e)
			if rec.Code != http.StatusUnprocessableEntity || e.Code != tc.code {
				t.Errorf("%s with %v = %d %s, want 422 %s", path, tc.err, rec.Code, rec.Body, tc.code)
			}
		}
	}
}
