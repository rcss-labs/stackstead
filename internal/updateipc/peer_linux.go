package updateipc

import (
	"net"

	"github.com/rcss-labs/stackstead/internal/update"
	"golang.org/x/sys/unix"
)

func SocketUID(c net.Conn) (uint32, error) {
	u, ok := c.(*net.UnixConn)
	if !ok {
		return 0, update.ErrUnauthorized
	}
	raw, e := u.SyscallConn()
	if e != nil {
		return 0, e
	}
	var uid uint32
	var inner error
	e = raw.Control(func(fd uintptr) {
		cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		inner = err
		if err == nil {
			uid = cred.Uid
		}
	})
	if e != nil {
		return 0, e
	}
	return uid, inner
}

const SocketRoot = "/run/stackstead-updater"
