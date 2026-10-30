package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const partialSuffix = ".partial"

type localDest struct {
	cfg LocalDest
}

// NewLocal returns a destination writing into cfg.Path.
func NewLocal(cfg LocalDest) Destination { return &localDest{cfg: cfg} }

func (d *localDest) Name() string { return destLocal }

func (d *localDest) path(name string) string {
	return filepath.Join(d.cfg.Path, filepath.Base(name))
}

// Create writes to "<name>.partial"; Close syncs it and renames it into place.
func (d *localDest) Create(_ context.Context, archive string) (io.WriteCloser, error) {
	if err := os.MkdirAll(d.cfg.Path, 0o700); err != nil {
		return nil, fmt.Errorf("create backup folder: %w", err)
	}
	final := d.path(archive)
	f, err := os.OpenFile(final+partialSuffix, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", archive, err)
	}
	return &localWriter{File: f, final: final}, nil
}

type localWriter struct {
	*os.File
	final    string
	writeErr error
}

// Write remembers the first failure so Close never publishes a truncated archive.
func (w *localWriter) Write(p []byte) (int, error) {
	n, err := w.File.Write(p)
	if err != nil && w.writeErr == nil {
		w.writeErr = err
	}
	return n, err
}

func (w *localWriter) Close() error {
	partial := w.File.Name()
	if w.writeErr != nil {
		_ = w.File.Close()
		_ = os.Remove(partial)
		return fmt.Errorf("write %s: %w", w.final, w.writeErr)
	}
	if err := w.File.Sync(); err != nil {
		_ = w.File.Close()
		_ = os.Remove(partial)
		return fmt.Errorf("sync %s: %w", w.final, err)
	}
	if err := w.File.Close(); err != nil {
		_ = os.Remove(partial)
		return fmt.Errorf("close %s: %w", w.final, err)
	}
	if err := os.Rename(partial, w.final); err != nil {
		_ = os.Remove(partial)
		return fmt.Errorf("publish %s: %w", w.final, err)
	}
	return syncDir(filepath.Dir(w.final))
}

// syncDir makes a rename durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (d *localDest) Abort(_ context.Context, archive string) error {
	err := os.Remove(d.path(archive) + partialSuffix)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (d *localDest) PutSidecar(_ context.Context, archive string, manifest []byte) error {
	if err := os.MkdirAll(d.cfg.Path, 0o700); err != nil {
		return err
	}
	final := d.path(SidecarName(archive))
	partial := final + partialSuffix
	f, err := os.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(manifest)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(partial, final)
	}
	if err != nil {
		_ = os.Remove(partial)
		return fmt.Errorf("write sidecar of %s: %w", archive, err)
	}
	return syncDir(d.cfg.Path)
}

func (d *localDest) List(_ context.Context) ([]ArchiveRef, error) {
	entries, err := os.ReadDir(d.cfg.Path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var refs []ArchiveRef
	for _, e := range entries {
		slug, created, ok := ParseArchiveName(e.Name())
		if !ok || e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		refs = append(refs, ArchiveRef{Dest: destLocal, Name: e.Name(), HostSlug: slug, CreatedAt: created, Size: info.Size(), Encrypted: IsEncryptedName(e.Name())})
	}
	return refs, nil
}

func (d *localDest) Open(_ context.Context, archive string) (io.ReadCloser, error) {
	return os.Open(d.path(archive))
}

func (d *localDest) ReadSidecar(_ context.Context, archive string) (Manifest, error) {
	raw, err := os.ReadFile(d.path(SidecarName(archive)))
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, fmt.Errorf("parse manifest of %s: %w", archive, err)
	}
	return m, nil
}

// PruneLocal deletes all but the newest cfg.KeepLast archives of slug, with
// their sidecars, and returns the deleted paths. Other slugs and ".partial"
// files are never touched.
func PruneLocal(cfg LocalDest, slug string) ([]string, error) {
	if cfg.KeepLast < 1 {
		return nil, fmt.Errorf("prune: keep-last %d must be at least 1", cfg.KeepLast)
	}
	refs, err := (&localDest{cfg: cfg}).List(context.Background())
	if err != nil {
		return nil, err
	}
	var own []ArchiveRef
	for _, r := range refs {
		// List only returns names ParseArchiveName accepts, which excludes
		// ".partial" files; the suffix check is a defensive guard.
		if r.HostSlug == slug && !strings.HasSuffix(r.Name, partialSuffix) {
			own = append(own, r)
		}
	}
	sort.Slice(own, func(i, j int) bool { return own[i].CreatedAt.After(own[j].CreatedAt) })
	if len(own) <= cfg.KeepLast {
		return nil, nil
	}
	var deleted []string
	for _, r := range own[cfg.KeepLast:] {
		for _, name := range []string{r.Name, SidecarName(r.Name)} {
			p := filepath.Join(cfg.Path, name)
			if err := os.Remove(p); err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return deleted, err
			}
			deleted = append(deleted, p)
		}
	}
	return deleted, nil
}
