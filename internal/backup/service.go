package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

// unknownSize marks a mount whose size cannot be read, such as a bind mount.
const unknownSize int64 = -1

// ErrUnknownStack is returned when a selection names a stack Stackstead does not manage.
var ErrUnknownStack = errors.New("stack not found")

// VolumeSizes reads per-volume disk usage; *engine.Client satisfies it.
type VolumeSizes interface {
	DiskUsage(ctx context.Context, kind engine.DFKind) (engine.DFSnapshot, error)
}

var _ VolumeSizes = (*engine.Client)(nil)

// MountView is one volume or bind mount of a stack, with its last-known size.
type MountView struct {
	// Kind is "volume" or "bind"; Name is the volume name or host path.
	Kind   string
	Name   string
	Target string
	// Bytes is -1 when unknown.
	Bytes int64
}

// StackSelectionView is a stack's backup selection with what ticking
// Volumes would cost.
type StackSelectionView struct {
	StackSelection
	Mounts []MountView
}

// Service is the backup feature as the HTTP layer sees it: settings,
// per-stack selections, runs and the archives found at the destinations.
type Service struct {
	Settings  *ConfigStore
	History   *RunStore
	Runner    *Runner
	Scheduler *Scheduler
	Stacks    StackControl
	Sizes     VolumeSizes
	HTTP      *http.Client
	Now       func() time.Time
	Loc       *time.Location
	Logger    *slog.Logger
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) loc() *time.Location {
	if s.Loc != nil {
		return s.Loc
	}
	return time.Local
}

func (s *Service) logger() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

// Config returns the stored configuration.
func (s *Service) Config(ctx context.Context) (Config, error) { return s.Settings.Load(ctx) }

// SaveConfig stores cfg and tells the scheduler to pick it up. The caller
// validates first; an empty S3 secret keeps the stored one.
func (s *Service) SaveConfig(ctx context.Context, cfg Config) error {
	if err := s.Settings.Save(ctx, cfg); err != nil {
		return err
	}
	if s.Scheduler != nil {
		s.Scheduler.Reload()
	}
	return nil
}

// ClearS3Secret removes the stored S3 secret key.
func (s *Service) ClearS3Secret(ctx context.Context) error { return s.Settings.ClearS3Secret(ctx) }

// TestS3 checks the bucket settings, using the stored secret when cfg has none.
func (s *Service) TestS3(ctx context.Context, cfg S3Dest) error {
	if cfg.SecretKey == "" {
		stored, err := s.Settings.Load(ctx)
		if err != nil {
			return err
		}
		cfg.SecretKey = stored.S3.SecretKey
	}
	return TestS3(ctx, cfg, s.HTTP)
}

// Selections lists every stack with its selection (unticked when none was
// saved) and its mounts. A stack whose mounts cannot be resolved is listed
// without them.
func (s *Service) Selections(ctx context.Context) ([]StackSelectionView, error) {
	list, err := s.Stacks.List(ctx)
	if err != nil {
		return nil, err
	}
	saved, err := s.Settings.Selections(ctx)
	if err != nil {
		return nil, err
	}
	byStack := make(map[string]StackSelection, len(saved))
	for _, sel := range saved {
		byStack[sel.Stack] = sel
	}
	sizes := s.volumeSizes(ctx)

	out := make([]StackSelectionView, 0, len(list))
	for _, sum := range list {
		sel, ok := byStack[sum.Name]
		if !ok {
			sel = StackSelection{Stack: sum.Name, StopDuring: true}
		}
		view := StackSelectionView{StackSelection: sel, Mounts: []MountView{}}
		mounts, err := s.Stacks.Mounts(ctx, sum.Name)
		if err != nil {
			s.logger().Warn("backup: list mounts", "stack", sum.Name, "err", err)
		}
		for _, m := range mounts {
			mv := MountView{Kind: m.Kind, Name: m.Source, Target: m.Target, Bytes: unknownSize}
			if size, ok := sizes[m.Source]; ok && m.Kind == "volume" {
				mv.Bytes = size
			}
			view.Mounts = append(view.Mounts, mv)
		}
		out = append(out, view)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Stack < out[j].Stack })
	return out, nil
}

// volumeSizes maps volume name to bytes; it is empty when Docker cannot say.
func (s *Service) volumeSizes(ctx context.Context) map[string]int64 {
	sizes := map[string]int64{}
	if s.Sizes == nil {
		return sizes
	}
	snap, err := s.Sizes.DiskUsage(ctx, engine.DFVolumes)
	if err != nil {
		s.logger().Warn("backup: read volume sizes", "err", err)
		return sizes
	}
	for _, v := range snap.Volumes {
		if v.UsageData != nil && v.UsageData.Size >= 0 {
			sizes[v.Name] = v.UsageData.Size
		}
	}
	return sizes
}

// SetSelection stores the selection of a stack Stackstead manages.
func (s *Service) SetSelection(ctx context.Context, sel StackSelection) error {
	list, err := s.Stacks.List(ctx)
	if err != nil {
		return err
	}
	for _, sum := range list {
		if sum.Name == sel.Stack {
			return s.Settings.SetSelection(ctx, sel)
		}
	}
	return ErrUnknownStack
}

// Start begins a manual backup run.
func (s *Service) Start(ctx context.Context, trigger, user string) (stacks.Operation, error) {
	return s.Runner.Start(ctx, trigger, user)
}

// Runs returns the most recent runs, newest first.
func (s *Service) Runs(ctx context.Context, limit int) ([]Run, error) {
	return s.History.Recent(ctx, limit)
}

// Archives lists the archives at every enabled destination, newest first. A
// destination that cannot be read is logged and left out so the others still
// show; it is an error only when every enabled destination fails.
func (s *Service) Archives(ctx context.Context) ([]ArchiveRef, error) {
	cfg, err := s.Settings.Load(ctx)
	if err != nil {
		return nil, err
	}
	var dests []Destination
	if cfg.Local.Enabled {
		dests = append(dests, NewLocal(cfg.Local))
	}
	if cfg.S3.Enabled {
		dests = append(dests, NewS3(cfg.S3, s.HTTP))
	}
	out := []ArchiveRef{}
	var failures []error
	for _, d := range dests {
		refs, err := d.List(ctx)
		if err != nil {
			s.logger().Warn("backup: list archives", "destination", d.Name(), "err", err)
			failures = append(failures, fmt.Errorf("%s: %w", d.Name(), err))
			continue
		}
		out = append(out, refs...)
	}
	if len(dests) > 0 && len(failures) == len(dests) {
		return nil, errors.Join(failures...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// NextRun reports when the schedule fires next, if it is enabled.
func (s *Service) NextRun(ctx context.Context) (time.Time, bool) {
	cfg, err := s.Settings.Load(ctx)
	if err != nil {
		return time.Time{}, false
	}
	return NextRun(cfg.Schedule, s.now(), s.loc())
}
