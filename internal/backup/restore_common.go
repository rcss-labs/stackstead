package backup

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/rcss-labs/stackstead/internal/engine"
)

// Restore code shared by the whole-host and the single-stack restore.

// rootSet opens host directories only through an os.Root of an allowed root
// (or of the data dir), so a symlink on the way can only resolve inside it.
type rootSet struct {
	allowed []string
	dataDir string
	open    map[string]*os.Root // root dir -> its os.Root
}

func newRootSet(allowed []string, dataDir string) *rootSet {
	return &rootSet{allowed: allowed, dataDir: dataDir, open: map[string]*os.Root{}}
}

// openRoot returns the os.Root of dir, opening (and creating) it once.
func (rs *rootSet) openRoot(dir string) (*os.Root, error) {
	if r, ok := rs.open[dir]; ok {
		return r, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	rs.open[dir] = r
	return r, nil
}

// allowedRootOf returns the first allowed root containing p and p relative to it.
func (rs *rootSet) allowedRootOf(p string) (root, rel string, ok bool) {
	for _, r := range rs.allowed {
		if rel, ok := relUnder(p, r); ok {
			return r, rel, true
		}
	}
	return "", "", false
}

// openUnder creates and opens dir through the os.Root of its allowed root.
// The caller closes the returned root.
func (rs *rootSet) openUnder(dir string) (*os.Root, error) {
	rootDir, rel, ok := rs.allowedRootOf(dir)
	if !ok {
		return nil, fmt.Errorf("%s is %s", dir, reasonOutsideRoots)
	}
	root, err := rs.openRoot(rootDir)
	if err != nil {
		return nil, err
	}
	if rel == "." {
		return root.OpenRoot(".")
	}
	if err := root.MkdirAll(rel, 0o755); err != nil {
		return nil, err
	}
	return root.OpenRoot(rel)
}

func (rs *rootSet) close() {
	for _, r := range rs.open {
		r.Close()
	}
}

// prepareBind makes sure a bind source is inside an allowed root, creates it
// through that root's os.Root when missing, and returns it with symlinks
// resolved (rechecked against the resolved roots). Docker runs as root, so a
// bind source is the one way a crafted archive could reach anywhere on the
// host; anything outside the roots, or covering more than the bind's own data
// (see bindCovers), is skipped with a reason.
func (rs *rootSet) prepareBind(source string, stackDirs []string) (resolved, skip string, err error) {
	if !filepath.IsAbs(source) || !underAnyRoot(source, rs.allowed) {
		return "", "bind source is " + reasonOutsideRoots, nil
	}
	if _, statErr := os.Lstat(source); errors.Is(statErr, fs.ErrNotExist) {
		rootDir, rel, _ := rs.allowedRootOf(source)
		root, err := rs.openRoot(rootDir)
		if err != nil {
			return "", "", err
		}
		if err := root.MkdirAll(rel, 0o755); err != nil {
			return "", "", fmt.Errorf("create bind source: %w", err)
		}
	}
	resolved, err = filepath.EvalSymlinks(source)
	if err != nil {
		return "", "", err
	}
	if !underAnyRoot(resolved, resolvedRoots(rs.allowed)) {
		return "", "bind source resolves to " + resolved + ", " + reasonOutsideRoots, nil
	}
	if reason := bindCovers(resolved, rs.allowed, stackDirs, rs.dataDir); reason != "" {
		return "", reason, nil
	}
	return resolved, "", nil
}

// bindCovers explains why emptying the resolved bind source would destroy
// more than that bind's data: it is an allowed root, holds a stack directory
// being restored, or holds the data dir. "" means it is safe.
func bindCovers(resolved string, allowed, stackDirs []string, dataDir string) string {
	for _, r := range append(slices.Clone(allowed), resolvedRoots(allowed)...) {
		if filepath.Clean(r) == resolved {
			return "bind source is an allowed root"
		}
	}
	for _, sd := range stackDirs {
		for _, dir := range withResolved(sd) {
			if underRoot(dir, resolved) {
				return "bind source contains the stack directory " + sd
			}
		}
	}
	for _, dir := range withResolved(dataDir) {
		if underRoot(dir, resolved) {
			return "bind source contains the Stackstead data directory"
		}
	}
	return ""
}

// withResolved returns p (made absolute) and, when it exists, p with
// symlinks resolved.
func withResolved(p string) []string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = filepath.Clean(p)
	}
	out := []string{abs}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil && resolved != abs {
		out = append(out, resolved)
	}
	return out
}

// verifyArchiveManifest checks the manifest found at the end of the archive:
// it must be readable by this version, and when a sidecar drove the restore
// it must describe the same archive.
func verifyArchiveManifest(inArchive, driving Manifest, fromSidecar bool, installed string) error {
	if err := checkManifest(inArchive, installed); err != nil {
		return err
	}
	if !fromSidecar {
		return nil
	}
	if !inArchive.CreatedAt.Equal(driving.CreatedAt) || inArchive.InstanceID != driving.InstanceID ||
		inArchive.StacksteadVersion != driving.StacksteadVersion || inArchive.Format != driving.Format {
		return fmt.Errorf("%w: archive created %s by instance %q with version %s (format %d), sidecar says %s by %q with version %s (format %d)",
			ErrManifestMismatch,
			inArchive.CreatedAt.UTC().Format(time.RFC3339), inArchive.InstanceID, inArchive.StacksteadVersion, inArchive.Format,
			driving.CreatedAt.UTC().Format(time.RFC3339), driving.InstanceID, driving.StacksteadVersion, driving.Format)
	}
	return nil
}

// trackedReader remembers a read error, so a damaged archive is told apart
// from a failure to write one file.
type trackedReader struct {
	r   io.Reader
	err error
}

func (t *trackedReader) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		t.err = err
	}
	return n, err
}

// extractEntry writes one archive entry to rel inside root. Only os.Root is
// used, so nothing can land outside it. A failure to write returns an item
// and a nil error; a failure to read the archive returns an error.
func extractEntry(root *os.Root, prefix, rel string, hdr *tar.Header, body io.Reader) (*ItemResult, error) {
	rel = path.Clean(rel)
	if rel == "." || rel == "" {
		return nil, nil
	}
	name := filepath.FromSlash(rel)
	perm := hdr.FileInfo().Mode().Perm()
	fail := func(err error) (*ItemResult, error) {
		return &ItemResult{Kind: "base", Name: hdr.Name, Status: itemFailed, Note: err.Error()}, nil
	}
	ensureParent := func() error {
		if parent := filepath.Dir(name); parent != "." {
			return root.MkdirAll(parent, 0o755)
		}
		return nil
	}
	// replace clears a non-directory already at name, so links are never followed.
	replace := func() error {
		info, err := root.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.IsDir() {
			return fmt.Errorf("%s is a directory", rel)
		}
		return root.Remove(name)
	}
	switch hdr.Typeflag {
	case tar.TypeDir:
		if err := root.MkdirAll(name, perm|0o700); err != nil {
			return fail(err)
		}
	case tar.TypeReg:
		if err := ensureParent(); err != nil {
			return fail(err)
		}
		if info, err := root.Lstat(name); err == nil && info.Mode()&fs.ModeSymlink != 0 {
			if err := root.Remove(name); err != nil {
				return fail(err)
			}
		}
		f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
		if err != nil {
			return fail(err)
		}
		src := &trackedReader{r: body}
		_, err = io.Copy(f, src)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if src.err != nil {
			return nil, damaged(fmt.Errorf("read %s: %w", hdr.Name, src.err))
		}
		if err == nil {
			err = root.Chmod(name, perm)
		}
		if err != nil {
			return fail(err)
		}
	case tar.TypeSymlink:
		if err := ensureParent(); err != nil {
			return fail(err)
		}
		if err := replace(); err != nil {
			return fail(err)
		}
		if err := root.Symlink(hdr.Linkname, name); err != nil {
			return fail(err)
		}
	case tar.TypeLink:
		target, ok := strings.CutPrefix(hdr.Linkname, prefix)
		if !ok {
			return fail(fmt.Errorf("hard link target %q is outside %s", hdr.Linkname, prefix))
		}
		if err := ensureParent(); err != nil {
			return fail(err)
		}
		if err := replace(); err != nil {
			return fail(err)
		}
		if err := root.Link(filepath.FromSlash(path.Clean(target)), name); err != nil {
			return fail(err)
		}
	default:
		return &ItemResult{Kind: "base", Name: hdr.Name, Status: itemSkipped, Note: "unsupported entry type"}, nil
	}
	return nil, nil
}

// drain consumes the rest of a TarStreamUnder stream. Its error can only come
// from reading the archive, so it is fatal.
func drain(stream io.Reader) error {
	if _, err := io.Copy(io.Discard, stream); err != nil {
		return damaged(err)
	}
	return nil
}

// createNetworkSpecs creates the networks of specs that do not exist yet and
// reports one item per spec. An existing network is kept as it is; a failure
// is reported and does not stop the others.
func createNetworkSpecs(ctx context.Context, eng Engine, specs []engine.NetworkSpec, out func(string)) []ItemResult {
	if len(specs) == 0 {
		return nil
	}
	var items []ItemResult
	nets, err := eng.Networks(ctx)
	if err != nil {
		for _, s := range specs {
			items = append(items, ItemResult{Kind: "network", Name: s.Name, Status: itemFailed, Note: "list networks: " + err.Error()})
		}
		return items
	}
	existing := map[string]bool{}
	for _, n := range nets {
		existing[n.Name] = true
	}
	for _, s := range specs {
		if existing[s.Name] {
			items = append(items, ItemResult{Kind: "network", Name: s.Name, Status: itemSkipped, Note: "already exists; kept as it is"})
			continue
		}
		if out != nil {
			out("creating network " + s.Name)
		}
		if _, err := eng.CreateNetwork(ctx, s); err != nil {
			items = append(items, ItemResult{Kind: "network", Name: s.Name, Status: itemFailed, Note: err.Error()})
			continue
		}
		items = append(items, ItemResult{Kind: "network", Name: s.Name, Status: itemOK})
	}
	return items
}

// composeVolumeLabels marks a recreated named volume as belonging to the
// stack's compose project, so compose adopts it without a warning.
func composeVolumeLabels(project, volume string) map[string]string {
	short, ok := strings.CutPrefix(volume, project+"_")
	if project == "" || !ok || short == "" {
		return nil
	}
	return map[string]string{"com.docker.compose.project": project, "com.docker.compose.volume": short}
}
