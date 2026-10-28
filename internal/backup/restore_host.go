package backup

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/stacks"
	"github.com/rcss-labs/stackstead/internal/store"
)

var (
	// ErrArchiveDamaged reports an archive that is truncated or malformed.
	ErrArchiveDamaged = errors.New("backup: archive is damaged or incomplete")
	// ErrManifestMismatch reports a sidecar that does not describe the archive.
	ErrManifestMismatch = errors.New("backup: the manifest sidecar does not match the archive")
	// ErrConflictsChanged refuses a restore when the host changed after the
	// user confirmed the conflicts.
	ErrConflictsChanged = errors.New("backup: existing Docker state changed since it was confirmed; inspect the archive again")
	// ErrRestorePending refuses a restore while an earlier one awaits a restart.
	ErrRestorePending = errors.New("backup: a host restore is already pending; restart Stackstead to finish it")
)

// Archive entries a host restore treats specially.
const (
	entryDB       = "base/stackstead.db"
	entryConfig   = "base/config.yaml"
	entryNetworks = "base/networks.json"
	basePrefix    = "base/"
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

// Conflict kinds and actions.
const (
	conflictVolume    = "volume"
	conflictNetwork   = "network"
	conflictContainer = "container"
	conflictStackDir  = "stack-dir"
	actionOverwrite   = "overwrite"
	actionRecreate    = "recreate"
	actionKeep        = "keep"
	// actionStopped is a running container whose project's volumes are
	// replaced: it is stopped first and its stack starts after the restart.
	actionStopped = "stopped during restore"

	// containerStopGrace is Docker's stop timeout, in seconds.
	containerStopGrace = 10
	// containerStartTimeout bounds starting the stopped containers again
	// after a failed restore.
	containerStartTimeout = 2 * time.Minute
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
	// Accepted, when set, is called once RestoreHost has checked the archive
	// and the confirmed conflicts, before it changes anything.
	Accepted func()
	// Deferred says the caller finishes the restore by restarting Stackstead
	// (the server does); the closing progress line then says so.
	Deferred bool
	// HTTP is used for S3 sources; nil means http.DefaultClient.
	HTTP *http.Client
}

// Conflict is existing Docker or filesystem state a host restore meets.
type Conflict struct {
	// Kind is "volume", "network", "container" or "stack-dir".
	Kind string `json:"kind"`
	Name string `json:"name"`
	// Action is "overwrite" (volume contents, stack files), "recreate"
	// (containers, by compose at start), "stopped during restore" (a running
	// container whose project's volumes are replaced) or "keep" (an existing
	// network is used as it is).
	Action string `json:"action"`
}

// HostSummary is what InspectHost shows before a host restore.
type HostSummary struct {
	Manifest  Manifest   `json:"manifest"`
	Conflicts []Conflict `json:"conflicts"`
	// ConflictsDigest identifies Conflicts. The user confirms them once and
	// RestoreHost requires the digest back.
	ConflictsDigest string `json:"conflictsDigest"`
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
	return checkArchiveVersion(m.StacksteadVersion, installed)
}

func damaged(err error) error {
	if errors.Is(err, ErrUnsafePath) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrArchiveDamaged, err)
}

// InspectHost reads the manifest of a whole-host archive and lists what
// already exists on this host. With a sidecar it reads only the base/ section
// of the archive (for networks.json); without one it reads the archive to its
// end for manifest.json, which also proves the archive is complete. It
// refuses an archive from a newer Stackstead. It changes nothing.
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
		// With a sidecar, only base/ is needed: networks.json lives there.
		if haveManifest && !strings.HasPrefix(hdr.Name, basePrefix) {
			break
		}
		switch hdr.Name {
		case entryNetworks:
			if specs, err = decodeNetworks(body); err != nil {
				return HostSummary{}, err
			}
		case ManifestEntryName:
			if m, err = decodeArchiveManifest(body); err != nil {
				return HostSummary{}, err
			}
			haveManifest = true
		}
	}
	if !haveManifest {
		return HostSummary{}, damaged(errors.New("manifest.json is missing"))
	}
	if err := checkManifest(m, env.Version); err != nil {
		return HostSummary{}, err
	}
	conflicts, err := hostConflicts(ctx, env, m, specs)
	if err != nil {
		return HostSummary{}, err
	}
	return HostSummary{Manifest: m, Conflicts: conflicts, ConflictsDigest: conflictsDigest(conflicts)}, nil
}

func decodeArchiveManifest(body io.Reader) (Manifest, error) {
	var m Manifest
	if err := json.NewDecoder(io.LimitReader(body, maxSmallJSON)).Decode(&m); err != nil {
		return Manifest{}, damaged(fmt.Errorf("read manifest: %w", err))
	}
	return m, nil
}

func decodeNetworks(body io.Reader) ([]engine.NetworkSpec, error) {
	var specs []engine.NetworkSpec
	if err := json.NewDecoder(io.LimitReader(body, maxSmallJSON)).Decode(&specs); err != nil {
		return nil, damaged(fmt.Errorf("read %s: %w", entryNetworks, err))
	}
	return specs, nil
}

// conflictsDigest is a sha256 over the sorted conflicts.
func conflictsDigest(conflicts []Conflict) string {
	sorted := slices.Clone(conflicts)
	slices.SortFunc(sorted, func(a, b Conflict) int {
		return strings.Compare(a.Kind+"\x00"+a.Name+"\x00"+a.Action, b.Kind+"\x00"+b.Name+"\x00"+b.Action)
	})
	h := sha256.New()
	for _, c := range sorted {
		fmt.Fprintf(h, "%s\x00%s\x00%s\n", c.Kind, c.Name, c.Action)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// pathInsideRoots reports whether p lies inside an allowed root, both as
// written and with symlinks resolved as far as p exists.
func pathInsideRoots(p string, roots []string) bool {
	if !underAnyRoot(p, roots) {
		return false
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return true // nothing there yet to follow
	}
	return underAnyRoot(resolved, resolvedRoots(roots))
}

func resolvedRoots(roots []string) []string {
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		if resolved, err := filepath.EvalSymlinks(r); err == nil {
			out = append(out, resolved)
		} else {
			out = append(out, r)
		}
	}
	return out
}

// hostConflicts lists existing state the restore meets. Paths outside the
// allowed roots are never looked at: they are not restored.
func hostConflicts(ctx context.Context, env HostEnv, m Manifest, specs []engine.NetworkSpec) ([]Conflict, error) {
	eng := env.Engine
	var out []Conflict
	projects := map[string]bool{}
	for _, s := range m.Stacks {
		projects[s.Project] = true
		for _, v := range s.Volumes {
			if failedInManifest(m, conflictVolume, s.Name, v.Name) {
				continue
			}
			if v.Kind == volumeKindBind {
				if pathInsideRoots(v.Name, env.AllowedRoots) {
					if _, err := os.Stat(v.Name); err == nil {
						out = append(out, Conflict{Kind: conflictVolume, Name: v.Name, Action: actionOverwrite})
					}
				}
				continue
			}
			_, err := eng.InspectVolume(ctx, v.Name)
			switch {
			case err == nil:
				out = append(out, Conflict{Kind: conflictVolume, Name: v.Name, Action: actionOverwrite})
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
				out = append(out, Conflict{Kind: conflictNetwork, Name: s.Name, Action: actionKeep})
			}
		}
	}
	containers, err := eng.Containers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	replaced := projectsWithReplacedVolumes(m, env.AllowedRoots)
	for _, c := range containers {
		p := c.Project()
		switch {
		case p == "" || !projects[p]:
		case replaced[p] && containerActive(c):
			out = append(out, Conflict{Kind: conflictContainer, Name: c.Name, Action: actionStopped})
		default:
			out = append(out, Conflict{Kind: conflictContainer, Name: c.Name, Action: actionRecreate})
		}
	}
	for _, s := range m.Stacks {
		if s.ComposePath == "" {
			continue
		}
		dir := filepath.Dir(s.ComposePath)
		if !pathInsideRoots(dir, env.AllowedRoots) {
			continue
		}
		if _, err := os.Stat(dir); err == nil {
			out = append(out, Conflict{Kind: conflictStackDir, Name: dir, Action: actionOverwrite})
		}
	}
	return out, nil
}

// projectsWithReplacedVolumes returns the compose projects whose volumes a
// host restore of m refills: stacks inside the allowed roots with a volume,
// or a bind inside the roots, whose backup did not fail.
func projectsWithReplacedVolumes(m Manifest, roots []string) map[string]bool {
	out := map[string]bool{}
	for _, s := range m.Stacks {
		if s.Project == "" || !filepath.IsAbs(s.ComposePath) || !underAnyRoot(filepath.Dir(s.ComposePath), roots) {
			continue
		}
		for _, v := range s.Volumes {
			if failedInManifest(m, conflictVolume, s.Name, v.Name) || (v.Kind == volumeKindBind && !pathInsideRoots(v.Name, roots)) {
				continue
			}
			out[s.Project] = true
			break
		}
	}
	return out
}

// containerActive reports a container that may be writing to its volumes.
func containerActive(c engine.Container) bool {
	return c.State == "running" || c.State == "restarting"
}

// failedInManifest reports whether the backup recorded the item as failed.
func failedInManifest(m Manifest, kind, stack, name string) bool {
	return slices.ContainsFunc(m.Items, func(it ItemResult) bool {
		return it.Kind == kind && it.Stack == stack && it.Name == name && it.Status == itemFailed
	})
}

// RestoreHost restores a whole-host archive in one streaming pass: it stages
// and verifies the database, writes each stack's files to its original path
// (skipping paths outside the allowed roots), creates missing networks,
// refills volumes and loads images, and finally writes the pending marker
// that ApplyPending acts on at the next start. The live database is never
// touched.
//
// confirmedDigest is the ConflictsDigest the user confirmed; a fresh
// inspection that differs refuses the restore before anything changes. Any
// error before stack files are written leaves the host as it was. With a
// sidecar there is no full pre-scan, so an archive damaged after the stack
// files fails the restore with those files (and anything restored before the
// damage) already in place; no pending marker is written then.
func RestoreHost(ctx context.Context, src HostSource, env HostEnv, startStacks bool, confirmedDigest string) (HostReport, error) {
	sum, err := InspectHost(ctx, src, env)
	if err != nil {
		return HostReport{}, err
	}
	if sum.ConflictsDigest != confirmedDigest {
		return HostReport{}, ErrConflictsChanged
	}
	dir := restoreDir(env.DataDir)
	if _, err := os.Stat(filepath.Join(dir, pendingFile)); err == nil {
		return HostReport{}, ErrRestorePending
	}
	if err := clearStaging(dir); err != nil {
		return HostReport{}, fmt.Errorf("clear restore staging: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return HostReport{}, fmt.Errorf("create restore staging: %w", err)
	}
	finished := false
	defer func() {
		if !finished {
			_ = clearStaging(dir)
		}
	}()
	if env.Accepted != nil {
		env.Accepted()
	}

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
		stacks: map[string]*stackTarget{}, rs: newRootSet(env.AllowedRoots, env.DataDir),
		volumesSeen: map[string]bool{}, networksDone: map[string]bool{},
		stoppedProjects: map[string]bool{}, stopFailed: map[string]bool{},
	}
	defer h.closeRoots()
	if err := h.run(); err != nil {
		h.startStopped()
		return HostReport{Items: h.report()}, err
	}
	p := Pending{StagedDB: filepath.Join(dir, stagedDBName), PauseSchedule: true}
	if startStacks {
		p.StartStacks = h.stacksToStart()
	}
	p.StartStacks = h.withStoppedStacks(p.StartStacks)
	if err := writePending(env.DataDir, p); err != nil {
		h.startStopped()
		return HostReport{Items: h.report()}, fmt.Errorf("record pending restore: %w", err)
	}
	finished = true
	if env.Deferred {
		h.out("restore staged; it completes when Stackstead restarts")
	}
	return HostReport{Items: h.report()}, nil
}

// clearStaging empties the restore folder except for the uploaded archive
// (the source of this restore or of a retry), an upload still being written,
// and an earlier restore's unfinished post-start work.
func clearStaging(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		switch e.Name() {
		case uploadName, uploadName + partialSuffix, postApplyFile:
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// stackTarget is where one archived stack is restored to.
type stackTarget struct {
	entry   stacks.Entry
	dir     string
	skipped string // reason the stack is not restored, "" when it is
	root    *os.Root
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
	rs           *rootSet
	library      *os.Root
	libraryItems []ItemResult
	librarySeen  bool
	dbStaged     bool
	networks     []engine.NetworkSpec
	networksDone map[string]bool
	pastStacks   bool
	helperReady  error
	helperTried  bool
	volumesSeen  map[string]bool
	items        []ItemResult
	sawManifest  bool

	// R36: running containers of projects whose volumes are replaced are
	// stopped before the first volume; see stopForVolumes.
	stopTried       bool
	stopped         []engine.Container
	stoppedProjects map[string]bool
	stopFailed      map[string]bool
}

func (h *hostRestore) out(line string) {
	if h.env.Out != nil {
		h.env.Out(line)
	}
}

func (h *hostRestore) run() error {
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
	if !h.dbStaged {
		return damaged(fmt.Errorf("%s is missing", entryDB))
	}
	if !h.sawManifest {
		return damaged(errors.New("manifest.json is missing"))
	}
	h.createNetworks()
	return nil
}

// needsDatabase reports entries that change the host, which may only come
// after the database has been staged and verified.
func needsDatabase(name string) bool {
	for _, p := range []string{stacksPrefix, libraryPrefix, volumesPrefix, imagesPrefix} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func (h *hostRestore) entry(hdr *tar.Header, body io.Reader) error {
	name := hdr.Name
	if needsDatabase(name) && !h.dbStaged {
		return damaged(fmt.Errorf("%q comes before %s", name, entryDB))
	}
	switch {
	case name == ManifestEntryName:
		return h.checkArchiveManifest(body)
	case name == entryDB:
		if h.dbStaged {
			return damaged(errors.New("archive holds a second database"))
		}
		return h.stageDatabase(body)
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
	case strings.HasPrefix(name, basePrefix):
		if strings.HasPrefix(name, libraryPrefix) {
			return h.libraryEntry(hdr, body)
		}
		return nil
	}
	if !strings.HasPrefix(name, volumesPrefix) && !strings.HasPrefix(name, imagesPrefix) {
		return damaged(fmt.Errorf("unexpected entry %q", name))
	}
	// Only volumes/ and images/ get here, which needsDatabase gated on the
	// staged database. Stack files are done: networks come next.
	if !h.pastStacks {
		h.pastStacks = true
		h.createNetworks()
	}
	switch {
	case strings.HasPrefix(name, volumesPrefix):
		return h.volume(hdr, body)
	case strings.HasPrefix(name, imagesPrefix):
		return h.images(hdr, body)
	}
	return nil
}

// checkArchiveManifest makes sure the manifest that drove the restore (often
// the sidecar) describes this archive.
func (h *hostRestore) checkArchiveManifest(body io.Reader) error {
	m, err := decodeArchiveManifest(body)
	if err != nil {
		return err
	}
	// The manifest that drove the restore is either the sidecar or this very
	// manifest (read by InspectHost), so it is always compared.
	if err := verifyArchiveManifest(m, h.manifest, true, h.env.Version); err != nil {
		return err
	}
	h.sawManifest = true
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
		if !filepath.IsAbs(e.ComposePath) || !underAnyRoot(t.dir, h.env.AllowedRoots) {
			t.skipped = fmt.Sprintf("%s is %s", t.dir, reasonOutsideRoots)
		}
		h.stacks[e.Name] = t
		h.order = append(h.order, e.Name)
	}
	h.dbStaged = true
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
	h.items = append(h.items, ItemResult{Kind: "base", Name: "config.yaml", Status: itemSkipped, Note: "not applied; the archived copy is kept at " + p})
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
		root, err := h.rs.openUnder(t.dir)
		if err != nil {
			t.skipped = "cannot write " + t.dir + ": " + err.Error()
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
		lib, err := h.openLibrary()
		if err != nil {
			h.libraryItems = append(h.libraryItems, ItemResult{Kind: "base", Name: "images-library", Status: itemFailed, Note: err.Error()})
			return nil
		}
		h.library = lib
	}
	item, err := extractEntry(h.library, libraryPrefix, strings.TrimPrefix(hdr.Name, libraryPrefix), hdr, body)
	if item != nil {
		h.libraryItems = append(h.libraryItems, *item)
	}
	return err
}

// openLibrary opens <data>/images through the data dir's os.Root.
func (h *hostRestore) openLibrary() (*os.Root, error) {
	data, err := h.rs.openRoot(h.env.DataDir)
	if err != nil {
		return nil, err
	}
	if err := data.MkdirAll(imagesLibraryDir, 0o700); err != nil {
		return nil, err
	}
	return data.OpenRoot(imagesLibraryDir)
}

// createNetworks creates the archived networks that do not exist yet. An
// existing network is kept as it is. A failure is reported and does not stop
// the restore.
func (h *hostRestore) createNetworks() {
	if !h.dbStaged {
		return // nothing changes on the host before the database is verified
	}
	var pending []engine.NetworkSpec
	for _, s := range h.networks {
		if !h.networksDone[s.Name] {
			pending = append(pending, s)
		}
	}
	for _, spec := range pending {
		h.networksDone[spec.Name] = true
	}
	h.items = append(h.items, createNetworkSpecs(h.ctx, h.env.Engine, pending, h.env.Out)...)
}

func (h *hostRestore) manifestStack(stack string) ManifestStack {
	s, _ := findManifestStack(h.manifest, stack)
	return s
}

// failedName returns the name of an item the backup recorded as failed whose
// archive key is key, so its leftover entries can be reported by name.
func (h *hostRestore) failedName(kind, stack, key string) (string, bool) {
	for _, it := range h.manifest.Items {
		if it.Kind != kind || it.Stack != stack || it.Status != itemFailed || it.Name == "" {
			continue
		}
		var keys []string
		if kind == conflictVolume {
			keys = []string{VolumeKey(stacks.Mount{Kind: "volume", Source: it.Name}), VolumeKey(stacks.Mount{Kind: volumeKindBind, Source: it.Name})}
		} else {
			keys = []string{ImageKey(it.Name)}
		}
		if slices.Contains(keys, key) {
			return it.Name, true
		}
	}
	return "", false
}

// unlisted reports an archived volume or image the manifest does not list
// for restore, which happens when its backup failed part-way.
func (h *hostRestore) unlisted(kind, stack, key string) {
	if name, ok := h.failedName(kind, stack, key); ok {
		h.items = append(h.items, ItemResult{Kind: kind, Stack: stack, Name: name, Status: itemSkipped, Note: "its backup failed; the archived data is incomplete"})
		return
	}
	h.items = append(h.items, ItemResult{Kind: kind, Stack: stack, Name: key, Status: itemSkipped, Note: "not listed in the manifest"})
}

func (h *hostRestore) volume(hdr *tar.Header, body io.Reader) error {
	parts := strings.SplitN(strings.TrimPrefix(hdr.Name, volumesPrefix), "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return nil
	}
	stack, key := parts[0], parts[1]
	prefix := volumesPrefix + stack + "/" + key + "/"
	stream := TarStreamUnder(h.r, prefix, hdr, body)
	h.stopForVolumes()
	t := h.target(stack)
	if t.skipped != "" || h.volumesSeen[prefix] {
		return drain(stream)
	}
	h.volumesSeen[prefix] = true
	ms := h.manifestStack(stack)
	i := slices.IndexFunc(ms.Volumes, func(v ManifestVolume) bool { return v.Key == key })
	if i < 0 || failedInManifest(h.manifest, conflictVolume, stack, ms.Volumes[i].Name) {
		h.unlisted(conflictVolume, stack, key)
		return drain(stream)
	}
	mv := ms.Volumes[i]
	if err := checkVolumeKey(mv); err != nil {
		h.items = append(h.items, ItemResult{Kind: conflictVolume, Stack: stack, Name: mv.Name, Status: itemFailed, Note: err.Error()})
		return drain(stream)
	}
	if h.stopFailed[ms.Project] {
		h.items = append(h.items, ItemResult{Kind: conflictVolume, Stack: stack, Name: mv.Name, Status: itemSkipped, Note: "a container of the stack could not be stopped; the volume is left as it was"})
		return drain(stream)
	}
	var bind *bindTarget
	if mv.Kind == volumeKindBind {
		if archivedAsFile(hdr, prefix) {
			h.items = append(h.items, ItemResult{Kind: conflictVolume, Stack: stack, Name: mv.Name, Status: itemSkipped, Note: noteArchivedFileBind})
			return drain(stream)
		}
		target, skip, err := h.rs.prepareBind(mv.Name, h.restoredStackDirs())
		if skip != "" || err != nil {
			item := ItemResult{Kind: conflictVolume, Stack: stack, Name: mv.Name, Status: itemSkipped, Note: skip}
			if err != nil {
				item.Status, item.Note = itemFailed, err.Error()
			}
			h.items = append(h.items, item)
			return drain(stream)
		}
		bind = &target
	}
	h.out("restoring " + mv.Kind + " " + mv.Name + " of " + stack)
	err := h.fillVolume(stack, mv, bind, stream)
	if derr := drain(stream); derr != nil {
		return derr
	}
	if err != nil {
		h.items = append(h.items, ItemResult{Kind: conflictVolume, Stack: stack, Name: mv.Name, Status: itemFailed, Note: err.Error()})
		return nil
	}
	h.items = append(h.items, ItemResult{Kind: conflictVolume, Stack: stack, Name: mv.Name, Status: itemOK})
	return nil
}

// stopForVolumes stops, once and before any volume is emptied, the running
// containers of the restored projects whose volumes are replaced (R36). A
// project with a container that would not stop keeps its volumes.
func (h *hostRestore) stopForVolumes() {
	if h.stopTried {
		return
	}
	h.stopTried = true
	replaced := projectsWithReplacedVolumes(h.manifest, h.env.AllowedRoots)
	want := map[string]bool{}
	for _, s := range h.manifest.Stacks {
		if t, ok := h.stacks[s.Name]; ok && t.skipped == "" && replaced[s.Project] {
			want[s.Project] = true
		}
	}
	if len(want) == 0 {
		return
	}
	containers, err := h.env.Engine.Containers(h.ctx)
	if err != nil {
		for p := range want {
			h.stopFailed[p] = true
		}
		h.items = append(h.items, ItemResult{Kind: conflictContainer, Status: itemFailed, Note: "list containers: " + err.Error()})
		return
	}
	for _, c := range containers {
		p := c.Project()
		if !want[p] || !containerActive(c) {
			continue
		}
		h.out("stopping container " + c.Name)
		if err := h.env.Engine.ContainerAction(h.ctx, c.ID, "stop", containerStopGrace); err != nil {
			h.stopFailed[p] = true
			h.items = append(h.items, ItemResult{Kind: conflictContainer, Name: c.Name, Status: itemFailed, Note: "stop failed: " + err.Error()})
			continue
		}
		h.stopped = append(h.stopped, c)
		h.stoppedProjects[p] = true
		h.items = append(h.items, ItemResult{Kind: conflictContainer, Name: c.Name, Status: itemOK, Note: actionStopped})
	}
}

// withStoppedStacks adds to start the restored stacks whose containers were
// stopped here, so they come back after the restart.
func (h *hostRestore) withStoppedStacks(start []string) []string {
	for _, s := range h.manifest.Stacks {
		t, ok := h.stacks[s.Name]
		if ok && t.skipped == "" && h.stoppedProjects[s.Project] && !slices.Contains(start, s.Name) {
			start = append(start, s.Name)
		}
	}
	return start
}

// startStopped starts the containers stopped here again when the restore
// fails: no pending marker will start their stacks. It runs even when the
// restore was cancelled.
func (h *hostRestore) startStopped() {
	if len(h.stopped) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(h.ctx), containerStartTimeout)
	defer cancel()
	for _, c := range h.stopped {
		h.out("starting container " + c.Name)
		if err := h.env.Engine.ContainerAction(ctx, c.ID, "start", containerStopGrace); err != nil {
			h.items = append(h.items, ItemResult{Kind: conflictContainer, Name: c.Name, Status: itemFailed, Note: "left stopped: start failed: " + err.Error()})
		}
	}
	h.stopped = nil
}

// restoredStackDirs lists the directories of the stacks being restored.
func (h *hostRestore) restoredStackDirs() []string {
	var dirs []string
	for _, name := range h.order {
		if t := h.stacks[name]; t.skipped == "" && t.dir != "" {
			dirs = append(dirs, t.dir)
		}
	}
	return dirs
}

func (h *hostRestore) ensureHelper() error {
	if !h.helperTried {
		h.helperTried = true
		h.helperReady = EnsureHelperImage(h.ctx, h.env.Engine, h.env.Out)
	}
	return h.helperReady
}

// fillVolume makes sure the helper image is present and replaces the
// volume's (or, with bind set, the vetted bind source's) contents with stream.
// A bind is vetted again after the image check, which may pull.
func (h *hostRestore) fillVolume(stack string, mv ManifestVolume, bind *bindTarget, stream io.Reader) error {
	if err := h.ensureHelper(); err != nil {
		return fmt.Errorf("helper image: %w", err)
	}
	if bind != nil {
		if err := h.rs.revetBind(mv.Name, h.restoredStackDirs(), *bind); err != nil {
			return err
		}
		return RestoreBind(h.ctx, h.env.Engine, stack, *bind, stream)
	}
	labels := composeVolumeLabels(h.stacks[stack].entry.Project, mv.Name)
	return RestoreVolume(h.ctx, h.env.Engine, stack, stacks.Mount{Kind: mv.Kind, Source: mv.Name, Target: mv.Target}, labels, stream)
}

// images loads one archived image, stored under images/<stack>/<image key>/.
func (h *hostRestore) images(hdr *tar.Header, body io.Reader) error {
	parts := strings.SplitN(strings.TrimPrefix(hdr.Name, imagesPrefix), "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return nil
	}
	stack, key := parts[0], parts[1]
	stream := TarStreamUnder(h.r, imagesPrefix+stack+"/"+key+"/", hdr, body)
	if t := h.target(stack); t.skipped != "" {
		return drain(stream)
	}
	ms := h.manifestStack(stack)
	i := slices.IndexFunc(ms.Images, func(ref string) bool { return ImageKey(ref) == key })
	if i < 0 || failedInManifest(h.manifest, "image", stack, ms.Images[i]) {
		h.unlisted("image", stack, key)
		return drain(stream)
	}
	ref := ms.Images[i]
	h.out("loading image " + ref + " of " + stack)
	err := loadImageChecked(h.ctx, h.env.Engine, ref, stream)
	if derr := drain(stream); derr != nil {
		return derr
	}
	if err != nil {
		h.items = append(h.items, ItemResult{Kind: "image", Stack: stack, Name: ref, Status: itemFailed, Note: err.Error()})
		return nil
	}
	h.items = append(h.items, ItemResult{Kind: "image", Stack: stack, Name: ref, Status: itemOK})
	return nil
}

func (h *hostRestore) closeRoots() {
	for _, t := range h.stacks {
		if t.root != nil {
			t.root.Close()
		}
	}
	h.rs.close()
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
			items = append(items, ItemResult{Kind: "stack-files", Stack: name, Name: t.dir, Status: itemSkipped, Note: t.skipped})
		case t.root != nil:
			items = append(items, t.failed...)
			status := itemOK
			if len(t.failed) > 0 {
				status = itemFailed
			}
			items = append(items, ItemResult{Kind: "stack-files", Stack: name, Name: t.dir, Status: status})
		}
	}
	items = append(items, h.libraryItems...)
	if h.library != nil {
		items = append(items, ItemResult{Kind: "base", Name: "images-library", Status: itemOK})
	}
	return append(items, h.items...)
}
