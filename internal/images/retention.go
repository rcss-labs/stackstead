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

// Keep returns the IDs of the versions the policy keeps. Succeeded and failed
// rows are ranked by StartedAt, newest first, and kept while their rank is at
// most KeepLast or their age is under KeepDays. Building rows are always
// kept, and so is the newest succeeded version, so a policy can never leave
// an image with nothing to run. A policy with both limits at zero keeps all.
func Keep(policy Retention, versions []Version, now time.Time) map[string]bool {
	keep := make(map[string]bool, len(versions))
	ranked := make([]Version, 0, len(versions))
	for _, v := range versions {
		if v.Status == VersionBuilding || (policy.KeepLast <= 0 && policy.KeepDays <= 0) {
			keep[v.ID] = true
			continue
		}
		ranked = append(ranked, v)
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].StartedAt.After(ranked[j].StartedAt) })
	window := time.Duration(policy.KeepDays) * 24 * time.Hour
	newestSucceeded := false
	for i, v := range ranked {
		if policy.KeepLast > 0 && i+1 <= policy.KeepLast {
			keep[v.ID] = true
		}
		if policy.KeepDays > 0 && now.Sub(v.StartedAt) < window {
			keep[v.ID] = true
		}
		if v.Status == VersionSucceeded && !newestSucceeded {
			newestSucceeded = true
			keep[v.ID] = true
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

	remove := func() {
		for _, v := range removable {
			if ctx.Err() != nil {
				return
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
	}
	if inOp {
		remove()
	} else if err := s.runExclusive(ctx, slug, owner, remove); err != nil {
		if errors.Is(err, errImageBusy) {
			s.log.Debug("image retention deferred: an operation is running", "image", slug)
			return result, nil
		}
		return result, err
	}
	result.Kept = len(versions) - len(result.Removed)
	return result, nil
}

var errImageBusy = errors.New("image has a running operation")

// runExclusive runs fn as an operation on the image, so no build, push or
// fetch of it runs meanwhile, and waits for it to finish.
func (s *Service) runExclusive(ctx context.Context, slug, owner string, fn func()) error {
	done := make(chan struct{})
	_, err := s.ops.StartTimeout(OpKey(owner, slug), "retain", s.timeout, func(context.Context, func(string)) error {
		defer close(done)
		fn()
		return nil
	})
	if errors.Is(err, stacks.ErrBusy) {
		return errImageBusy
	}
	if err != nil {
		return err
	}
	<-done
	return nil
}

// removeForRetention removes one version under the image lock. The row is
// read again first: a version deleted or restarted since the usage check is left alone.
func (s *Service) removeForRetention(ctx context.Context, user, slug, owner string, listed Version) (bool, error) {
	unlock := s.lockImage(slug)
	defer unlock()
	v, err := s.store.VersionByID(ctx, listed.ID)
	if errors.Is(err, ErrVersionNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if v.Status == VersionBuilding {
		return false, nil
	}
	if err := s.removeVersionLocked(ctx, slug, owner, v); err != nil {
		return false, err
	}
	s.record(ctx, user, "image.retain", slug, v.Tag, v.Tag, "succeeded", "")
	return true, nil
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
	After  func(time.Duration) <-chan time.Time
	Logger *slog.Logger
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

func (w *Sweeper) logger() *slog.Logger {
	if w.Logger != nil {
		return w.Logger
	}
	return slog.Default()
}

func (w *Sweeper) sweep(ctx context.Context) {
	log := w.logger()
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
