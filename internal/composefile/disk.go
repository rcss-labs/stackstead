package composefile

import (
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

const newFileMode fs.FileMode = 0o644

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
// version is kept as path + ".bak". The file's permissions are preserved.
//
// The check and the write are not atomic with respect to other processes;
// callers serialize saves of the same stack.
func Save(path string, data []byte, expectedHash string) error {
	// Write through a symlink to its target so the link survives the rename.
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	mode := newFileMode
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
		mode = info.Mode().Perm()
		if err := WriteAtomic(path+".bak", current, mode); err != nil {
			return fmt.Errorf("write backup: %w", err)
		}
	}
	return WriteAtomic(path, data, mode)
}

// WriteAtomic writes to a temporary file in the same directory, syncs it and
// renames it over path, so readers see either the old or the new content.
func WriteAtomic(path string, data []byte, mode fs.FileMode) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
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
	if err = tmp.Chmod(mode); err != nil {
		return err
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

// syncDir makes the rename durable across a crash.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
