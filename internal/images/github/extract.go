package github

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

const (
	maxPathBytes    = 4096
	maxSegmentBytes = 255
	// The decompressed stream (including entries outside subdir) may reach
	// archiveFactor*maxTotal plus tarOverhead for tar headers and padding.
	archiveFactor = 4
	tarOverhead   = 1 << 20
)

var (
	ErrBadPath         = errors.New("invalid file path")
	ErrFileTooLarge    = errors.New("file is larger than the per-file limit")
	ErrContextTooLarge = errors.New("files are larger than the build context limit")
	ErrArchiveTooLarge = errors.New("repository archive is too large")
)

// validatePath mirrors images.ValidatePath (internal/images/source.go), which
// this package cannot import because internal/images imports it. Keep the two
// in step: relative, no "..", no backslash or NUL, segments at most 255
// bytes, total at most 4096.
func validatePath(p string) (string, error) {
	bad := func(why string) error {
		shown := p
		if len(shown) > 100 {
			shown = shown[:100] + "..."
		}
		return fmt.Errorf("%w: %q (%s)", ErrBadPath, shown, why)
	}
	switch {
	case p == "":
		return "", bad("empty")
	case len(p) > maxPathBytes:
		return "", bad("longer than 4096 bytes")
	case strings.IndexByte(p, 0) >= 0:
		return "", bad("contains a NUL byte")
	case strings.Contains(p, `\`):
		return "", bad("contains a backslash")
	case strings.HasPrefix(p, "/"):
		return "", bad("must be relative")
	}
	var segs []string
	for _, s := range strings.Split(p, "/") {
		switch {
		case s == "" || s == ".":
			continue
		case s == "..":
			return "", bad("contains ..")
		case len(s) > maxSegmentBytes:
			return "", bad("a name is longer than 255 bytes")
		}
		segs = append(segs, s)
	}
	if len(segs) == 0 {
		return "", bad("empty")
	}
	return strings.Join(segs, "/"), nil
}

// limitedReader fails (instead of reporting EOF) once more than n bytes have
// been read, so a gzip bomb surfaces as ErrArchiveTooLarge.
type limitedReader struct {
	r io.Reader
	n int64
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.n <= 0 {
		return 0, ErrArchiveTooLarge
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	n, err := l.r.Read(p)
	l.n -= int64(n)
	return n, err
}

// ExtractContext unpacks a GitHub tarball into dst, which must be an empty
// directory. The top-level "<owner>-<repo>-<sha>/" component is stripped and
// only entries under subdir ("" is the repo root) are written, relative to
// subdir. Symlinks, hard links and devices are skipped. A hostile path fails
// the whole extraction with ErrBadPath. Each file is capped at maxFile bytes,
// the written content at maxTotal (ErrContextTooLarge) and the decompressed
// archive at 4*maxTotal (ErrArchiveTooLarge).
//
// On any error dst may hold partial content; the caller must remove dst.
func ExtractContext(tarGz io.Reader, subdir, dst string, maxFile, maxTotal int64) (int, error) {
	cleanSub := ""
	if subdir != "" {
		var err error
		if cleanSub, err = validatePath(subdir); err != nil {
			return 0, err
		}
	}
	entries, err := os.ReadDir(dst)
	if err != nil {
		return 0, fmt.Errorf("destination: %w", err)
	}
	if len(entries) > 0 {
		return 0, errors.New("destination is not empty")
	}
	root, err := os.OpenRoot(dst)
	if err != nil {
		return 0, fmt.Errorf("destination: %w", err)
	}
	defer root.Close()

	gz, err := gzip.NewReader(tarGz)
	if err != nil {
		return 0, errors.New("not a gzip archive")
	}
	defer gz.Close()
	tr := tar.NewReader(&limitedReader{r: gz, n: archiveFactor*maxTotal + tarOverhead})

	var files int
	var total int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return files, nil
		}
		if err != nil {
			if errors.Is(err, ErrArchiveTooLarge) {
				return files, err
			}
			return files, fmt.Errorf("read archive: %w", err)
		}
		if hdr.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		name, err := validatePath(hdr.Name)
		if err != nil {
			return files, err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue // directories are created on demand; links and devices are skipped
		}
		_, rel, ok := strings.Cut(name, "/")
		if !ok {
			continue // a file beside the top-level folder
		}
		if cleanSub != "" {
			if !strings.HasPrefix(rel, cleanSub+"/") {
				continue
			}
			rel = rel[len(cleanSub)+1:]
		}
		if hdr.Size > maxFile {
			return files, fmt.Errorf("%w: %q", ErrFileTooLarge, rel)
		}
		if total+hdr.Size > maxTotal {
			return files, ErrContextTooLarge
		}
		mode := os.FileMode(0o644)
		if hdr.Mode&0o100 != 0 {
			mode = 0o755
		}
		if err := writeFile(root, rel, mode, tr, hdr.Size); err != nil {
			return files, err
		}
		total += hdr.Size
		files++
	}
}

func writeFile(root *os.Root, rel string, mode os.FileMode, src io.Reader, size int64) error {
	if dir := path.Dir(rel); dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create folder for %q: %w", rel, err)
		}
	}
	f, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("create %q: %w", rel, err)
	}
	n, copyErr := io.Copy(f, io.LimitReader(src, size))
	chmodErr := f.Chmod(mode) // the umask may have narrowed it
	closeErr := f.Close()
	switch {
	case copyErr != nil:
		return fmt.Errorf("write %q: %w", rel, copyErr)
	case n != size:
		return fmt.Errorf("write %q: short entry", rel)
	case chmodErr != nil:
		return chmodErr
	}
	return closeErr
}
