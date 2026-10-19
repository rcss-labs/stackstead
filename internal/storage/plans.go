package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/rcss-labs/stackstead/internal/actionlog"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

var (
	ErrValidation   = errors.New("invalid cleanup request")
	ErrPlanNotFound = errors.New("cleanup plan not found")
	ErrConflict     = errors.New("cleanup conflict")
	ErrWrongPhrase  = errors.New("wrong confirmation phrase")
	ErrUnavailable  = errors.New("storage unavailable")
)

const buildCacheWarning = "This engine cannot delete build cache records by id. Docker will delete all build cache that is not in use at the moment you confirm, which may differ from this list."

type CleanupEngine interface {
	InspectImage(context.Context, string) (engine.ImageInspect, error)
	InspectVolume(context.Context, string) (engine.VolumeInspect, error)
	RemoveImage(context.Context, string) (engine.ImageDeleteResult, error)
	RemoveVolume(context.Context, string) error
	BuildPruneByID(context.Context) (bool, error)
	PruneBuildCache(context.Context, []string) (int64, error)
}
type ActionWriter interface {
	Append(context.Context, actionlog.Entry) error
}
type PlanRequest struct{ Kind, Selection, Ref, Name string }
type PlanItem struct {
	Kind, ID, Ref string
	Bytes         *int64
	fingerprint   string
	untag         bool
	declarations  []Declaration
}
type ProtectedItem struct{ Kind, ID, Ref, Reason, Stack, StackState string }
type Reclaim struct {
	Min, Max *int64
	Exact    bool
}
type Plan struct {
	ID, Kind, Mode       string
	CreatedAt, ExpiresAt time.Time
	Items                []PlanItem
	Protected            []ProtectedItem
	Warnings             []string
	Reclaim              Reclaim
	ConfirmPhrase        string
}

var volumeNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
var imageIDPattern = regexp.MustCompile(`^(sha256:)?[a-fA-F0-9]{12,64}$`)
var imageReferencePattern = regexp.MustCompile(`^(?:[a-zA-Z0-9][a-zA-Z0-9.-]*(?::[0-9]+)?/)?[a-z0-9]+(?:[._-]+[a-z0-9]+)*(?:/[a-z0-9]+(?:[._-]+[a-z0-9]+)*)*(?::[a-zA-Z0-9_][a-zA-Z0-9_.-]{0,127})?(?:@sha256:[a-fA-F0-9]{64})?$`)

// FieldError identifies a bad JSON field without requiring message parsing.
type FieldError struct {
	Field, Message string
	cause          error
}

func (e *FieldError) Error() string           { return e.Message }
func (e *FieldError) Unwrap() error           { return e.cause }
func (e *FieldError) ValidationField() string { return e.Field }
func invalidField(field, message string) error {
	return &FieldError{Field: field, Message: message, cause: ErrValidation}
}
func validatePlan(r PlanRequest) error {
	switch r.Kind {
	case "images", "volumes", "build-cache":
		expected := "unused"
		if r.Kind == "images" {
			expected = "dangling"
		}
		if r.Selection != expected {
			return invalidField("selection", "invalid cleanup selection")
		}
		if r.Ref != "" {
			return invalidField("ref", "reference is not allowed for a bulk plan")
		}
		if r.Name != "" {
			return invalidField("name", "name is not allowed for a bulk plan")
		}
	case "image", "volume":
		if r.Selection != "" {
			return invalidField("selection", "selection is not allowed for a single item")
		}
		if r.Kind == "image" {
			if r.Name != "" {
				return invalidField("name", "name is not allowed for an image")
			}
			if !imageIDPattern.MatchString(r.Ref) && !imageReferencePattern.MatchString(r.Ref) {
				return invalidField("ref", "invalid image reference")
			}
		} else {
			if r.Ref != "" {
				return invalidField("ref", "reference is not allowed for a volume")
			}
			if !volumeNamePattern.MatchString(r.Name) {
				return invalidField("name", "invalid volume name")
			}
		}
	default:
		return invalidField("kind", "invalid cleanup kind")
	}
	return nil
}

func planDFKind(kind string) engine.DFKind {
	switch kind {
	case "image", "images":
		return engine.DFImages
	case "volume", "volumes":
		return engine.DFVolumes
	default:
		return engine.DFBuildCache
	}
}
func imageFingerprint(i engine.Image) string {
	tags := slices.Clone(i.RepoTags)
	slices.Sort(tags)
	return i.ID + "\x00" + strings.Join(tags, "\x00")
}
func volumeFingerprint(v engine.Volume) string {
	if v.CreatedAt.IsZero() {
		return ""
	}
	return v.Name + "\x00" + v.CreatedAt.UTC().Format(time.RFC3339Nano)
}
func shortImageID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	return id[:min(12, len(id))]
}
func imageMatches(i Image, ref string) bool {
	if imageIDPattern.MatchString(ref) {
		return strings.HasPrefix(strings.TrimPrefix(i.ID, "sha256:"), strings.TrimPrefix(ref, "sha256:"))
	}
	for _, r := range append(slices.Clone(i.Tags), i.Digests...) {
		if normalizeReference(r) == normalizeReference(ref) {
			return true
		}
	}
	return false
}
func consumerReason(c []Consumer) string {
	names := []string{}
	for _, v := range c[:min(3, len(c))] {
		names = append(names, v.Name)
	}
	return "used by container " + strings.Join(names, ", ")
}
func (s *Service) CreatePlan(ctx context.Context, r PlanRequest) (Plan, error) {
	if err := validatePlan(r); err != nil {
		return Plan{}, err
	}
	in, err := s.freshLists(ctx)
	if err != nil {
		return Plan{}, fmt.Errorf("%w: reading inventory: %v", ErrUnavailable, err)
	}
	kind := planDFKind(r.Kind)
	snap, dfErr := s.FreshDF(ctx, kind)
	if dfErr != nil {
		return Plan{}, fmt.Errorf("%w: sizes unavailable, try again", ErrUnavailable)
	}
	if ctx.Err() != nil {
		return Plan{}, ctx.Err()
	}
	in.DF = map[engine.DFKind]DFReading{}
	if dfErr == nil {
		in.DF[kind] = DFReading{Snapshot: snap, LoadedAt: s.cfg.Now()}
	}
	inv := Classify(in)
	p := Plan{Kind: r.Kind, CreatedAt: s.cfg.Now(), Items: []PlanItem{}, Protected: []ProtectedItem{}, Warnings: []string{}, Reclaim: Reclaim{Exact: kind != engine.DFImages}}
	p.ExpiresAt = p.CreatedAt.Add(5 * time.Minute)
	low, high := sizeSum{}, sizeSum{}
	switch kind {
	case engine.DFImages:
		found := false
		for _, i := range inv.Images {
			if r.Kind == "image" && !imageMatches(i, r.Ref) {
				continue
			}
			if r.Kind == "images" && (!i.Dangling || len(i.Containers) > 0) {
				continue
			}
			found = true
			if len(i.Containers) > 0 {
				return Plan{}, fmt.Errorf("%w: image %s is %s", ErrConflict, r.Ref, consumerReason(i.Containers))
			}
			ref := i.ID
			display := shortImageID(i.ID)
			if r.Kind == "image" && !imageIDPattern.MatchString(r.Ref) {
				ref = r.Ref
				display = r.Ref
			}
			item := PlanItem{Kind: "image", ID: i.ID, Ref: display, Bytes: i.Size, untag: len(i.Tags) > 1 && ref != i.ID, declarations: slices.Clone(i.DeclaredBy)}
			// fingerprint comes from the unmodified engine metadata, including sentinel tags.
			for _, raw := range in.Images {
				if raw.ID == i.ID {
					item.fingerprint = imageFingerprint(raw)
				}
			}
			if ref == i.ID {
				item.Ref = shortImageID(i.ID)
			}
			if !item.untag && (i.Size == nil || i.SharedSize == nil) {
				return Plan{}, fmt.Errorf("%w: sizes unavailable, try again", ErrUnavailable)
			}
			if item.untag {
				zero := int64(0)
				item.Bytes = &zero
				low.add(&zero)
				high.add(&zero)
			} else {
				high.add(i.Size)
				if i.Size == nil || i.SharedSize == nil {
					low.add(nil)
				} else {
					n := max(int64(0), *i.Size-*i.SharedSize)
					low.add(&n)
				}
			}
			p.Items = append(p.Items, item)
			if r.Kind == "image" {
				for _, d := range i.DeclaredBy {
					if d.StackState != "running" {
						p.Warnings = append(p.Warnings, fmt.Sprintf("Stack %s declares %s; its next Up may pull or build the image again.", d.Stack, d.Ref))
					}
				}
			}
		}
		if r.Kind == "image" && !found {
			return Plan{}, ErrPlanNotFound
		}
	case engine.DFVolumes:
		found := false
		for _, v := range inv.Volumes {
			if r.Kind == "volume" && v.Name != r.Name {
				continue
			}
			found = true
			if v.Protection != nil {
				if r.Kind == "volume" {
					return Plan{}, fmt.Errorf("%w: volume %s is protected by stack %s", ErrConflict, v.Name, v.Protection.Stack)
				}
				if v.Status == "unused" {
					state := ""
					for _, d := range in.Stacks {
						if d.Name == v.Protection.Stack {
							state = d.State
						}
					}
					p.Protected = append(p.Protected, ProtectedItem{Kind: "volume", ID: v.Name, Ref: v.Name, Reason: "stack", Stack: v.Protection.Stack, StackState: state})
				}
				continue
			}
			if v.Status == "in-use" {
				if r.Kind == "volume" {
					reason := "used by containers"
					if len(v.Containers) > 0 {
						reason = consumerReason(v.Containers)
					}
					return Plan{}, fmt.Errorf("%w: volume %s is %s", ErrConflict, v.Name, reason)
				}
				continue
			}
			if len(inv.DeclarationsUnknown) > 0 {
				return Plan{}, fmt.Errorf("%w: declarations unknown for %s", ErrUnavailable, strings.Join(inv.DeclarationsUnknown, ", "))
			}
			if v.CreatedAt.IsZero() {
				return Plan{}, fmt.Errorf("%w: volume %s creation time unavailable", ErrConflict, v.Name)
			}
			if v.Size == nil || v.RefCount == nil {
				return Plan{}, fmt.Errorf("%w: sizes unavailable, try again", ErrUnavailable)
			}
			p.Items = append(p.Items, PlanItem{Kind: "volume", ID: v.Name, Ref: v.Name, Bytes: v.Size, fingerprint: volumeFingerprint(engine.Volume{Name: v.Name, CreatedAt: v.CreatedAt})})
			low.add(v.Size)
			high.add(v.Size)
		}
		if r.Kind == "volume" && !found {
			return Plan{}, ErrPlanNotFound
		}
	case engine.DFBuildCache:
		e, ok := s.cfg.Engine.(CleanupEngine)
		if !ok {
			return Plan{}, ErrUnavailable
		}
		exact, err := e.BuildPruneByID(ctx)
		if err != nil {
			return Plan{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		p.Reclaim.Exact = exact
		if !exact {
			p.Mode = "all-unused"
			p.Warnings = append(p.Warnings, buildCacheWarning)
		}
		for _, b := range inv.BuildCache {
			if b.InUse || b.Shared {
				continue
			}
			if b.Size == nil {
				return Plan{}, fmt.Errorf("%w: sizes unavailable, try again", ErrUnavailable)
			}
			p.Items = append(p.Items, PlanItem{Kind: "build-cache", ID: b.ID, Ref: b.ID, Bytes: b.Size, fingerprint: b.ID})
			low.add(b.Size)
			high.add(b.Size)
		}
	}
	p.Reclaim.Min, p.Reclaim.Max = low.result(), high.result()
	if (r.Kind == "image" || r.Kind == "volume") && len(p.Items) == 1 {
		p.ConfirmPhrase = p.Items[0].Ref
	} else {
		noun := map[string]string{"images": "images", "volumes": "volumes", "build-cache": "build cache records"}[r.Kind]
		if len(p.Items) == 1 {
			noun = map[string]string{"images": "image", "volumes": "volume", "build-cache": "build cache record"}[r.Kind]
		}
		p.ConfirmPhrase = fmt.Sprintf("delete %d %s", len(p.Items), noun)
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return Plan{}, err
	}
	p.ID = "p_" + hex.EncodeToString(id[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.plans) >= 32 {
		s.plans = s.plans[1:]
	}
	s.plans = append(s.plans, clonePlan(p))
	return p, nil
}
func clonePlan(p Plan) Plan {
	p.Items = slices.Clone(p.Items)
	for i := range p.Items {
		p.Items[i].Bytes = knownSize(p.Items[i].Bytes)
		p.Items[i].declarations = slices.Clone(p.Items[i].declarations)
	}
	p.Protected = slices.Clone(p.Protected)
	p.Warnings = slices.Clone(p.Warnings)
	p.Reclaim.Min = knownSize(p.Reclaim.Min)
	p.Reclaim.Max = knownSize(p.Reclaim.Max)
	return p
}

// confirmPlanLocked validates the current stored plan. The caller holds s.mu.
func (s *Service) confirmPlanLocked(id, phrase, username string) (int, Plan, error) {
	at := -1
	for i, p := range s.plans {
		if p.ID == id {
			at = i
			break
		}
	}
	if at < 0 {
		return -1, Plan{}, ErrPlanNotFound
	}
	p := s.plans[at]
	if !s.cfg.Now().Before(p.ExpiresAt) {
		s.plans = append(s.plans[:at], s.plans[at+1:]...)
		return -1, Plan{}, ErrPlanNotFound
	}
	if len(p.Items) == 0 {
		return -1, Plan{}, fmt.Errorf("%w: empty plan", ErrConflict)
	}
	if phrase != p.ConfirmPhrase {
		return -1, Plan{}, &FieldError{Field: "confirm", Message: ErrWrongPhrase.Error(), cause: ErrWrongPhrase}
	}
	if username == "" {
		return -1, Plan{}, invalidField("username", "username required")
	}
	if s.cfg.Ops == nil {
		return -1, Plan{}, ErrUnavailable
	}
	if _, ok := s.cfg.Engine.(CleanupEngine); !ok {
		return -1, Plan{}, ErrUnavailable
	}
	return at, p, nil
}

func (s *Service) ConfirmPlan(ctx context.Context, id, phrase, username string) (stacks.Operation, error) {
	s.mu.Lock()
	_, _, err := s.confirmPlanLocked(id, phrase, username)
	s.mu.Unlock()
	if err != nil {
		return stacks.Operation{}, err
	}
	// Network and Compose preflight must not block cached DF readers or publication.
	if _, err := s.freshLists(ctx); err != nil {
		return stacks.Operation{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Preflight can outlive expiry, eviction, or another confirmation. Never
	// retain a slice index across that unlocked interval.
	at, p, err := s.confirmPlanLocked(id, phrase, username)
	if err != nil {
		return stacks.Operation{}, err
	}
	e := s.cfg.Engine.(CleanupEngine)
	ready := make(chan string, 1)
	o, err := s.cfg.Ops.Start("", "storage-cleanup", func(ctx context.Context, out func(string)) error {
		return s.executePlan(ctx, e, p, username, <-ready, out)
	})
	if err != nil {
		if errors.Is(err, stacks.ErrBusy) {
			return stacks.Operation{}, fmt.Errorf("%w: %w", ErrConflict, err)
		}
		return stacks.Operation{}, err
	}
	s.plans = append(s.plans[:at], s.plans[at+1:]...)
	ready <- o.ID
	return o, nil
}
