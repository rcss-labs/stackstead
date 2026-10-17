package updatehost

import (
	"golang.org/x/sys/unix"
	"os"
	"strconv"
)

func childPeerUID(fd int) (uint32, error) {
	c, e := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if e != nil {
		return 0, e
	}
	return c.Uid, nil
}
func inheritedChildFDs() ([]int, error) { return inheritedChildFDsAt("/proc/self/fd") }
func inheritedChildFDsAt(path string) ([]int, error) {
	dir, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer dir.Close()
	entries, e := dir.Readdirnames(-1)
	if e != nil {
		return nil, e
	}
	var result []int
	for _, entry := range entries {
		fd, e := strconv.Atoi(entry)
		if e != nil {
			continue
		}
		flags, e := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if e == nil && flags&unix.FD_CLOEXEC == 0 {
			result = append(result, fd)
		}
	}
	return result, nil
}
func childResourceLimits(request childRequest) error {
	if err := unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{Cur: 0, Max: 0}); err != nil {
		return err
	}
	if err := unix.Setrlimit(unix.RLIMIT_CPU, &unix.Rlimit{Cur: 600, Max: 605}); err != nil {
		return err
	}
	limit := request.Limit
	if request.Role == childRestoreDB {
		limit = request.Expected.Budget
	}
	if limit > 0 {
		return unix.Setrlimit(unix.RLIMIT_FSIZE, &unix.Rlimit{Cur: uint64(limit), Max: uint64(limit)})
	}
	return nil
}
