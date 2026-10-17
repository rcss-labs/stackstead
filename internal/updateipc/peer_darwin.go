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
		cred, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		inner = err
		if err == nil {
			if cred.Version != 0 /* XUCRED_VERSION in sys/ucred.h */ {
				inner = update.ErrUnauthorized
			} else {
				uid = cred.Uid
			}
		}
	})
	if e != nil {
		return 0, e
	}
	return uid, inner
}

const SocketRoot = "/var/run/stackstead-updater"
