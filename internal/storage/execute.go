package storage

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/rcss-labs/stackstead/internal/actionlog"
	"github.com/rcss-labs/stackstead/internal/engine"
)

func (s *Service) executePlan(ctx context.Context, e CleanupEngine, p Plan, username, operationID string, out func(string)) error {
	kind := planDFKind(p.Kind)
	defer func() {
		s.lists.Invalidate()
		s.mu.Lock()
		s.df[kind].invalid = true
		s.ensureLocked(kind, false)
		s.mu.Unlock()
	}()
	record := func(item PlanItem, result, detail string, bytes *int64) {
		action := "storage.prune"
		if p.Kind == "image" || p.Kind == "volume" {
			action = "storage.delete"
		}
		if s.cfg.Actions == nil {
			out("could not record action")
			return
		}
		if s.cfg.Actions != nil {
			err := s.cfg.Actions.Append(ctx, actionlog.Entry{Username: username, Kind: action, Scope: "host", Target: item.Ref, Detail: detail, Bytes: bytes, Result: result, OperationID: operationID})
			if err != nil {
				out("could not record action")
			}
		}
	}
	if kind == engine.DFBuildCache {
		return s.executeBuild(ctx, e, p, out, record)
	}
	// A single fresh reading supplies current volume reference counts for the run.
	var volumeDF engine.DFSnapshot
	var volumeErr error
	if kind == engine.DFVolumes {
		volumeDF, volumeErr = s.FreshDF(ctx, kind)
	}
	failed := false
	for _, item := range p.Items {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		result, detail := "deleted", ""
		in, err := s.freshLists(ctx)
		if err != nil {
			result = "failed"
			detail = "could not recheck inventory"
		} else {
			if kind == engine.DFVolumes {
				var v engine.Volume
				v, err = e.InspectVolume(ctx, item.ID)
				if errors.Is(err, engine.ErrVolumeNotFound) {
					result, detail = "skipped", "already gone"
				} else if err != nil {
					result, detail = "failed", "could not inspect volume"
				} else if volumeFingerprint(v) == "" || volumeFingerprint(v) != item.fingerprint {
					result, detail = "skipped", "fingerprint changed"
				} else {
					in.Volumes = []engine.Volume{v}
					in.DF = map[engine.DFKind]DFReading{}
					if volumeErr == nil {
						in.DF[engine.DFVolumes] = DFReading{Snapshot: volumeDF, LoadedAt: s.cfg.Now()}
					}
					inv := Classify(in)
					row := inv.Volumes[0]
					switch {
					case row.Protection != nil:
						result, detail = "skipped", "now declared by stack "+row.Protection.Stack
					case len(row.Containers) > 0:
						result, detail = "skipped", "now "+consumerReason(row.Containers)
					case row.RefCount != nil && *row.RefCount > 0:
						result, detail = "skipped", "now used by containers"
					case len(inv.DeclarationsUnknown) > 0:
						result, detail = "skipped", "declarations unknown for "+strings.Join(inv.DeclarationsUnknown, ", ")
					case volumeErr != nil || row.RefCount == nil:
						result, detail = "skipped", "volume usage unavailable"
					default:
						err = e.RemoveVolume(ctx, item.ID)
					}
				}
			} else {
				var i engine.ImageInspect
				i, err = e.InspectImage(ctx, item.ID)
				if errors.Is(err, engine.ErrImageNotFound) {
					result, detail = "skipped", "already gone"
				} else if err != nil {
					result, detail = "failed", "could not inspect image"
				} else if imageFingerprint(i.Image) != item.fingerprint {
					result, detail = "skipped", "fingerprint changed"
				} else {
					in.Images = []engine.Image{i.Image}
					row := Classify(in).Images[0]
					if len(row.Containers) > 0 {
						result, detail = "skipped", "now "+consumerReason(row.Containers)
					} else if newlyDeclared(row.DeclaredBy, item.declarations) != "" {
						result, detail = "skipped", "now declared by stack "+newlyDeclared(row.DeclaredBy, item.declarations)
					} else {
						ref := item.ID
						if !imageIDPattern.MatchString(item.Ref) {
							ref = item.Ref
						}
						_, err = e.RemoveImage(ctx, ref)
						if item.untag {
							result = "untagged"
						}
					}
				}
			}
			if err != nil && result != "skipped" {
				result = "failed"
				if detail == "" {
					detail = err.Error()
				}
			}
		}
		if result == "failed" {
			failed = true
		}
		line := fmt.Sprintf("%s %s %s", result, item.Kind, item.Ref)
		if result == "untagged" {
			line = "untagged " + item.Ref
		}
		if detail != "" {
			line += ": " + detail
		} else if result == "deleted" && item.Bytes != nil {
			line += fmt.Sprintf(" (%d bytes)", *item.Bytes)
		}
		out(line)
		record(item, result, detail, item.Bytes)
	}
	if failed {
		return errors.New("one or more storage items failed")
	}
	return nil
}

func (s *Service) executeBuild(ctx context.Context, e CleanupEngine, p Plan, out func(string), record func(PlanItem, string, string, *int64)) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	before, err := s.FreshDF(ctx, engine.DFBuildCache)
	if err != nil {
		return fmt.Errorf("recheck build cache: %w", err)
	}
	current := map[string]engine.DFBuildCacheRecord{}
	for _, b := range before.BuildCache {
		current[b.ID] = b
	}
	if p.Mode == "all-unused" {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		bytes, err := e.PruneBuildCache(ctx, nil)
		item := PlanItem{Kind: "build-cache", Ref: "build-cache/all-unused"}
		if err != nil {
			out("failed build-cache/all-unused: " + err.Error())
			record(item, "failed", err.Error(), nil)
			return err
		}
		out(fmt.Sprintf("reclaimed %d bytes", bytes))
		record(item, "deleted", "all unused build cache", &bytes)
		after, err := s.FreshDF(ctx, engine.DFBuildCache)
		if err != nil {
			out("could not read build cache after cleanup")
			return nil
		}
		remaining := map[string]bool{}
		for _, b := range after.BuildCache {
			remaining[b.ID] = true
		}
		for _, b := range before.BuildCache {
			if !remaining[b.ID] {
				out("deleted build-cache " + b.ID)
			}
		}
		return nil
	}
	ids := []string{}
	selected := []PlanItem{}
	for _, item := range p.Items {
		b, ok := current[item.ID]
		reason := ""
		if !ok {
			reason = "already gone"
		} else if b.InUse || b.Shared {
			reason = "now used or shared"
		}
		if reason != "" {
			out("skipped build-cache " + item.Ref + ": " + reason)
			record(item, "skipped", reason, item.Bytes)
			continue
		}
		ids = append(ids, item.ID)
		selected = append(selected, item)
	}
	if len(ids) == 0 {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	bytes, err := e.PruneBuildCache(ctx, ids)
	for _, item := range selected {
		result, detail := "deleted", ""
		if err != nil {
			result, detail = "failed", err.Error()
		}
		out(result + " build-cache " + item.Ref)
		record(item, result, detail, item.Bytes)
	}
	if err != nil {
		return err
	}
	out(fmt.Sprintf("reclaimed %d bytes", bytes))
	return nil
}

func newlyDeclared(current, previous []Declaration) string {
	for _, d := range current {
		if !slices.ContainsFunc(previous, func(old Declaration) bool {
			return d.Stack == old.Stack && d.Service == old.Service && d.Ref == old.Ref
		}) {
			return d.Stack
		}
	}
	return ""
}
