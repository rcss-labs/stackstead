//go:build darwin || linux || freebsd

// Package lockfile is the advisory lock that keeps a second server (or a
// database restore) from running against the same data directory.
package lockfile

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// Lock is a held lock; Release it when done.
type Lock struct {
	mu   sync.Mutex
	f    *os.File
	path string
}

// HeldError means another process (or open file description) holds the lock.
type HeldError struct {
	Path string
	PID  int // 0 when the holder's pid could not be read
}

func (e *HeldError) Error() string {
	return fmt.Sprintf("%s is held by pid %d (is the server running?)", e.Path, e.PID)
}

// Acquire takes an exclusive, non-blocking flock on path (created 0600) and
// records the caller's pid in it for messages. The lock belongs to the open
// file description, so a second Acquire in the same process also fails.
func Acquire(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		pid := readPID(f)
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, &HeldError{Path: path, PID: pid}
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	if err := f.Truncate(0); err != nil {
		f.Close()
		return nil, fmt.Errorf("write lock pid: %w", err)
	}
	if _, err := f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0); err != nil {
		f.Close()
		return nil, fmt.Errorf("write lock pid: %w", err)
	}
	return &Lock{f: f, path: path}, nil
}

func readPID(f *os.File) int {
	buf := make([]byte, 32)
	n, _ := f.ReadAt(buf, 0)
	pid, err := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
	if err != nil || pid < 0 {
		return 0
	}
	return pid
}

// Release unlocks and closes the lock. The file is deliberately left in place:
// deleting it would race with another process that already opened it.
func (l *Lock) Release() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	f := l.f
	l.f = nil
	unlockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	if err := f.Close(); err != nil {
		return err
	}
	return unlockErr
}
