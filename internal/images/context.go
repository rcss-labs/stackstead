package images

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const maxDockerignoreBytes = 1 << 20

// Materialize writes the manifest's files under dir, which must be empty (it
// is created if missing). Every file is a copy written through an os.Root on
// dir, so nothing can land outside it; blobs keep their own 0600 mode.
func Materialize(dir string, m Manifest, b *Blobs) error {
	for p := range m {
		if clean, err := ValidatePath(p); err != nil || clean != p {
			return badPath(p, "not a clean manifest path")
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("build directory %s is not empty", dir)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()

	paths := make([]string, 0, len(m))
	for p := range m {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		e := m[p]
		if parent := filepath.Dir(filepath.FromSlash(p)); parent != "." {
			if err := root.MkdirAll(parent, 0o755); err != nil {
				return fmt.Errorf("creating folder for %q: %w", p, err)
			}
		}
		if err := materializeOne(root, p, e, b); err != nil {
			return fmt.Errorf("writing %q: %w", p, err)
		}
	}
	return nil
}

func materializeOne(root *os.Root, p string, e FileEntry, b *Blobs) error {
	rel := filepath.FromSlash(p)
	src, err := b.pathFor(e.Hash)
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	mode := fs.FileMode(0o644)
	if e.Mode == 0o755 {
		mode = 0o755
	}
	out, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n != e.Size || hex.EncodeToString(h.Sum(nil)) != e.Hash {
		root.Remove(rel)
		return fmt.Errorf("blob for %q is corrupt (hash or size does not match the manifest)", p)
	}
	return root.Chmod(rel, mode)
}

// WriteContext writes dir as a tar build context: regular files only, those
// excluded by dir/.dockerignore left out, sorted by path, owner 0, mtime at
// the epoch, mode 0755 for executables and 0644 otherwise. The file at
// dockerfilePath (empty means "Dockerfile") and .dockerignore are always kept,
// whatever the ignore rules say.
func WriteContext(ctx context.Context, w io.Writer, dir, dockerfilePath string) error {
	if dockerfilePath == "" {
		dockerfilePath = "Dockerfile"
	}
	dockerfilePath, err := ValidatePath(dockerfilePath)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()

	ignore := &Ignore{}
	if f, err := root.Open(".dockerignore"); err == nil {
		src, rerr := io.ReadAll(io.LimitReader(f, maxDockerignoreBytes+1))
		f.Close()
		if rerr != nil {
			return rerr
		}
		if len(src) > maxDockerignoreBytes {
			return errors.New(".dockerignore is larger than 1 MiB")
		}
		if ignore, err = ParseDockerignore(src); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	var files []string
	err = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.Type().IsRegular() && (p == dockerfilePath || !ignore.Excluded(p)) {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(files)

	tw := tar.NewWriter(w)
	for _, p := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := writeContextFile(tw, root, p); err != nil {
			return err
		}
	}
	return tw.Close()
}

func writeContextFile(tw *tar.Writer, root *os.Root, p string) error {
	f, err := root.Open(filepath.FromSlash(p))
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	mode := int64(0o644)
	if info.Mode().Perm()&0o111 != 0 {
		mode = 0o755
	}
	err = tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     p,
		Size:     info.Size(),
		Mode:     mode,
		ModTime:  time.Unix(0, 0),
		Format:   tar.FormatPAX,
	})
	if err != nil {
		return err
	}
	if n, err := io.Copy(tw, io.LimitReader(f, info.Size())); err != nil || n != info.Size() {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return fmt.Errorf("reading %q: %w", p, err)
	}
	return nil
}
