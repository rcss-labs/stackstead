package images

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rcss-labs/stackstead/internal/actionlog"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/imageref"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

// Engine is the part of the Docker engine client the image library uses.
type Engine interface {
	Build(ctx context.Context, contextTar io.Reader, opts engine.BuildOptions, out func(string)) (engine.BuildResult, error)
	Push(ctx context.Context, ref string, auth engine.RegistryAuth, out func(string)) (string, error)
	TagImage(ctx context.Context, source, repo, tag string) error
	SaveImage(ctx context.Context, ref string) (io.ReadCloser, error)
	LoadImage(ctx context.Context, tar io.Reader) error
	InspectImage(ctx context.Context, ref string) (engine.ImageInspect, error)
	RemoveImage(ctx context.Context, ref string) (engine.ImageDeleteResult, error)
	Arch(ctx context.Context) (string, error)
}

var _ Engine = (*engine.Client)(nil)

// Self identifies this server.
type Self struct{ InstanceID, Name, Slug string }

// ActionLog records operator actions.
type ActionLog interface {
	Append(context.Context, actionlog.Entry) error
}

// Deps are the collaborators of a Service. Dir is the images root: every
// image keeps blobs/, logs/ and tmp-<id>/ under Dir/<slug>.
type Deps struct {
	Store     *Store
	Ops       *stacks.Ops
	Engine    Engine
	Dir       string
	Limits    Limits
	OpTimeout time.Duration
	Actions   ActionLog
	Self      func(ctx context.Context) (Self, error)
	Usage     UsageSource
	// Registries supplies push credentials; nil makes push routes answer registry_unconfigured.
	Registries RegistryAuth
	Now        func() time.Time
	Logger     *slog.Logger
}

// Service is the image library: images, source revisions, builds and versions.
type Service struct {
	store   *Store
	ops     *stacks.Ops
	eng     Engine
	dir     string
	limits  Limits
	timeout time.Duration
	actions ActionLog
	self    func(ctx context.Context) (Self, error)
	now     func() time.Time
	log     *slog.Logger

	registries RegistryAuth

	usageMu sync.RWMutex
	usage   UsageSource

	locks sync.Map // slug -> *sync.Mutex

	beforeBuildLock func() // test hook: runs in Build before the image lock
}

var _ stacks.LibraryFetcher = (*Service)(nil)

const (
	defaultMaxFileBytes    = 128 << 20
	defaultMaxContextBytes = 1 << 30
)

// NewService returns a Service over d. Zero limits fall back to the defaults.
func NewService(d Deps) *Service {
	if d.Limits.MaxFileBytes <= 0 {
		d.Limits.MaxFileBytes = defaultMaxFileBytes
	}
	if d.Limits.MaxContextBytes <= 0 {
		d.Limits.MaxContextBytes = defaultMaxContextBytes
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Service{store: d.Store, ops: d.Ops, eng: d.Engine, dir: d.Dir, limits: d.Limits, timeout: d.OpTimeout,
		actions: d.Actions, self: d.Self, usage: d.Usage, registries: d.Registries, now: d.Now, log: d.Logger}
}

// SetUsage binds the usage source after construction (stacks needs the
// service as its library fetcher, so the service is built first).
func (s *Service) SetUsage(u UsageSource) {
	s.usageMu.Lock()
	s.usage = u
	s.usageMu.Unlock()
}

// OpKey is the operation key of an image in the shared operations manager.
func OpKey(owner, slug string) string { return "image/" + owner + "/" + slug }

// Ref returns the library reference of a version.
func (s *Service) Ref(owner, slug, tag string) string { return imageref.Format(owner, slug, tag) }

// lockImage serializes source edits, blob collection and deletion of one image.
func (s *Service) lockImage(slug string) (unlock func()) {
	m, _ := s.locks.LoadOrStore(slug, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (s *Service) imageDir(slug string) string { return filepath.Join(s.dir, slug) }

func (s *Service) blobs(slug string) (*Blobs, error) {
	return OpenBlobs(filepath.Join(s.imageDir(slug), "blobs"))
}

func (s *Service) selfInfo(ctx context.Context) (Self, string, error) {
	if s.self == nil {
		return Self{}, "", errors.New("server identity is not configured")
	}
	me, err := s.self(ctx)
	if err != nil {
		return Self{}, "", fmt.Errorf("read server identity: %w", err)
	}
	owner := imageref.OwnerID(me.InstanceID)
	if owner == "" {
		return Self{}, "", errors.New("server instance id is too short")
	}
	return me, owner, nil
}

// record appends an action log entry. Failures are logged, never returned:
// the action itself already happened.
func (s *Service) record(ctx context.Context, user, kind, slug, target, detail, result, opID string) {
	if s.actions == nil {
		return
	}
	e := actionlog.Entry{Username: user, Kind: kind, Scope: slug, Target: target, Detail: detail, Result: result, OperationID: opID}
	if err := s.actions.Append(context.WithoutCancel(ctx), e); err != nil {
		s.log.Warn("append action log failed", "kind", kind, "image", slug, "err", err)
	}
}

// ---- images ----

// CreateRequest describes a new image.
type CreateRequest struct {
	Name           string
	Source         Source
	DockerfilePath string
	GitHub         GitHubSource
	BuildArgs      map[string]string
	Retention      *Retention
}

// UpdateRequest changes the mutable settings of an image. An empty
// DockerfilePath keeps the current one.
type UpdateRequest struct {
	Name, DockerfilePath string
	GitHub               GitHubSource
	BuildArgs            map[string]string
	Retention            Retention
}

func validateSettings(path string, args map[string]string) (string, error) {
	if path == "" {
		path = "Dockerfile"
	}
	clean, err := ValidatePath(path)
	if err != nil {
		return "", fieldError("dockerfilePath", err.Error())
	}
	if err := ValidateBuildArgs(args); err != nil {
		return "", err
	}
	return clean, nil
}

func validateRetention(r Retention) error {
	if r.KeepLast < 0 || r.KeepDays < 0 {
		return fieldError("retention", "retention values must not be negative")
	}
	return nil
}

// Create adds an image. An upload image starts with revision 1 holding the
// default Dockerfile at its Dockerfile path.
func (s *Service) Create(ctx context.Context, user string, req CreateRequest) (Image, error) {
	if err := ValidateName(req.Name); err != nil {
		return Image{}, err
	}
	slug := Slugify(req.Name)
	if slug == "" {
		return Image{}, fieldError("name", "name must contain at least one letter or digit")
	}
	if err := ValidateSlug(slug); err != nil {
		return Image{}, err
	}
	if req.Source == "" {
		req.Source = SourceUpload
	}
	if req.Source != SourceUpload && req.Source != SourceGitHub {
		return Image{}, fieldError("source", "source must be upload or github")
	}
	path, err := validateSettings(req.DockerfilePath, req.BuildArgs)
	if err != nil {
		return Image{}, err
	}
	var retention Retention
	if req.Retention != nil {
		retention = *req.Retention
	}
	if err := validateRetention(retention); err != nil {
		return Image{}, err
	}
	if req.Source == SourceGitHub && req.GitHub.Repo == "" {
		return Image{}, fieldError("github.repo", "repository is required")
	}

	unlock := s.lockImage(slug)
	defer unlock()
	img, err := s.store.CreateImage(ctx, Image{Slug: slug, Name: req.Name, Source: req.Source, DockerfilePath: path,
		GitHub: req.GitHub, BuildArgs: req.BuildArgs, Retention: retention})
	if err != nil {
		return Image{}, err
	}
	if req.Source == SourceUpload {
		if err := s.seedRevision(ctx, img); err != nil {
			s.store.DeleteImage(context.WithoutCancel(ctx), img.ID)
			os.RemoveAll(s.imageDir(slug))
			return Image{}, err
		}
	}
	s.record(ctx, user, "image.create", slug, slug, img.Name, "succeeded", "")
	return img, nil
}

func (s *Service) seedRevision(ctx context.Context, img Image) error {
	b, err := s.blobs(img.Slug)
	if err != nil {
		return fmt.Errorf("open blobs: %w", err)
	}
	m, _, err := ApplyDockerfile(Manifest{}, b, img.DockerfilePath, []byte(DefaultDockerfile), s.limits)
	if err != nil {
		return err
	}
	_, err = s.store.AddRevision(ctx, img.ID, "created from template", m.JSON())
	return err
}

// Get returns the image with the slug.
func (s *Service) Get(ctx context.Context, slug string) (Image, error) {
	return s.store.Image(ctx, slug)
}

// Update changes an image's settings. Its slug never changes.
func (s *Service) Update(ctx context.Context, user, slug string, req UpdateRequest) (Image, error) {
	if err := ValidateName(req.Name); err != nil {
		return Image{}, err
	}
	if err := validateRetention(req.Retention); err != nil {
		return Image{}, err
	}
	unlock := s.lockImage(slug)
	defer unlock()
	img, err := s.store.Image(ctx, slug)
	if err != nil {
		return Image{}, err
	}
	path := req.DockerfilePath
	if path == "" {
		path = img.DockerfilePath
	}
	if path, err = validateSettings(path, req.BuildArgs); err != nil {
		return Image{}, err
	}
	img.Name, img.DockerfilePath, img.BuildArgs, img.Retention = req.Name, path, req.BuildArgs, req.Retention
	if img.Source == SourceGitHub {
		img.GitHub = req.GitHub
	}
	if err := s.store.UpdateImage(ctx, img); err != nil {
		return Image{}, err
	}
	s.record(ctx, user, "image.update", slug, slug, img.Name, "succeeded", "")
	return s.store.Image(ctx, slug)
}

// Delete removes an image, its versions, Docker tags, blobs and logs. It
// refuses with *InUseError while any stack uses one of its versions, and with
// ErrBuildRunning during a build.
func (s *Service) Delete(ctx context.Context, user, slug string) error {
	unlock := s.lockImage(slug)
	defer unlock()
	img, err := s.store.Image(ctx, slug)
	if err != nil {
		return err
	}
	_, owner, err := s.selfInfo(ctx)
	if err != nil {
		return err
	}
	versions, err := s.store.Versions(ctx, img.ID)
	if err != nil {
		return err
	}
	var live []Version
	for _, v := range versions {
		switch v.Status {
		case VersionBuilding:
			return ErrBuildRunning
		case VersionSucceeded:
			live = append(live, v)
		}
	}
	for _, v := range live {
		if err := s.checkUnused(ctx, owner, slug, v.Tag); err != nil {
			return err
		}
	}
	for _, v := range live {
		if err := s.removeDockerTag(ctx, imageref.Format(owner, slug, v.Tag)); err != nil {
			return err
		}
	}
	if err := s.store.DeleteImage(ctx, img.ID); err != nil {
		return err
	}
	if err := os.RemoveAll(s.imageDir(slug)); err != nil {
		s.log.Warn("remove image directory failed", "image", slug, "err", err)
	}
	s.record(ctx, user, "image.delete", slug, slug, img.Name, "succeeded", "")
	return nil
}

func (s *Service) removeDockerTag(ctx context.Context, ref string) error {
	if _, err := s.eng.RemoveImage(ctx, ref); err != nil && !errors.Is(err, engine.ErrImageNotFound) {
		return fmt.Errorf("remove docker image %s: %w", ref, err)
	}
	return nil
}

// ---- source ----

func (s *Service) manifestOf(ctx context.Context, img Image) (Manifest, int, error) {
	rev, ok, err := s.store.LatestRevision(ctx, img.ID)
	if err != nil {
		return nil, 0, err
	}
	if !ok {
		return Manifest{}, 0, nil
	}
	m, err := ParseManifest(rev.ManifestJSON)
	return m, rev.Number, err
}

// Files returns the latest revision's files and its number.
func (s *Service) Files(ctx context.Context, slug string) (Manifest, int, error) {
	img, err := s.store.Image(ctx, slug)
	if err != nil {
		return nil, 0, err
	}
	return s.manifestOf(ctx, img)
}

const maxReadFileBytes = 1 << 20

func (s *Service) readFrom(img Image, m Manifest, p string) ([]byte, error) {
	clean, err := ValidatePath(p)
	if err != nil {
		return nil, err
	}
	e, ok := m[clean]
	if !ok {
		return nil, fmt.Errorf("%w: %q", fs.ErrNotExist, clean)
	}
	if e.Size > maxReadFileBytes {
		return nil, ErrFileTooLarge
	}
	b, err := s.blobs(img.Slug)
	if err != nil {
		return nil, err
	}
	f, err := b.Open(e.Hash)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxReadFileBytes+1))
}

// ReadFile returns one file of the latest revision; ErrFileTooLarge over 1 MiB.
func (s *Service) ReadFile(ctx context.Context, slug, p string) ([]byte, error) {
	img, err := s.store.Image(ctx, slug)
	if err != nil {
		return nil, err
	}
	m, _, err := s.manifestOf(ctx, img)
	if err != nil {
		return nil, err
	}
	return s.readFrom(img, m, p)
}

// Dockerfile returns the latest Dockerfile and its revision number.
func (s *Service) Dockerfile(ctx context.Context, slug string) ([]byte, int, error) {
	img, err := s.store.Image(ctx, slug)
	if err != nil {
		return nil, 0, err
	}
	m, n, err := s.manifestOf(ctx, img)
	if err != nil {
		return nil, 0, err
	}
	b, err := s.readFrom(img, m, img.DockerfilePath)
	return b, n, err
}

// RevisionDockerfile returns the Dockerfile as of revision n.
func (s *Service) RevisionDockerfile(ctx context.Context, slug string, n int) ([]byte, error) {
	img, err := s.store.Image(ctx, slug)
	if err != nil {
		return nil, err
	}
	rev, err := s.store.Revision(ctx, img.ID, n)
	if err != nil {
		return nil, err
	}
	m, err := ParseManifest(rev.ManifestJSON)
	if err != nil {
		return nil, err
	}
	return s.readFrom(img, m, img.DockerfilePath)
}

// Revisions lists the image's revisions, newest first.
func (s *Service) Revisions(ctx context.Context, slug string) ([]Revision, error) {
	img, err := s.store.Image(ctx, slug)
	if err != nil {
		return nil, err
	}
	return s.store.Revisions(ctx, img.ID)
}

// ErrRevisionConflict means the Dockerfile was saved against a stale revision.
var ErrRevisionConflict = errors.New("the source changed since you opened it")

type sourceEdit func(m Manifest, b *Blobs, img Image) (Manifest, string, error)

// editSource runs one source mutation under the image lock: the edit (which
// may upload blobs) and the new revision are written without another edit,
// restore or blob collection interleaving. A negative expect skips the
// revision check. When the edit fails, blobs it
// stored are collected under the same lock.
func (s *Service) editSource(ctx context.Context, user, slug string, expect int, edit sourceEdit) (Revision, error) {
	unlock := s.lockImage(slug)
	defer unlock()
	img, err := s.store.Image(ctx, slug)
	if err != nil {
		return Revision{}, err
	}
	if img.Source != SourceUpload {
		return Revision{}, fieldError("source", "this image's source is not editable")
	}
	m, n, err := s.manifestOf(ctx, img)
	if err != nil {
		return Revision{}, err
	}
	if expect >= 0 && expect != n {
		return Revision{}, ErrRevisionConflict
	}
	b, err := s.blobs(slug)
	if err != nil {
		return Revision{}, err
	}
	next, summary, err := edit(m, b, img)
	var rev Revision
	if err == nil {
		rev, err = s.store.AddRevision(ctx, img.ID, summary, next.JSON())
	}
	if err != nil {
		s.collectLocked(ctx, img)
		return Revision{}, err
	}
	s.record(ctx, user, "image.source", slug, slug, summary, "succeeded", "")
	return rev, nil
}

// collectLocked removes blobs no revision references. Caller holds the image lock.
func (s *Service) collectLocked(ctx context.Context, img Image) (int, error) {
	revs, err := s.store.Revisions(ctx, img.ID)
	if err != nil {
		return 0, err
	}
	keep := map[string]bool{}
	for _, r := range revs {
		m, err := ParseManifest(r.ManifestJSON)
		if err != nil {
			return 0, err // unreadable manifest: collect nothing
		}
		for _, e := range m {
			keep[e.Hash] = true
		}
	}
	b, err := s.blobs(img.Slug)
	if err != nil {
		return 0, err
	}
	n, err := b.GC(keep)
	if err != nil {
		s.log.Warn("blob collection failed", "image", img.Slug, "err", err)
	}
	return n, err
}

// CollectBlobs removes unreferenced blobs of an image under its source lock.
func (s *Service) CollectBlobs(ctx context.Context, slug string) (int, error) {
	unlock := s.lockImage(slug)
	defer unlock()
	img, err := s.store.Image(ctx, slug)
	if err != nil {
		return 0, err
	}
	return s.collectLocked(ctx, img)
}

// SaveDockerfile saves the Dockerfile as a new revision; ErrRevisionConflict
// unless revision is the latest.
func (s *Service) SaveDockerfile(ctx context.Context, user, slug string, content []byte, revision int) (Revision, error) {
	return s.editSource(ctx, user, slug, revision, func(m Manifest, b *Blobs, img Image) (Manifest, string, error) {
		return ApplyDockerfile(m, b, img.DockerfilePath, content, s.limits)
	})
}

// Upload adds or replaces files.
func (s *Service) Upload(ctx context.Context, user, slug string, files []UploadFile) (Revision, error) {
	return s.editSource(ctx, user, slug, -1, func(m Manifest, b *Blobs, _ Image) (Manifest, string, error) {
		return ApplyUpload(m, b, files, s.limits)
	})
}

// Rename moves a file or folder.
func (s *Service) Rename(ctx context.Context, user, slug, from, to string) (Revision, error) {
	return s.editSource(ctx, user, slug, -1, func(m Manifest, _ *Blobs, _ Image) (Manifest, string, error) {
		return ApplyRename(m, from, to)
	})
}

// DeleteFile removes a file or folder.
func (s *Service) DeleteFile(ctx context.Context, user, slug, p string) (Revision, error) {
	return s.editSource(ctx, user, slug, -1, func(m Manifest, _ *Blobs, _ Image) (Manifest, string, error) {
		return ApplyDelete(m, p)
	})
}

// Restore makes revision n the newest revision again.
func (s *Service) Restore(ctx context.Context, user, slug string, n int) (Revision, error) {
	return s.editSource(ctx, user, slug, -1, func(_ Manifest, _ *Blobs, img Image) (Manifest, string, error) {
		rev, err := s.store.Revision(ctx, img.ID, n)
		if err != nil {
			return nil, "", err
		}
		m, err := ParseManifest(rev.ManifestJSON)
		return m, fmt.Sprintf("restored revision %d", n), err
	})
}

// ---- startup ----

const interruptedMessage = "interrupted by server restart"

// Start repairs state left by a crash or restart: building versions are marked
// failed and leftover build directories are removed.
func (s *Service) Start(ctx context.Context) error {
	building, err := s.store.BuildingVersions(ctx)
	if err != nil {
		return err
	}
	for _, v := range building {
		if _, err := s.finishBuilding(ctx, v.ID, VersionResult{Status: VersionFailed, Error: interruptedMessage}); err != nil {
			return err
		}
	}
	if _, err := s.store.db.ExecContext(ctx, `UPDATE image_pushes SET status='failed', error=?, updated_at=? WHERE status='pushing'`,
		interruptedMessage, s.store.stamp().Unix()); err != nil {
		return fmt.Errorf("fail interrupted pushes: %w", err)
	}
	if matches, err := filepath.Glob(filepath.Join(s.dir, "*", "tmp-*")); err == nil {
		for _, m := range matches {
			os.RemoveAll(m)
		}
	}
	return nil
}

// finishBuilding records a result only while the version is still building,
// so a late or repeated finish cannot overwrite a final state.
func (s *Service) finishBuilding(ctx context.Context, id string, r VersionResult) (bool, error) {
	res, err := s.store.db.ExecContext(ctx, `UPDATE image_versions SET status=?, arch=?, image_digest=?, size=?, error=?, ended_at=?
		WHERE id=? AND status='building'`, string(r.Status), r.Arch, r.ImageDigest, r.Size, r.Error, s.store.stamp().Unix(), id)
	if err != nil {
		return false, fmt.Errorf("finish version: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
