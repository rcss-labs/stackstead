//go:build unix

package lockfile_test

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rcss-labs/stackstead/internal/lockfile"
)

func lockPath(t *testing.T) string {
	return filepath.Join(t.TempDir(), "stackstead.lock")
}

func TestAcquireTwiceFails(t *testing.T) {
	path := lockPath(t)
	first, err := lockfile.Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer first.Release()

	_, err = lockfile.Acquire(path)
	var held *lockfile.HeldError
	if !errors.As(err, &held) {
		t.Fatalf("second Acquire err = %v, want *HeldError", err)
	}
	if held.PID != os.Getpid() || held.Path != path {
		t.Errorf("HeldError = %+v, want pid %d path %s", held, os.Getpid(), path)
	}
	if !strings.Contains(err.Error(), "held by pid "+strconv.Itoa(os.Getpid())) {
		t.Errorf("message %q lacks the holder pid", err)
	}
}

func TestReleaseAllowsReacquire(t *testing.T) {
	path := lockPath(t)
	first, err := lockfile.Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Errorf("second Release = %v, want idempotent nil", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("lock file must stay after Release: %v", err)
	}
	second, err := lockfile.Acquire(path)
	if err != nil {
		t.Fatalf("reacquire: %v", err)
	}
	second.Release()
}

func TestAcquireCreatesPrivateFileWithPID(t *testing.T) {
	path := lockPath(t)
	l, err := lockfile.Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, %v; want 0600", info.Mode().Perm(), err)
	}
	data, _ := os.ReadFile(path)
	if strings.TrimSpace(string(data)) != strconv.Itoa(os.Getpid()) {
		t.Errorf("file content = %q, want pid", data)
	}
}

func TestHeldErrorPIDZeroWhenUnreadable(t *testing.T) {
	path := lockPath(t)
	l, err := lockfile.Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if err := os.Truncate(path, 0); err != nil { // holder's pid text is gone
		t.Fatal(err)
	}
	_, err = lockfile.Acquire(path)
	var held *lockfile.HeldError
	if !errors.As(err, &held) || held.PID != 0 {
		t.Fatalf("err = %v, want HeldError with PID 0", err)
	}
}
