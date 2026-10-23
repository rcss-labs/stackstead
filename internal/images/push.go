package images

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/registry"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

// ErrRegistryUnconfigured is answered when a push needs a registry that has
// no credentials (or when no registry service is wired).
var ErrRegistryUnconfigured = registry.ErrUnconfigured

// RegistryAuth is the part of the registry service the image library uses.
type RegistryAuth interface {
	Credentials(ctx context.Context, id string) (engine.RegistryAuth, string, error)
	Get(ctx context.Context, id string) (registry.Registry, error)
}

// PushTargetRequest names a registry repository an image is pushed to.
type PushTargetRequest struct {
	RegistryID, Repository string
	AutoPush               bool
}

const maxRepositoryLen = 255

var repositoryPattern = regexp.MustCompile(`^[a-z0-9]+(?:[._/-][a-z0-9]+)*$`)

// ValidateRepository enforces the repository name rules.
func ValidateRepository(repo string) error {
	if repo == "" {
		return fieldError("repository", "repository is required")
	}
	if len(repo) > maxRepositoryLen || !repositoryPattern.MatchString(repo) {
		return fieldError("repository", "repository must be lowercase letters and digits separated by single ., _, - or /, at most 255 characters")
	}
	return nil
}

func (s *Service) checkTargetRequest(ctx context.Context, req PushTargetRequest) error {
	if s.registries == nil {
		return ErrRegistryUnconfigured
	}
	if err := ValidateRepository(req.Repository); err != nil {
		return err
	}
	if req.RegistryID == "" {
		return fieldError("registryId", "registry is required")
	}
	if _, err := s.registries.Get(ctx, req.RegistryID); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			return fieldError("registryId", "registry not found")
		}
		return err
	}
	return nil
}

func duplicateTarget(targets []PushTarget, req PushTargetRequest, exceptID string) error {
	for _, t := range targets {
		if t.ID != exceptID && t.RegistryID == req.RegistryID && t.Repository == req.Repository {
			return fieldError("repository", "this registry and repository is already a push target")
		}
	}
	return nil
}

// AddPushTarget adds a push target to an image.
func (s *Service) AddPushTarget(ctx context.Context, user, slug string, req PushTargetRequest) (PushTarget, error) {
	if err := s.checkTargetRequest(ctx, req); err != nil {
		return PushTarget{}, err
	}
	unlock := s.lockImage(slug)
	defer unlock()
	img, err := s.store.Image(ctx, slug)
	if err != nil {
		return PushTarget{}, err
	}
	targets, err := s.store.PushTargets(ctx, img.ID)
	if err != nil {
		return PushTarget{}, err
	}
	if err := duplicateTarget(targets, req, ""); err != nil {
		return PushTarget{}, err
	}
	return s.store.AddPushTarget(ctx, PushTarget{ImageID: img.ID, RegistryID: req.RegistryID, Repository: req.Repository, AutoPush: req.AutoPush})
}

// UpdatePushTarget changes a push target of an image.
func (s *Service) UpdatePushTarget(ctx context.Context, user, slug, id string, req PushTargetRequest) (PushTarget, error) {
	if err := s.checkTargetRequest(ctx, req); err != nil {
		return PushTarget{}, err
	}
	unlock := s.lockImage(slug)
	defer unlock()
	img, err := s.store.Image(ctx, slug)
	if err != nil {
		return PushTarget{}, err
	}
	targets, err := s.store.PushTargets(ctx, img.ID)
	if err != nil {
		return PushTarget{}, err
	}
	t, ok := findTarget(targets, id)
	if !ok {
		return PushTarget{}, ErrTargetNotFound
	}
	if err := duplicateTarget(targets, req, id); err != nil {
		return PushTarget{}, err
	}
	moved := t.RegistryID != req.RegistryID || t.Repository != req.Repository
	t.RegistryID, t.Repository, t.AutoPush = req.RegistryID, req.Repository, req.AutoPush
	if err := s.store.UpdatePushTarget(ctx, t); err != nil {
		return PushTarget{}, err
	}
	if moved {
		// Earlier pushes went to the old destination; they say nothing about the new one.
		if _, err := s.store.db.ExecContext(ctx, `DELETE FROM image_pushes WHERE target_id = ?`, id); err != nil {
			return PushTarget{}, fmt.Errorf("clear pushes of moved target: %w", err)
		}
	}
	return t, nil
}

// DeletePushTarget removes a push target and its push records.
func (s *Service) DeletePushTarget(ctx context.Context, user, slug, id string) error {
	unlock := s.lockImage(slug)
	defer unlock()
	img, err := s.store.Image(ctx, slug)
	if err != nil {
		return err
	}
	targets, err := s.store.PushTargets(ctx, img.ID)
	if err != nil {
		return err
	}
	if _, ok := findTarget(targets, id); !ok {
		return ErrTargetNotFound
	}
	return s.store.DeletePushTarget(ctx, id)
}

func findTarget(targets []PushTarget, id string) (PushTarget, bool) {
	for _, t := range targets {
		if t.ID == id {
			return t, true
		}
	}
	return PushTarget{}, false
}

// Push starts an operation that pushes the live succeeded version with the
// tag to the target. A running build or push of the image answers
// ErrBuildRunning; an unconfigured registry answers ErrRegistryUnconfigured.
func (s *Service) Push(ctx context.Context, user, slug, tag, targetID string) (stacks.Operation, error) {
	if s.registries == nil {
		return stacks.Operation{}, ErrRegistryUnconfigured
	}
	_, owner, err := s.selfInfo(ctx)
	if err != nil {
		return stacks.Operation{}, err
	}
	img, err := s.store.Image(ctx, slug)
	if err != nil {
		return stacks.Operation{}, err
	}
	v, err := s.store.Version(ctx, img.ID, tag)
	if err != nil {
		return stacks.Operation{}, err
	}
	if v.Status != VersionSucceeded {
		return stacks.Operation{}, fieldError("tag", "only a succeeded version can be pushed")
	}
	targets, err := s.store.PushTargets(ctx, img.ID)
	if err != nil {
		return stacks.Operation{}, err
	}
	target, ok := findTarget(targets, targetID)
	if !ok {
		return stacks.Operation{}, ErrTargetNotFound
	}
	reg, err := s.registries.Get(ctx, target.RegistryID)
	if err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			return stacks.Operation{}, ErrTargetNotFound
		}
		return stacks.Operation{}, err
	}
	auth, host, err := s.registries.Credentials(ctx, target.RegistryID)
	if err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			return stacks.Operation{}, ErrTargetNotFound
		}
		return stacks.Operation{}, err
	}
	// Credentials may discover the host (ECR); the earlier Get can predate it.
	if host != "" {
		reg.Host = host
	}

	opReady := make(chan struct{})
	var opID string
	op, err := s.ops.StartTimeout(OpKey(owner, slug), "push", s.timeout, func(opCtx context.Context, out func(string)) error {
		<-opReady
		return s.pushVersion(opCtx, user, img, v, target, reg, auth, opID, out)
	})
	if err != nil {
		if errors.Is(err, stacks.ErrBusy) {
			return stacks.Operation{}, ErrBuildRunning
		}
		return stacks.Operation{}, err
	}
	opID = op.ID
	close(opReady)
	return op, nil
}

// pushVersion tags the version for the registry, pushes it, untags it again
// and records the outcome on the push row and in the action log. It holds no
// image lock. The returned error is already scrubbed of the credential.
func (s *Service) pushVersion(ctx context.Context, user string, img Image, v Version, target PushTarget, reg registry.Registry, auth engine.RegistryAuth, opID string, out func(string)) error {
	scrub := newScrubber(auth.Password)
	emit := func(line string) { out(scrub(line)) }
	bg := context.WithoutCancel(ctx)
	detail := reg.Name + " " + target.Repository + ":" + v.Tag

	digest, err := s.runPush(ctx, img, v, target, reg, auth, emit)
	if err != nil {
		msg := scrub(err.Error())
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			msg = "push timed out"
		case ctx.Err() != nil:
			msg = "push cancelled"
		}
		err = errors.New(msg)
		emit("push failed: " + msg)
		s.finishPush(bg, Push{VersionID: v.ID, TargetID: target.ID, Status: PushFailed, Error: msg})
		s.record(bg, user, "image.push", img.Slug, v.Tag, detail+": "+msg, "failed", opID)
		return err
	}
	emit("pushed " + detail + " " + digest)
	s.finishPush(bg, Push{VersionID: v.ID, TargetID: target.ID, Status: PushPushed, Digest: digest})
	s.record(bg, user, "image.push", img.Slug, v.Tag, detail, "succeeded", opID)
	return nil
}

func (s *Service) runPush(ctx context.Context, img Image, v Version, target PushTarget, reg registry.Registry, auth engine.RegistryAuth, emit func(string)) (string, error) {
	_, owner, err := s.selfInfo(ctx)
	if err != nil {
		return "", err
	}
	if err := s.store.SetPush(ctx, Push{VersionID: v.ID, TargetID: target.ID, Status: PushPushing}); err != nil {
		return "", fmt.Errorf("the push target or version was removed")
	}
	ref := s.Ref(owner, img.Slug, v.Tag)
	pushRef := registry.PushRef(reg, target.Repository, v.Tag)
	repo, tag, err := splitRef(pushRef)
	if err != nil {
		return "", err
	}
	emit(fmt.Sprintf("pushing to %s", reg.Name))
	if err := s.eng.TagImage(ctx, ref, repo, tag); err != nil {
		return "", fmt.Errorf("tag for push: %w", err)
	}
	defer func() {
		// Untag only: the library reference keeps the image alive.
		if _, err := s.eng.RemoveImage(context.WithoutCancel(ctx), pushRef); err != nil && !errors.Is(err, engine.ErrImageNotFound) {
			s.log.Warn("untag pushed image failed", "image", img.Slug, "err", err)
		}
	}()
	digest, err := s.eng.Push(ctx, pushRef, auth, emit)
	if err != nil {
		return "", err
	}
	return digest, nil
}

// finishPush writes the final push state. When the target or version was
// deleted meanwhile (a deleted registry cascades its targets away) the row is
// gone with them and the result is dropped; a failure with the row still
// there is logged.
func (s *Service) finishPush(ctx context.Context, p Push) {
	err := s.store.SetPush(ctx, p)
	if err == nil {
		return
	}
	if _, verr := s.store.VersionByID(ctx, p.VersionID); errors.Is(verr, ErrVersionNotFound) {
		return
	}
	if !s.targetExists(ctx, p.VersionID, p.TargetID) {
		return
	}
	s.log.Error("record push state failed", "version", p.VersionID, "target", p.TargetID, "err", err)
}

func (s *Service) targetExists(ctx context.Context, versionID, targetID string) bool {
	v, err := s.store.VersionByID(ctx, versionID)
	if err != nil {
		return false
	}
	targets, err := s.store.PushTargets(ctx, v.ImageID)
	if err != nil {
		return true
	}
	_, ok := findTarget(targets, targetID)
	return ok
}

// autoPush pushes a freshly built upload version to every auto-push target
// whose registry is configured, one after another inside the build operation.
// A failure is recorded on the push and never fails the build.
func (s *Service) autoPush(ctx context.Context, user string, img Image, v Version, opID string, out func(string)) {
	if s.registries == nil || img.Source != SourceUpload {
		return
	}
	targets, err := s.store.PushTargets(ctx, img.ID)
	if err != nil {
		s.log.Warn("list push targets for auto-push failed", "image", img.Slug, "err", err)
		return
	}
	for _, t := range targets {
		if !t.AutoPush {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		if _, err := s.store.VersionByID(ctx, v.ID); errors.Is(err, ErrVersionNotFound) {
			return // version deleted meanwhile: nothing left to push or record
		}
		reg, err := s.registries.Get(ctx, t.RegistryID)
		if err != nil {
			if !errors.Is(err, registry.ErrNotFound) {
				s.log.Warn("auto-push skipped: read registry failed", "image", img.Slug, "target", t.ID, "err", err)
			}
			continue
		}
		auth, host, err := s.registries.Credentials(ctx, t.RegistryID)
		if err != nil {
			if !errors.Is(err, registry.ErrUnconfigured) && !errors.Is(err, registry.ErrNotFound) {
				// registry.Service already scrubs its own errors of stored secrets.
				msg := err.Error()
				bg := context.WithoutCancel(ctx)
				s.finishPush(bg, Push{VersionID: v.ID, TargetID: t.ID, Status: PushFailed, Error: msg})
				s.record(bg, user, "image.push", img.Slug, v.Tag, reg.Name+" "+t.Repository+":"+v.Tag+": "+msg, "failed", opID)
				out("push to " + reg.Name + " failed: " + msg)
			}
			continue
		}
		if host != "" {
			reg.Host = host
		}
		// pushVersion reports its own failure; the build is not affected.
		_ = s.pushVersion(ctx, user, img, v, t, reg, auth, opID, out)
	}
}

// splitRef splits repo:tag, refusing a reference with no registry host or tag
// (for example a registry whose host is not known yet).
func splitRef(ref string) (repo, tag string, err error) {
	i := strings.LastIndex(ref, ":")
	if ref == "" || i <= 0 || i == len(ref)-1 || strings.HasPrefix(ref, "/") {
		return "", "", fmt.Errorf("invalid push reference %q: registry host is unknown", ref)
	}
	return ref[:i], ref[i+1:], nil
}

// newScrubber returns a function that replaces every occurrence of the
// secrets with a marker.
func newScrubber(secrets ...string) func(string) string {
	return func(s string) string {
		for _, secret := range secrets {
			if secret != "" {
				s = strings.ReplaceAll(s, secret, "[redacted]")
			}
		}
		return s
	}
}
