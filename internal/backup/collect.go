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
	noteEscapingSymlink = "symlink points outside the archived directory"
	createdLabel        = "dev.stackstead.created"
)

// CollectStackFiles archives the stack's directory under base/stacks/<name>/,
// leaving out the skip paths (directory bind-mount sources, which are volume
// data; a skip path that is a file is kept, see isFileBind).
// Sockets, FIFOs, devices and symlinks that leave the directory are not
// archived; each is returned as a skipped ItemResult.
func CollectStackFiles(w *Writer, e stacks.Entry, skip []string) ([]ItemResult, error) {
	src := filepath.Dir(e.ComposePath)
	if _, err := os.Stat(src); err != nil {
		return nil, err
	}
	items, err := collectTree(w, src, "base/stacks/"+e.Name, effectiveSkips(e, src, skip))
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
			if p == root {
				return walkErr
			}
			items = append(items, ItemResult{Kind: "base", Name: prefix + "/" + filepath.ToSlash(strings.TrimPrefix(p, root+string(filepath.Separator))), Status: "failed", Note: walkErr.Error()})
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
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

// effectiveSkips drops skip entries that would lose the stack's definition:
// the stack directory itself or an ancestor (a `.:/app` bind), the compose
// files and .env, and any single file (a file bind is stack files, R35).
func effectiveSkips(e stacks.Entry, root string, skip []string) []string {
	protected := map[string]bool{filepath.Clean(e.ComposePath): true, filepath.Join(root, ".env"): true}
	for _, pattern := range []string{"compose*.y*ml", "docker-compose*.y*ml"} {
		matches, _ := filepath.Glob(filepath.Join(root, pattern))
		for _, m := range matches {
			protected[m] = true
		}
	}
	root = filepath.Clean(root)
	var out []string
	for _, s := range skip {
		s = filepath.Clean(s)
		if s == root || strings.HasPrefix(root, s+string(filepath.Separator)) || protected[s] || isFile(s) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// isFile reports whether p exists on this host and is not a directory.
func isFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// noteFileBindOutside is the skipped-item note of a file bind that is neither
// volume data nor inside the stack directory.
const noteFileBindOutside = "file bind outside the stack directory is not backed up"

// splitMounts sorts a stack's mounts per R35. Volume data is named volumes
// and directory binds (a missing bind source counts as a directory: Docker
// creates one). A bind of a single file is not volume data: inside the stack
// directory it is archived with the stack files, outside it it is returned in
// outsideFiles and not backed up.
func splitMounts(e stacks.Entry, mounts []stacks.Mount) (volumeData, outsideFiles []stacks.Mount) {
	stackDir := filepath.Dir(e.ComposePath)
	for _, m := range mounts {
		switch {
		case m.Kind != "bind" || !isFile(m.Source):
			volumeData = append(volumeData, m)
		case !underRoot(m.Source, stackDir):
			outsideFiles = append(outsideFiles, m)
		}
	}
	return volumeData, outsideFiles
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
