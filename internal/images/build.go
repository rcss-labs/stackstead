package images

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"

	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/imageref"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

// BuildRequest asks for a new version. BuildArgs override the image defaults;
// SaveArgs merges them into the defaults before the build. Ref (GitHub images
// only) is a 40-hex commit, or a branch or tag name resolved to one; empty
// means the head of the image's branch.
type BuildRequest struct {
	Tag       string
	BuildArgs map[string]string
	SaveArgs  bool
	Ref       string
}

const maxErrorBytes = 4096

// Build records a building version and starts the build as an operation. The
// version row is written first, so a taken tag or a running build is answered
// before any operation exists.
func (s *Service) Build(ctx context.Context, user, slug string, req BuildRequest) (Version, stacks.Operation, error) {
	if err := ValidateTag(req.Tag); err != nil {
		return Version{}, stacks.Operation{}, err
	}
	if err := ValidateBuildArgs(req.BuildArgs); err != nil {
		return Version{}, stacks.Operation{}, err
	}
	_, owner, err := s.selfInfo(ctx)
	if err != nil {
		return Version{}, stacks.Operation{}, err
	}
	// A GitHub commit is resolved over the network, so before the image lock.
	pre, err := s.store.Image(ctx, slug)
	if err != nil {
		return Version{}, stacks.Operation{}, err
	}
	if pre.Source != SourceGitHub && req.Ref != "" {
		return Version{}, stacks.Operation{}, fieldError("ref", "only GitHub images can be built at a ref")
	}
	var commit string
	if pre.Source == SourceGitHub {
		if commit, err = s.resolveCommit(ctx, pre.GitHub, req.Ref); err != nil {
			return Version{}, stacks.Operation{}, err
		}
	}

	if s.beforeBuildLock != nil {
		s.beforeBuildLock()
	}

	// The image is read under the lock so a concurrent update or delete is
	// seen, and a saved-defaults write never overwrites other fields.
	unlock := s.lockImage(slug)
	img, err := s.store.Image(ctx, slug)
	if err != nil {
		unlock()
		return Version{}, stacks.Operation{}, err
	}
	if img.Source == SourceGitHub && (img.GitHub.Repo != pre.GitHub.Repo || img.GitHub.Branch != pre.GitHub.Branch) {
		unlock()
		return Version{}, stacks.Operation{}, fieldError("github", "the image's repository changed, try again")
	}
	if img.Source != SourceUpload && img.Source != SourceGitHub {
		unlock()
		return Version{}, stacks.Operation{}, fieldError("source", "building from this source is not supported")
	}
	previous := maps.Clone(img.BuildArgs)
	args := maps.Clone(img.BuildArgs)
	if args == nil {
		args = map[string]string{}
	}
	maps.Copy(args, req.BuildArgs)

	next := Version{ImageID: img.ID, Tag: req.Tag, BuildArgs: args}
	var ok bool
	if img.Source == SourceGitHub {
		next.Commit = commit
	} else {
		var rev Revision
		rev, ok, err = s.store.LatestRevision(ctx, img.ID)
		if err == nil && !ok {
			err = errors.New("image has no source revision")
		}
		next.Revision = rev.Number
	}
	var v Version
	if err == nil {
		v, err = s.store.StartVersion(ctx, next)
	}
	if err == nil && req.SaveArgs {
		img.BuildArgs = args
		if err = s.store.UpdateImage(ctx, img); err != nil {
			err = fmt.Errorf("save build defaults: %w", err)
			s.store.DeleteVersion(context.WithoutCancel(ctx), v.ID)
		}
	}
	unlock()
	if err != nil {
		return Version{}, stacks.Operation{}, err
	}

	opReady := make(chan struct{})
	var opID string
	op, err := s.ops.StartTimeout(OpKey(owner, slug), "build", s.timeout, func(opCtx context.Context, out func(string)) error {
		<-opReady
		return s.runBuild(opCtx, user, img, v, owner, opID, args, out)
	})
	if err != nil {
		bg := context.WithoutCancel(ctx)
		if derr := s.store.DeleteVersion(bg, v.ID); derr != nil {
			s.log.Error("remove version after failed build start", "image", slug, "version", v.ID, "err", derr)
		}
		if req.SaveArgs {
			s.restoreDefaults(bg, slug, previous)
		}
		if errors.Is(err, stacks.ErrBusy) {
			return Version{}, stacks.Operation{}, ErrImageBusy
		}
		return Version{}, stacks.Operation{}, err
	}
	opID = op.ID
	v.OperationID = op.ID
	_, uerr := s.store.db.ExecContext(ctx, `UPDATE image_versions SET operation_id = ? WHERE id = ?`, op.ID, v.ID)
	close(opReady)
	if uerr != nil {
		s.log.Warn("record build operation failed", "image", slug, "err", uerr)
	}
	return v, op, nil
}

func (s *Service) runBuild(ctx context.Context, user string, img Image, v Version, owner, opID string, args map[string]string, out func(string)) (err error) {
	ref := s.Ref(owner, img.Slug, v.Tag)
	bl, lerr := openBuildLog(s.logPath(img.Slug, v.ID))
	emit := out
	if lerr != nil {
		s.log.Warn("open build log failed", "image", img.Slug, "err", lerr)
	} else {
		defer bl.close()
		emit = func(line string) { out(line); bl.line(line) }
	}
	result := VersionResult{Status: VersionFailed, Error: "build aborted"}
	finished := false
	defer func() {
		if r := recover(); r != nil {
			if !finished {
				result.Error = fmt.Sprintf("build panicked: %v", r)
				s.finishAndLog(ctx, user, img, v, opID, result)
			}
			panic(r)
		}
		if !finished {
			s.finishAndLog(ctx, user, img, v, opID, result)
		}
	}()

	inspect, err := s.build(ctx, img, v, ref, args, emit)
	if err != nil {
		result.Error = buildErrorText(ctx, err)
		emit("build failed: " + result.Error)
		return err
	}
	arch := inspect.Architecture
	result = VersionResult{Status: VersionSucceeded, Arch: arch, ImageDigest: inspect.ID, Size: inspect.Size}
	// The version is finished before auto-push so it shows as succeeded while
	// pushes run; a push failure never changes the build outcome.
	s.finishAndLog(ctx, user, img, v, opID, result)
	finished = true
	s.autoPush(ctx, user, img, v, opID, emit)
	s.applyRetentionAfterBuild(ctx, user, img.Slug)
	return nil
}

func buildErrorText(ctx context.Context, err error) string {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "build timed out"
	case ctx.Err() != nil:
		return "build cancelled"
	}
	msg := err.Error()
	if len(msg) > maxErrorBytes {
		msg = msg[:maxErrorBytes] + "…"
	}
	return msg
}

func (s *Service) finishAndLog(ctx context.Context, user string, img Image, v Version, opID string, r VersionResult) {
	bg := context.WithoutCancel(ctx)
	if _, err := s.finishBuilding(bg, v.ID, r); err != nil {
		s.log.Error("finish version failed", "image", img.Slug, "version", v.ID, "err", err)
	}
	result := "failed"
	if r.Status == VersionSucceeded {
		result = "succeeded"
	}
	detail := "tag " + v.Tag
	if r.Error != "" {
		detail += ": " + r.Error
	}
	s.record(bg, user, "image.build", img.Slug, v.Tag, detail, result, opID)
}

// build materializes the version's revision, streams it to the engine and
// inspects the result.
func (s *Service) build(ctx context.Context, img Image, v Version, ref string, args map[string]string, emit func(string)) (engine.ImageInspect, error) {
	tmp := filepath.Join(s.imageDir(img.Slug), "tmp-"+v.ID)
	defer os.RemoveAll(tmp)
	if img.Source == SourceGitHub {
		emit(fmt.Sprintf("downloading %s at %s", img.GitHub.Repo, v.Commit))
		if err := s.extractGitHub(ctx, img, v.Commit, tmp); err != nil {
			return engine.ImageInspect{}, err
		}
		emit(fmt.Sprintf("building %s from commit %s", ref, v.Commit))
	} else {
		rev, err := s.store.Revision(ctx, img.ID, v.Revision)
		if err != nil {
			return engine.ImageInspect{}, err
		}
		m, err := ParseManifest(rev.ManifestJSON)
		if err != nil {
			return engine.ImageInspect{}, err
		}
		blobs, err := s.blobs(img.Slug)
		if err != nil {
			return engine.ImageInspect{}, err
		}
		if err := Materialize(tmp, m, blobs); err != nil {
			return engine.ImageInspect{}, fmt.Errorf("prepare build context: %w", err)
		}
		emit(fmt.Sprintf("building %s from revision %d", ref, v.Revision))
	}
	pr, pw := io.Pipe()
	written := make(chan struct{})
	go func() {
		defer close(written)
		pw.CloseWithError(WriteContext(pw, tmp, img.DockerfilePath))
	}()
	res, err := s.eng.Build(ctx, pr, engine.BuildOptions{
		Tags:       []string{ref},
		Dockerfile: img.DockerfilePath,
		BuildArgs:  args,
		Labels:     map[string]string{"stackstead.image": img.Slug, "stackstead.version": v.ID},
	}, emit)
	pr.Close()
	<-written
	if err != nil {
		return engine.ImageInspect{}, err
	}
	inspect, err := s.eng.InspectImage(ctx, ref)
	if err != nil {
		return engine.ImageInspect{}, fmt.Errorf("inspect built image: %w", err)
	}
	if inspect.ID == "" {
		inspect.ID = res.ImageID
	}
	return inspect, nil
}

// ---- versions ----

// VersionDetail is a version with its build log and the stacks using it.
type VersionDetail struct {
	Version
	Log    string
	UsedBy []UsedBy
}

func (s *Service) versionOf(ctx context.Context, slug, id string) (Image, Version, error) {
	img, err := s.store.Image(ctx, slug)
	if err != nil {
		return Image{}, Version{}, err
	}
	v, err := s.store.VersionByID(ctx, id)
	if err != nil {
		return Image{}, Version{}, err
	}
	if v.ImageID != img.ID {
		return Image{}, Version{}, ErrVersionNotFound
	}
	return img, v, nil
}

// VersionByTag returns the live (non-failed) version with the tag.
func (s *Service) VersionByTag(ctx context.Context, slug, tag string) (Version, error) {
	img, err := s.store.Image(ctx, slug)
	if err != nil {
		return Version{}, err
	}
	return s.store.Version(ctx, img.ID, tag)
}

// VersionDetail returns the version with the ID, its log and its users.
func (s *Service) VersionDetail(ctx context.Context, slug, id string) (VersionDetail, error) {
	img, v, err := s.versionOf(ctx, slug, id)
	if err != nil {
		return VersionDetail{}, err
	}
	log, err := s.readLog(img.Slug, v.ID)
	if err != nil {
		return VersionDetail{}, err
	}
	d := VersionDetail{Version: v, Log: log, UsedBy: []UsedBy{}}
	if v.Status == VersionSucceeded {
		_, owner, err := s.selfInfo(ctx)
		if err != nil {
			return VersionDetail{}, err
		}
		if d.UsedBy, _, err = s.usedBy(ctx, imageref.Ref{Owner: owner, Slug: slug, Tag: v.Tag}); err != nil {
			return VersionDetail{}, err
		}
	}
	return d, nil
}

// DeleteVersion removes a version, its Docker tag and its log. It refuses with
// *InUseError while a stack uses the version and ErrBuildRunning while it builds.
func (s *Service) DeleteVersion(ctx context.Context, user, slug, id string) error {
	unlock := s.lockImage(slug)
	defer unlock()
	img, v, err := s.versionOf(ctx, slug, id)
	if err != nil {
		return err
	}
	if v.Status == VersionBuilding {
		return ErrBuildRunning
	}
	_, owner, err := s.selfInfo(ctx)
	if err != nil {
		return err
	}
	if v.Status == VersionSucceeded {
		if err := s.checkUnused(ctx, imageref.Ref{Owner: owner, Slug: slug, Tag: v.Tag}); err != nil {
			return err
		}
	}
	if err := s.removeVersionLocked(ctx, img.Slug, owner, v); err != nil {
		return err
	}
	s.record(ctx, user, "image.version", slug, v.Tag, "deleted version "+v.Tag, "succeeded", "")
	return nil
}

// removeVersionLocked deletes a version: its Docker tag (never forced, so an
// image ID shared with another tag survives), its row with the pushes and its
// log. The caller holds the image lock and has checked that it is unused.
func (s *Service) removeVersionLocked(ctx context.Context, slug, owner string, v Version) error {
	// Only a live version owns its reference; a failed one may share the tag.
	if v.Status == VersionSucceeded {
		if err := s.removeDockerTag(ctx, s.Ref(owner, slug, v.Tag)); err != nil {
			return err
		}
	}
	return s.deleteVersionRow(ctx, slug, v)
}

// deleteVersionRow removes a version's row (pushes cascade) and its log file.
func (s *Service) deleteVersionRow(ctx context.Context, slug string, v Version) error {
	if err := s.store.DeleteVersion(ctx, v.ID); err != nil {
		return err
	}
	os.Remove(s.logPath(slug, v.ID))
	return nil
}

// restoreDefaults puts back the build defaults a build that never started had saved.
func (s *Service) restoreDefaults(ctx context.Context, slug string, args map[string]string) {
	unlock := s.lockImage(slug)
	defer unlock()
	img, err := s.store.Image(ctx, slug)
	if err == nil {
		img.BuildArgs = args
		err = s.store.UpdateImage(ctx, img)
	}
	if err != nil {
		s.log.Error("restore build defaults failed", "image", slug, "err", err)
	}
}
