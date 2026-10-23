package images

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const blobTmpPrefix = ".tmp-"

// staleBlobTmpAge is how old an abandoned upload temp file must be before GC
// removes it; younger ones may belong to a Put in progress.
const staleBlobTmpAge = time.Hour

// Blobs is a content-addressed file store: one 0600 file per SHA-256 in a
// 0700 directory.
type Blobs struct{ dir string }

// OpenBlobs creates dir (0700) if needed and returns the store.
func OpenBlobs(dir string) (*Blobs, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	return &Blobs{dir: dir}, nil
}

func blobHashValid(h string) bool {
	if len(h) != sha256.Size*2 {
		return false
	}
	for _, c := range h {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func (b *Blobs) pathFor(hash string) (string, error) {
	if !blobHashValid(hash) {
		return "", fmt.Errorf("invalid blob hash %q", hash)
	}
	return filepath.Join(b.dir, hash), nil
}

// Put stores r and returns its SHA-256 and size. It fails with
// ErrFileTooLarge, leaving nothing behind, when r holds more than maxBytes.
// Storing content that already exists is a no-op.
func (b *Blobs) Put(r io.Reader, maxBytes int64) (hash string, size int64, err error) {
	tmp, err := os.CreateTemp(b.dir, blobTmpPrefix+"*")
	if err != nil {
		return "", 0, err
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			os.Remove(tmpName)
		}
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(r, maxBytes+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", 0, err
	}
	if n > maxBytes {
		return "", 0, ErrFileTooLarge
	}
	hash = hex.EncodeToString(h.Sum(nil))
	dst := filepath.Join(b.dir, hash)
	if _, statErr := os.Stat(dst); statErr == nil {
		os.Remove(tmpName)
		return hash, n, nil
	}
	if err = os.Chmod(tmpName, 0o600); err != nil {
		return "", 0, err
	}
	if err = os.Rename(tmpName, dst); err != nil {
		return "", 0, err
	}
	return hash, n, nil
}

// Open opens the blob for reading.
func (b *Blobs) Open(hash string) (*os.File, error) {
	p, err := b.pathFor(hash)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

// Remove deletes one blob; a missing blob is not an error.
func (b *Blobs) Remove(hash string) error {
	p, err := b.pathFor(hash)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// GC removes every blob whose hash is not in keep, plus abandoned temp files,
// and reports how many blobs it removed.
func (b *Blobs) GC(keep map[string]bool) (removed int, err error) {
	entries, err := os.ReadDir(b.dir)
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		name := e.Name()
		switch {
		case strings.HasPrefix(name, blobTmpPrefix):
			if info, ierr := e.Info(); ierr == nil && time.Since(info.ModTime()) > staleBlobTmpAge {
				os.Remove(filepath.Join(b.dir, name))
			}
		case blobHashValid(name) && !keep[name]:
			if rerr := os.Remove(filepath.Join(b.dir, name)); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
				err = errors.Join(err, rerr)
				continue
			}
			removed++
		}
	}
	return removed, err
}
