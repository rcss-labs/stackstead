package backup

import (
	"context"
	"errors"
	"fmt"
	"github.com/rcss-labs/stackstead/internal/secret"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/stacks"
	"github.com/rcss-labs/stackstead/internal/store"
	"github.com/rcss-labs/stackstead/internal/version"
)

var (
	// ErrArchiveNewer refuses an archive written by a newer Stackstead.
	ErrArchiveNewer      = errors.New("archive is from a newer Stackstead version")
	ErrStackNotInArchive = errors.New("stack is not in this archive")
	// ErrTargetOccupied means <StacksDir>/<name> holds files of no registered stack.
	ErrTargetOccupied = errors.New("target directory is not empty and belongs to no stack of that name")
)

// Plan warnings and skip reasons, shown verbatim in the preview.
const (
	warnVolumesReplaced = "volume contents are replaced and not kept"
	reasonOutsideRoots  = "outside allowed roots"
	volumeKindBind      = "bind"
)

// StackRestoreRequest is what the user picked in the restore preview.
type StackRestoreRequest struct {
	Archive ArchiveRef
	Stack   string
	// NewName restores under a different name, which also becomes the
	// compose project name. Empty keeps the archived name.
	NewName                string
	Files, Volumes, Images bool
	// VolumeKeys limits the volumes restored; empty means all in the archive.
	VolumeKeys []string
	// Start starts the restored stack once it is in place. False leaves it
	// stopped, which is also what an omitted API field means.
	Start bool
	// Passphrase opens an encrypted archive; empty falls back to the key file.
	Passphrase secret.Secret
	// AllowUnencrypted accepts an unauthenticated plaintext archive. Planning
	// never needs it: it only reads.
	AllowUnencrypted bool
}

// FileDiff pairs a file's current content on this host with its archived content.
type FileDiff struct{ Path, Old, New string }

// PlannedVolume is one archived volume or bind mount and whether it is restored.
type PlannedVolume struct {
	Key, Kind, Name, Target string
	Bytes                   int64
	Skip                    bool
	Reason                  string
}

// StackRestorePlan is the preview of a single-stack restore.
type StackRestorePlan struct {
	Stack, TargetName, TargetDir string
	// ComposePath is the compose file the restore writes: the existing
	// stack's file when overwriting, else the archived file name in TargetDir.
	ComposePath string
	// Project is the compose project the restored stack is registered with.
	Project string
	Origin  stacks.Origin
	// Overwrites means a stack named TargetName already exists here.
	Overwrites      bool
	FileDiffs       []FileDiff
	Volumes         []PlannedVolume
	Images          []string
	MissingNetworks []engine.NetworkSpec
	CrossHost       bool
	Warnings        []string
	// Encrypted and KeyID say how the archive is protected.
	Encrypted bool
	KeyID     string
	// Foreign is true when the archive was written by another instance.
	Foreign bool
	// Risks are the host-reaching settings of the archived compose files.
	Risks []ComposeRisk
}

// PlanEnv is the host state a plan is made against.
type PlanEnv struct {
	Registry     *stacks.Registry
	AllowedRoots []string
	StacksDir    string
	SelfSlug     string
	// SelfInstanceID is this host's instance id, to tell a foreign archive.
	SelfInstanceID string
	Networks       func(ctx context.Context) ([]engine.Network, error)
	ReadCurrent    func(path string) ([]byte, error)
	// ReadArchived reads a file of the stack's directory from the staged
	// archive, by path relative to that directory. Nil leaves FileDiff.New empty.
	ReadArchived func(rel string) ([]byte, error)
	// ArchivedNetworks are the network specs from the archive's networks.json.
	ArchivedNetworks []engine.NetworkSpec
}

// PlanStackRestore works out where and how a stack from m would be restored.
// It never reads the archive: archivedEntry is the stack's row from the
// archived database (see ArchivedEntry).
func PlanStackRestore(ctx context.Context, m Manifest, archivedEntry stacks.Entry, req StackRestoreRequest, env PlanEnv) (StackRestorePlan, error) {
	if err := checkArchiveVersion(m.StacksteadVersion, version.Version); err != nil {
		return StackRestorePlan{}, err
	}
	ms, ok := findManifestStack(m, req.Stack)
	if !ok {
		return StackRestorePlan{}, fmt.Errorf("%w: %q", ErrStackNotInArchive, req.Stack)
	}

	plan := StackRestorePlan{
		Stack:      ms.Name,
		TargetName: ms.Name,
		Origin:     archivedEntry.Origin,
		CrossHost:  m.HostSlug != env.SelfSlug,
		Foreign:    m.InstanceID == "" || m.InstanceID != env.SelfInstanceID,
	}
	if plan.Origin == "" {
		plan.Origin = stacks.Origin(ms.Origin)
	}
	composePath := archivedEntry.ComposePath
	if composePath == "" {
		composePath = ms.ComposePath
	}

	if req.NewName != "" {
		plan.TargetName = req.NewName
	}
	// The archived name is untrusted input: it becomes a directory name.
	if err := stacks.ValidateName(plan.TargetName); err != nil {
		return StackRestorePlan{}, fmt.Errorf("restore as %q: %w", plan.TargetName, err)
	}
	existing, err := lookupStack(ctx, env.Registry, plan.TargetName)
	if err != nil {
		return StackRestorePlan{}, err
	}
	if existing != nil && req.NewName != "" {
		return StackRestorePlan{}, fmt.Errorf("%w: %q", stacks.ErrStackExists, req.NewName)
	}
	plan.Overwrites = existing != nil

	originalDir := filepath.Clean(filepath.Dir(composePath))
	var relocated bool
	plan.TargetDir, relocated, err = targetDir(originalDir, plan.TargetName, existing, env)
	if err != nil {
		return StackRestorePlan{}, err
	}
	archivedProject := archivedEntry.Project
	if archivedProject == "" {
		archivedProject = ms.Project
	}
	// R17: an overwrite writes the archived compose content into the existing
	// stack's compose file, whatever it is called.
	plan.ComposePath = filepath.Join(plan.TargetDir, filepath.Base(composePath))
	switch {
	case existing != nil:
		plan.Origin = existing.Origin
		plan.ComposePath = existing.ComposePath
		plan.Project = existing.Project
	case req.NewName != "" || archivedProject == "":
		plan.Project = plan.TargetName
	default:
		plan.Project = archivedProject
	}
	if existing == nil && relocated {
		plan.Origin = stacks.OriginManaged
	}
	if existing == nil {
		// A new stack's volumes are <project>_x: restoring them into a
		// project another stack owns would overwrite that stack's data.
		if err := checkProjectFree(ctx, env.Registry, plan.Project); err != nil {
			return StackRestorePlan{}, err
		}
	}

	if req.Files {
		diff, err := composeDiff(plan.ComposePath, filepath.Base(composePath), env)
		if err != nil {
			return StackRestorePlan{}, err
		}
		plan.FileDiffs = []FileDiff{diff}
	}
	if req.Volumes {
		plan.Volumes, err = planVolumes(ms.Volumes, req.VolumeKeys, env.AllowedRoots, originalDir, plan.TargetDir)
		if err != nil {
			return StackRestorePlan{}, err
		}
		renameProjectVolumes(plan.Volumes, archivedProject, plan.Project)
		for _, v := range plan.Volumes {
			if !v.Skip {
				plan.Warnings = append(plan.Warnings, warnVolumesReplaced)
				break
			}
		}
	}
	if req.Images {
		plan.Images = append([]string(nil), ms.Images...)
	}
	var netWarnings []string
	plan.MissingNetworks, netWarnings, err = missingNetworks(ctx, ms.Networks, env)
	if err != nil {
		return StackRestorePlan{}, err
	}
	plan.Warnings = append(plan.Warnings, netWarnings...)
	return plan, nil
}

// ArchivedEntry reads a stack's registry row from a staged copy of the
// archived database, opened read-only and never migrated.
func ArchivedEntry(ctx context.Context, stagedDBPath, stack string) (stacks.Entry, error) {
	db, err := store.OpenExisting(stagedDBPath, store.OpenOptions{ReadOnly: true, AllowOlderSchema: true})
	if err != nil {
		return stacks.Entry{}, fmt.Errorf("open archived database: %w", err)
	}
	defer db.Close()
	return stacks.NewRegistry(db, time.Now).Get(ctx, stack)
}

func findManifestStack(m Manifest, name string) (ManifestStack, bool) {
	for _, s := range m.Stacks {
		if s.Name == name {
			return s, true
		}
	}
	return ManifestStack{}, false
}

// lookupStack returns the registered stack called name, or nil.
func lookupStack(ctx context.Context, reg *stacks.Registry, name string) (*stacks.Entry, error) {
	e, err := reg.Get(ctx, name)
	if errors.Is(err, stacks.ErrStackNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// targetDir picks where the stack's files go, and reports whether the stack
// is relocated into <StacksDir>:
//   - overwriting a registered stack keeps that stack's directory, even
//     outside AllowedRoots: the host already runs it from there;
//   - otherwise the original directory, when inside AllowedRoots and free;
//   - otherwise <StacksDir>/<name>, which must be inside AllowedRoots and free.
func targetDir(original, name string, existing *stacks.Entry, env PlanEnv) (string, bool, error) {
	if existing != nil {
		return filepath.Clean(filepath.Dir(existing.ComposePath)), false, nil
	}
	if filepath.IsAbs(original) && underAnyRoot(original, env.AllowedRoots) {
		free, err := dirFree(original)
		if err != nil {
			return "", false, err
		}
		if free {
			return original, false, nil
		}
	}
	fallback := filepath.Join(env.StacksDir, name)
	if !underAnyRoot(fallback, env.AllowedRoots) {
		return "", false, fmt.Errorf("target directory %s is outside the allowed roots", fallback)
	}
	free, err := dirFree(fallback)
	if err != nil {
		return "", false, err
	}
	if !free {
		return "", false, fmt.Errorf("%w: %s", ErrTargetOccupied, fallback)
	}
	return fallback, true, nil
}

// checkProjectFree fails with stacks.ErrProjectTaken when a registered stack
// uses project.
func checkProjectFree(ctx context.Context, reg *stacks.Registry, project string) error {
	entries, err := reg.List(ctx)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Project == project {
			return fmt.Errorf("%w: compose project %q is already used by stack %s", stacks.ErrProjectTaken, project, e.Name)
		}
	}
	return nil
}

// dirFree reports whether dir does not exist or is empty.
func dirFree(dir string) (bool, error) {
	f, err := os.Open(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("check target directory: %w", err)
	}
	defer f.Close()
	names, err := f.Readdirnames(1)
	if len(names) > 0 {
		return false, nil
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("check target directory: %w", err)
	}
	return true, nil
}

func underAnyRoot(path string, roots []string) bool {
	for _, root := range roots {
		if underRoot(path, root) {
			return true
		}
	}
	return false
}

// underRoot reports whether path is root or inside it, comparing cleaned
// absolute paths component-wise rather than by string prefix. It holds when
// either the paths as written or the paths with symlinks resolved (as far as
// they exist) are nested, so a symlinked allowed root matches the resolved
// paths the database stores.
func underRoot(path, root string) bool {
	_, ok := relUnder(path, root)
	return ok
}

// relUnder returns path relative to root when path is inside it, as written or
// with symlinks resolved on both sides.
func relUnder(path, root string) (string, bool) {
	if !filepath.IsAbs(path) || !filepath.IsAbs(root) {
		return "", false
	}
	if rel, ok := plainRel(path, root); ok {
		return rel, true
	}
	return plainRel(evalExisting(path), evalExisting(root))
}

func plainRel(path, root string) (string, bool) {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// evalExisting resolves symlinks in the longest existing prefix of p and
// appends the rest unchanged.
func evalExisting(p string) string {
	p = filepath.Clean(p)
	var rest []string
	for cur := p; ; cur = filepath.Dir(cur) {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(append([]string{resolved}, rest...)...)
		}
		if filepath.Dir(cur) == cur {
			return p
		}
		rest = append([]string{filepath.Base(cur)}, rest...)
	}
}

// composeDiff pairs the current content of target with the archived compose
// file archivedRel (relative to the archived stack directory).
func composeDiff(target, archivedRel string, env PlanEnv) (FileDiff, error) {
	diff := FileDiff{Path: target}
	if env.ReadCurrent != nil {
		old, err := env.ReadCurrent(target)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return FileDiff{}, fmt.Errorf("read current %s: %w", target, err)
		}
		diff.Old = string(old)
	}
	if env.ReadArchived != nil {
		archived, err := env.ReadArchived(archivedRel)
		if err != nil {
			return FileDiff{}, fmt.Errorf("read archived %s: %w", archivedRel, err)
		}
		diff.New = string(archived)
	}
	return diff, nil
}

// planVolumes selects the requested volumes. Bind sources inside the archived
// compose directory follow the compose file to targetDir; other absolute
// binds outside the allowed roots are skipped.
func planVolumes(vols []ManifestVolume, keys, roots []string, originalDir, targetDir string) ([]PlannedVolume, error) {
	unmatched := map[string]bool{}
	for _, k := range keys {
		unmatched[k] = true
	}
	var out []PlannedVolume
	for _, v := range vols {
		if len(keys) > 0 && !unmatched[v.Key] {
			continue
		}
		delete(unmatched, v.Key)
		pv := PlannedVolume{Key: v.Key, Kind: v.Kind, Name: v.Name, Target: v.Target, Bytes: v.Bytes}
		if v.Kind == volumeKindBind && filepath.IsAbs(v.Name) {
			if rel, ok := relUnder(v.Name, originalDir); ok {
				pv.Name = filepath.Join(targetDir, rel)
			} else if !underAnyRoot(v.Name, roots) {
				pv.Skip, pv.Reason = true, reasonOutsideRoots
			}
		}
		out = append(out, pv)
	}
	if len(unmatched) > 0 {
		missing := make([]string, 0, len(unmatched))
		for k := range unmatched {
			missing = append(missing, strconv.Quote(k))
		}
		sort.Strings(missing)
		return nil, fmt.Errorf("volumes not in this archive: %s", strings.Join(missing, ", "))
	}
	return out, nil
}

// renameProjectVolumes moves the named volumes compose made for the archived
// project (<project>_<name>) to the target project, so a restore under a new
// name never writes into the original stack's volumes. Volumes with explicit
// names are shared by design and keep them.
func renameProjectVolumes(vols []PlannedVolume, from, to string) {
	if from == "" || from == to {
		return
	}
	for i, v := range vols {
		if v.Kind != "volume" {
			continue
		}
		if short, ok := strings.CutPrefix(v.Name, from+"_"); ok && short != "" {
			vols[i].Name = to + "_" + short
		}
	}
}

// missingNetworks returns the archived specs of networks the stack uses that
// do not exist on this host, plus warnings for those it cannot recreate.
func missingNetworks(ctx context.Context, used []string, env PlanEnv) ([]engine.NetworkSpec, []string, error) {
	if len(used) == 0 {
		return nil, nil, nil
	}
	if env.Networks == nil {
		return nil, []string{"networks were not checked: " + strings.Join(used, ", ")}, nil
	}
	present, err := env.Networks(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("list networks: %w", err)
	}
	have := map[string]bool{}
	for _, n := range present {
		have[n.Name] = true
	}
	specs := map[string]engine.NetworkSpec{}
	for _, s := range env.ArchivedNetworks {
		specs[s.Name] = s
	}
	var out []engine.NetworkSpec
	var warnings []string
	for _, name := range used {
		if have[name] {
			continue
		}
		if spec, ok := specs[name]; ok {
			out = append(out, spec)
		} else {
			warnings = append(warnings, fmt.Sprintf("network %q is missing here and not in the archive", name))
		}
	}
	return out, warnings, nil
}

// checkArchiveVersion refuses an archive newer than the running binary. A
// dev build on either side, or an installed version that is not a release
// number, cannot be ordered, so it is never refused; an archive from a
// non-dev build must carry a parsable version.
func checkArchiveVersion(archive, installed string) error {
	if archive == installed || isDevVersion(archive) || isDevVersion(installed) {
		return nil
	}
	a, err := parseSemver(archive)
	if err != nil {
		return fmt.Errorf("archive version: %w", err)
	}
	i, err := parseSemver(installed)
	if err != nil {
		return nil // an unstamped or custom build cannot be ordered, like dev
	}
	for n := range a.core {
		if a.core[n] > i.core[n] {
			return fmt.Errorf("%w: archive %s, installed %s", ErrArchiveNewer, archive, installed)
		}
		if a.core[n] < i.core[n] {
			return nil
		}
	}
	// A release is newer than a pre-release of the same version.
	if i.pre && !a.pre {
		return fmt.Errorf("%w: archive %s, installed %s", ErrArchiveNewer, archive, installed)
	}
	return nil
}

// isDevVersion matches "dev" and the "dev-dirty" form version.Get reports.
func isDevVersion(v string) bool { return v == "dev" || strings.HasPrefix(v, "dev-") }

type semver struct {
	core [3]int
	pre  bool
}

// parseSemver reads vX.Y.Z, ignoring a leading "v" and the content of any
// pre-release or build suffix.
func parseSemver(v string) (semver, error) {
	var out semver
	s := strings.TrimPrefix(v, "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	if i := strings.IndexByte(s, '-'); i >= 0 {
		s, out.pre = s[:i], true
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return out, fmt.Errorf("unrecognised version %q", v)
	}
	for n, p := range parts {
		x, err := strconv.Atoi(p)
		if err != nil || x < 0 {
			return out, fmt.Errorf("unrecognised version %q", v)
		}
		out.core[n] = x
	}
	return out, nil
}
