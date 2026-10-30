package composefile

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// ErrConflict means the file on disk is not the version the caller loaded:
// it was changed (or created) by someone else in the meantime.
var ErrConflict = errors.New("compose file changed on disk since it was loaded")

// newFileMode is the creation mode for a new compose file. It is passed to the
// kernel unmasked so the process umask decides the final permissions.
const newFileMode fs.FileMode = 0o666

// Hash identifies a file version for optimistic concurrency checks.
func Hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Load reads a compose file and returns its content and version hash.
func Load(path string) ([]byte, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	return data, Hash(data), nil
}

// Save writes data to path atomically. expectedHash must be the Hash of the
// version the caller loaded, or "" to create a file that must not exist yet;
// otherwise ErrConflict is returned and nothing is written. The previous
// version is kept as path + ".bak". An existing file keeps its permissions; a
// new file is created 0666 minus the process umask.
//
// The check and the write are not atomic with respect to other processes;
// callers serialize saves of the same stack.
func Save(path string, data []byte, expectedHash string) error {
	// Write through a symlink to its target so the link survives the rename.
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	mode, keepMode := newFileMode, false
	current, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if expectedHash != "" {
			return ErrConflict
		}
	case err != nil:
		return err
	default:
		if expectedHash == "" || Hash(current) != expectedHash {
			return ErrConflict
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		mode, keepMode = info.Mode().Perm(), true
		if err := writeAtomic(path+".bak", current, mode, true); err != nil {
			return fmt.Errorf("write backup: %w", err)
		}
	}
	return writeAtomic(path, data, mode, keepMode)
}

// WriteAtomic writes to a temporary file in the same directory, syncs it and
// renames it over path, so readers see either the old or the new content. The
// file gets exactly mode, regardless of the umask.
func WriteAtomic(path string, data []byte, mode fs.FileMode) error {
	return writeAtomic(path, data, mode, true)
}

// writeAtomic is WriteAtomic; with exact false, mode is only the creation mode
// and the umask narrows it, as for any newly created file. (os.CreateTemp
// would force 0600, and reading the umask to compensate is process-wide and
// racy, so the temp file is created directly with the wanted mode.)
func writeAtomic(path string, data []byte, mode fs.FileMode, exact bool) (err error) {
	dir := filepath.Dir(path)
	tmp, err := createTemp(dir, "."+filepath.Base(path)+".", ".tmp", mode)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if exact {
		if err = tmp.Chmod(mode); err != nil {
			return err
		}
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return syncDir(dir)
}

// createTemp creates a new file dir/prefix<random>suffix with the given
// creation mode (narrowed by the umask), like os.CreateTemp.
func createTemp(dir, prefix, suffix string, mode fs.FileMode) (*os.File, error) {
	for try := 0; ; try++ {
		var random [8]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, err
		}
		name := filepath.Join(dir, prefix+hex.EncodeToString(random[:])+suffix)
		f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, mode)
		if errors.Is(err, fs.ErrExist) && try < 100 {
			continue
		}
		return f, err
	}
}

// syncDir makes the rename durable across a crash.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
