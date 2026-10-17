package updatecontrol

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"time"

	"github.com/rcss-labs/stackstead/internal/update"
	"github.com/rcss-labs/stackstead/internal/updateipc"
	"golang.org/x/sys/unix"
)

var instanceID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// Listen uses only the root-authored registration path. Bootstrap provisions the
// root-owned runtime/apps parents and an app-owned 0700 instance directory.
func Listen(in update.Instance) (net.Listener, error) {
	root := filepath.Join(updateipc.SocketRoot, "apps")
	if runtime.GOOS == "darwin" {
		root = "/private" + root
	}
	canonical := in.ControlSocket
	if runtime.GOOS == "darwin" && canonical == filepath.Join(updateipc.SocketRoot, "apps", in.ID, "control.sock") {
		canonical = "/private" + canonical
	}
	in.ControlSocket = canonical
	// Check every fixed ancestor; only Darwin's known /var alias is normalized.
	for p := filepath.Dir(root); p != "/"; p = filepath.Dir(p) {
		if e := checkDirectory(p, 0, false); e != nil {
			return nil, e
		}
	}
	return listenControl(in, root, 0)
}
func checkDirectory(path string, uid uint32, private bool) error {
	st, e := os.Lstat(path)
	if e != nil {
		return e
	}
	var raw unix.Stat_t
	e = unix.Lstat(path, &raw)
	ok := e == nil
	if !ok || !st.IsDir() || raw.Uid != uid || st.Mode().Perm()&0022 != 0 || private && st.Mode().Perm()&0077 != 0 {
		return update.ErrUnauthorized
	}
	return nil
}
func listenControl(in update.Instance, root string, rootUID uint32) (net.Listener, error) {
	if !instanceID.MatchString(in.ID) || in.ControlSocket != filepath.Join(root, in.ID, "control.sock") || len(in.ControlSocket) > 103 {
		return nil, update.ErrIncompatible
	}
	if e := checkDirectory(root, rootUID, false); e != nil {
		return nil, e
	}
	if e := checkDirectory(filepath.Dir(in.ControlSocket), in.UID, true); e != nil {
		return nil, e
	}
	if st, e := os.Lstat(in.ControlSocket); e == nil {
		var raw unix.Stat_t
		e = unix.Lstat(in.ControlSocket, &raw)
		ok := e == nil
		if !ok || st.Mode()&os.ModeSocket == 0 || raw.Uid != in.UID {
			return nil, update.ErrUnauthorized
		}
		c, err := net.DialTimeout("unix", in.ControlSocket, time.Second)
		if err == nil {
			c.Close()
			return nil, update.ErrBusy
		}
		if !errors.Is(err, unix.ECONNREFUSED) {
			return nil, update.ErrUnavailable
		}
		if err = os.Remove(in.ControlSocket); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	l, e := net.Listen("unix", in.ControlSocket)
	if e != nil {
		return nil, e
	}
	if e = os.Chmod(in.ControlSocket, 0600); e != nil {
		l.Close()
		return nil, e
	}
	return l, nil
}
