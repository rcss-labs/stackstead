// Package fsguard confines file browsing to a set of allowed directories.
// Every path is symlink-resolved before it is compared with the (also
// resolved) roots, so links and ".." cannot lead out of them.
package fsguard

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"strings"
)

// ErrOutsideRoots means the path is not inside any allowed directory.
var ErrOutsideRoots = errors.New("path is outside the allowed directories")

// Entry is one item of a directory listing.
type Entry struct {
	Name  string
	IsDir bool
	Size  int64
}

// Resolve cleans p, requires it to be absolute, resolves symlinks and returns
// the resolved path if it lies within (or equals) one of roots, which are
// resolved too.
//
// When resolution fails, the error reveals nothing about paths outside the
// roots. If the failure is a *fs.PathError or a bare errno (not found, not a
// directory, permission denied, ...) and the nearest existing ancestor of the
// failing path is outside every root, the result is ErrOutsideRoots; if
// inside, the underlying error kind is returned naming only p. Any other failure, notably symlink
// loops, cannot be attributed to a component and fails closed as
// ErrOutsideRoots, even when the loop lies inside a root.
func Resolve(roots []string, p string) (string, error) {
	return resolveWith(resolveRoots(roots), p)
}

// resolveRoots resolves the roots once; unresolvable or relative roots are
// dropped, so a bad root fails closed.
func resolveRoots(roots []string) []string {
	var resolved []string
	for _, root := range roots {
		if !filepath.IsAbs(root) {
			continue
		}
		if r, err := filepath.EvalSymlinks(root); err == nil {
			resolved = append(resolved, r)
		}
	}
	return resolved
}

func resolveWith(resolvedRoots []string, p string) (string, error) {
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("path %q is not absolute", p)
	}
	p = filepath.Clean(p)

	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		failed := p
		var pathErr *fs.PathError
		var errno syscall.Errno
		switch {
		case errors.As(err, &pathErr):
			failed, err = pathErr.Path, pathErr.Err
		case errors.As(err, &errno):
			// ENOTDIR comes back bare; judge it by the resolved parent of p.
			err = errno
		default:
			// Loops ("too many links") and unknown errors do not say which
			// component failed: fail closed.
			return "", ErrOutsideRoots
		}
		if !existingAncestorWithin(resolvedRoots, failed) {
			return "", ErrOutsideRoots
		}
		return "", &fs.PathError{Op: "resolve", Path: p, Err: err}
	}
	if !withinAny(resolvedRoots, resolved) {
		return "", ErrOutsideRoots
	}
	return resolved, nil
}

// List returns the entries of dir (after Resolve): directories first, then
// files, each group sorted by name. Symlinks that resolve outside roots, and
// broken ones, are omitted; a symlink to a directory is listed as a directory.
func List(roots []string, dir string) ([]Entry, error) {
	resolvedRoots := resolveRoots(roots)
	resolved, err := resolveWith(resolvedRoots, dir)
	if err != nil {
		return nil, err
	}
	items, err := os.ReadDir(resolved)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(items))
	for _, item := range items {
		var info fs.FileInfo
		if item.Type()&os.ModeSymlink != 0 {
			full := filepath.Join(resolved, item.Name())
			target, err := resolveWith(resolvedRoots, full)
			if err != nil {
				continue
			}
			if info, err = os.Stat(target); err != nil {
				continue
			}
		} else if info, err = item.Info(); err != nil { // Lstat: never follows a swapped-in link
			continue
		}
		e := Entry{Name: item.Name(), IsDir: info.IsDir()}
		if !e.IsDir {
			e.Size = info.Size()
		}
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return entries[i].Name < entries[j].Name
	})
	return entries, nil
}

// existingAncestorWithin resolves the nearest existing ancestor of p and
// reports whether it lies within one of the resolved roots.
func existingAncestorWithin(resolvedRoots []string, p string) bool {
	for dir := filepath.Dir(p); ; dir = filepath.Dir(dir) {
		if r, err := filepath.EvalSymlinks(dir); err == nil {
			return withinAny(resolvedRoots, r)
		}
		if dir == filepath.Dir(dir) {
			return false
		}
	}
}

func withinAny(roots []string, p string) bool {
	for _, root := range roots {
		if within(root, p) {
			return true
		}
	}
	return false
}

// within reports whether p equals root or lies below it, comparing whole
// path elements so that /srv/stacks2 is not inside /srv/stacks.
func within(root, p string) bool {
	if p == root {
		return true
	}
	prefix := root
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(p, prefix)
}
