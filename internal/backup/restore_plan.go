package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
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
	Origin                       stacks.Origin
	// Overwrites means a stack named TargetName already exists here.
	Overwrites      bool
	FileDiffs       []FileDiff
	Volumes         []PlannedVolume
	Images          []string
	MissingNetworks []engine.NetworkSpec
	CrossHost       bool
	Warnings        []string
}

// PlanEnv is the host state a plan is made against.
type PlanEnv struct {
	Registry     *stacks.Registry
	AllowedRoots []string
	StacksDir    string
	SelfSlug     string
	Networks     func(ctx context.Context) ([]engine.Network, error)
	ReadCurrent  func(path string) ([]byte, error)
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
	}
	if plan.Origin == "" {
		plan.Origin = stacks.Origin(ms.Origin)
	}
	composePath := archivedEntry.ComposePath
	if composePath == "" {
		composePath = ms.ComposePath
	}

	existing, err := lookupStack(ctx, env.Registry, ms.Name)
	if err != nil {
		return StackRestorePlan{}, err
	}
	if req.NewName != "" {
		if err := stacks.ValidateName(req.NewName); err != nil {
			return StackRestorePlan{}, err
		}
		plan.TargetName = req.NewName
		existing, err = lookupStack(ctx, env.Registry, req.NewName)
		if err != nil {
			return StackRestorePlan{}, err
		}
		if existing != nil {
			return StackRestorePlan{}, fmt.Errorf("%w: %q", stacks.ErrStackExists, req.NewName)
		}
	}
	plan.Overwrites = existing != nil

	plan.TargetDir, err = targetDir(filepath.Dir(composePath), plan.TargetName, existing, env)
	if err != nil {
		return StackRestorePlan{}, err
	}

	if req.Files {
		diff, err := composeDiff(filepath.Join(plan.TargetDir, filepath.Base(composePath)), env)
		if err != nil {
			return StackRestorePlan{}, err
		}
		plan.FileDiffs = []FileDiff{diff}
	}
	if req.Volumes {
		plan.Volumes, err = planVolumes(ms.Volumes, req.VolumeKeys, env.AllowedRoots)
		if err != nil {
			return StackRestorePlan{}, err
		}
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
	plan.MissingNetworks, err = missingNetworks(ctx, ms.Networks, env)
	if err != nil {
		return StackRestorePlan{}, err
	}
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

// targetDir keeps the original directory when it is inside the allowed roots
// and either free or already the target stack's own directory; otherwise the
// stack goes to <StacksDir>/<name>.
func targetDir(original, name string, existing *stacks.Entry, env PlanEnv) (string, error) {
	original = filepath.Clean(original)
	if filepath.IsAbs(original) && underAnyRoot(original, env.AllowedRoots) {
		if existing != nil && filepath.Clean(filepath.Dir(existing.ComposePath)) == original {
			return original, nil
		}
		free, err := dirFree(original)
		if err != nil {
			return "", err
		}
		if free {
			return original, nil
		}
	}
	return filepath.Join(env.StacksDir, name), nil
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
// absolute paths component-wise rather than by string prefix.
func underRoot(path, root string) bool {
	if !filepath.IsAbs(path) || !filepath.IsAbs(root) {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func composeDiff(target string, env PlanEnv) (FileDiff, error) {
	diff := FileDiff{Path: target}
	if env.ReadCurrent != nil {
		old, err := env.ReadCurrent(target)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return FileDiff{}, fmt.Errorf("read current %s: %w", target, err)
		}
		diff.Old = string(old)
	}
	if env.ReadArchived != nil {
		rel := filepath.Base(target)
		archived, err := env.ReadArchived(rel)
		if err != nil {
			return FileDiff{}, fmt.Errorf("read archived %s: %w", rel, err)
		}
		diff.New = string(archived)
	}
	return diff, nil
}

func planVolumes(vols []ManifestVolume, keys []string, roots []string) ([]PlannedVolume, error) {
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
		// Relative binds follow the compose file into the target directory.
		if v.Kind == volumeKindBind && filepath.IsAbs(v.Name) && !underAnyRoot(v.Name, roots) {
			pv.Skip, pv.Reason = true, reasonOutsideRoots
		}
		out = append(out, pv)
	}
	for k := range unmatched {
		return nil, fmt.Errorf("volume %q is not in this archive", k)
	}
	return out, nil
}

// missingNetworks returns the archived specs of networks the stack uses that
// do not exist on this host.
func missingNetworks(ctx context.Context, used []string, env PlanEnv) ([]engine.NetworkSpec, error) {
	if len(used) == 0 || env.Networks == nil {
		return nil, nil
	}
	present, err := env.Networks(ctx)
	if err != nil {
		return nil, fmt.Errorf("list networks: %w", err)
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
	for _, name := range used {
		if spec, ok := specs[name]; ok && !have[name] {
			out = append(out, spec)
		}
	}
	return out, nil
}

// checkArchiveVersion refuses an archive newer than the running binary. A
// dev build on either side cannot be ordered, so it is never refused.
func checkArchiveVersion(archive, installed string) error {
	if isDevVersion(archive) || isDevVersion(installed) {
		return nil
	}
	a, err := parseSemver(archive)
	if err != nil {
		return fmt.Errorf("archive version: %w", err)
	}
	i, err := parseSemver(installed)
	if err != nil {
		return fmt.Errorf("installed version: %w", err)
	}
	for n := range a {
		if a[n] > i[n] {
			return fmt.Errorf("%w: archive %s, installed %s", ErrArchiveNewer, archive, installed)
		}
		if a[n] < i[n] {
			return nil
		}
	}
	return nil
}

// isDevVersion matches "dev" and the "dev-dirty" form version.Get reports.
func isDevVersion(v string) bool { return v == "dev" || strings.HasPrefix(v, "dev-") }

// parseSemver reads vX.Y.Z, ignoring a leading "v" and any pre-release or
// build suffix.
func parseSemver(v string) ([3]int, error) {
	s := strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	var out [3]int
	if len(parts) != 3 {
		return out, fmt.Errorf("unrecognised version %q", v)
	}
	for n, p := range parts {
		x, err := strconv.Atoi(p)
		if err != nil || x < 0 {
			return out, fmt.Errorf("unrecognised version %q", v)
		}
		out[n] = x
	}
	return out, nil
}
