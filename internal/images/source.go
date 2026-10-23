package images

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
)

const (
	maxPathBytes    = 4096
	maxSegmentBytes = 255
	// maxDockerfileBytes matches the Dockerfile limit used for stacks.
	maxDockerfileBytes = 1 << 20

	// DefaultDockerfile is the starting Dockerfile of a new image.
	DefaultDockerfile = "FROM alpine:3.20\n# Add build steps below, for example:\n# RUN apk add --no-cache curl\n"
)

var (
	ErrFileTooLarge    = errors.New("file is larger than the per-file limit")
	ErrContextTooLarge = errors.New("files are larger than the build context limit")
	ErrBadPath         = errors.New("invalid file path")
	ErrBadDockerfile   = errors.New("invalid Dockerfile")
)

// Limits are the upload caps in bytes.
type Limits struct{ MaxFileBytes, MaxContextBytes int64 }

// UploadFile is one file of an upload.
type UploadFile struct {
	Path    string
	Content io.Reader
}

func badPath(p, why string) error {
	shown := p
	if len(shown) > 100 {
		shown = shown[:100] + "..."
	}
	return fmt.Errorf("%w: %q (%s)", ErrBadPath, shown, why)
}

// ValidatePath cleans an upload, rename or tarball path: relative, no "..",
// no backslash or NUL, segments at most 255 bytes, total at most 4096.
func ValidatePath(p string) (string, error) {
	switch {
	case p == "":
		return "", badPath(p, "empty")
	case len(p) > maxPathBytes:
		return "", badPath(p, "longer than 4096 bytes")
	case strings.IndexByte(p, 0) >= 0:
		return "", badPath(p, "contains a NUL byte")
	case strings.Contains(p, `\`):
		return "", badPath(p, "contains a backslash")
	case strings.HasPrefix(p, "/"):
		return "", badPath(p, "must be relative")
	}
	var segs []string
	for _, s := range strings.Split(p, "/") {
		switch {
		case s == "" || s == ".":
			continue
		case s == "..":
			return "", badPath(p, "contains ..")
		case len(s) > maxSegmentBytes:
			return "", badPath(p, "a name is longer than 255 bytes")
		}
		segs = append(segs, s)
	}
	if len(segs) == 0 {
		return "", badPath(p, "empty")
	}
	return strings.Join(segs, "/"), nil
}

// pathConflict reports a file that stands where a folder is needed, or the
// reverse, for a new file at p.
func (m Manifest) pathConflict(p string) bool {
	for dir := path.Dir(p); dir != "."; dir = path.Dir(dir) {
		if _, ok := m[dir]; ok {
			return true
		}
	}
	return m.hasChildren(p)
}

func (m Manifest) hasChildren(dir string) bool {
	prefix := dir + "/"
	for k := range m {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}

func plural(n int, one string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %ss", n, one)
}

// ApplyUpload stores files as blobs and returns the manifest with them added
// or replaced (a replaced file keeps its mode). m is not modified; blobs
// stored before a failure stay for GC to collect.
func ApplyUpload(m Manifest, b *Blobs, files []UploadFile, lim Limits) (Manifest, string, error) {
	paths := make([]string, len(files))
	for i, f := range files {
		clean, err := ValidatePath(f.Path)
		if err != nil {
			return nil, "", err
		}
		paths[i] = clean
	}
	next := m.clone()
	total := next.totalBytes()
	for i, f := range files {
		p := paths[i]
		old, replacing := next[p]
		if !replacing && next.pathConflict(p) {
			return nil, "", badPath(f.Path, "conflicts with an existing file or folder")
		}
		if replacing {
			total -= old.Size
		}
		limit := lim.MaxFileBytes
		contextBound := false
		if room := lim.MaxContextBytes - total; room < limit {
			limit, contextBound = max(room, 0), true
		}
		hash, size, err := b.Put(f.Content, limit)
		switch {
		case errors.Is(err, ErrFileTooLarge) && contextBound:
			return nil, "", fmt.Errorf("%w: %q", ErrContextTooLarge, p)
		case errors.Is(err, ErrFileTooLarge):
			return nil, "", fmt.Errorf("%w: %q", ErrFileTooLarge, p)
		case err != nil:
			return nil, "", fmt.Errorf("storing %q: %w", p, err)
		}
		mode := uint32(0o644)
		if replacing {
			mode = old.Mode
		}
		next[p] = FileEntry{Hash: hash, Size: size, Mode: mode}
		total += size
	}
	return next, "uploaded " + plural(len(files), "file"), nil
}

// subtree returns the manifest keys equal to p or under p/.
func (m Manifest) subtree(p string) []string {
	var keys []string
	prefix := p + "/"
	for k := range m {
		if k == p || strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// ApplyRename moves a file, or every file under a folder, to a new path.
func ApplyRename(m Manifest, from, to string) (Manifest, string, error) {
	src, err := ValidatePath(from)
	if err != nil {
		return nil, "", err
	}
	dst, err := ValidatePath(to)
	if err != nil {
		return nil, "", err
	}
	asDir := strings.HasSuffix(from, "/")
	var keys []string
	if _, isFile := m[src]; isFile && !asDir {
		keys = []string{src}
	} else if asDir || m.hasChildren(src) {
		keys = nil
		for _, k := range m.subtree(src) {
			if k != src {
				keys = append(keys, k)
			}
		}
	}
	if len(keys) == 0 {
		return nil, "", fmt.Errorf("%w: %q", fs.ErrNotExist, src)
	}
	if strings.HasPrefix(dst, src+"/") {
		return nil, "", badPath(to, "cannot move a folder into itself")
	}
	next := m.clone()
	for _, k := range keys {
		delete(next, k)
	}
	for _, k := range keys {
		target := dst + strings.TrimPrefix(k, src)
		if _, taken := next[target]; taken || next.pathConflict(target) {
			return nil, "", fmt.Errorf("%w: %q", fs.ErrExist, target)
		}
		next[target] = m[k]
	}
	return next, fmt.Sprintf("renamed %s → %s", src, dst), nil
}

// ApplyDelete removes a file, or every file under a folder.
func ApplyDelete(m Manifest, p string) (Manifest, string, error) {
	clean, err := ValidatePath(p)
	if err != nil {
		return nil, "", err
	}
	keys := m.subtree(clean)
	if len(keys) == 0 {
		return nil, "", fmt.Errorf("%w: %q", fs.ErrNotExist, clean)
	}
	next := m.clone()
	for _, k := range keys {
		delete(next, k)
	}
	return next, "deleted " + clean, nil
}

// ApplyDockerfile stores content as the file at p (normally "Dockerfile").
func ApplyDockerfile(m Manifest, b *Blobs, p string, content []byte) (Manifest, string, error) {
	clean, err := ValidatePath(p)
	if err != nil {
		return nil, "", err
	}
	switch {
	case len(bytes.TrimSpace(content)) == 0:
		return nil, "", fmt.Errorf("%w: Dockerfile is required", ErrBadDockerfile)
	case len(content) > maxDockerfileBytes:
		return nil, "", fmt.Errorf("%w: Dockerfile is larger than 1 MiB", ErrBadDockerfile)
	case bytes.IndexByte(content, 0) >= 0:
		return nil, "", fmt.Errorf("%w: Dockerfile must not contain NUL bytes", ErrBadDockerfile)
	}
	old, replacing := m[clean]
	if !replacing && m.pathConflict(clean) {
		return nil, "", badPath(p, "conflicts with an existing file or folder")
	}
	hash, size, err := b.Put(bytes.NewReader(content), maxDockerfileBytes)
	if err != nil {
		return nil, "", err
	}
	mode := uint32(0o644)
	if replacing {
		mode = old.Mode
	}
	next := m.clone()
	next[clean] = FileEntry{Hash: hash, Size: size, Mode: mode}
	return next, "edited " + clean, nil
}
