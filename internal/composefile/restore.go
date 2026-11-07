package composefile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// ErrNoBackup means there is no usable path+".bak" to restore from.
var ErrNoBackup = errors.New("no backup of the compose file")

// maxBackupSize bounds what a restore reads; compose files are far smaller.
const maxBackupSize = 4 << 20

// readBackup returns the contents of the regular file path+".bak". A link, a
// folder or anything else is treated as no backup, so a restore never reads
// or copies content from outside the stack folder.
func readBackup(path string) ([]byte, error) {
	bak := path + ".bak"
	info, err := os.Lstat(bak)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNoBackup
		}
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxBackupSize {
		return nil, ErrNoBackup
	}
	return os.ReadFile(bak)
}

// HasBackup reports whether RestoreBackup would find a backup for path.
func HasBackup(path string) bool {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	info, err := os.Lstat(path + ".bak")
	return err == nil && info.Mode().IsRegular() && info.Size() <= maxBackupSize
}

// RestoreBackup replaces the compose file with path+".bak" and keeps the file
// it replaced as path+".broken". expectedHash must be the Hash of the version
// the caller saw, otherwise ErrConflict is returned and nothing is written;
// ErrNoBackup means there is nothing to restore. The backup itself is left in
// place, so it still equals the restored file. The file keeps its permissions.
//
// Like Save, the check and the write are not atomic with respect to other
// processes; callers serialize changes to the same stack.
func RestoreBackup(path, expectedHash string) error {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	backup, err := readBackup(path)
	if err != nil {
		return err
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if expectedHash == "" || Hash(current) != expectedHash {
		return ErrConflict
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	mode := info.Mode().Perm()
	if err := writeAtomic(path+".broken", current, mode, true); err != nil {
		return fmt.Errorf("keep broken copy: %w", err)
	}
	return writeAtomic(path, backup, mode, true)
}
