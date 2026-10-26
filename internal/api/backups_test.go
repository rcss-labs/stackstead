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
	"github.com/rcss-labs/stackstead/internal/backup"
	"github.com/rcss-labs/stackstead/internal/secret"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

type fakeBackups struct {
	cfg      backup.Config
	saved    *backup.Config
	startErr error
	user     string
	trigger  string
	selected backup.StackSelection
	unknown  bool
}

func (f *fakeBackups) Config(context.Context) (backup.Config, error) { return f.cfg, nil }
func (f *fakeBackups) SaveConfig(_ context.Context, cfg backup.Config) error {
	f.saved = &cfg
	f.cfg = cfg
	return nil
}
func (f *fakeBackups) ClearS3Secret(context.Context) error         { f.cfg.S3.SecretKey = ""; return nil }
func (f *fakeBackups) TestS3(context.Context, backup.S3Dest) error { return nil }
func (f *fakeBackups) Selections(context.Context) ([]backup.StackSelectionView, error) {
	return []backup.StackSelectionView{{
		StackSelection: backup.StackSelection{Stack: "web", Volumes: true},
		Mounts:         []backup.MountView{{Kind: "volume", Name: "data", Target: "/data", Bytes: 42}, {Kind: "bind", Name: "/srv", Target: "/srv", Bytes: -1}},
	}}, nil
}
func (f *fakeBackups) SetSelection(_ context.Context, sel backup.StackSelection) error {
	if f.unknown {
		return backup.ErrUnknownStack
	}
	f.selected = sel
	return nil
}
func (f *fakeBackups) Start(_ context.Context, trigger, user string) (stacks.Operation, error) {
	f.trigger, f.user = trigger, user
	if f.startErr != nil {
		return stacks.Operation{}, f.startErr
	}
	return stacks.Operation{ID: "op1", Stack: "host:backup", Action: "backup", State: stacks.OpRunning, StartedAt: time.Unix(1, 0).UTC(), ExitCode: -1}, nil
}
func (f *fakeBackups) Runs(context.Context, int) ([]backup.Run, error) {
	return []backup.Run{{ID: 3, Trigger: "manual", Status: "success", StartedAt: time.Unix(1, 0).UTC()}}, nil
}
func (f *fakeBackups) Archives(context.Context) ([]backup.ArchiveRef, error) {
	return []backup.ArchiveRef{{Dest: "local", Name: "a.tar.gz", HostSlug: "h", CreatedAt: time.Unix(1, 0).UTC(), Size: 5}}, nil
}
func (f *fakeBackups) NextRun(context.Context) (time.Time, bool) { return time.Time{}, false }

func newBackupEnv(t *testing.T, fake *fakeBackups) (*testEnv, *http.Cookie) {
	t.Helper()
	env := newEnvWith(t, "", func(o *api.Options) { o.Backups = fake })
	env.createAdmin(t)
	return env, env.login(t)
}

func validBackupConfig() backup.Config {
	return backup.Config{
		Schedule: backup.Schedule{Frequency: "daily", Time: "03:00"},
		Local:    backup.LocalDest{KeepLast: 7},
	}
}

func TestBackupConfigNeverReturnsSecret(t *testing.T) {
	cfg := validBackupConfig()
	cfg.S3 = backup.S3Dest{Enabled: true, Bucket: "b", SecretKey: secret.Secret("hunter2-secret-value")}
	env, cookie := newBackupEnv(t, &fakeBackups{cfg: cfg})

	rec := env.do(t, "GET", "/api/backups/config", "", withCookie(cookie))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "hunter2") || strings.Contains(rec.Body.String(), "redacted") {
		t.Errorf("body leaks the secret: %s", rec.Body)
	}
	var got map[string]map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["s3"]["secretKeySet"] != true {
		t.Errorf("secretKeySet = %v, want true", got["s3"]["secretKeySet"])
	}
	if _, present := got["s3"]["secretKey"]; present {
		t.Error("response has a secretKey field")
	}
}

func TestPutConfigValidation400(t *testing.T) {
	fake := &fakeBackups{cfg: validBackupConfig()}
	env, cookie := newBackupEnv(t, fake)

	bad := `{"schedule":{"enabled":true,"frequency":"daily","weekday":0,"time":"25:99"},"local":{"enabled":false,"path":"","keepLast":7},"s3":{}}`
	rec := env.do(t, "PUT", "/api/backups/config", bad, withCookie(cookie))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "HH:MM") {
		t.Errorf("body lacks the validation message: %s", rec.Body)
	}
	if fake.saved != nil {
		t.Error("an invalid configuration was saved")
	}

	good := `{"schedule":{"enabled":false,"frequency":"daily","weekday":0,"time":"04:15"},"local":{"enabled":false,"path":"","keepLast":3},"s3":{"secretKey":"new-secret"}}`
	rec = env.do(t, "PUT", "/api/backups/config", good, withCookie(cookie))
	if rec.Code != http.StatusOK {
		t.Fatalf("valid PUT status = %d: %s", rec.Code, rec.Body)
	}
	if fake.saved == nil || fake.saved.Schedule.Time != "04:15" || fake.saved.S3.SecretKey.Reveal() != "new-secret" {
		t.Errorf("saved = %+v", fake.saved)
	}
	if strings.Contains(rec.Body.String(), "new-secret") {
		t.Errorf("PUT response echoes the secret: %s", rec.Body)
	}
}

func TestRunReturns409WhenBusy(t *testing.T) {
	fake := &fakeBackups{cfg: validBackupConfig(), startErr: stacks.ErrBusy}
	env, cookie := newBackupEnv(t, fake)

	rec := env.do(t, "POST", "/api/backups/run", `{}`, withCookie(cookie))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var body apitypes.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Code != "backup_running" {
		t.Errorf("body = %s (%v)", rec.Body, err)
	}
}

func TestBackupRunStartsOperationAsSessionUser(t *testing.T) {
	fake := &fakeBackups{cfg: validBackupConfig()}
	env, cookie := newBackupEnv(t, fake)

	rec := env.do(t, "POST", "/api/backups/run", `{}`, withCookie(cookie))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var body apitypes.BackupRunStarted
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Operation.ID != "op1" {
		t.Errorf("body = %s (%v)", rec.Body, err)
	}
	if fake.trigger != "manual" || fake.user != "admin" {
		t.Errorf("started with trigger %q user %q", fake.trigger, fake.user)
	}
}

func TestBackupRoutesRequireSession(t *testing.T) {
	env, _ := newBackupEnv(t, &fakeBackups{cfg: validBackupConfig()})
	for _, route := range [][2]string{
		{"GET", "/api/backups/config"}, {"GET", "/api/backups/stacks"}, {"GET", "/api/backups/runs"}, {"GET", "/api/backups/archives"},
	} {
		if rec := env.do(t, route[0], route[1], ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a session = %d", route[0], route[1], rec.Code)
		}
	}
}

func TestBackupStacksRunsArchivesAndSelection(t *testing.T) {
	fake := &fakeBackups{cfg: validBackupConfig()}
	env, cookie := newBackupEnv(t, fake)

	var rows []apitypes.BackupStack
	rec := env.do(t, "GET", "/api/backups/stacks", "", withCookie(cookie))
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil || len(rows) != 1 || rows[0].Mounts[1].Bytes != -1 {
		t.Errorf("stacks = %s (%v)", rec.Body, err)
	}
	var runs []apitypes.BackupRun
	rec = env.do(t, "GET", "/api/backups/runs?limit=5", "", withCookie(cookie))
	if err := json.Unmarshal(rec.Body.Bytes(), &runs); err != nil || len(runs) != 1 || runs[0].EndedAt != nil {
		t.Errorf("runs = %s (%v)", rec.Body, err)
	}
	if rec = env.do(t, "GET", "/api/backups/runs?limit=0", "", withCookie(cookie)); rec.Code != http.StatusBadRequest {
		t.Errorf("limit=0 status = %d", rec.Code)
	}
	var archives []apitypes.BackupArchive
	rec = env.do(t, "GET", "/api/backups/archives", "", withCookie(cookie))
	if err := json.Unmarshal(rec.Body.Bytes(), &archives); err != nil || len(archives) != 1 || archives[0].Dest != "local" {
		t.Errorf("archives = %s (%v)", rec.Body, err)
	}

	rec = env.do(t, "PUT", "/api/backups/stacks/web", `{"volumes":true,"images":false,"stopDuring":true}`, withCookie(cookie))
	if rec.Code != http.StatusNoContent || fake.selected != (backup.StackSelection{Stack: "web", Volumes: true, StopDuring: true}) {
		t.Errorf("PUT stack = %d, selected %+v", rec.Code, fake.selected)
	}
	fake.unknown = true
	if rec = env.do(t, "PUT", "/api/backups/stacks/nope", `{}`, withCookie(cookie)); rec.Code != http.StatusNotFound {
		t.Errorf("unknown stack status = %d", rec.Code)
	}
}
