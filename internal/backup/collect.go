package backup

import (
	"archive/tar"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/stacks"
	"github.com/rcss-labs/stackstead/internal/store"
)

const (
	noteEscapingSymlink = "symlink points outside the stack directory"
	createdLabel        = "dev.stackstead.created"
)

// CollectStackFiles archives the stack's directory under base/stacks/<name>/,
// leaving out the skip paths (bind-mount sources, which are volume data).
// Sockets, FIFOs, devices and symlinks that leave the directory are not
// archived; each is returned as a skipped ItemResult.
func CollectStackFiles(w *Writer, e stacks.Entry, skip []string) ([]ItemResult, error) {
	src := filepath.Dir(e.ComposePath)
	if _, err := os.Stat(src); err != nil {
		return nil, err
	}
	items, err := collectTree(w, src, "base/stacks/"+e.Name, skip)
	for i := range items {
		items[i].Stack = e.Name
	}
	return items, err
}

// CollectDir archives src under prefix with the same rules as CollectStackFiles.
// A missing src is not an error.
func CollectDir(w *Writer, src, prefix string) ([]ItemResult, error) {
	if _, err := os.Stat(src); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return collectTree(w, src, strings.TrimSuffix(prefix, "/"), nil)
}

func collectTree(w *Writer, root, prefix string, skip []string) ([]ItemResult, error) {
	var items []ItemResult
	skipped := map[string]bool{}
	for _, s := range skip {
		skipped[filepath.Clean(s)] = true
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if skipped[p] {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		name := prefix + "/"
		if rel != "." {
			name = prefix + "/" + filepath.ToSlash(rel)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch mode := info.Mode(); {
		case mode.IsDir():
			hdr, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			return w.AddFile(name+dirSuffix(name), hdr, nil)
		case mode&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			if !linkStaysInside(rel, target) {
				items = append(items, ItemResult{Kind: "base", Name: name, Status: "skipped", Note: noteEscapingSymlink})
				return nil
			}
			hdr, err := tar.FileInfoHeader(info, target)
			if err != nil {
				return err
			}
			return w.AddFile(name, hdr, nil)
		case mode.IsRegular():
			f, err := os.Open(p)
			if err != nil {
				items = append(items, ItemResult{Kind: "base", Name: name, Status: "failed", Note: err.Error()})
				return nil
			}
			defer f.Close()
			hdr, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			return w.AddFile(name, hdr, f)
		default:
			items = append(items, ItemResult{Kind: "base", Name: name, Status: "skipped", Note: "special file (socket, pipe or device) cannot be archived"})
			return nil
		}
	})
	return items, err
}

func dirSuffix(name string) string {
	if strings.HasSuffix(name, "/") {
		return ""
	}
	return "/"
}

// linkStaysInside reports whether a symlink at rel (relative to the tree root)
// with the given target resolves to a path inside the root.
func linkStaysInside(rel, target string) bool {
	if path.IsAbs(filepath.ToSlash(target)) || filepath.IsAbs(target) {
		return false
	}
	resolved := path.Join(path.Dir(filepath.ToSlash(rel)), filepath.ToSlash(target))
	return resolved != ".." && !strings.HasPrefix(resolved, "../")
}

// CollectNetworks returns the JSON of the networks Stackstead created, as
// []engine.NetworkSpec, and how many there are.
func CollectNetworks(ctx context.Context, eng Engine) ([]byte, int, error) {
	nets, err := eng.Networks(ctx)
	if err != nil {
		return nil, 0, err
	}
	specs := []engine.NetworkSpec{}
	for _, n := range nets {
		if n.Labels[createdLabel] != "true" {
			continue
		}
		spec := engine.NetworkSpec{Name: n.Name, Driver: n.Driver, Internal: n.Internal, Parent: n.Parent(), IPvlanMode: n.IPvlanMode()}
		if len(n.Subnets) > 0 {
			sn := n.Subnets[0]
			spec.Subnet = sn.Prefix.String()
			if sn.Gateway.IsValid() {
				spec.Gateway = sn.Gateway.String()
			}
			if sn.IPRange.IsValid() {
				spec.IPRange = sn.IPRange.String()
			}
		}
		specs = append(specs, spec)
	}
	data, err := json.Marshal(specs)
	if err != nil {
		return nil, 0, err
	}
	return data, len(specs), nil
}

// CollectDB writes a consistent snapshot of the database into a fresh
// directory under tmpDir and returns the snapshot path. cleanup removes it.
func CollectDB(ctx context.Context, db *sql.DB, tmpDir string) (string, func(), error) {
	dir, err := os.MkdirTemp(tmpDir, "stackstead-db-")
	if err != nil {
		return "", nil, fmt.Errorf("snapshot dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	dest := filepath.Join(dir, "stackstead.db")
	if err := store.Backup(ctx, db, dest); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("snapshot database: %w", err)
	}
	return dest, cleanup, nil
}
