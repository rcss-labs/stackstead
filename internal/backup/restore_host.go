package backup

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/stacks"
	"github.com/rcss-labs/stackstead/internal/store"
)

var (
	// ErrArchiveNewer refuses an archive written by a newer Stackstead.
	ErrArchiveNewer = errors.New("backup: archive is from a newer Stackstead version")
	// ErrArchiveDamaged reports an archive that is truncated or malformed.
	ErrArchiveDamaged = errors.New("backup: archive is damaged or incomplete")
	// ErrRestorePending refuses a restore while an earlier one awaits a restart.
	ErrRestorePending = errors.New("backup: a host restore is already pending; restart Stackstead to finish it")
)

// Archive entries a host restore treats specially.
const (
	entryDB       = "base/stackstead.db"
	entryConfig   = "base/config.yaml"
	entryNetworks = "base/networks.json"
	stacksPrefix  = "base/stacks/"
	libraryPrefix = "base/images-library/"
	volumesPrefix = "volumes/"
	imagesPrefix  = "images/"

	// imagesLibraryDir is where the image library lives under the data dir.
	imagesLibraryDir = "images"
	maxSmallJSON     = 4 << 20
)

// Host source kinds.
const (
	SourceS3     = "s3"
	SourcePath   = "path"
	SourceUpload = "upload"
)

// HostSource is where a whole-host archive comes from. For S3, Key is the full
// object key in S3.Bucket (S3.Prefix is not applied). For "path" and "upload",
// Path is the archive file on this machine.
type HostSource struct {
	Kind string
	Path string
	S3   S3Dest
	Key  string
}

// HostEnv is what a host restore works against.
type HostEnv struct {
	Engine       Engine
	DataDir      string
	AllowedRoots []string
	Version      string
	Out          func(string)
	// HTTP is used for S3 sources; nil means http.DefaultClient.
	HTTP *http.Client
}

// Conflict is existing Docker or filesystem state a host restore replaces.
type Conflict struct {
	// Kind is "volume", "network", "container" or "stack-dir".
	Kind string `json:"kind"`
	Name string `json:"name"`
	// Action is "overwrite" or "recreate".
	Action string `json:"action"`
}

// HostSummary is what InspectHost shows before a host restore.
type HostSummary struct {
	Manifest  Manifest   `json:"manifest"`
	Conflicts []Conflict `json:"conflicts"`
}

// HostReport lists the outcome of every restored item.
type HostReport struct {
	Items []ItemResult `json:"items"`
}

// OpenHostSource opens the archive stream of src.
func OpenHostSource(ctx context.Context, src HostSource, hc *http.Client) (io.ReadCloser, error) {
	switch src.Kind {
	case SourceS3:
		if src.Key == "" {
			return nil, errors.New("backup: S3 source needs an object key")
		}
		rc, _, err := newS3Client(src.S3, httpClient(hc)).Get(ctx, src.Key)
		return rc, err
	case SourcePath, SourceUpload:
		return os.Open(src.Path)
	default:
		return nil, fmt.Errorf("backup: unknown restore source %q", src.Kind)
	}
}

func httpClient(hc *http.Client) *http.Client {
	if hc == nil {
		return http.DefaultClient
	}
	return hc
}

// readHostSidecar returns the sidecar manifest of src when there is a readable one.
func readHostSidecar(ctx context.Context, src HostSource, hc *http.Client) (Manifest, bool) {
	var raw []byte
	switch src.Kind {
	case SourceS3:
		rc, _, err := newS3Client(src.S3, httpClient(hc)).Get(ctx, SidecarName(src.Key))
		if err != nil {
			return Manifest{}, false
		}
		defer rc.Close()
		if raw, err = io.ReadAll(io.LimitReader(rc, maxSmallJSON)); err != nil {
			return Manifest{}, false
		}
	case SourcePath:
		var err error
		if raw, err = os.ReadFile(SidecarName(src.Path)); err != nil {
			return Manifest{}, false
		}
	default:
		return Manifest{}, false
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, false
	}
	return m, true
}

func checkManifest(m Manifest, installed string) error {
	if m.Format > ManifestFormat {
		return fmt.Errorf("%w: archive format %d, this version reads up to %d", ErrArchiveNewer, m.Format, ManifestFormat)
	}
	if archiveNewer(m.StacksteadVersion, installed) {
		return fmt.Errorf("%w: archive was written by %s, this is %s", ErrArchiveNewer, m.StacksteadVersion, installed)
	}
	return nil
}

func damaged(err error) error {
	if errors.Is(err, ErrUnsafePath) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrArchiveDamaged, err)
}

// InspectHost reads the manifest of a whole-host archive (from the sidecar
// when there is one, otherwise by reading the archive to its end) and lists
// what already exists on this host. It refuses an archive from a newer
// Stackstead. It changes nothing.
func InspectHost(ctx context.Context, src HostSource, env HostEnv) (HostSummary, error) {
	m, haveManifest := readHostSidecar(ctx, src, env.HTTP)
	if haveManifest {
		if err := checkManifest(m, env.Version); err != nil {
			return HostSummary{}, err
		}
	}
	rc, err := OpenHostSource(ctx, src, env.HTTP)
	if err != nil {
		return HostSummary{}, err
	}
	defer rc.Close()
	r, err := NewReader(rc)
	if err != nil {
		return HostSummary{}, damaged(err)
	}
	var specs []engine.NetworkSpec
	for {
		hdr, body, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return HostSummary{}, damaged(err)
		}
		switch {
		case hdr.Name == entryNetworks:
			if specs, err = decodeNetworks(body); err != nil {
				return HostSummary{}, err
			}
		case hdr.Name == ManifestEntryName && !haveManifest:
			if err := json.NewDecoder(io.LimitReader(body, maxSmallJSON)).Decode(&m); err != nil {
				return HostSummary{}, damaged(fmt.Errorf("read manifest: %w", err))
			}
			haveManifest = true
		}
		// With a sidecar, only the small base files at the front are needed.
		if haveManifest && pastBaseFiles(hdr.Name) {
			break
		}
	}
	if !haveManifest {
		return HostSummary{}, damaged(errors.New("manifest.json is missing"))
	}
	if err := checkManifest(m, env.Version); err != nil {
		return HostSummary{}, err
	}
	conflicts, err := hostConflicts(ctx, env.Engine, m, specs)
	if err != nil {
		return HostSummary{}, err
	}
	return HostSummary{Manifest: m, Conflicts: conflicts}, nil
}

// pastBaseFiles reports whether name lies beyond the top-level base files
// (database, config and networks), which always come first.
func pastBaseFiles(name string) bool {
	return name != entryDB && name != entryConfig && name != entryNetworks
}

func decodeNetworks(body io.Reader) ([]engine.NetworkSpec, error) {
	var specs []engine.NetworkSpec
	if err := json.NewDecoder(io.LimitReader(body, maxSmallJSON)).Decode(&specs); err != nil {
		return nil, damaged(fmt.Errorf("read %s: %w", entryNetworks, err))
	}
	return specs, nil
}

func hostConflicts(ctx context.Context, eng Engine, m Manifest, specs []engine.NetworkSpec) ([]Conflict, error) {
	var out []Conflict
	projects := map[string]bool{}
	for _, s := range m.Stacks {
		projects[s.Project] = true
		for _, v := range s.Volumes {
			if v.Kind == "bind" {
				if _, err := os.Stat(v.Name); err == nil {
					out = append(out, Conflict{Kind: "volume", Name: v.Name, Action: "overwrite"})
				}
				continue
			}
			_, err := eng.InspectVolume(ctx, v.Name)
			switch {
			case err == nil:
				out = append(out, Conflict{Kind: "volume", Name: v.Name, Action: "overwrite"})
			case !errors.Is(err, engine.ErrVolumeNotFound):
				return nil, fmt.Errorf("inspect volume %s: %w", v.Name, err)
			}
		}
	}
	if len(specs) > 0 {
		nets, err := eng.Networks(ctx)
		if err != nil {
			return nil, fmt.Errorf("list networks: %w", err)
		}
		existing := map[string]bool{}
		for _, n := range nets {
			existing[n.Name] = true
		}
		for _, s := range specs {
			if existing[s.Name] {
				out = append(out, Conflict{Kind: "network", Name: s.Name, Action: "recreate"})
			}
		}
	}
	containers, err := eng.Containers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	for _, c := range containers {
		if p := c.Project(); p != "" && projects[p] {
			out = append(out, Conflict{Kind: "container", Name: c.Name, Action: "recreate"})
		}
	}
	for _, s := range m.Stacks {
		if s.ComposePath == "" {
			continue
		}
		dir := filepath.Dir(s.ComposePath)
		if _, err := os.Stat(dir); err == nil {
			out = append(out, Conflict{Kind: "stack-dir", Name: dir, Action: "overwrite"})
		}
	}
	return out, nil
}

// RestoreHost restores a whole-host archive in one streaming pass: it stages
// and verifies the database, writes each stack's files to its original path
// (skipping paths outside the allowed roots), recreates missing networks,
// refills volumes and loads images, and finally writes the pending marker
// that ApplyPending acts on at the next start. The live database is never
// touched. Any error before stack files are written leaves the host as it was.
// Existing volumes listed by InspectHost are overwritten; existing networks
// are kept as they are.
func RestoreHost(ctx context.Context, src HostSource, env HostEnv, startStacks bool) (HostReport, error) {
	sum, err := InspectHost(ctx, src, env)
	if err != nil {
		return HostReport{}, err
	}
	dir := restoreDir(env.DataDir)
	if _, err := os.Stat(filepath.Join(dir, pendingFile)); err == nil {
		return HostReport{}, ErrRestorePending
	}
	if err := os.RemoveAll(dir); err != nil {
		return HostReport{}, fmt.Errorf("clear restore staging: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return HostReport{}, fmt.Errorf("create restore staging: %w", err)
	}
	finished := false
	defer func() {
		if !finished {
			_ = os.RemoveAll(dir)
		}
	}()

	rc, err := OpenHostSource(ctx, src, env.HTTP)
	if err != nil {
		return HostReport{}, err
	}
	defer rc.Close()
	r, err := NewReader(rc)
	if err != nil {
		return HostReport{}, damaged(err)
	}
	h := &hostRestore{
		ctx: ctx, env: env, r: r, manifest: sum.Manifest, dir: dir,
		stacks: map[string]*stackTarget{}, volumesSeen: map[string]bool{}, networksDone: map[string]bool{},
	}
	defer h.closeRoots()
	if err := h.run(); err != nil {
		return HostReport{Items: h.report()}, err
	}
	p := Pending{StagedDB: filepath.Join(dir, stagedDBName), PauseSchedule: true}
	if startStacks {
		p.StartStacks = h.stacksToStart()
	}
	if err := writePending(env.DataDir, p); err != nil {
		return HostReport{Items: h.report()}, fmt.Errorf("record pending restore: %w", err)
	}
	finished = true
	h.out("restore staged; it completes when Stackstead restarts")
	return HostReport{Items: h.report()}, nil
}

// stackTarget is where one archived stack is restored to.
type stackTarget struct {
	entry   stacks.Entry
	dir     string
	skipped string // reason the stack is not restored, "" when it is
	root    *os.Root
	seen    bool
	failed  []ItemResult
}

type hostRestore struct {
	ctx          context.Context
	env          HostEnv
	r            *Reader
	manifest     Manifest
	dir          string
	stacks       map[string]*stackTarget
	order        []string
	library      *os.Root
	libraryItems []ItemResult
	librarySeen  bool
	networks     []engine.NetworkSpec
	networksDone map[string]bool
	pastStacks   bool
	helperReady  error
	helperTried  bool
	volumesSeen  map[string]bool
	items        []ItemResult
	sawManifest  bool
}

func (h *hostRestore) out(line string) {
	if h.env.Out != nil {
		h.env.Out(line)
	}
}

func (h *hostRestore) run() error {
	hdr, body, err := h.r.Next()
	if errors.Is(err, io.EOF) {
		return damaged(errors.New("archive is empty"))
	}
	if err != nil {
		return damaged(err)
	}
	if hdr.Name != entryDB {
		return damaged(fmt.Errorf("archive starts with %q, not %s", hdr.Name, entryDB))
	}
	if err := h.stageDatabase(body); err != nil {
		return err
	}
	for {
		hdr, body, err := h.r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return damaged(err)
		}
		if err := h.entry(hdr, body); err != nil {
			return err
		}
	}
	if !h.sawManifest {
		return damaged(errors.New("manifest.json is missing"))
	}
	h.createNetworks()
	return nil
}

// stageDatabase writes the archived database to the staging dir, verifies it
// and reads from it where each stack goes.
func (h *hostRestore) stageDatabase(body io.Reader) error {
	h.out("staging the database")
	staged := filepath.Join(h.dir, stagedDBName)
	f, err := os.OpenFile(staged, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("stage database: %w", err)
	}
	_, err = io.Copy(f, body)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return damaged(fmt.Errorf("stage database: %w", err))
	}
	if _, err := store.VerifyFile(h.ctx, staged); err != nil {
		return fmt.Errorf("archived database: %w", err)
	}
	db, err := store.OpenExisting(staged, store.OpenOptions{ReadOnly: true, AllowOlderSchema: true})
	if err != nil {
		return fmt.Errorf("archived database: %w", err)
	}
	entries, err := stacks.NewRegistry(db, time.Now).List(h.ctx)
	db.Close()
	if err != nil {
		return fmt.Errorf("archived database: %w", err)
	}
	for _, e := range entries {
		t := &stackTarget{entry: e, dir: filepath.Dir(e.ComposePath)}
		if !filepath.IsAbs(e.ComposePath) || !h.insideRoots(t.dir) {
			t.skipped = fmt.Sprintf("%s is outside the allowed roots", t.dir)
		}
		h.stacks[e.Name] = t
		h.order = append(h.order, e.Name)
	}
	return nil
}

func (h *hostRestore) insideRoots(dir string) bool {
	dir = filepath.Clean(dir)
	for _, root := range h.env.AllowedRoots {
		rel, err := filepath.Rel(filepath.Clean(root), dir)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}

func (h *hostRestore) entry(hdr *tar.Header, body io.Reader) error {
	name := hdr.Name
	switch {
	case name == ManifestEntryName:
		h.sawManifest = true
		return nil
	case name == entryDB:
		return damaged(errors.New("archive holds a second database"))
	case name == entryConfig:
		return h.stageConfig(body)
	case name == entryNetworks:
		specs, err := decodeNetworks(body)
		if err != nil {
			return err
		}
		h.networks = append(h.networks, specs...)
		if h.pastStacks {
			h.createNetworks()
		}
		return nil
	case strings.HasPrefix(name, stacksPrefix):
		return h.stackEntry(hdr, body)
	}
	// Stack files are done: networks come next, before any container data.
	if !h.pastStacks {
		h.pastStacks = true
		h.createNetworks()
	}
	switch {
	case strings.HasPrefix(name, libraryPrefix):
		return h.libraryEntry(hdr, body)
	case strings.HasPrefix(name, volumesPrefix):
		return h.volume(hdr, body)
	case strings.HasPrefix(name, imagesPrefix):
		return h.images(hdr, body)
	}
	return nil
}

// stageConfig keeps the archived config.yaml for reference; it is not applied.
func (h *hostRestore) stageConfig(body io.Reader) error {
	p := filepath.Join(h.dir, stagedConfigYML)
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("stage config.yaml: %w", err)
	}
	_, err = io.Copy(f, body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return damaged(fmt.Errorf("stage config.yaml: %w", err))
	}
	h.items = append(h.items, ItemResult{Kind: "base", Name: "config.yaml", Status: "skipped", Note: "not applied; the archived copy is kept at " + p})
	return nil
}

// target returns the stack's restore target, recording stacks the archived
// database does not know as skipped.
func (h *hostRestore) target(stack string) *stackTarget {
	t, ok := h.stacks[stack]
	if !ok {
		t = &stackTarget{entry: stacks.Entry{Name: stack}, skipped: "not registered in the archived database"}
		h.stacks[stack] = t
		h.order = append(h.order, stack)
	}
	t.seen = true
	return t
}

func (h *hostRestore) stackEntry(hdr *tar.Header, body io.Reader) error {
	stack, rel, _ := strings.Cut(strings.TrimPrefix(hdr.Name, stacksPrefix), "/")
	if stack == "" {
		return nil
	}
	t := h.target(stack)
	if t.skipped != "" {
		return nil
	}
	if t.root == nil {
		h.out("writing files of " + stack + " to " + t.dir)
		if err := os.MkdirAll(t.dir, 0o755); err != nil {
			t.skipped = "cannot create " + t.dir + ": " + err.Error()
			return nil
		}
		root, err := os.OpenRoot(t.dir)
		if err != nil {
			t.skipped = "cannot open " + t.dir + ": " + err.Error()
			return nil
		}
		t.root = root
	}
	item, err := extractEntry(t.root, stacksPrefix+stack+"/", rel, hdr, body)
	if item != nil {
		item.Stack = stack
		t.failed = append(t.failed, *item)
	}
	return err
}

func (h *hostRestore) libraryEntry(hdr *tar.Header, body io.Reader) error {
	if h.library == nil {
		if h.librarySeen {
			return nil // the library dir could not be opened
		}
		h.librarySeen = true
		dir := filepath.Join(h.env.DataDir, imagesLibraryDir)
		err := os.MkdirAll(dir, 0o700)
		if err == nil {
			h.library, err = os.OpenRoot(dir)
		}
		if err != nil {
			h.libraryItems = append(h.libraryItems, ItemResult{Kind: "base", Name: "images-library", Status: "failed", Note: err.Error()})
			return nil
		}
	}
	item, err := extractEntry(h.library, libraryPrefix, strings.TrimPrefix(hdr.Name, libraryPrefix), hdr, body)
	if item != nil {
		h.libraryItems = append(h.libraryItems, *item)
	}
	return err
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
		return &ItemResult{Kind: "base", Name: hdr.Name, Status: "failed", Note: err.Error()}, nil
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
		return &ItemResult{Kind: "base", Name: hdr.Name, Status: "skipped", Note: "unsupported entry type"}, nil
	}
	return nil, nil
}

// createNetworks creates the archived networks that do not exist yet. A
// failure is reported and does not stop the restore.
func (h *hostRestore) createNetworks() {
	var pending []engine.NetworkSpec
	for _, s := range h.networks {
		if !h.networksDone[s.Name] {
			pending = append(pending, s)
		}
	}
	if len(pending) == 0 {
		return
	}
	existing := map[string]bool{}
	nets, err := h.env.Engine.Networks(h.ctx)
	if err != nil {
		for _, s := range pending {
			h.networksDone[s.Name] = true
			h.items = append(h.items, ItemResult{Kind: "network", Name: s.Name, Status: "failed", Note: "list networks: " + err.Error()})
		}
		return
	}
	for _, n := range nets {
		existing[n.Name] = true
	}
	for _, s := range pending {
		h.networksDone[s.Name] = true
		if existing[s.Name] {
			h.items = append(h.items, ItemResult{Kind: "network", Name: s.Name, Status: "skipped", Note: "already exists; kept as it is"})
			continue
		}
		h.out("creating network " + s.Name)
		if _, err := h.env.Engine.CreateNetwork(h.ctx, s); err != nil {
			h.items = append(h.items, ItemResult{Kind: "network", Name: s.Name, Status: "failed", Note: err.Error()})
			continue
		}
		h.items = append(h.items, ItemResult{Kind: "network", Name: s.Name, Status: "ok"})
	}
}

// drain consumes the rest of a TarStreamUnder stream. Its error can only come
// from reading the archive, so it is fatal.
func drain(stream io.Reader) error {
	if _, err := io.Copy(io.Discard, stream); err != nil {
		return damaged(err)
	}
	return nil
}

func (h *hostRestore) manifestVolume(stack, key string) (ManifestVolume, bool) {
	for _, s := range h.manifest.Stacks {
		if s.Name != stack {
			continue
		}
		for _, v := range s.Volumes {
			if v.Key == key {
				return v, true
			}
		}
	}
	return ManifestVolume{}, false
}

func (h *hostRestore) volume(hdr *tar.Header, body io.Reader) error {
	parts := strings.SplitN(strings.TrimPrefix(hdr.Name, volumesPrefix), "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return nil
	}
	stack, key := parts[0], parts[1]
	prefix := volumesPrefix + stack + "/" + key + "/"
	stream := TarStreamUnder(h.r, prefix, hdr, body)
	t := h.target(stack)
	if t.skipped != "" || h.volumesSeen[prefix] {
		return drain(stream)
	}
	h.volumesSeen[prefix] = true
	mv, ok := h.manifestVolume(stack, key)
	if !ok {
		h.items = append(h.items, ItemResult{Kind: "volume", Stack: stack, Name: key, Status: "failed", Note: "not described in the manifest"})
		return drain(stream)
	}
	h.out("restoring " + mv.Kind + " " + mv.Name + " of " + stack)
	err := h.fillVolume(stack, mv, stream)
	if derr := drain(stream); derr != nil {
		return derr
	}
	if err != nil {
		h.items = append(h.items, ItemResult{Kind: "volume", Stack: stack, Name: mv.Name, Status: "failed", Note: err.Error()})
		return nil
	}
	h.items = append(h.items, ItemResult{Kind: "volume", Stack: stack, Name: mv.Name, Status: "ok"})
	return nil
}

func (h *hostRestore) ensureHelper() error {
	if !h.helperTried {
		h.helperTried = true
		h.helperReady = EnsureHelperImage(h.ctx, h.env.Engine, h.env.Out)
	}
	return h.helperReady
}

// fillVolume empties the volume (or bind source) with a helper container and
// then extracts the stream into it through Docker. The helper is always removed.
func (h *hostRestore) fillVolume(stack string, mv ManifestVolume, stream io.Reader) error {
	eng := h.env.Engine
	if err := h.ensureHelper(); err != nil {
		return fmt.Errorf("helper image: %w", err)
	}
	switch mv.Kind {
	case "volume":
		_, err := eng.InspectVolume(h.ctx, mv.Name)
		if errors.Is(err, engine.ErrVolumeNotFound) {
			err = eng.CreateVolume(h.ctx, mv.Name, h.volumeLabels(stack, mv.Name))
		}
		if err != nil {
			return err
		}
	case "bind":
		if !filepath.IsAbs(mv.Name) {
			return fmt.Errorf("bind source %q is not absolute", mv.Name)
		}
		if err := os.MkdirAll(mv.Name, 0o755); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown mount kind %q", mv.Kind)
	}
	id, err := eng.CreateContainer(h.ctx, engine.HelperSpec{
		Image:  HelperImage,
		Mounts: []engine.HelperMount{{Type: mv.Kind, Source: mv.Name, Target: "/data"}},
		Cmd:    []string{"find", "/data", "-mindepth", "1", "-delete"},
		Labels: map[string]string{"dev.stackstead.restore-stack": stack},
	})
	if err != nil {
		return fmt.Errorf("create helper: %w", err)
	}
	defer func() {
		// The caller's context may be cancelled; the helper must still go.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(h.ctx), 30*time.Second)
		defer cancel()
		_ = eng.RemoveContainer(cleanupCtx, id, true)
	}()
	if err := eng.StartContainer(h.ctx, id); err != nil {
		return fmt.Errorf("start helper: %w", err)
	}
	code, err := eng.WaitContainer(h.ctx, id)
	if err != nil {
		return fmt.Errorf("empty %s: %w", mv.Name, err)
	}
	if code != 0 {
		return fmt.Errorf("empty %s: helper exited with %d", mv.Name, code)
	}
	if err := eng.PutArchive(h.ctx, id, "/", stream); err != nil {
		return fmt.Errorf("write %s: %w", mv.Name, err)
	}
	return nil
}

// volumeLabels marks a recreated named volume as belonging to the stack's
// compose project, so compose adopts it without a warning.
func (h *hostRestore) volumeLabels(stack, volume string) map[string]string {
	project := h.stacks[stack].entry.Project
	short, ok := strings.CutPrefix(volume, project+"_")
	if project == "" || !ok || short == "" {
		return nil
	}
	return map[string]string{"com.docker.compose.project": project, "com.docker.compose.volume": short}
}

func (h *hostRestore) images(hdr *tar.Header, body io.Reader) error {
	stack, _, _ := strings.Cut(strings.TrimPrefix(hdr.Name, imagesPrefix), "/")
	if stack == "" {
		return nil
	}
	stream := TarStreamUnder(h.r, imagesPrefix+stack+"/", hdr, body)
	if t := h.target(stack); t.skipped != "" {
		return drain(stream)
	}
	h.out("loading images of " + stack)
	err := h.env.Engine.LoadImage(h.ctx, stream)
	if derr := drain(stream); derr != nil {
		return derr
	}
	if err != nil {
		h.items = append(h.items, ItemResult{Kind: "image", Stack: stack, Status: "failed", Note: err.Error()})
		return nil
	}
	h.items = append(h.items, ItemResult{Kind: "image", Stack: stack, Status: "ok"})
	return nil
}

func (h *hostRestore) closeRoots() {
	for _, t := range h.stacks {
		if t.root != nil {
			t.root.Close()
		}
	}
	if h.library != nil {
		h.library.Close()
	}
}

// stacksToStart lists the stacks that were running at backup time and whose
// files were restored.
func (h *hostRestore) stacksToStart() []string {
	var out []string
	for _, s := range h.manifest.Stacks {
		t, ok := h.stacks[s.Name]
		if s.WasRunning && ok && t.skipped == "" && t.root != nil && len(t.failed) == 0 {
			out = append(out, s.Name)
		}
	}
	return out
}

func (h *hostRestore) report() []ItemResult {
	var items []ItemResult
	for _, name := range h.order {
		t := h.stacks[name]
		switch {
		case t.skipped != "":
			items = append(items, ItemResult{Kind: "stack-files", Stack: name, Name: t.dir, Status: "skipped", Note: t.skipped})
		case t.root != nil:
			items = append(items, t.failed...)
			status := "ok"
			if len(t.failed) > 0 {
				status = "failed"
			}
			items = append(items, ItemResult{Kind: "stack-files", Stack: name, Name: t.dir, Status: status})
		}
	}
	items = append(items, h.libraryItems...)
	if h.library != nil {
		items = append(items, ItemResult{Kind: "base", Name: "images-library", Status: "ok"})
	}
	return append(items, h.items...)
}
