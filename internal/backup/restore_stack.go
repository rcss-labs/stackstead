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

	"github.com/rcss-labs/stackstead/internal/actionlog"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/peers"
	"github.com/rcss-labs/stackstead/internal/stacks"
	"github.com/rcss-labs/stackstead/internal/store"
)

const (
	restoreOpAction   = "restore"
	restoreActionKind = "backup.restore"
	// preRestoreDir, under <DataDir>/backups, keeps the files a restore replaced.
	preRestoreDir = "pre-restore"
	// restoreStagingPattern names the temp dirs staging an archived database.
	restoreStagingPattern = "restore-stack-*"
	// maxArchivedCompose bounds the archived compose file read for a preview diff.
	maxArchivedCompose = 4 << 20
)

var (
	// ErrBadArchiveRef rejects an unknown destination or an archive name that
	// is not one Stackstead writes.
	ErrBadArchiveRef = errors.New("backup: unknown destination or archive name")
	// ErrNothingToRestore rejects a request that selects no part of the stack.
	ErrNothingToRestore = errors.New("backup: choose the files, the volumes or the images to restore")
	// ErrFilesRequired rejects restoring only data into a stack that does not
	// exist here: without its files there is nothing to register.
	ErrFilesRequired = errors.New("backup: a stack that does not exist here is restored with its files")
)

// RestoreControl is the part of stacks.Service a single-stack restore uses.
// Stop and start bypass the operations manager, since the restore itself
// holds the stack's operation slot.
type RestoreControl interface {
	StopForRestore(ctx context.Context, stack string, out func(string)) error
	StartRestored(ctx context.Context, stack string, out func(string)) error
	RegisterRestored(ctx context.Context, e stacks.Entry) error
}

var _ RestoreControl = (*stacks.Service)(nil)

// ArchiveManifest returns the manifest of an archive: its sidecar, or the
// archive's own manifest.json when there is no sidecar.
func (r *Runner) ArchiveManifest(ctx context.Context, dest, name string) (Manifest, error) {
	d, err := r.destination(ctx, dest, name)
	if err != nil {
		return Manifest{}, err
	}
	m, _, err := readArchiveManifest(ctx, d, name)
	return m, err
}

// PlanRestore previews a single-stack restore. It reads the archive's base/
// section only and changes nothing.
func (r *Runner) PlanRestore(ctx context.Context, req StackRestoreRequest) (StackRestorePlan, error) {
	sr, err := r.openStackRestore(ctx, req, func(string) {})
	if err != nil {
		return StackRestorePlan{}, err
	}
	defer sr.close()
	if err := sr.planWithDiff(); err != nil {
		return StackRestorePlan{}, err
	}
	return sr.plan, nil
}

// RestoreStack restores one stack from an archive as an operation on the
// target stack's own operation key, so it excludes every other operation on
// that stack. The archive is checked and the restore planned before the
// operation starts, so a refused restore changes nothing. It returns
// stacks.ErrBusy while the target stack has an operation running.
func (r *Runner) RestoreStack(ctx context.Context, req StackRestoreRequest, user string) (stacks.Operation, error) {
	if user == "" {
		user = systemUser
	}
	target := req.Stack
	if req.NewName != "" {
		target = req.NewName
	}
	if err := stacks.ValidateName(target); err != nil {
		return stacks.Operation{}, err
	}
	if err := r.preflight(ctx, req); err != nil {
		return stacks.Operation{}, err
	}
	opIDs := make(chan string, 1)
	op, err := r.d.Ops.StartTimeout(target, restoreOpAction, runOpTimeout, func(ctx context.Context, out func(string)) error {
		return r.restore(ctx, req, target, user, <-opIDs, out)
	})
	if err != nil {
		return op, err
	}
	opIDs <- op.ID
	return op, nil
}

// preflight plans the restore against the archive's database and networks
// without changing anything.
func (r *Runner) preflight(ctx context.Context, req StackRestoreRequest) error {
	sr, err := r.openStackRestore(ctx, req, func(string) {})
	if err != nil {
		return err
	}
	defer sr.close()
	return sr.planAtStackFiles()
}

func (r *Runner) restore(ctx context.Context, req StackRestoreRequest, target, user, opID string, out func(string)) error {
	sr, err := r.openStackRestore(ctx, req, out)
	if err == nil {
		defer sr.close()
		err = sr.execute()
	}
	var items []ItemResult
	if sr != nil {
		items = sr.items
	}
	failed := failedItems(items)
	result, detail := runStatusSuccess, restoreDetail(items)
	switch {
	case err != nil:
		result, detail = runStatusFailed, err.Error()
		if len(failed) > 0 {
			detail += "; " + restoreDetail(failed)
		}
	case len(failed) > 0:
		result = runStatusFailed
		err = fmt.Errorf("restore of %s finished with %d failed item(s): %s", target, len(failed), detail)
	}
	out("restore " + result)
	entry := actionlog.Entry{
		TS: r.d.Now(), Username: user, Kind: restoreActionKind, Scope: target,
		Target: req.Archive.Name, Detail: detail, Result: result, OperationID: opID,
	}
	if aerr := r.d.Actions.Append(context.WithoutCancel(ctx), entry); aerr != nil {
		r.d.Logger.Error("record restore action", "stack", target, "err", aerr)
	}
	return err
}

// failedItems returns the failed items that fail the restore. A network that
// could not be created is only a warning: the stack may still start.
func failedItems(items []ItemResult) []ItemResult {
	var out []ItemResult
	for _, it := range items {
		if it.Status == itemFailed && it.Kind != "network" {
			out = append(out, it)
		}
	}
	return out
}

func restoreDetail(items []ItemResult) string {
	var parts []string
	for _, it := range items {
		if it.Status == itemOK {
			continue
		}
		s := it.Kind
		if it.Name != "" {
			s += " " + it.Name
		}
		parts = append(parts, s+" "+it.Status+": "+it.Note)
	}
	if len(parts) == 0 {
		return "restored"
	}
	return strings.Join(parts, "; ")
}

// destination opens the destination an archive is read from, after checking
// that dest is a known destination and name is an archive name without any
// path in it.
func (r *Runner) destination(ctx context.Context, dest, name string) (Destination, error) {
	if !ValidArchiveName(name) {
		return nil, fmt.Errorf("%w: archive %q", ErrBadArchiveRef, name)
	}
	if dest != destLocal && dest != destS3 {
		return nil, fmt.Errorf("%w: destination %q", ErrBadArchiveRef, dest)
	}
	cfg, err := r.d.Config.Load(ctx)
	if err != nil {
		return nil, err
	}
	if dest == destLocal {
		if cfg.Local.Path == "" {
			return nil, errors.New("backup: no local backup folder is configured")
		}
		return r.newLocal(cfg.Local), nil
	}
	if cfg.S3.Bucket == "" {
		return nil, errors.New("backup: no S3 bucket is configured")
	}
	return r.newS3(cfg.S3, r.d.HTTP), nil
}

// ValidArchiveName accepts only names Stackstead writes, with no path in them.
func ValidArchiveName(name string) bool {
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return false
	}
	_, _, ok := ParseArchiveName(name)
	return ok
}

// readArchiveManifest returns the sidecar manifest, or reads the archive to
// its end for manifest.json when there is no sidecar. fromSidecar tells which.
func readArchiveManifest(ctx context.Context, d Destination, name string) (m Manifest, fromSidecar bool, err error) {
	if m, err := d.ReadSidecar(ctx, name); err == nil {
		return m, true, nil
	}
	rc, err := d.Open(ctx, name)
	if err != nil {
		return Manifest{}, false, err
	}
	defer rc.Close()
	ar, err := NewReader(rc)
	if err != nil {
		return Manifest{}, false, damaged(err)
	}
	for {
		hdr, body, err := ar.Next()
		if errors.Is(err, io.EOF) {
			return Manifest{}, false, damaged(errors.New("manifest.json is missing"))
		}
		if err != nil {
			return Manifest{}, false, damaged(err)
		}
		if hdr.Name == ManifestEntryName {
			m, err := decodeArchiveManifest(body)
			return m, false, err
		}
	}
}

// stackRestore is one single-stack restore, from opening the archive to
// starting the stack again.
type stackRestore struct {
	r   *Runner
	ctx context.Context
	req StackRestoreRequest
	out func(string)

	manifest    Manifest
	fromSidecar bool
	ms          ManifestStack
	rc          io.Closer
	ar          *Reader
	stageDir    string
	archived    stacks.Entry // the stack's row in the archived database, if any
	archivedRel string       // the compose file's name in the archived stack directory
	networks    []engine.NetworkSpec
	plan        StackRestorePlan
	eof         bool

	rs          *rootSet
	files       *os.Root
	filesFailed []ItemResult
	filesSeen   bool
	pastFiles   bool
	stopped     bool
	registered  bool
	// dataBlocked stops volume and image restores for a new stack that was
	// not registered: its project may belong to another stack.
	dataBlocked bool
	helperTried bool
	helperErr   error
	volumesDone map[string]bool
	imagesDone  map[string]bool
	sawManifest bool
	items       []ItemResult

	stackPrefix  string // base/stacks/<archived name>/
	volumePrefix string // volumes/<archived name>/
	imagePrefix  string // images/<archived name>/
}

// openStackRestore checks the archive's manifest, opens its stream and
// stages its database, which is always the first entry.
func (r *Runner) openStackRestore(ctx context.Context, req StackRestoreRequest, out func(string)) (*stackRestore, error) {
	if !req.Files && !req.Volumes && !req.Images {
		return nil, ErrNothingToRestore
	}
	d, err := r.destination(ctx, req.Archive.Dest, req.Archive.Name)
	if err != nil {
		return nil, err
	}
	m, fromSidecar, err := readArchiveManifest(ctx, d, req.Archive.Name)
	if err != nil {
		return nil, err
	}
	if err := checkManifest(m, r.d.Version); err != nil {
		return nil, err
	}
	ms, ok := findManifestStack(m, req.Stack)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrStackNotInArchive, req.Stack)
	}
	sr := &stackRestore{
		r: r, ctx: ctx, req: req, out: out, manifest: m, fromSidecar: fromSidecar, ms: ms,
		volumesDone: map[string]bool{}, imagesDone: map[string]bool{},
		stackPrefix:  stacksPrefix + req.Stack + "/",
		volumePrefix: volumesPrefix + req.Stack + "/",
		imagePrefix:  imagesPrefix + req.Stack + "/",
	}
	rc, err := d.Open(ctx, req.Archive.Name)
	if err != nil {
		return nil, err
	}
	sr.rc = rc
	if sr.ar, err = NewReader(rc); err != nil {
		sr.close()
		return nil, damaged(err)
	}
	if err := sr.stageDatabase(); err != nil {
		sr.close()
		return nil, err
	}
	return sr, nil
}

func (sr *stackRestore) close() {
	if sr.files != nil {
		sr.files.Close()
	}
	if sr.rs != nil {
		sr.rs.close()
	}
	if sr.rc != nil {
		sr.rc.Close()
	}
	if sr.stageDir != "" {
		_ = os.RemoveAll(sr.stageDir)
	}
}

// stageDatabase copies base/stackstead.db into a temporary directory under
// the data dir, verifies it and reads the stack's row from it.
func (sr *stackRestore) stageDatabase() error {
	hdr, body, err := sr.ar.Next()
	if err != nil {
		return damaged(fmt.Errorf("read %s: %w", entryDB, err))
	}
	if hdr.Name != entryDB {
		return damaged(fmt.Errorf("the first entry is %q, not %s", hdr.Name, entryDB))
	}
	if sr.stageDir, err = os.MkdirTemp(sr.r.d.DataDir, restoreStagingPattern); err != nil {
		return fmt.Errorf("stage database: %w", err)
	}
	staged := filepath.Join(sr.stageDir, stagedDBName)
	f, err := os.OpenFile(staged, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("stage database: %w", err)
	}
	_, err = io.Copy(f, body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return damaged(fmt.Errorf("stage database: %w", err))
	}
	if _, err := store.VerifyFile(sr.ctx, staged); err != nil {
		return fmt.Errorf("archived database: %w", err)
	}
	sr.archived, err = ArchivedEntry(sr.ctx, staged, sr.req.Stack)
	if err != nil && !errors.Is(err, stacks.ErrStackNotFound) {
		return err
	}
	composePath := sr.archived.ComposePath
	if composePath == "" {
		composePath = sr.ms.ComposePath
	}
	sr.archivedRel = filepath.Base(composePath)
	return nil
}

// next returns the next archive entry, or ok false at the end.
func (sr *stackRestore) next() (*tar.Header, io.Reader, bool, error) {
	if sr.eof {
		return nil, nil, false, nil
	}
	hdr, body, err := sr.ar.Next()
	if errors.Is(err, io.EOF) {
		sr.eof = true
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, damaged(err)
	}
	return hdr, body, true, nil
}

// readBaseHead reads config.yaml and networks.json, stopping before the
// first stack file (which it hands back to the reader).
func (sr *stackRestore) readBaseHead() error {
	for {
		hdr, body, ok, err := sr.next()
		if err != nil || !ok {
			return err
		}
		switch hdr.Name {
		case entryConfig:
		case entryNetworks:
			if sr.networks, err = decodeNetworks(body); err != nil {
				return err
			}
		case entryDB:
			return damaged(errors.New("archive holds a second database"))
		default:
			sr.ar.Unread(hdr, body)
			return nil
		}
	}
}

func (sr *stackRestore) planEnv() (PlanEnv, error) {
	ident, err := sr.r.d.Identity(sr.ctx)
	if err != nil {
		return PlanEnv{}, fmt.Errorf("read host identity: %w", err)
	}
	return PlanEnv{
		Registry: sr.r.d.Registry, AllowedRoots: sr.r.d.AllowedRoots, StacksDir: sr.r.d.StacksDir,
		SelfSlug: peers.Slugify(ident.Name), Networks: sr.r.d.Engine.Networks, ReadCurrent: os.ReadFile,
		ArchivedNetworks: sr.networks,
	}, nil
}

func (sr *stackRestore) makePlan(env PlanEnv) error {
	plan, err := PlanStackRestore(sr.ctx, sr.manifest, sr.archived, sr.req, env)
	if err != nil {
		return err
	}
	if !plan.Overwrites && !sr.req.Files {
		return ErrFilesRequired
	}
	sr.plan = plan
	return nil
}

// planAtStackFiles plans from the database and networks only, leaving the
// reader at the first stack file.
func (sr *stackRestore) planAtStackFiles() error {
	if err := sr.readBaseHead(); err != nil {
		return err
	}
	env, err := sr.planEnv()
	if err != nil {
		return err
	}
	return sr.makePlan(env)
}

// planWithDiff reads all of base/ to find the archived compose file, so the
// plan can show the diff against the current one.
func (sr *stackRestore) planWithDiff() error {
	if err := sr.readBaseHead(); err != nil {
		return err
	}
	var compose []byte
	found := false
	for {
		hdr, body, ok, err := sr.next()
		if err != nil {
			return err
		}
		if !ok || !strings.HasPrefix(hdr.Name, basePrefix) {
			break
		}
		if hdr.Name == sr.stackPrefix+sr.archivedRel && hdr.Typeflag == tar.TypeReg {
			if compose, err = io.ReadAll(io.LimitReader(body, maxArchivedCompose)); err != nil {
				return damaged(err)
			}
			found = true
		}
	}
	env, err := sr.planEnv()
	if err != nil {
		return err
	}
	env.ReadArchived = func(rel string) ([]byte, error) {
		if rel != sr.archivedRel || !found {
			return nil, fmt.Errorf("%s is not in the archive", rel)
		}
		return compose, nil
	}
	return sr.makePlan(env)
}

func (sr *stackRestore) item(it ItemResult) {
	sr.items = append(sr.items, it)
	if it.Status != itemOK {
		sr.out(fmt.Sprintf("%s: %s %s: %s", it.Status, it.Kind, it.Name, it.Note))
	}
}

// execute performs the restore. A stack stopped here, or a new stack that
// was running at backup time, is started on every path once it exists.
func (sr *stackRestore) execute() error {
	if err := sr.planAtStackFiles(); err != nil {
		return err
	}
	sr.rs = newRootSet(sr.r.d.AllowedRoots, sr.r.d.DataDir)
	defer sr.startAgain()
	if sr.plan.Overwrites {
		if err := sr.stopIfRunning(); err != nil {
			return err
		}
		if sr.req.Files {
			if err := sr.preRestoreCopy(); err != nil {
				return err
			}
		}
	}
	for {
		hdr, body, ok, err := sr.next()
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		if err := sr.entry(hdr, body); err != nil {
			return err
		}
	}
	if !sr.sawManifest {
		return damaged(errors.New("manifest.json is missing"))
	}
	sr.finishFiles()
	sr.reportMissing()
	return nil
}

// stopIfRunning stops the existing target stack when it is running.
func (sr *stackRestore) stopIfRunning() error {
	list, err := sr.r.d.Stacks.List(sr.ctx)
	if err != nil {
		return fmt.Errorf("list stacks: %w", err)
	}
	i := slices.IndexFunc(list, func(s stacks.Summary) bool { return s.Name == sr.plan.TargetName })
	if i < 0 || (list[i].Status != stacks.StatusRunning && list[i].Status != stacks.StatusPartial) {
		return nil
	}
	sr.out("stopping " + sr.plan.TargetName)
	// A failed stop may have stopped some services; the deferred start brings them back.
	sr.stopped = true
	if err := sr.r.d.Restore.StopForRestore(sr.ctx, sr.plan.TargetName, sr.out); err != nil {
		return fmt.Errorf("stop %s: %w", sr.plan.TargetName, err)
	}
	return nil
}

// startAgain starts the stack when it was stopped here, or when it is new and
// was running at backup time. It runs even when the restore is cancelled.
func (sr *stackRestore) startAgain() {
	if !sr.stopped && (!sr.registered || !sr.ms.WasRunning) {
		return
	}
	name := sr.plan.TargetName
	sr.out("starting " + name)
	startCtx, cancel := context.WithTimeout(context.WithoutCancel(sr.ctx), restartTimeout)
	defer cancel()
	if err := sr.r.d.Restore.StartRestored(startCtx, name, sr.out); err != nil {
		sr.item(ItemResult{Kind: "stack", Stack: name, Name: "start", Status: itemFailed, Note: "stack left stopped: " + err.Error()})
	}
}

// preRestoreCopy copies the stack's current files, without its bind-mount
// data, to <DataDir>/backups/pre-restore/<stack>-<UTC ts>/.
func (sr *stackRestore) preRestoreCopy() error {
	name := sr.plan.TargetName
	dst, err := freeDir(filepath.Join(sr.r.d.DataDir, "backups", preRestoreDir, name+"-"+sr.r.d.Now().UTC().Format(archiveTimeLayout)))
	if err != nil {
		return err
	}
	var skip []string
	mounts, err := sr.r.d.Stacks.Mounts(sr.ctx, name)
	if err != nil {
		sr.item(ItemResult{Kind: "stack-files", Stack: name, Name: "pre-restore copy", Status: itemSkipped,
			Note: "bind mounts unknown, so their data may be copied too: " + err.Error()})
	}
	for _, m := range mounts {
		// A file bind is part of the stack files (R35), so it is copied too.
		if m.Kind == volumeKindBind && !isFile(m.Source) {
			skip = append(skip, filepath.Clean(m.Source))
		}
	}
	sr.out("copying the current files of " + name + " to " + dst)
	if err := copyTree(sr.plan.TargetDir, dst, skip); err != nil {
		return fmt.Errorf("copy current files to %s: %w", dst, err)
	}
	return nil
}

// freeDir returns base, or base-2, base-3, ... when it is taken, so two
// restores within one second keep both copies.
func freeDir(base string) (string, error) {
	for n := 1; n < 1000; n++ {
		p := base
		if n > 1 {
			p = fmt.Sprintf("%s-%d", base, n)
		}
		if _, err := os.Lstat(p); errors.Is(err, fs.ErrNotExist) {
			return p, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("no free pre-restore directory for %s", base)
}

// copyTree copies regular files, directories and symlinks (as links) from
// src to a new dst, leaving out the paths in skip. A missing src copies nothing.
func copyTree(src, dst string, skip []string) error {
	if _, err := os.Stat(src); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != src && slices.Contains(skip, p) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch mode := info.Mode(); {
		case mode.IsDir():
			return os.Mkdir(out, mode.Perm()|0o700)
		case mode&fs.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(link, out)
		case mode.IsRegular():
			return copyFile(p, out, mode.Perm())
		}
		return nil // sockets, devices and pipes are not stack files
	})
}

func copyFile(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

func (sr *stackRestore) entry(hdr *tar.Header, body io.Reader) error {
	name := hdr.Name
	switch {
	case name == ManifestEntryName:
		return sr.checkArchiveManifest(body)
	case name == entryDB:
		return damaged(errors.New("archive holds a second database"))
	case strings.HasPrefix(name, sr.stackPrefix):
		if sr.req.Files {
			return sr.stackFile(hdr, body)
		}
		return nil
	case strings.HasPrefix(name, basePrefix):
		return nil
	case !strings.HasPrefix(name, volumesPrefix) && !strings.HasPrefix(name, imagesPrefix):
		return damaged(fmt.Errorf("unexpected entry %q", name))
	}
	sr.finishFiles()
	switch {
	case strings.HasPrefix(name, sr.volumePrefix):
		return sr.volume(hdr, body)
	case strings.HasPrefix(name, sr.imagePrefix):
		return sr.image(hdr, body)
	}
	return nil // another stack's data
}

// checkArchiveManifest makes sure the manifest that drove the restore (often
// the sidecar) describes this archive.
func (sr *stackRestore) checkArchiveManifest(body io.Reader) error {
	m, err := decodeArchiveManifest(body)
	if err != nil {
		return err
	}
	if err := verifyArchiveManifest(m, sr.manifest, sr.fromSidecar, sr.r.d.Version); err != nil {
		return err
	}
	sr.sawManifest = true
	return nil
}

// openFiles opens the target directory through the os.Root of its allowed
// root. An existing stack outside the roots is overwritten in place, through
// an os.Root of its own directory.
func (sr *stackRestore) openFiles() (*os.Root, error) {
	dir := sr.plan.TargetDir
	if underAnyRoot(dir, sr.r.d.AllowedRoots) {
		return sr.rs.openUnder(dir)
	}
	if sr.plan.Overwrites {
		// R29: the stack is already registered and run from this directory
		// (the plan keeps an existing stack's directory, R14), so overwriting
		// it in place is allowed. os.Root still keeps every write inside it.
		return os.OpenRoot(dir)
	}
	return nil, fmt.Errorf("%s is %s", dir, reasonOutsideRoots)
}

func (sr *stackRestore) stackFile(hdr *tar.Header, body io.Reader) error {
	if sr.pastFiles {
		return damaged(fmt.Errorf("%q comes after volume or image data", hdr.Name))
	}
	if !sr.filesSeen {
		sr.filesSeen = true
		sr.out("writing the files of " + sr.plan.TargetName + " to " + sr.plan.TargetDir)
		root, err := sr.openFiles()
		if err != nil {
			sr.filesFailed = append(sr.filesFailed, ItemResult{Kind: "stack-files", Stack: sr.plan.TargetName, Name: sr.plan.TargetDir, Status: itemFailed, Note: err.Error()})
			return nil
		}
		sr.files = root
	}
	if sr.files == nil {
		return nil
	}
	rel := strings.TrimPrefix(hdr.Name, sr.stackPrefix)
	// R17: the archived compose file is written under the existing file's name.
	if path.Clean(rel) == sr.archivedRel {
		rel = filepath.Base(sr.plan.ComposePath)
	}
	item, err := extractEntry(sr.files, sr.stackPrefix, rel, hdr, body)
	if item != nil {
		item.Stack = sr.plan.TargetName
		sr.filesFailed = append(sr.filesFailed, *item)
	}
	return err
}

// finishFiles runs once the stack files are behind: it reports them,
// registers a new stack and creates the networks it needs.
func (sr *stackRestore) finishFiles() {
	if sr.pastFiles {
		return
	}
	sr.pastFiles = true
	if sr.req.Files {
		switch {
		case !sr.filesSeen:
			sr.item(ItemResult{Kind: "stack-files", Stack: sr.plan.TargetName, Name: sr.plan.TargetDir, Status: itemFailed, Note: "the archive holds no files for this stack"})
		case len(sr.filesFailed) > 0:
			for _, it := range sr.filesFailed {
				sr.item(it)
			}
		default:
			sr.item(ItemResult{Kind: "stack-files", Stack: sr.plan.TargetName, Name: sr.plan.TargetDir, Status: itemOK})
			if !sr.plan.Overwrites {
				sr.register()
			}
		}
	}
	if !sr.plan.Overwrites && !sr.registered {
		sr.dataBlocked = true
	}
	for _, it := range createNetworkSpecs(sr.ctx, sr.r.d.Engine, sr.plan.MissingNetworks, sr.out) {
		sr.item(it)
	}
}

// register adds the registry row of a new or recreated stack: the archived
// row minus anything host-specific, at its new place and project.
func (sr *stackRestore) register() {
	e := stacks.Entry{Name: sr.plan.TargetName, ComposePath: sr.plan.ComposePath, Origin: sr.plan.Origin, Project: sr.plan.Project}
	if err := sr.r.d.Restore.RegisterRestored(sr.ctx, e); err != nil {
		sr.item(ItemResult{Kind: "stack", Stack: e.Name, Name: "register", Status: itemFailed, Note: err.Error()})
		return
	}
	sr.registered = true
}

// archiveKey returns the key of <prefix><key>/....
func archiveKey(name, prefix string) string {
	key, _, _ := strings.Cut(strings.TrimPrefix(name, prefix), "/")
	return key
}

const (
	noteBackupFailed  = "its backup failed; the archived data is incomplete"
	noteNotRegistered = "not restored: the stack could not be registered"
)

func (sr *stackRestore) volume(hdr *tar.Header, body io.Reader) error {
	key := archiveKey(hdr.Name, sr.volumePrefix)
	stream := TarStreamUnder(sr.ar, sr.volumePrefix+key+"/", hdr, body)
	i := slices.IndexFunc(sr.plan.Volumes, func(v PlannedVolume) bool { return v.Key == key })
	if key == "" || i < 0 || sr.volumesDone[key] || !sr.req.Volumes {
		return drain(stream)
	}
	sr.volumesDone[key] = true
	if sr.dataBlocked {
		sr.item(ItemResult{Kind: conflictVolume, Stack: sr.plan.TargetName, Name: sr.plan.Volumes[i].Name, Status: itemFailed, Note: noteNotRegistered})
		return drain(stream)
	}
	pv := sr.plan.Volumes[i]
	j := slices.IndexFunc(sr.ms.Volumes, func(v ManifestVolume) bool { return v.Key == key })
	switch {
	case pv.Skip:
		sr.item(ItemResult{Kind: conflictVolume, Stack: sr.plan.TargetName, Name: pv.Name, Status: itemSkipped, Note: pv.Reason})
		return drain(stream)
	case j < 0 || failedInManifest(sr.manifest, conflictVolume, sr.req.Stack, sr.ms.Volumes[j].Name):
		sr.item(ItemResult{Kind: conflictVolume, Stack: sr.plan.TargetName, Name: pv.Name, Status: itemSkipped, Note: noteBackupFailed})
		return drain(stream)
	}
	if err := checkVolumeKey(sr.ms.Volumes[j]); err != nil {
		sr.item(ItemResult{Kind: conflictVolume, Stack: sr.plan.TargetName, Name: pv.Name, Status: itemFailed, Note: err.Error()})
		return drain(stream)
	}
	var bind *bindTarget
	var protected []string
	if pv.Kind == volumeKindBind {
		if archivedAsFile(hdr, sr.volumePrefix+key+"/") {
			sr.item(ItemResult{Kind: conflictVolume, Stack: sr.plan.TargetName, Name: pv.Name, Status: itemSkipped, Note: noteArchivedFileBind})
			return drain(stream)
		}
		// Without the stack list the bind cannot be vetted, so it is not mounted.
		var target bindTarget
		var skip string
		protected, err := sr.protectedDirs()
		if err == nil {
			target, skip, err = sr.rs.prepareBind(pv.Name, protected)
		}
		if skip != "" || err != nil {
			it := ItemResult{Kind: conflictVolume, Stack: sr.plan.TargetName, Name: pv.Name, Status: itemSkipped, Note: skip}
			if err != nil {
				it.Status, it.Note = itemFailed, err.Error()
			}
			sr.item(it)
			return drain(stream)
		}
		bind = &target
	}
	sr.out("restoring " + pv.Kind + " " + pv.Name)
	err := sr.ensureHelper()
	switch {
	case err != nil:
	case bind != nil:
		// Vet again: the helper image check above may have pulled.
		if err = sr.rs.revetBind(pv.Name, protected, *bind); err == nil {
			err = RestoreBind(sr.ctx, sr.r.d.Engine, sr.plan.TargetName, *bind, stream)
		}
	default:
		mount := stacks.Mount{Kind: pv.Kind, Source: pv.Name, Target: pv.Target}
		err = RestoreVolume(sr.ctx, sr.r.d.Engine, sr.plan.TargetName, mount, composeVolumeLabels(sr.plan.Project, pv.Name), stream)
	}
	if derr := drain(stream); derr != nil {
		return derr
	}
	if err != nil {
		sr.item(ItemResult{Kind: conflictVolume, Stack: sr.plan.TargetName, Name: pv.Name, Status: itemFailed, Note: err.Error()})
		return nil
	}
	sr.item(ItemResult{Kind: conflictVolume, Stack: sr.plan.TargetName, Name: pv.Name, Status: itemOK})
	return nil
}

// protectedDirs lists the directories a bind source must not contain: the
// target's and every registered stack's, so emptying a bind never wipes a stack.
func (sr *stackRestore) protectedDirs() ([]string, error) {
	entries, err := sr.r.d.Registry.List(sr.ctx)
	if err != nil {
		return nil, fmt.Errorf("list registered stacks: %w", err)
	}
	dirs := []string{sr.plan.TargetDir}
	for _, e := range entries {
		dirs = append(dirs, filepath.Dir(e.ComposePath))
	}
	return dirs, nil
}

func (sr *stackRestore) ensureHelper() error {
	if !sr.helperTried {
		sr.helperTried = true
		if err := EnsureHelperImage(sr.ctx, sr.r.d.Engine, sr.out); err != nil {
			sr.helperErr = fmt.Errorf("helper image: %w", err)
		}
	}
	return sr.helperErr
}

func (sr *stackRestore) image(hdr *tar.Header, body io.Reader) error {
	key := archiveKey(hdr.Name, sr.imagePrefix)
	stream := TarStreamUnder(sr.ar, sr.imagePrefix+key+"/", hdr, body)
	i := slices.IndexFunc(sr.plan.Images, func(ref string) bool { return ImageKey(ref) == key })
	if key == "" || i < 0 || sr.imagesDone[key] {
		return drain(stream)
	}
	sr.imagesDone[key] = true
	ref := sr.plan.Images[i]
	if sr.dataBlocked {
		sr.item(ItemResult{Kind: "image", Stack: sr.plan.TargetName, Name: ref, Status: itemFailed, Note: noteNotRegistered})
		return drain(stream)
	}
	if failedInManifest(sr.manifest, "image", sr.req.Stack, ref) {
		sr.item(ItemResult{Kind: "image", Stack: sr.plan.TargetName, Name: ref, Status: itemSkipped, Note: noteBackupFailed})
		return drain(stream)
	}
	sr.out("loading image " + ref)
	err := loadImageChecked(sr.ctx, sr.r.d.Engine, ref, stream)
	if derr := drain(stream); derr != nil {
		return derr
	}
	if err != nil {
		sr.item(ItemResult{Kind: "image", Stack: sr.plan.TargetName, Name: ref, Status: itemFailed, Note: err.Error()})
		return nil
	}
	sr.item(ItemResult{Kind: "image", Stack: sr.plan.TargetName, Name: ref, Status: itemOK})
	return nil
}

// reportMissing fails the planned volumes and images the archive did not hold.
func (sr *stackRestore) reportMissing() {
	if sr.req.Volumes {
		for _, pv := range sr.plan.Volumes {
			if !pv.Skip && !sr.volumesDone[pv.Key] {
				sr.item(ItemResult{Kind: conflictVolume, Stack: sr.plan.TargetName, Name: pv.Name, Status: itemFailed, Note: "not found in the archive"})
			}
		}
	}
	for _, ref := range sr.plan.Images {
		if !sr.imagesDone[ImageKey(ref)] {
			sr.item(ItemResult{Kind: "image", Stack: sr.plan.TargetName, Name: ref, Status: itemFailed, Note: "not found in the archive"})
		}
	}
}
