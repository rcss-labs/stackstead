package updatehost

import (
	"github.com/rcss-labs/stackstead/internal/update"
	"golang.org/x/sys/unix"
	"os"
)

// F_GETLK observes locks without taking them. dpkg must acquire its own lock in
// the child; a conflict appearing after this probe is a native tool failure.
func debLocks() error {
	d, err := trustedDirectory("/var/lib/dpkg", 0, "")
	if err != nil {
		return err
	}
	defer d.Close()
	for _, name := range []string{"lock-frontend", "lock"} {
		fd, err := unix.Openat(int(d.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err != nil {
			return update.ErrSetupRequired
		}
		f := os.NewFile(uintptr(fd), name)
		var st unix.Stat_t
		if unix.Fstat(fd, &st) != nil || st.Uid != 0 || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0022 != 0 {
			f.Close()
			return update.ErrSetupRequired
		}
		lock := unix.Flock_t{Type: unix.F_WRLCK, Whence: int16(os.SEEK_SET), Start: 0, Len: 0}
		err = unix.FcntlFlock(uintptr(fd), unix.F_GETLK, &lock)
		f.Close()
		if err != nil {
			return update.ErrSetupRequired
		}
		if lock.Type != unix.F_UNLCK {
			return update.ErrBusy
		}
	}
	return nil
}
