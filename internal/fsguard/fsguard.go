// Package fsguard confines file browsing to a set of allowed directories.
// Every path is symlink-resolved before it is compared with the (also
// resolved) roots, so links and ".." cannot lead out of them.
package fsguard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
// resolved too. A path that does not exist yields an error wrapping
// fs.ErrNotExist, unless its nearest existing ancestor (resolved) is outside
// every root: then it is ErrOutsideRoots, so existence outside the roots is
// not revealed.
func Resolve(roots []string, p string) (string, error) {
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("path %q is not absolute", p)
	}
	p = filepath.Clean(p)

	var resolvedRoots []string
	for _, root := range roots {
		if !filepath.IsAbs(root) {
			continue
		}
		if r, err := filepath.EvalSymlinks(root); err == nil {
			resolvedRoots = append(resolvedRoots, r)
		}
	}

	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !existingAncestorWithin(resolvedRoots, p) {
			return "", ErrOutsideRoots
		}
		return "", err
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
	resolved, err := Resolve(roots, dir)
	if err != nil {
		return nil, err
	}
	items, err := os.ReadDir(resolved)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(items))
	for _, item := range items {
		full := filepath.Join(resolved, item.Name())
		if item.Type()&os.ModeSymlink != 0 {
			if _, err := Resolve(roots, full); err != nil {
				continue
			}
		}
		info, err := os.Stat(full) // follows links; the target is confined above
		if err != nil {
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
