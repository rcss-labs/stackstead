//go:build !(linux || darwin)

package host

import (
	"errors"
	"runtime"
)

// OSStatfs is unsupported on this platform, so the disk section is null.
type OSStatfs struct{}

func (OSStatfs) Statfs(string) (free, total uint64, err error) {
	return 0, 0, errors.New("statfs is not supported on " + runtime.GOOS)
}
