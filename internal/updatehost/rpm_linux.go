package updatehost

import (
	"os"
	"path/filepath"

	"github.com/rcss-labs/stackstead/internal/update"
	"golang.org/x/sys/unix"
)

// Permit the documented Fedora database relocation only. Both the source
// parent and the canonical target are descriptor-walked as root-owned paths;
// the single symlink itself must also be root-owned. Other aliases fail closed.
func rpmDirectory(path string) (*os.File, error) {
	if path != "/var/lib/rpm" && path != "/usr/lib/sysimage/rpm" {
		return nil, update.ErrSetupRequired
	}
	d, err := trustedDirectory(path, 0, "")
	if err == nil || path != "/var/lib/rpm" {
		return d, err
	}
	parent, err := trustedDirectory("/var/lib", 0, "")
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	var st unix.Stat_t
	if unix.Fstatat(int(parent.Fd()), "rpm", &st, unix.AT_SYMLINK_NOFOLLOW) != nil || st.Uid != 0 || st.Mode&unix.S_IFMT != unix.S_IFLNK {
		return nil, update.ErrSetupRequired
	}
	target := make([]byte, 128)
	n, err := unix.Readlinkat(int(parent.Fd()), "rpm", target)
	if err != nil || (string(target[:n]) != "../../usr/lib/sysimage/rpm" && string(target[:n]) != "/usr/lib/sysimage/rpm") {
		return nil, update.ErrSetupRequired
	}
	return trustedDirectory("/usr/lib/sysimage/rpm", 0, "")
}

// RPM 4.20 lib/rpmlock.c uses whole-file fcntl record locks (F_SETLK),
// not flock. F_GETLK observes contention without taking the child's lock.
func rpmLocks(db, path string) error {
	database, err := rpmDirectory(db)
	if err != nil {
		return err
	}
	database.Close()
	d, err := rpmDirectory(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	fd, err := unix.Openat(int(d.Fd()), filepath.Base(path), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return update.ErrSetupRequired
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Uid != 0 || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0022 != 0 {
		return update.ErrSetupRequired
	}
	lock := unix.Flock_t{Type: unix.F_WRLCK, Whence: int16(os.SEEK_SET), Start: 0, Len: 0}
	if unix.FcntlFlock(uintptr(fd), unix.F_GETLK, &lock) != nil {
		return update.ErrSetupRequired
	}
	if lock.Type != unix.F_UNLCK {
		return update.ErrBusy
	}
	return nil
}
