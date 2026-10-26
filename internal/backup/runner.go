package backup

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rcss-labs/stackstead/internal/actionlog"
	"github.com/rcss-labs/stackstead/internal/peers"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

// Run outcomes stored in backup_runs and the action log.
const (
	runStatusSuccess = "success"
	runStatusPartial = "partial"
)

const (
	itemOK      = "ok"
	itemFailed  = "failed"
	itemSkipped = "skipped"
)

const (
	runOpKey      = "host:backup"
	runOpAction   = "backup"
	runOpTimeout  = 12 * time.Hour
	runActionKind = "backup.run"
	// restartTimeout bounds the restart of a stack stopped for the backup.
	restartTimeout = 5 * time.Minute
	systemUser     = "system"
)

// StackControl is the part of stacks.Service the runner uses.
type StackControl interface {
	List(ctx context.Context) ([]stacks.Summary, error)
	Mounts(ctx context.Context, stack string) ([]stacks.Mount, error)
	// ImageRefs also names the services that only build, when no container shows their image.
	ImageRefs(ctx context.Context, stack string) (refs, buildOnly []string, err error)
	ActWait(ctx context.Context, stack, action string, out func(string)) error
	// StartAfterBackup starts a stack the backup stopped, without going
	// through the operations manager, so it works during shutdown.
	StartAfterBackup(ctx context.Context, stack string, out func(string)) error
}

var _ StackControl = (*stacks.Service)(nil)

// Deps are the collaborators of a Runner.
type Deps struct {
	DB     *sql.DB
	Config *ConfigStore
	Runs   *RunStore
	Stacks StackControl
	// Restore stops, starts and registers stacks for a single-stack restore.
	Restore  RestoreControl
	Registry *stacks.Registry
	Engine   Engine
	Ops      *stacks.Ops
	Actions  *actionlog.Store
	Identity func(ctx context.Context) (peers.Identity, error)
	DataDir  string
	// AllowedRoots and StacksDir place the files of a restored stack.
	AllowedRoots []string
	StacksDir    string
	ConfigPath   string
	ImagesDir    string
	Version      string
	Now          func() time.Time
	HTTP         *http.Client
	Logger       *slog.Logger
}

// Runner performs backup runs, one at a time.
type Runner struct {
	d Deps
	// Destination constructors; tests replace them with in-memory ones.
	newLocal func(LocalDest) Destination
	newS3    func(S3Dest, *http.Client) Destination
}

// NewRunner returns a Runner over d.
func NewRunner(d Deps) *Runner {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.HTTP == nil {
		d.HTTP = http.DefaultClient
	}
	sweepRestoreStaging(d.DataDir, d.Logger)
	return &Runner{d: d, newLocal: NewLocal, newS3: NewS3}
}

// sweepRestoreStaging removes the staging dirs single-stack restores leave
// behind when the process stops mid-restore. It runs once at construction,
// before any restore can start.
func sweepRestoreStaging(dataDir string, logger *slog.Logger) {
	if dataDir == "" {
		return
	}
	stale, _ := filepath.Glob(filepath.Join(dataDir, restoreStagingPattern))
	for _, dir := range stale {
		if err := os.RemoveAll(dir); err != nil {
			logger.Warn("remove stale restore staging", "dir", dir, "err", err)
		}
	}
}

// LastRun returns the most recent finished run.
func (r *Runner) LastRun(ctx context.Context) (Run, bool, error) {
	return r.d.Runs.Last(ctx)
}

// Start begins a backup run as an operation. It returns stacks.ErrBusy when a
// run is already in progress.
func (r *Runner) Start(ctx context.Context, trigger, username string) (stacks.Operation, error) {
	if username == "" {
		username = systemUser
	}
	opIDs := make(chan string, 1)
	op, err := r.d.Ops.StartTimeout(runOpKey, runOpAction, runOpTimeout, func(ctx context.Context, out func(string)) error {
		return r.run(ctx, trigger, username, opIDs, out)
	})
	if err != nil {
		return op, err
	}
	opIDs <- op.ID
	return op, nil
}

// ImageKey is the path-safe id of an image inside the archive, used in
// images/<stack>/<key>/: i-<first 12 hex of sha256(ref)>.
func ImageKey(ref string) string {
	sum := sha256.Sum256([]byte(ref))
	return "i-" + hex.EncodeToString(sum[:])[:12]
}

func (r *Runner) run(ctx context.Context, trigger, username string, opIDs <-chan string, out func(string)) error {
	runID, err := r.d.Runs.Begin(ctx, trigger)
	if err != nil {
		return err
	}
	j := &job{r: r, ctx: ctx, out: out, stacks: map[string]*ManifestStack{}, mounts: map[string]mountsResult{}}
	defer func() {
		// Record a panicking run as failed and drop its archive, then let Ops
		// recover the panic and mark the operation failed.
		if p := recover(); p != nil {
			j.fatal = fmt.Errorf("backup panicked: %v", p)
			j.abortArchive()
			r.finish(ctx, runID, trigger, username, j, opIDs, out)
			panic(p)
		}
	}()
	j.execute()
	return r.finish(ctx, runID, trigger, username, j, opIDs, out)
}

// finish records the run and its action log entry and returns the run's
// fatal error, if any.
func (r *Runner) finish(ctx context.Context, runID int64, trigger, username string, j *job, opIDs <-chan string, out func(string)) error {
	run := Run{ID: runID, Trigger: trigger, Status: j.status(), Items: j.items, Destinations: j.reached}
	if j.fatal != nil {
		run.Error = j.fatal.Error()
	} else {
		run.Archive, run.Size = j.archive, j.size
	}
	out("backup " + run.Status)

	// The run's outcome is recorded even when the operation was cancelled.
	saveCtx := context.WithoutCancel(ctx)
	if err := r.d.Runs.Finish(saveCtx, run); err != nil {
		r.d.Logger.Error("record backup run", "run", runID, "err", err)
	}
	entry := actionlog.Entry{
		TS: r.d.Now(), Username: username, Kind: runActionKind, Scope: "host",
		Target: run.Archive, Detail: runDetail(run), Result: run.Status, OperationID: <-opIDs,
	}
	if run.Archive != "" {
		size := run.Size
		entry.Bytes = &size
	}
	if err := r.d.Actions.Append(saveCtx, entry); err != nil {
		r.d.Logger.Error("record backup action", "run", runID, "err", err)
	}
	if j.fatal != nil {
		return j.fatal
	}
	return nil
}

func runDetail(run Run) string {
	if run.Error != "" {
		return run.Error
	}
	failed := 0
	for _, it := range run.Items {
		if it.Status == itemFailed {
			failed++
		}
	}
	detail := "stored in " + strings.Join(run.Destinations, ", ")
	if failed > 0 {
		detail += fmt.Sprintf("; %d item(s) failed", failed)
	}
	return detail
}

type mountsResult struct {
	mounts []stacks.Mount
	err    error
}

// job is the state of one run.
type job struct {
	r   *Runner
	ctx context.Context
	out func(string)

	fatal    error
	items    []ItemResult
	reached  []string
	archive  string
	size     int64
	manifest Manifest

	sink      *countingWriter
	primary   Destination               // set while an archive is being written
	dw        io.WriteCloser            // the primary's writer until it is closed
	stackList []*ManifestStack          // every registered stack, in name order
	stacks    map[string]*ManifestStack // by name
	listed    map[string]bool           // stacks the stack list still reports
	mounts    map[string]mountsResult
}

func (j *job) status() string {
	if j.fatal != nil {
		return runStatusFailed
	}
	for _, it := range j.items {
		if it.Status == itemFailed {
			return runStatusPartial
		}
	}
	return runStatusSuccess
}

func (j *job) item(it ItemResult) { j.items = append(j.items, it) }

// failItem records a failed item. When the archive stream itself has failed
// the error is fatal, since nothing more can be written.
func (j *job) failItem(it ItemResult) error {
	it.Status = itemFailed
	j.item(it)
	j.out(fmt.Sprintf("failed: %s %s %s: %s", it.Kind, it.Stack, it.Name, it.Note))
	if j.sink != nil && j.sink.err != nil {
		return fmt.Errorf("write archive: %w", j.sink.err)
	}
	return nil
}

func (j *job) execute() {
	ctx, d := j.ctx, j.r.d
	cfg, err := d.Config.Load(ctx)
	if err != nil {
		j.fatal = err
		return
	}
	selections, err := d.Config.Selections(ctx)
	if err != nil {
		j.fatal = err
		return
	}
	ident, err := d.Identity(ctx)
	if err != nil {
		j.fatal = fmt.Errorf("read host identity: %w", err)
		return
	}
	slug := peers.Slugify(ident.Name)
	now := d.Now().UTC()
	j.archive = ArchiveName(slug, now)
	j.manifest = Manifest{
		Format: ManifestFormat, StacksteadVersion: d.Version, Host: ident.Name, HostSlug: slug,
		InstanceID: ident.InstanceID, CreatedAt: now,
	}

	// With both destinations the archive is written locally and that file is
	// uploaded; with S3 alone it streams straight into the multipart upload.
	var primary, upload Destination
	switch {
	case cfg.Local.Enabled && cfg.S3.Enabled:
		primary, upload = j.r.newLocal(cfg.Local), j.r.newS3(cfg.S3, d.HTTP)
	case cfg.Local.Enabled:
		primary = j.r.newLocal(cfg.Local)
	case cfg.S3.Enabled:
		primary = j.r.newS3(cfg.S3, d.HTTP)
	default:
		j.fatal = errors.New("no backup destination is enabled")
		return
	}

	j.out(fmt.Sprintf("writing %s to %s", j.archive, primary.Name()))
	dw, err := primary.Create(ctx, j.archive)
	if err != nil {
		j.fatal = fmt.Errorf("create archive in %s: %w", primary.Name(), err)
		return
	}
	j.primary, j.dw = primary, dw
	j.sink = &countingWriter{w: dw}
	aw := NewWriter(j.sink)
	err = j.writeSections(aw, selections)
	if err == nil {
		// The in-archive manifest describes the archive's contents; upload
		// failures happen later and are only on the run record.
		j.manifest.Status = j.status()
		j.manifest.Items = j.items
		j.manifest.Stacks = make([]ManifestStack, 0, len(j.stackList))
		for _, ms := range j.stackList {
			j.manifest.Stacks = append(j.manifest.Stacks, *ms)
		}
		err = aw.Close(j.manifest)
	}
	if err == nil && j.sink.err != nil {
		err = j.sink.err
	}
	if err != nil {
		j.abortArchive()
		j.fatal = err
		return
	}
	j.dw = nil
	if err := dw.Close(); err != nil {
		j.fatal = fmt.Errorf("finish archive in %s: %w", primary.Name(), err)
		return
	}
	j.size = j.sink.n
	j.reached = append(j.reached, primary.Name())
	j.out(fmt.Sprintf("archive written (%d bytes)", j.size))

	sidecar, err := json.MarshalIndent(j.manifest, "", "  ")
	if err != nil {
		j.fatal = err
		return
	}
	// The sidecar matches the in-archive manifest, which describes the
	// archive's contents; an upload failure below is only on the run record.
	if err := primary.PutSidecar(ctx, j.archive, sidecar); err != nil {
		_ = j.failItem(ItemResult{Kind: "upload", Name: primary.Name(), Note: "write manifest sidecar: " + err.Error()})
	}
	if primary.Name() == destLocal {
		if deleted, err := PruneLocal(cfg.Local, slug); err != nil {
			d.Logger.Warn("prune local backups", "err", err)
			j.out("prune failed: " + err.Error())
		} else if len(deleted) > 0 {
			j.out(fmt.Sprintf("pruned %d old file(s)", len(deleted)))
		}
	}
	if upload != nil {
		j.out("uploading to " + upload.Name())
		if err := j.upload(primary, upload, sidecar); err != nil {
			_ = j.failItem(ItemResult{Kind: "upload", Name: upload.Name(), Note: err.Error()})
		} else {
			j.reached = append(j.reached, upload.Name())
		}
	}
}

// abortArchive discards an archive that is still being written.
func (j *job) abortArchive() {
	if j.dw == nil {
		return
	}
	dw := j.dw
	j.dw = nil
	if err := j.primary.Abort(context.WithoutCancel(j.ctx), j.archive); err != nil {
		j.r.d.Logger.Warn("abort backup archive", "archive", j.archive, "err", err)
	}
	_ = dw.Close() // after Abort, Close never publishes
}

// upload copies the finished archive from one destination to another, on
// this goroutine (S3 uploads are not goroutine-safe).
func (j *job) upload(from, to Destination, sidecar []byte) error {
	src, err := from.Open(j.ctx, j.archive)
	if err != nil {
		return err
	}
	defer src.Close()
	w, err := to.Create(j.ctx, j.archive)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, src); err != nil {
		_ = to.Abort(context.WithoutCancel(j.ctx), j.archive)
		_ = w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return to.PutSidecar(j.ctx, j.archive, sidecar)
}

func (j *job) writeSections(aw *Writer, selections []StackSelection) error {
	if err := j.writeBase(aw); err != nil {
		return err
	}
	sort.Slice(selections, func(a, b int) bool { return selections[a].Stack < selections[b].Stack })
	present := selections[:0:0]
	for _, sel := range selections {
		if !sel.Volumes && !sel.Images {
			continue
		}
		if j.stacks[sel.Stack] == nil || !j.listed[sel.Stack] {
			j.skipMissing(sel)
			continue
		}
		present = append(present, sel)
	}
	var helperErr error
	for _, sel := range present {
		if sel.Volumes {
			helperErr = EnsureHelperImage(j.ctx, j.r.d.Engine, j.out)
			break
		}
	}
	for _, sel := range present {
		if !sel.Volumes {
			continue
		}
		if helperErr != nil {
			if err := j.failItem(ItemResult{Kind: "volume", Stack: sel.Stack, Note: "helper image unavailable: " + helperErr.Error()}); err != nil {
				return err
			}
			continue
		}
		if err := j.backUpVolumes(aw, sel); err != nil {
			return err
		}
	}
	for _, sel := range present {
		if sel.Images {
			if err := j.saveImages(aw, sel.Stack); err != nil {
				return err
			}
		}
	}
	return nil
}

func (j *job) skipMissing(sel StackSelection) {
	j.out("skipping " + sel.Stack + ": the stack no longer exists")
	if sel.Volumes {
		j.item(ItemResult{Kind: "volume", Stack: sel.Stack, Status: itemSkipped, Note: "stack no longer exists"})
	}
	if sel.Images {
		j.item(ItemResult{Kind: "image", Stack: sel.Stack, Status: itemSkipped, Note: "stack no longer exists"})
	}
}

func (j *job) writeBase(aw *Writer) error {
	ctx, d := j.ctx, j.r.d
	j.out("backing up the database")
	snapshot, cleanup, err := CollectDB(ctx, d.DB, d.DataDir)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := addHostFile(aw, "base/stackstead.db", snapshot); err != nil {
		return fmt.Errorf("archive database: %w", err)
	}
	j.item(ItemResult{Kind: "base", Name: "stackstead.db", Status: itemOK})

	if d.ConfigPath != "" {
		err := addHostFile(aw, "base/config.yaml", d.ConfigPath)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return fmt.Errorf("archive config.yaml: %w", err)
		default:
			j.item(ItemResult{Kind: "base", Name: "config.yaml", Status: itemOK})
		}
	}

	networks, count, err := CollectNetworks(ctx, d.Engine)
	if err != nil {
		return fmt.Errorf("read networks: %w", err)
	}
	if err := aw.AddFile("base/networks.json", &tar.Header{Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(networks)), ModTime: j.manifest.CreatedAt}, strings.NewReader(string(networks))); err != nil {
		return fmt.Errorf("archive networks: %w", err)
	}
	j.manifest.Networks = count

	if err := j.writeStackFiles(aw); err != nil {
		return err
	}

	if d.ImagesDir != "" {
		j.out("backing up the image library")
		items, err := CollectDir(aw, d.ImagesDir, "base/images-library")
		if err != nil {
			return fmt.Errorf("archive image library: %w", err)
		}
		j.items = append(j.items, items...)
	}
	return nil
}

func (j *job) writeStackFiles(aw *Writer) error {
	ctx, d := j.ctx, j.r.d
	entries, err := d.Registry.List(ctx)
	if err != nil {
		return err
	}
	summaries, err := d.Stacks.List(ctx)
	if err != nil {
		return fmt.Errorf("list stacks: %w", err)
	}
	j.listed = map[string]bool{}
	running := map[string]bool{}
	for _, s := range summaries {
		j.listed[s.Name] = true
		running[s.Name] = s.Status == stacks.StatusRunning || s.Status == stacks.StatusPartial
	}
	networksOf, err := j.createdNetworksByProject()
	if err != nil {
		return err
	}
	sort.Slice(entries, func(a, b int) bool { return entries[a].Name < entries[b].Name })
	for _, e := range entries {
		ms := &ManifestStack{Name: e.Name, ComposePath: e.ComposePath, Origin: string(e.Origin), Project: e.Project,
			WasRunning: running[e.Name], Networks: networksOf[e.Project]}
		j.stackList = append(j.stackList, ms)
		j.stacks[e.Name] = ms

		j.out("backing up the files of " + e.Name)
		mounts, err := d.Stacks.Mounts(ctx, e.Name)
		j.mounts[e.Name] = mountsResult{mounts: mounts, err: err}
		if err != nil {
			if err := j.failItem(ItemResult{Kind: "stack-files", Stack: e.Name, Note: "resolve mounts: " + err.Error()}); err != nil {
				return err
			}
			continue
		}
		var binds []string
		for _, m := range mounts {
			if m.Kind == "bind" {
				binds = append(binds, m.Source)
			}
		}
		items, err := CollectStackFiles(aw, e, binds)
		if errors.Is(err, fs.ErrNotExist) {
			j.item(ItemResult{Kind: "stack-files", Stack: e.Name, Status: itemSkipped, Note: "stack directory is missing"})
			continue
		}
		if err != nil {
			return fmt.Errorf("archive files of %s: %w", e.Name, err)
		}
		j.items = append(j.items, items...)
		j.item(ItemResult{Kind: "stack-files", Stack: e.Name, Status: itemOK})
	}
	return nil
}

// createdNetworksByProject maps each compose project to the sorted names of
// the Stackstead-created networks its containers are attached to.
func (j *job) createdNetworksByProject() (map[string][]string, error) {
	nets, err := j.r.d.Engine.Networks(j.ctx)
	if err != nil {
		return nil, fmt.Errorf("read networks: %w", err)
	}
	created := map[string]bool{}
	for _, n := range nets {
		if n.Labels[createdLabel] == "true" {
			created[n.Name] = true
		}
	}
	out := map[string][]string{}
	if len(created) == 0 {
		return out, nil
	}
	containers, err := j.r.d.Engine.Containers(j.ctx)
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	seen := map[string]bool{}
	for _, c := range containers {
		project := c.Project()
		if project == "" {
			continue
		}
		for name := range c.Networks {
			if created[name] && !seen[project+"\x00"+name] {
				seen[project+"\x00"+name] = true
				out[project] = append(out[project], name)
			}
		}
	}
	for _, names := range out {
		sort.Strings(names)
	}
	return out, nil
}

// backUpVolumes copies the stack's mounts, stopping it first when asked to.
// A stack that was stopped here is always started again, on every path.
func (j *job) backUpVolumes(aw *Writer, sel StackSelection) (err error) {
	name := sel.Stack
	res := j.mounts[name]
	if res.err != nil {
		return j.failItem(ItemResult{Kind: "volume", Stack: name, Note: "resolve mounts: " + res.err.Error()})
	}
	if len(res.mounts) == 0 {
		return nil
	}
	ms := j.stacks[name]
	wasRunning := ms.WasRunning
	if !sel.StopDuring || !wasRunning {
		ms.LiveCopy = wasRunning
		return j.copyMounts(aw, name, res.mounts)
	}

	j.out("stopping " + name)
	stopErr := j.r.d.Stacks.ActWait(j.ctx, name, "stop", j.out)
	if errors.Is(stopErr, stacks.ErrBusy) {
		// Nothing was stopped, so there is nothing to start again.
		return j.failItem(ItemResult{Kind: "volume", Stack: name, Note: "stack is busy with another operation; volumes not copied"})
	}
	defer func() {
		j.out("starting " + name)
		// The stack must come back even when the run is cancelled or the
		// server is shutting down.
		startCtx, cancel := context.WithTimeout(context.WithoutCancel(j.ctx), restartTimeout)
		defer cancel()
		if startErr := j.r.d.Stacks.StartAfterBackup(startCtx, name, j.out); startErr != nil {
			if ferr := j.failItem(ItemResult{Kind: "volume", Stack: name, Name: "restart", Note: "stack left stopped: start failed: " + startErr.Error()}); err == nil {
				err = ferr
			}
		}
	}()
	if stopErr != nil {
		// A failed stop may have stopped some services; the deferred start brings them back.
		return j.failItem(ItemResult{Kind: "volume", Stack: name, Note: "stop failed; volumes not copied: " + stopErr.Error()})
	}
	return j.copyMounts(aw, name, res.mounts)
}

func (j *job) copyMounts(aw *Writer, stack string, mounts []stacks.Mount) error {
	ms := j.stacks[stack]
	for _, m := range mounts {
		j.out(fmt.Sprintf("copying %s %s of %s", m.Kind, m.Source, stack))
		mv, err := CopyVolume(j.ctx, j.r.d.Engine, aw, stack, m)
		if err != nil {
			if ferr := j.failItem(ItemResult{Kind: "volume", Stack: stack, Name: m.Source, Note: err.Error()}); ferr != nil {
				return ferr
			}
			continue
		}
		ms.Volumes = append(ms.Volumes, mv)
		j.item(ItemResult{Kind: "volume", Stack: stack, Name: m.Source, Status: itemOK, Bytes: mv.Bytes})
	}
	return nil
}

func (j *job) saveImages(aw *Writer, stack string) error {
	refs, buildOnly, err := j.r.d.Stacks.ImageRefs(j.ctx, stack)
	if err != nil {
		return j.failItem(ItemResult{Kind: "image", Stack: stack, Note: "resolve images: " + err.Error()})
	}
	for _, svc := range buildOnly {
		j.item(ItemResult{Kind: "image", Stack: stack, Name: svc, Status: itemSkipped, Note: "image for build-only service " + svc + " not present"})
	}
	ms := j.stacks[stack]
	for _, ref := range refs {
		j.out(fmt.Sprintf("saving image %s of %s", ref, stack))
		n, err := j.saveImage(aw, stack, ref)
		if err != nil {
			if ferr := j.failItem(ItemResult{Kind: "image", Stack: stack, Name: ref, Note: err.Error()}); ferr != nil {
				return ferr
			}
			continue
		}
		ms.Images = append(ms.Images, ref)
		j.item(ItemResult{Kind: "image", Stack: stack, Name: ref, Status: itemOK, Bytes: n})
	}
	return nil
}

func (j *job) saveImage(aw *Writer, stack, ref string) (int64, error) {
	body, err := j.r.d.Engine.SaveImage(j.ctx, ref)
	if err != nil {
		return 0, err
	}
	defer body.Close()
	return aw.AddTarStream("images/"+stack+"/"+ImageKey(ref)+"/", body)
}

// addHostFile archives one regular file from the host under name.
func addHostFile(aw *Writer, name, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	return aw.AddFile(name, hdr, f)
}

// countingWriter counts the bytes written and remembers the first error, so
// an item failure can be told apart from a broken archive stream.
type countingWriter struct {
	w   io.Writer
	n   int64
	err error
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	if err != nil && c.err == nil {
		c.err = err
	}
	return n, err
}
