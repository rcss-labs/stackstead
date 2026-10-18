package update

import (
	"errors"
	"os"
	"syscall"
)

// DpkgFrontendLocked probes the locks dpkg and apt take, without blocking:
// lock-frontend (apt) and lock (dpkg itself).
func DpkgFrontendLocked() (bool, error) {
	return dpkgLocksHeldAt("/var/lib/dpkg/lock-frontend", "/var/lib/dpkg/lock")
}

// dpkgLocksHeldAt reports whether any of the lock files is held.
func dpkgLocksHeldAt(paths ...string) (bool, error) {
	for _, path := range paths {
		held, err := dpkgFrontendLockedAt(path)
		if err != nil || held {
			return held, err
		}
	}
	return false, nil
}

func dpkgFrontendLockedAt(path string) (bool, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	lock := syscall.Flock_t{Type: syscall.F_WRLCK, Whence: 0, Start: 0, Len: 0}
	err = syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &lock)
	if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EACCES) {
		return true, nil
	}
	return false, err
}
