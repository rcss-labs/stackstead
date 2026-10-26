//go:build darwin || linux || freebsd

package lockfile

import (
	"path/filepath"
	"syscall"
	"testing"
)

// A host restore re-executes the server in place. The lock must not survive
// into the new process image, or the new server would find its own lock held.
func TestLockIsNotInheritedAcrossExec(t *testing.T) {
	l, err := Acquire(filepath.Join(t.TempDir(), "stackstead.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, l.f.Fd(), syscall.F_GETFD, 0)
	if errno != 0 {
		t.Fatal(errno)
	}
	if flags&syscall.FD_CLOEXEC == 0 {
		t.Error("the lock file descriptor is not close-on-exec")
	}
}
