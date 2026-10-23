package images

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/rcss-labs/stackstead/internal/imageref"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

const (
	sweepFirstDelay = 5 * time.Minute
	sweepInterval   = 6 * time.Hour
	// SweepUser is the action-log user of removals made by the background sweep.
	SweepUser = "system"
)

// RetentionResult says what one retention run did. Skipped names the
// unreachable server that blocked the run; it is empty otherwise.
type RetentionResult struct {
	Removed []Version
	Kept    int
	Skipped string
}

// Keep returns the IDs of the versions the policy keeps. KeepLast counts
// succeeded versions only, newest first by StartedAt, so a burst of failed
// builds never evicts a good version. KeepDays keeps any row, of either status,
// younger than that many days. A failed row is also kept when it started after
// the oldest succeeded version KeepLast keeps (it is part of that recent
// history). Building rows are always kept, and so is the newest succeeded
// version, so a policy can never leave an image with nothing to run. A policy
// with both limits at zero keeps everything.
func Keep(policy Retention, versions []Version, now time.Time) map[string]bool {
	keep := make(map[string]bool, len(versions))
	if policy.KeepLast <= 0 && policy.KeepDays <= 0 {
		for _, v := range versions {
			keep[v.ID] = true
		}
		return keep
	}
	window := time.Duration(policy.KeepDays) * 24 * time.Hour
	var succeeded []Version
	for _, v := range versions {
		switch {
		case v.Status == VersionBuilding:
			keep[v.ID] = true
		case policy.KeepDays > 0 && now.Sub(v.StartedAt) < window:
			keep[v.ID] = true
		}
		if v.Status == VersionSucceeded {
			succeeded = append(succeeded, v)
		}
	}
	sort.SliceStable(succeeded, func(i, j int) bool { return succeeded[i].StartedAt.After(succeeded[j].StartedAt) })
	var oldestKept time.Time
	haveBound := false
	for i, v := range succeeded {
		if i == 0 {
			keep[v.ID] = true // the newest succeeded version is never removed
		}
		if policy.KeepLast > 0 && i < policy.KeepLast {
			keep[v.ID] = true
			oldestKept, haveBound = v.StartedAt, true
		}
	}
	if haveBound {
		for _, v := range versions {
			if v.Status == VersionFailed && v.StartedAt.After(oldestKept) {
				keep[v.ID] = true
			}
		}
	}
	return keep
}

// ApplyRetention removes the versions of the image that its policy no longer
// keeps and that no stack on this server or a paired one runs. When a peer
// cannot be asked whether a version is used, nothing is removed and Skipped
// names the peer. Revisions and registry tags are never touched.
func (s *Service) ApplyRetention(ctx context.Context, user, slug string) (RetentionResult, error) {
	return s.applyRetention(ctx, user, slug, false)
}

// applyRetention is ApplyRetention; inOp says the caller already holds the
// image's operation (the after-build hook), so the removal must not start one.
func (s *Service) applyRetention(ctx context.Context, user, slug string, inOp bool) (RetentionResult, error) {
	img, err := s.store.Image(ctx, slug)
	if err != nil {
		return RetentionResult{}, err
	}
	versions, err := s.store.Versions(ctx, img.ID)
	if err != nil {
		return RetentionResult{}, err
	}
	result := RetentionResult{Kept: len(versions)}
	keep := Keep(img.Retention, versions, s.now())
	var candidates []Version
	for _, v := range versions {
		if !keep[v.ID] {
			candidates = append(candidates, v)
		}
	}
	if len(candidates) == 0 {
		return result, nil
	}
	_, owner, err := s.selfInfo(ctx)
	if err != nil {
		return result, err
	}

	// Ask first, with no lock held: usage checks call peers. A failed row owns
	// no Docker tag and no stack can run it, so only succeeded ones are asked about.
	var removable []Version
	for _, v := range candidates {
		if v.Status == VersionSucceeded {
			used, unreachable, err := s.usedBy(ctx, imageref.Ref{Owner: owner, Slug: slug, Tag: v.Tag})
			if err != nil {
				return result, err
			}
			if len(unreachable) > 0 {
				names := make([]string, len(unreachable))
				for i, u := range unreachable {
					names[i] = u.Name
				}
				result.Skipped = strings.Join(names, ", ")
				s.log.Warn("image retention skipped: cannot confirm use", "image", slug, "server", result.Skipped)
				return result, nil
			}
			if len(used) > 0 {
				continue
			}
		}
		removable = append(removable, v)
	}
	if len(removable) == 0 {
		return result, nil
	}

	remove := func(ctx context.Context) error {
		for _, v := range removable {
			if err := ctx.Err(); err != nil {
				return err
			}
			removed, err := s.removeForRetention(ctx, user, img.Slug, owner, v)
			if err != nil {
				s.log.Warn("image retention: remove version failed", "image", slug, "tag", v.Tag, "err", err)
				continue
			}
			if removed {
				result.Removed = append(result.Removed, v)
			}
		}
		return nil
	}
	if inOp {
		if err := remove(ctx); err != nil {
			return result, err
		}
	} else if err := s.runExclusive(ctx, slug, owner, remove); err != nil {
		if errors.Is(err, ErrImageBusy) {
			s.log.Debug("image retention deferred: an operation is running", "image", slug)
			return result, nil
		}
		result.Kept = len(versions) - len(result.Removed)
		return result, err
	}
	result.Kept = len(versions) - len(result.Removed)
	return result, nil
}

// runExclusive runs fn as an operation on the image, so no build, push or
// fetch of it runs meanwhile, and waits for it to finish. fn gets the
// operation's context, which ends on its timeout or when ctx is cancelled. It
// returns fn's error, or an error for a panic in fn.
func (s *Service) runExclusive(ctx context.Context, slug, owner string, fn func(context.Context) error) error {
	done := make(chan struct{})
	var fnErr error
	op, err := s.ops.StartTimeout(OpKey(owner, slug), "retain", s.timeout, func(opCtx context.Context, _ func(string)) error {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				fnErr = fmt.Errorf("retention panicked: %v", r)
			}
		}()
		fnErr = fn(opCtx)
		return nil
	})
	if errors.Is(err, stacks.ErrBusy) {
		return ErrImageBusy
	}
	if err != nil {
		return err
	}
	select {
	case <-done:
	case <-ctx.Done():
		s.ops.Cancel(op.ID)
		<-done
	}
	return fnErr
}

// removeForRetention removes one version. The image lock is held only for the
// row checks and the final delete, never across the Docker call: the row is
// read again before the untag, and a version deleted meanwhile is left alone. Builds, pushes and fetches are excluded by the
// image's operation slot, which the caller holds.
func (s *Service) removeForRetention(ctx context.Context, user, slug, owner string, listed Version) (bool, error) {
	live, v, err := s.recheckVersion(ctx, slug, listed.ID)
	if err != nil || !live {
		return false, err
	}
	if v.Status == VersionSucceeded {
		if err := s.removeDockerTag(ctx, s.Ref(owner, slug, v.Tag)); err != nil {
			return false, err
		}
	}
	bg := context.WithoutCancel(ctx) // the tag is gone: finish the row even if ctx just ended
	unlock := s.lockImage(slug)
	err = s.deleteVersionRow(bg, slug, v)
	unlock()
	if errors.Is(err, ErrVersionNotFound) {
		return false, nil // deleted by hand while the tag was being removed
	}
	if err != nil {
		return false, err
	}
	s.record(bg, user, "image.retain", slug, v.Tag, v.Tag, "succeeded", "")
	return true, nil
}

// recheckVersion reads the row under the image lock; live is false when it is
// gone or building.
func (s *Service) recheckVersion(ctx context.Context, slug, id string) (live bool, v Version, err error) {
	unlock := s.lockImage(slug)
	defer unlock()
	v, err = s.store.VersionByID(ctx, id)
	if errors.Is(err, ErrVersionNotFound) {
		return false, Version{}, nil
	}
	if err != nil {
		return false, Version{}, err
	}
	return v.Status != VersionBuilding, v, nil
}

// applyRetentionAfterBuild prunes after a successful build. It runs inside the
// build operation and never fails or panics the build.
func (s *Service) applyRetentionAfterBuild(ctx context.Context, user, slug string) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Warn("image retention after build panicked", "image", slug, "panic", fmt.Sprint(r))
		}
	}()
	img, err := s.store.Image(ctx, slug)
	if err != nil {
		s.log.Warn("image retention after build failed", "image", slug, "err", err)
		return
	}
	if img.Retention == (Retention{}) {
		return
	}
	if _, err := s.applyRetention(ctx, user, slug, true); err != nil {
		s.log.Warn("image retention after build failed", "image", slug, "err", err)
	}
}

// Sweeper applies every image's retention policy in the background.
type Sweeper struct {
	Service *Service
	// After returns a channel that fires after d; nil means time.After.
	After func(time.Duration) <-chan time.Time
}

// Run sweeps 5 minutes after it starts and every 6 hours after that, until
// ctx is cancelled. A panic or failure in one sweep never stops it.
func (w *Sweeper) Run(ctx context.Context) {
	after := w.After
	if after == nil {
		after = time.After
	}
	delay := sweepFirstDelay
	for {
		select {
		case <-ctx.Done():
			return
		case <-after(delay):
		}
		w.sweep(ctx)
		delay = sweepInterval
	}
}

func (w *Sweeper) sweep(ctx context.Context) {
	log := w.Service.log
	images, err := w.Service.store.Images(ctx)
	if err != nil {
		log.Warn("image retention sweep: list images failed", "err", err)
		return
	}
	for _, img := range images {
		if ctx.Err() != nil {
			return
		}
		if img.Retention == (Retention{}) {
			continue
		}
		w.sweepImage(ctx, log, img.Slug)
	}
}

func (w *Sweeper) sweepImage(ctx context.Context, log *slog.Logger, slug string) {
	defer func() {
		if r := recover(); r != nil {
			log.Warn("image retention sweep panicked", "image", slug, "panic", fmt.Sprint(r))
		}
	}()
	if _, err := w.Service.ApplyRetention(ctx, SweepUser, slug); err != nil {
		log.Warn("image retention sweep failed", "image", slug, "err", err)
	}
}
