package stacks

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
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
	case len(bytes.TrimSpace(content)) == 0:
		msg = "Dockerfile is required"
	case len(content) > MaxDockerfileBytes:
		msg = "Dockerfile is larger than 1 MiB"
	case bytes.IndexByte(content, 0) >= 0:
		msg = "Dockerfile must not contain NUL bytes"
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
	// Include is checked before "no build" on purpose: an included file may
	// change what this service builds, so "no build" would be a guess.
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

// fileState says what a path held when it was read.
type fileState int

const (
	fileMissing fileState = iota
	fileOK
	fileNotRegular // directory, FIFO, link, or a file where a folder should be
	fileTooLarge   // more than MaxDockerfileBytes
)

const (
	reasonNotRegular = "not a regular file"
	reasonTooLarge   = "larger than 1 MiB"
)

// readLimited reads an open file, enforcing MaxDockerfileBytes while reading
// so an oversized file is never held in memory.
func readLimited(f *os.File) ([]byte, fs.FileMode, fileState, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, 0, fileMissing, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, fileNotRegular, nil
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxDockerfileBytes+1))
	if err != nil {
		return nil, 0, fileMissing, err
	}
	if len(data) > MaxDockerfileBytes {
		return nil, 0, fileTooLarge, nil
	}
	return data, info.Mode().Perm(), fileOK, nil
}

// classifyOpenError maps an open failure to a state; ok is false when the
// error is not one of the expected path conditions.
func classifyOpenError(err error) (state fileState, ok bool) {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fileMissing, true
	case errors.Is(err, syscall.ELOOP), errors.Is(err, syscall.ENOTDIR):
		return fileNotRegular, true
	}
	return fileMissing, false
}

// openFlags never follows a final symlink and never blocks on a FIFO.
const openFlags = os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK

// readRegular reads the file at path without following a final symlink.
func readRegular(path string) ([]byte, fs.FileMode, fileState, error) {
	f, err := os.OpenFile(path, openFlags, 0)
	if err != nil {
		if state, ok := classifyOpenError(err); ok {
			return nil, 0, state, nil
		}
		return nil, 0, fileMissing, err
	}
	defer f.Close()
	return readLimited(f)
}

// isEscape reports whether err is an os.Root refusal to leave its folder.
func isEscape(err error) bool {
	return err != nil && strings.Contains(err.Error(), "path escapes")
}

// writeDockerfile writes content to path, which must lie inside stackDir once
// symlinks are resolved. See writeDockerfileInRoot for the write itself.
func writeDockerfile(stackDir, path string, content []byte, expectedHash *string) (hash string, rollback func() error, err error) {
	noop := func() error { return nil }
	realStack, err := filepath.EvalSymlinks(stackDir)
	if err != nil {
		return "", noop, ErrOutsideStack
	}
	resolvedParent, err := resolveExisting(filepath.Dir(path))
	if err != nil || !withinDir(realStack, resolvedParent) {
		return "", noop, ErrOutsideStack
	}
	rel, err := filepath.Rel(realStack, filepath.Join(resolvedParent, filepath.Base(path)))
	if err != nil {
		return "", noop, ErrOutsideStack
	}
	// Every later step goes through the opened folder, so a link swapped in
	// after the checks above cannot lead the write out of it.
	root, err := os.OpenRoot(realStack)
	if err != nil {
		return "", noop, err
	}
	defer root.Close()
	hash, undo, err := writeIn(root, rel, content, expectedHash)
	if err != nil {
		return "", noop, err
	}
	// The caller may roll back long after this function returned, so the
	// rollback opens the folder itself instead of holding a descriptor.
	return hash, func() error {
		r, err := os.OpenRoot(realStack)
		if err != nil {
			return err
		}
		defer r.Close()
		return undo(r)
	}, nil
}

// writeDockerfileInRoot writes the Dockerfile at rel (relative to the root's
// folder): missing folders are created (0777), a final symlink is refused,
// expectedHash is checked unless nil ("" means the file must not exist), and
// the content goes to a temp file renamed into place. The new file is 0666;
// the process umask narrows new folders and files, and an existing file keeps
// its mode. rollback undoes the write and removes the folders this call
// created.
func writeDockerfileInRoot(root *os.Root, rel string, content []byte, expectedHash *string) (hash string, rollback func() error, err error) {
	hash, undo, err := writeIn(root, rel, content, expectedHash)
	if err != nil {
		return "", func() error { return nil }, err
	}
	return hash, func() error { return undo(root) }, nil
}

// writeIn does the write; undo takes the root to act through.
func writeIn(root *os.Root, rel string, content []byte, expectedHash *string) (hash string, undo func(*os.Root) error, err error) {
	noop := func(*os.Root) error { return nil }
	var created []string
	removeDirs := func(root *os.Root) error {
		var errs []error
		for i := len(created) - 1; i >= 0; i-- {
			errs = append(errs, root.Remove(created[i]))
		}
		return errors.Join(errs...)
	}
	undoDirs := func() { _ = removeDirs(root) }
	fail := func(err error) (string, func(*os.Root) error, error) {
		undoDirs()
		switch {
		case isEscape(err):
			return "", noop, ErrOutsideStack
		case errors.Is(err, syscall.ENOTDIR):
			return "", noop, &NotEditableError{Reason: reasonNotRegular}
		}
		var pathErr *fs.PathError
		if errors.As(err, &pathErr) { // keep absolute-looking detail out of the message
			err = pathErr.Err
		}
		return "", noop, fmt.Errorf("write Dockerfile: %w", err)
	}

	// Create missing folders one level at a time so each is recorded.
	parts := strings.Split(filepath.ToSlash(filepath.Dir(rel)), "/")
	dir := ""
	for _, part := range parts {
		if part == "." || part == "" {
			continue
		}
		dir = filepath.Join(dir, part)
		_, err := root.Lstat(dir)
		if err == nil {
			continue
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return fail(err)
		}
		if err := root.Mkdir(dir, 0o777); err != nil { // the umask narrows it
			return fail(err)
		}
		created = append(created, dir)
	}

	var old []byte
	mode := fs.FileMode(0o666) // new file: the umask narrows it
	exists := false
	info, err := root.Lstat(rel)
	switch {
	case err == nil:
		if info.Mode()&fs.ModeSymlink != 0 {
			undoDirs()
			return "", noop, ErrOutsideStack // never write through a link
		}
		f, err := root.OpenFile(rel, openFlags, 0)
		if err != nil {
			return fail(err)
		}
		data, perm, state, err := readLimited(f)
		f.Close()
		switch {
		case err != nil:
			return fail(err)
		case state == fileNotRegular:
			undoDirs()
			return "", noop, &NotEditableError{Reason: reasonNotRegular}
		case state == fileTooLarge:
			undoDirs()
			return "", noop, &NotEditableError{Reason: reasonTooLarge}
		}
		old, mode, exists = data, perm, true
	case !errors.Is(err, fs.ErrNotExist):
		return fail(err)
	}
	if expectedHash != nil {
		if exists != (*expectedHash != "") || (exists && composefile.Hash(old) != *expectedHash) {
			undoDirs()
			return "", noop, ErrDockerfileConflict
		}
	}
	if err := writeAtomicIn(root, rel, content, mode, exists); err != nil {
		return fail(err)
	}
	undo = func(root *os.Root) error {
		if exists {
			return writeAtomicIn(root, rel, old, mode, true)
		}
		return errors.Join(root.Remove(rel), removeDirs(root))
	}
	return composefile.Hash(content), undo, nil
}

// writeAtomicIn writes rel through a temp file in the same folder and renames
// it into place, all inside root. With exact, the file gets mode whatever the
// umask; otherwise mode is the creation mode and the umask narrows it.
func writeAtomicIn(root *os.Root, rel string, data []byte, mode fs.FileMode, exact bool) error {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(rel), "."+filepath.Base(rel)+"."+hex.EncodeToString(suffix[:])+".tmp")
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		if exact {
			err = f.Chmod(mode)
		}
		if err == nil {
			err = f.Sync()
		}
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = root.Rename(tmp, rel)
	}
	if err != nil {
		_ = root.Remove(tmp)
		return err
	}
	if d, derr := root.Open(filepath.Dir(rel)); derr == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
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
	realStack, err := filepath.EvalSymlinks(stackDir)
	if err != nil {
		return DockerfileView{}, err
	}
	resolvedParent, err := resolveExisting(filepath.Dir(path))
	if err != nil {
		return DockerfileView{}, err
	}
	// Show the path relative to the resolved stack folder, whichever way the
	// compose file spelled it.
	resolved := filepath.Join(resolvedParent, filepath.Base(path))
	rel, err := filepath.Rel(realStack, resolved)
	if err != nil {
		return DockerfileView{}, err
	}
	view := DockerfileView{Path: filepath.ToSlash(rel), Editable: true}
	// Read the file named by path, not what a final link points to.
	data, _, state, err := readRegular(resolved)
	switch {
	case err != nil:
		return DockerfileView{}, err
	case state == fileNotRegular:
		return DockerfileView{Path: view.Path, Reason: reasonNotRegular}, nil
	case state == fileTooLarge:
		return DockerfileView{Path: view.Path, Reason: reasonTooLarge}, nil
	case state == fileOK:
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
	case errors.Is(err, fs.ErrNotExist):
		return view, nil
	case err != nil:
		view.Reason = reasonOutsideRoots
		return view, nil
	}
	data, _, state, err := readRegular(resolved)
	switch {
	case err != nil, state == fileNotRegular, state == fileTooLarge:
		view.Reason = reasonOutsideRoots
	case state == fileOK:
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
