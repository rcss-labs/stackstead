package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/backup"
	"github.com/rcss-labs/stackstead/internal/lockfile"
)

func runRestore(env *testEnv, args ...string) error {
	root := &Command{Name: "stackstead", Sub: []*Command{restoreCommand()}}
	return Run(context.Background(), root, append([]string{"restore"}, args...), env.Env)
}

type restoreCalls struct {
	inspected backup.HostSource
	restored  backup.HostSource
	started   bool
	digest    string
	applied   int
}

// fakeRestoreSeams replaces the engine seams and records what the command asked of them.
func fakeRestoreSeams(t *testing.T, inspectErr error) *restoreCalls {
	t.Helper()
	calls := &restoreCalls{}
	origI, origR, origA := restoreInspect, restoreRun, restoreApply
	t.Cleanup(func() { restoreInspect, restoreRun, restoreApply = origI, origR, origA })
	restoreInspect = func(_ context.Context, src backup.HostSource, _ backup.HostEnv) (backup.HostSummary, error) {
		calls.inspected = src
		if inspectErr != nil {
			return backup.HostSummary{}, inspectErr
		}
		return backup.HostSummary{
			Manifest:        backup.Manifest{Host: "oldbox", Stacks: []backup.ManifestStack{{Name: "web"}}},
			Conflicts:       []backup.Conflict{{Kind: "volume", Name: "web_data", Action: "overwrite"}},
			ConflictsDigest: "digest-1",
		}, nil
	}
	restoreRun = func(_ context.Context, src backup.HostSource, _ backup.HostEnv, start bool, digest string) (backup.HostReport, error) {
		calls.restored, calls.started, calls.digest = src, start, digest
		return backup.HostReport{}, nil
	}
	restoreApply = func(string, time.Time) (*backup.Pending, error) { calls.applied++; return nil, nil }
	return calls
}

func TestRestoreRefusesWhileServerRuns(t *testing.T) {
	h := newLocalHost(t, true)
	calls := fakeRestoreSeams(t, nil)
	lock, err := lockfile.Acquire(filepath.Join(h.dataDir, "stackstead.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	err = runRestore(newTestEnv(t, nil), "/tmp/a.tar.gz", "--yes", "--config", h.cfgPath)
	wantCode(t, err, ExitConflict)
	if calls.restored.Kind != "" {
		t.Error("restore ran although the server holds the lock")
	}
}

func TestHostRestoreNeedsYesWithoutTTY(t *testing.T) {
	h := newLocalHost(t, true)
	calls := fakeRestoreSeams(t, nil)
	env := newTestEnv(t, nil)
	wantCode(t, runRestore(env, "/tmp/a.tar.gz", "--config", h.cfgPath), ExitUsage)
	mustContain(t, "stdout", env.Out.String(), "web_data")
	if calls.restored.Kind != "" || calls.applied != 0 {
		t.Error("restore ran without confirmation")
	}
}

func TestRestoreReadsS3KeysFromEnv(t *testing.T) {
	h := newLocalHost(t, true)
	calls := fakeRestoreSeams(t, nil)
	env := newTestEnv(t, map[string]string{"AWS_ACCESS_KEY_ID": "AKIATEST", "AWS_SECRET_ACCESS_KEY": "s3cr3t-value"})
	err := runRestore(env, "s3://my-bucket/host/a.tar.gz", "--yes", "--s3-region", "eu-west-1",
		"--s3-endpoint", "https://minio.local", "--s3-path-style", "--config", h.cfgPath)
	wantCode(t, err, ExitOK)
	src := calls.inspected
	if src.Kind != backup.SourceS3 || src.S3.Bucket != "my-bucket" || src.Key != "host/a.tar.gz" ||
		src.S3.Region != "eu-west-1" || src.S3.Endpoint != "https://minio.local" || !src.S3.PathStyle ||
		src.S3.AccessKeyID != "AKIATEST" || string(src.S3.SecretKey) != "s3cr3t-value" {
		t.Errorf("source = %+v", src)
	}
	if calls.restored.Key != "host/a.tar.gz" || calls.digest != "digest-1" || !calls.started || calls.applied != 1 {
		t.Errorf("restore calls = %+v", calls)
	}
	if strings.Contains(env.Out.String()+env.Err.String(), "s3cr3t-value") {
		t.Error("secret key was printed")
	}
}

func TestRestoreNoStartAndPathSource(t *testing.T) {
	h := newLocalHost(t, true)
	calls := fakeRestoreSeams(t, nil)
	archive := filepath.Join(t.TempDir(), "a.tar.gz")
	wantCode(t, runRestore(newTestEnv(t, nil), archive, "--yes", "--no-start", "--config", h.cfgPath), ExitOK)
	if calls.restored.Kind != backup.SourcePath || calls.restored.Path != archive || calls.started {
		t.Errorf("calls = %+v", calls)
	}
}

func TestRestoreRefusesNewerArchive(t *testing.T) {
	h := newLocalHost(t, true)
	calls := fakeRestoreSeams(t, fmt.Errorf("%w: archive 9.0.0, installed 1.0.0", backup.ErrArchiveNewer))
	err := runRestore(newTestEnv(t, nil), "/tmp/a.tar.gz", "--yes", "--config", h.cfgPath)
	wantCode(t, err, ExitValidation)
	if !errors.Is(err, backup.ErrArchiveNewer) || calls.restored.Kind != "" {
		t.Errorf("err = %v, restored = %+v", err, calls.restored)
	}
}

func TestRestoreRejectsS3WithoutCredentials(t *testing.T) {
	h := newLocalHost(t, true)
	fakeRestoreSeams(t, nil)
	err := runRestore(newTestEnv(t, nil), "s3://b/k.tar.gz", "--yes", "--config", h.cfgPath)
	wantCode(t, err, ExitUsage)
}
