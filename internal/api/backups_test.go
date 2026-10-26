package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
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

	restoreReq  backup.StackRestoreRequest
	restoreUser string
	restoreErr  error
	planErr     error
	manifestRef [2]string
}

func (f *fakeBackups) PlanRestore(_ context.Context, req backup.StackRestoreRequest) (backup.StackRestorePlan, error) {
	f.restoreReq = req
	if f.planErr != nil {
		return backup.StackRestorePlan{}, f.planErr
	}
	return backup.StackRestorePlan{
		Stack: req.Stack, TargetName: req.Stack, TargetDir: "/srv/stacks/web", ComposePath: "/srv/stacks/web/compose.yaml",
		Project: "web", Origin: stacks.OriginManaged, Overwrites: true,
		FileDiffs: []backup.FileDiff{{Path: "/srv/stacks/web/compose.yaml", Old: "a", New: "b"}},
		Volumes:   []backup.PlannedVolume{{Key: "b-1", Kind: "bind", Name: "/etc", Target: "/x", Skip: true, Reason: "outside allowed roots"}},
		Images:    []string{"nginx:1.27"},
		Warnings:  []string{"volume contents are replaced and not kept"},
	}, nil
}
func (f *fakeBackups) Restore(_ context.Context, req backup.StackRestoreRequest, user string) (stacks.Operation, error) {
	f.restoreReq, f.restoreUser = req, user
	if f.restoreErr != nil {
		return stacks.Operation{}, f.restoreErr
	}
	return stacks.Operation{ID: "op2", Stack: req.Stack, Action: "restore", State: stacks.OpRunning, StartedAt: time.Unix(1, 0).UTC(), ExitCode: -1}, nil
}
func (f *fakeBackups) ArchiveManifest(_ context.Context, dest, name string) (backup.Manifest, error) {
	f.manifestRef = [2]string{dest, name}
	return backup.Manifest{Format: 1, StacksteadVersion: "1.3.0", HostSlug: "old",
		Stacks: []backup.ManifestStack{{Name: "web", Volumes: []backup.ManifestVolume{{Key: "v-web_data", Kind: "volume", Name: "web_data"}}}}}, nil
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

const testArchive = "stackstead-old-20261001T030000Z.tar.gz"

func restoreBody(extra string) string {
	return `{"archive":{"dest":"local","name":"` + testArchive + `"},"stack":"web","files":true,"volumes":true,"images":false,"volumeKeys":["v-web_data"]` + extra + `}`
}

func errorCode(t *testing.T, body []byte) string {
	t.Helper()
	var e apitypes.ErrorBody
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("error body %s: %v", body, err)
	}
	return e.Code
}

func TestRestoreAPIRejectsNewerArchive(t *testing.T) {
	fake := &fakeBackups{cfg: validBackupConfig(), restoreErr: backup.ErrArchiveNewer, planErr: backup.ErrArchiveNewer}
	env, cookie := newBackupEnv(t, fake)
	for _, path := range []string{"/api/backups/restore", "/api/backups/restore/plan"} {
		rec := env.do(t, "POST", path, restoreBody(""), withCookie(cookie))
		if rec.Code != http.StatusBadRequest || errorCode(t, rec.Body.Bytes()) != "archive_newer" {
			t.Errorf("%s: status = %d: %s, want 400 archive_newer", path, rec.Code, rec.Body)
		}
	}
}

func TestRestoreAPIConflicts(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{stacks.ErrBusy, "stack_busy"},
		{fmt.Errorf("wrapped: %w", backup.ErrTargetOccupied), "target_occupied"},
		{fmt.Errorf("wrapped: %w", stacks.ErrProjectTaken), "project_taken"},
	} {
		fake := &fakeBackups{cfg: validBackupConfig(), restoreErr: tc.err}
		env, cookie := newBackupEnv(t, fake)
		rec := env.do(t, "POST", "/api/backups/restore", restoreBody(""), withCookie(cookie))
		if rec.Code != http.StatusConflict || errorCode(t, rec.Body.Bytes()) != tc.code {
			t.Errorf("%v: status = %d: %s, want 409 %s", tc.err, rec.Code, rec.Body, tc.code)
		}
	}
}

func TestRestoreAPIStartsOperation(t *testing.T) {
	fake := &fakeBackups{cfg: validBackupConfig()}
	env, cookie := newBackupEnv(t, fake)
	rec := env.do(t, "POST", "/api/backups/restore", restoreBody(`,"newName":"web2"`), withCookie(cookie))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got apitypes.BackupRestoreStarted
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Operation.ID != "op2" || got.Operation.Action != "restore" {
		t.Errorf("body = %s (%v)", rec.Body, err)
	}
	want := backup.StackRestoreRequest{Archive: backup.ArchiveRef{Dest: "local", Name: testArchive}, Stack: "web", NewName: "web2",
		Files: true, Volumes: true, VolumeKeys: []string{"v-web_data"}}
	if !reflect.DeepEqual(fake.restoreReq, want) || fake.restoreUser != "admin" {
		t.Errorf("request = %+v as %q, want %+v as admin", fake.restoreReq, fake.restoreUser, want)
	}
}

func TestRestoreAPIValidatesArchiveRef(t *testing.T) {
	fake := &fakeBackups{cfg: validBackupConfig()}
	env, cookie := newBackupEnv(t, fake)
	for _, body := range []string{
		`{"archive":{"dest":"ftp","name":"` + testArchive + `"},"stack":"web","files":true}`,
		`{"archive":{"dest":"local","name":"../etc/passwd"},"stack":"web","files":true}`,
	} {
		if rec := env.do(t, "POST", "/api/backups/restore", body, withCookie(cookie)); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d", body, rec.Code)
		}
	}
	for _, path := range []string{"/api/backups/archives/ftp/" + testArchive + "/manifest", "/api/backups/archives/local/notes.txt/manifest"} {
		if rec := env.do(t, "GET", path, "", withCookie(cookie)); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d", path, rec.Code)
		}
	}
	if fake.manifestRef != [2]string{} || fake.restoreReq.Stack != "" {
		t.Error("an invalid archive reference reached the service")
	}
}

func TestRestorePlanAndManifestAPI(t *testing.T) {
	fake := &fakeBackups{cfg: validBackupConfig()}
	env, cookie := newBackupEnv(t, fake)
	rec := env.do(t, "POST", "/api/backups/restore/plan", restoreBody(""), withCookie(cookie))
	if rec.Code != http.StatusOK {
		t.Fatalf("plan status = %d: %s", rec.Code, rec.Body)
	}
	var plan apitypes.BackupRestorePlan
	if err := json.Unmarshal(rec.Body.Bytes(), &plan); err != nil || !plan.Overwrites || plan.FileDiffs[0].New != "b" ||
		!plan.Volumes[0].Skip || plan.MissingNetworks == nil || plan.Origin != "managed" {
		t.Errorf("plan = %s (%v)", rec.Body, err)
	}

	rec = env.do(t, "GET", "/api/backups/archives/s3/"+testArchive+"/manifest", "", withCookie(cookie))
	var m apitypes.BackupManifest
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil || rec.Code != http.StatusOK || m.Stacks[0].Volumes[0].Key != "v-web_data" || m.Items == nil {
		t.Errorf("manifest = %d %s (%v)", rec.Code, rec.Body, err)
	}
	if fake.manifestRef != [2]string{"s3", testArchive} {
		t.Errorf("manifest ref = %v", fake.manifestRef)
	}
}

func TestRestoreAPIBadRequestCodes(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{backup.ErrFilesRequired, "files_required"},
		{backup.ErrNothingToRestore, "nothing_to_restore"},
		{fmt.Errorf("%w: destination \"x\"", backup.ErrBadArchiveRef), "bad_archive_ref"},
	} {
		fake := &fakeBackups{cfg: validBackupConfig(), restoreErr: tc.err, planErr: tc.err}
		env, cookie := newBackupEnv(t, fake)
		for _, path := range []string{"/api/backups/restore", "/api/backups/restore/plan"} {
			rec := env.do(t, "POST", path, restoreBody(""), withCookie(cookie))
			if rec.Code != http.StatusBadRequest || errorCode(t, rec.Body.Bytes()) != tc.code {
				t.Errorf("%s %v: status = %d: %s, want 400 %s", path, tc.err, rec.Code, rec.Body, tc.code)
			}
		}
	}
	// A reference the handler rejects itself carries the same code.
	env, cookie := newBackupEnv(t, &fakeBackups{cfg: validBackupConfig()})
	rec := env.do(t, "POST", "/api/backups/restore", `{"archive":{"dest":"ftp","name":"x"},"stack":"web","files":true}`, withCookie(cookie))
	if rec.Code != http.StatusBadRequest || errorCode(t, rec.Body.Bytes()) != "bad_archive_ref" {
		t.Errorf("handler-rejected ref: %d %s", rec.Code, rec.Body)
	}
	rec = env.do(t, "GET", "/api/backups/archives/local/notes.txt/manifest", "", withCookie(cookie))
	if rec.Code != http.StatusBadRequest || errorCode(t, rec.Body.Bytes()) != "bad_archive_ref" {
		t.Errorf("manifest ref: %d %s", rec.Code, rec.Body)
	}
}
