package update

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/sys/unix"
)

var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

const stateLimit = 4 << 20

type trustStore struct {
	root  string
	owner uint32
	// anchor is populated only by unexported test construction. Production walks from /.
	anchor       string
	publicDirs   bool
	beforeRename func() error
}

func (s *trustStore) check(f *os.File, directory, private bool) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	var st unix.Stat_t
	if err = unix.Fstat(int(f.Fd()), &st); err != nil {
		return err
	}
	if st.Uid != s.owner || info.Mode().Perm()&0022 != 0 || (private && info.Mode().Perm()&0077 != 0) {
		return fmt.Errorf("%w: insecure trust ownership or permissions", ErrSetupRequired)
	}
	if directory && !info.IsDir() || !directory && !info.Mode().IsRegular() {
		return fmt.Errorf("%w: invalid trust file type", ErrSetupRequired)
	}
	return nil
}
func (s *trustStore) directory(relative string, create bool) (*os.File, error) {
	if relative != "" && (filepath.IsAbs(relative) || filepath.Clean(relative) != relative || strings.HasPrefix(relative, "..")) {
		return nil, ErrUnauthorized
	}
	start := "/"
	path := strings.TrimPrefix(s.root, "/")
	if s.anchor != "" {
		start = s.anchor
		path = ""
	}
	fd, err := unix.Open(start, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(fd), start)
	if err = s.check(current, true, s.anchor != "" && !s.publicDirs); err != nil {
		current.Close()
		return nil, err
	}
	full := path
	if relative != "" {
		if full != "" {
			full += "/"
		}
		full += relative
	}
	for _, part := range strings.Split(full, "/") {
		if part == "" {
			continue
		}
		next, e := unix.Openat(int(current.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if e == unix.ENOENT && create {
			mode := uint32(0700)
			if s.publicDirs {
				mode = 0755
			}
			e = unix.Mkdirat(int(current.Fd()), part, mode)
			if e == nil || e == unix.EEXIST {
				if e = current.Sync(); e == nil {
					next, e = unix.Openat(int(current.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
				}
			}
		}
		current.Close()
		if e != nil {
			return nil, fmt.Errorf("%w: trust directory: %w", ErrSetupRequired, e)
		}
		current = os.NewFile(uintptr(next), part)
		if e = s.check(current, true, false); e != nil {
			current.Close()
			return nil, e
		}
	}
	if err = s.check(current, true, !s.publicDirs); err != nil {
		current.Close()
		return nil, err
	}
	return current, nil
}
func (s *trustStore) read(dir, name string) ([]byte, error) {
	d, err := s.directory(dir, false)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	f, err := s.openFile(d, name, unix.O_RDONLY, 0, true)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, stateLimit+1))
	if err == nil && len(b) > stateLimit {
		err = ErrIncompatible
	}
	return b, err
}
func (s *trustStore) openFile(d *os.File, name string, flags int, mode uint32, private bool) (*os.File, error) {
	if filepath.Base(name) != name || name == "." || name == ".." {
		return nil, ErrUnauthorized
	}
	fd, err := unix.Openat(int(d.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, mode)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	if err = s.check(f, false, private); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func (s *trustStore) atomic(dir, name string, data []byte, mode uint32) error {
	if (mode != 0700 && len(data) > stateLimit) || int64(len(data)) > artifactLimit {
		return ErrIncompatible
	}
	d, err := s.directory(dir, true)
	if err != nil {
		return err
	}
	defer d.Close()
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return err
	}
	temp := ".tmp-" + hex.EncodeToString(nonce[:])
	f, err := s.openFile(d, temp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, mode, mode == 0600)
	if err != nil {
		return err
	}
	defer unix.Unlinkat(int(d.Fd()), temp, 0)
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if s.beforeRename != nil {
		if err = s.beforeRename(); err != nil {
			return err
		}
	}
	if err = unix.Renameat(int(d.Fd()), temp, int(d.Fd()), name); err != nil {
		return err
	}
	return d.Sync()
}
func productionStore(root string) (*trustStore, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, ErrSetupRequired
	}
	s := &trustStore{root: root}
	d, err := s.directory("", false)
	if err != nil {
		return nil, err
	}
	d.Close()
	return s, nil
}
func validateStaging(path string) error {
	if path != filepath.Join(PrivateRoot, StagingDirectory) {
		return ErrSetupRequired
	}
	s, err := productionStore(PrivateRoot)
	if err != nil {
		return err
	}
	d, err := s.directory(StagingDirectory, true)
	if err == nil {
		err = d.Close()
	}
	return err
}
