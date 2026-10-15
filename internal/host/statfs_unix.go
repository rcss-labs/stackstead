//go:build linux || darwin

package host

import "syscall"

// OSStatfs reads filesystem usage with statfs(2).
type OSStatfs struct{}

// Statfs returns the space available to unprivileged users (Bavail) and the
// total size. Field types differ between linux and darwin, hence the
// conversions.
func (OSStatfs) Statfs(path string) (free, total uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bsize := uint64(st.Bsize)
	return uint64(st.Bavail) * bsize, uint64(st.Blocks) * bsize, nil
}
