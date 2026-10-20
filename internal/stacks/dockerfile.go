package stacks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/rcss-labs/stackstead/internal/composefile"
	"github.com/rcss-labs/stackstead/internal/fsguard"
)

// MaxDockerfileBytes is the largest Dockerfile Stackstead reads or writes.
const MaxDockerfileBytes = 1 << 20

var (
	ErrOutsideStack       = errors.New("this Dockerfile is outside the stack's folder and can't be edited here")
	ErrNoBuild            = errors.New("service does not build an image")
	ErrDockerfileConflict = errors.New("Dockerfile changed on disk since it was loaded")
)

const (
	reasonOutsideStack = "outside the stack folder"
	reasonOutsideRoots = "outside the allowed directories"
)

// NotEditableError means the service's Dockerfile cannot be located or edited
// from the compose file alone.
type NotEditableError struct{ Reason string }

func (e *NotEditableError) Error() string { return "Dockerfile can't be edited here: " + e.Reason }

// ValidateDockerfile checks a Dockerfile's size and bytes; problems are
// reported on field.
func ValidateDockerfile(field string, content []byte) error {
	msg := ""
	switch {
	case len(content) > MaxDockerfileBytes:
		msg = "Dockerfile is larger than 1 MiB"
	case bytes.IndexByte(content, 0) >= 0:
		msg = "Dockerfile contains a NUL byte"
	}
	if msg == "" {
		return nil
	}
	return &ValidationError{Fields: []FieldError{{Field: field, Message: msg}}}
}

// DockerfileView is a service's Dockerfile as read from disk. Reason says why
// it is not editable.
type DockerfileView struct {
	Content  []byte
	Hash     string
	Exists   bool
	Editable bool
	Path     string
	Reason   string
}

// hasVariable reports whether s references a compose variable (${X} or $X).
func hasVariable(s string) bool {
	if strings.Contains(s, "${") {
		return true
	}
	for i := 0; i+1 < len(s); i++ {
		if s[i] != '$' {
			continue
		}
		c := s[i+1]
		if c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			return true
		}
	}
	return false
}

// isRemoteContext reports whether a build context is a URL or git reference
// rather than a directory.
func isRemoteContext(c string) bool {
	return strings.Contains(c, "://") || strings.HasPrefix(c, "git@") ||
		strings.HasPrefix(c, "github.com/") || strings.Contains(c, ".git#")
}

// dockerfileTarget finds the Dockerfile a service builds from. path is the
// cleaned absolute path as the compose file names it; inside reports whether,
// with symlinks resolved, it lies within the stack folder.
func dockerfileTarget(stackDir string, f *composefile.File, service string) (path string, inside bool, err error) {
	ref, ok, err := f.BuildOf(service)
	if err != nil {
		return "", false, err
	}
	switch {
	case f.UsesInclude():
		return "", false, &NotEditableError{Reason: "uses include"}
	case f.UsesExtends(service):
		return "", false, &NotEditableError{Reason: "uses extends"}
	case !ok:
		return "", false, ErrNoBuild
	case ref.Inline:
		return "", false, &NotEditableError{Reason: "inline Dockerfile"}
	case isRemoteContext(ref.Context):
		return "", false, &NotEditableError{Reason: "remote build context"}
	case hasVariable(ref.Context) || hasVariable(ref.Dockerfile):
		return "", false, &NotEditableError{Reason: "uses variables"}
	}
	buildContext := ref.Context
	if buildContext == "" {
		buildContext = "."
	}
	if !filepath.IsAbs(buildContext) {
		buildContext = filepath.Join(stackDir, buildContext)
	}
	dockerfile := ref.Dockerfile
	if dockerfile == "" {
		dockerfile = "Dockerfile"
	}
	if filepath.IsAbs(dockerfile) {
		path = filepath.Clean(dockerfile)
	} else {
		path = filepath.Join(buildContext, dockerfile)
	}
	resolved, err := resolveExisting(path)
	if err != nil {
		return path, false, nil // unresolvable (dangling link, loop): fail closed
	}
	return path, withinDir(stackDir, resolved), nil
}

// withinDir reports whether resolved lies in the (symlink-resolved) dir.
func withinDir(dir, resolved string) bool {
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	return resolved == realDir || strings.HasPrefix(resolved, realDir+string(filepath.Separator))
}

// resolveExisting resolves symlinks in the longest existing prefix of the
// absolute, cleaned path p and appends the part that does not exist yet.
func resolveExisting(p string) (string, error) {
	for cur := p; ; cur = filepath.Dir(cur) {
		_, err := os.Lstat(cur)
		switch {
		case err == nil:
			real, err := filepath.EvalSymlinks(cur)
			if err != nil {
				return "", err
			}
			rest, _ := filepath.Rel(cur, p)
			return filepath.Join(real, rest), nil
		case !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR):
			return "", err
		case cur == filepath.Dir(cur):
			return "", err
		}
	}
}

// readRegular reads the file at path without following a final symlink.
// regular is false when the path is something other than a regular file.
func readRegular(path string, limit int64) (data []byte, mode fs.FileMode, exists, regular bool, err error) {
	// O_NONBLOCK keeps a FIFO from blocking the open; it is a no-op for files.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, 0, false, true, nil
	case errors.Is(err, syscall.ELOOP):
		return nil, 0, true, false, nil
	case err != nil:
		return nil, 0, false, false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, 0, false, false, err
	}
	if !info.Mode().IsRegular() || (limit > 0 && info.Size() > limit) {
		return nil, 0, true, false, nil
	}
	data, err = io.ReadAll(f)
	return data, info.Mode().Perm(), true, true, err
}

// writeDockerfile writes content to path, which must lie inside stackDir once
// symlinks are resolved. It creates missing folders (0755), refuses a final
// symlink, and checks expectedHash unless it is nil ("" means the file must
// not exist). The new file is 0644; an existing file keeps its mode. rollback
// undoes the write and removes the folders this call created.
func writeDockerfile(stackDir, path string, content []byte, expectedHash *string) (hash string, rollback func(), err error) {
	noop := func() {}
	parent := filepath.Dir(path)
	resolvedParent, err := resolveExisting(parent)
	if err != nil || !withinDir(stackDir, resolvedParent) {
		return "", noop, ErrOutsideStack
	}

	var created []string
	undoDirs := func() {
		for i := len(created) - 1; i >= 0; i-- {
			os.Remove(created[i])
		}
	}
	// Create missing folders one level at a time so each is recorded.
	var missing []string
	for dir := parent; ; dir = filepath.Dir(dir) {
		if _, err := os.Lstat(dir); err == nil || dir == filepath.Dir(dir) {
			break
		}
		missing = append(missing, dir)
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := os.Mkdir(missing[i], 0o755); err != nil {
			undoDirs()
			return "", noop, err
		}
		created = append(created, missing[i])
		if err := os.Chmod(missing[i], 0o755); err != nil {
			undoDirs()
			return "", noop, err
		}
	}
	// Re-resolve now that the folders exist: this is the folder we write into.
	resolvedParent, err = filepath.EvalSymlinks(parent)
	if err != nil || !withinDir(stackDir, resolvedParent) {
		undoDirs()
		return "", noop, ErrOutsideStack
	}
	target := filepath.Join(resolvedParent, filepath.Base(path))

	old, mode, exists, regular, err := readRegular(target, 0)
	switch {
	case err != nil:
		undoDirs()
		return "", noop, err
	case !regular:
		undoDirs()
		if info, lerr := os.Lstat(target); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", noop, ErrOutsideStack // never write through a link
		}
		return "", noop, &NotEditableError{Reason: "not a regular file"}
	}
	if expectedHash != nil {
		if exists != (*expectedHash != "") || (exists && composefile.Hash(old) != *expectedHash) {
			undoDirs()
			return "", noop, ErrDockerfileConflict
		}
	}
	if !exists {
		mode = 0o644
	}
	if err := composefile.WriteAtomic(target, content, mode); err != nil {
		undoDirs()
		return "", noop, fmt.Errorf("write Dockerfile: %w", err)
	}
	rollback = func() {
		if exists {
			_ = composefile.WriteAtomic(target, old, mode)
			return
		}
		os.Remove(target)
		undoDirs()
	}
	return composefile.Hash(content), rollback, nil
}

// Dockerfile reads the Dockerfile of a service's build.
func (s *Service) Dockerfile(ctx context.Context, stack, service string) (DockerfileView, error) {
	e, err := s.reg.Get(ctx, stack)
	if err != nil {
		return DockerfileView{}, err
	}
	_, _, f, err := readFile(e)
	if err != nil {
		return DockerfileView{}, err
	}
	stackDir := filepath.Dir(e.ComposePath)
	path, inside, err := dockerfileTarget(stackDir, f, service)
	var notEditable *NotEditableError
	if errors.As(err, &notEditable) {
		return DockerfileView{Reason: notEditable.Reason}, nil
	}
	if err != nil {
		return DockerfileView{}, err
	}
	if inside {
		return s.insideView(stackDir, path)
	}
	return s.outsideView(path)
}

func (s *Service) insideView(stackDir, path string) (DockerfileView, error) {
	rel, err := filepath.Rel(stackDir, path)
	if err != nil {
		return DockerfileView{}, err
	}
	view := DockerfileView{Path: filepath.ToSlash(rel), Editable: true}
	resolvedParent, err := resolveExisting(filepath.Dir(path))
	if err != nil {
		return DockerfileView{}, err
	}
	// Read the file named by path, not what a final link points to.
	data, _, exists, regular, err := readRegular(filepath.Join(resolvedParent, filepath.Base(path)), 0)
	switch {
	case err != nil:
		return DockerfileView{}, err
	case !regular:
		return DockerfileView{Path: view.Path, Reason: reasonOutsideStack}, nil
	case exists:
		view.Content, view.Exists, view.Hash = data, true, composefile.Hash(data)
	}
	return view, nil
}

// outsideView shows a Dockerfile outside the stack folder read-only, and only
// when it lies within the allowed roots.
func (s *Service) outsideView(path string) (DockerfileView, error) {
	view := DockerfileView{Path: path, Reason: reasonOutsideStack}
	resolved, err := fsguard.Resolve(s.roots, path)
	switch {
	case errors.Is(err, fsguard.ErrOutsideRoots):
		view.Reason = reasonOutsideRoots
		return view, nil
	case errors.Is(err, fs.ErrNotExist):
		return view, nil
	case err != nil:
		view.Reason = reasonOutsideRoots
		return view, nil
	}
	data, _, exists, regular, err := readRegular(resolved, MaxDockerfileBytes)
	if err != nil {
		view.Reason = reasonOutsideRoots
		return view, nil
	}
	if exists && regular {
		view.Content, view.Exists, view.Hash = data, true, composefile.Hash(data)
	}
	return view, nil
}

// SaveDockerfile writes a service's Dockerfile inside the stack folder. hash
// is the Hash of the version the caller loaded, or "" for a new file.
func (s *Service) SaveDockerfile(ctx context.Context, stack, service string, content []byte, hash string) (string, error) {
	if err := ValidateDockerfile("content", content); err != nil {
		return "", err
	}
	e, unlock, err := s.lockedEntry(ctx, stack)
	if err != nil {
		return "", err
	}
	defer unlock()
	_, _, f, err := readFile(e)
	if err != nil {
		return "", err
	}
	stackDir := filepath.Dir(e.ComposePath)
	path, inside, err := dockerfileTarget(stackDir, f, service)
	if err != nil {
		return "", err
	}
	if !inside {
		return "", ErrOutsideStack
	}
	newHash, _, err := writeDockerfile(stackDir, path, content, &hash)
	return newHash, err
}
