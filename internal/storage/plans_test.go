package storage

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/actionlog"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

type cleanupEngine struct {
	mu           sync.Mutex
	images       []engine.Image
	volumes      []engine.Volume
	containers   []engine.Container
	snap         engine.DFSnapshot
	exact        bool
	unavailable  bool
	dfFailure    bool
	removed      []string
	pruneIDs     [][]string
	fail         string
	beforeRemove func(string)
}

func (e *cleanupEngine) Images(context.Context) ([]engine.Image, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.unavailable {
		return nil, errors.New("offline")
	}
	return append([]engine.Image{}, e.images...), nil
}
func (e *cleanupEngine) Volumes(context.Context) ([]engine.Volume, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]engine.Volume{}, e.volumes...), nil
}
func (e *cleanupEngine) Containers(context.Context) ([]engine.Container, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]engine.Container{}, e.containers...), nil
}
func (e *cleanupEngine) DiskUsage(context.Context, engine.DFKind) (engine.DFSnapshot, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.unavailable {
		return engine.DFSnapshot{}, errors.New("offline")
	}
	if e.dfFailure {
		return engine.DFSnapshot{}, errors.New("df failed")
	}
	snap := e.snap
	if snap.Images == nil {
		for _, i := range e.images {
			snap.Images = append(snap.Images, engine.DFImage{ID: i.ID, Size: int64ptr(i.Size), SharedSize: int64ptr(0)})
		}
	}
	if snap.Volumes == nil {
		for _, v := range e.volumes {
			snap.Volumes = append(snap.Volumes, engine.DFVolume{Name: v.Name, UsageData: &engine.DFVolumeUsage{Size: 10, RefCount: 0}})
		}
	}
	return snap, nil
}
func (e *cleanupEngine) InspectImage(_ context.Context, ref string) (engine.ImageInspect, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, i := range e.images {
		if i.ID == ref || strings.TrimPrefix(i.ID, "sha256:") == ref {
			return engine.ImageInspect{Image: i}, nil
		}
		for _, tag := range i.RepoTags {
			if tag == ref {
				return engine.ImageInspect{Image: i}, nil
			}
		}
	}
	return engine.ImageInspect{}, engine.ErrImageNotFound
}
func (e *cleanupEngine) InspectVolume(_ context.Context, name string) (engine.VolumeInspect, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, v := range e.volumes {
		if v.Name == name {
			return v, nil
		}
	}
	return engine.Volume{}, engine.ErrVolumeNotFound
}
func (e *cleanupEngine) RemoveImage(ctx context.Context, ref string) (engine.ImageDeleteResult, error) {
	// Docker refuses an unforced delete by id while the image has more than one tag.
	e.mu.Lock()
	for _, i := range e.images {
		if (i.ID == ref || strings.TrimPrefix(i.ID, "sha256:") == ref) && len(i.RepoTags) > 1 {
			e.removed = append(e.removed, ref)
			e.mu.Unlock()
			return engine.ImageDeleteResult{}, errors.New("conflict: unable to delete " + ref + " (must be forced) - image is referenced in multiple repositories")
		}
	}
	e.mu.Unlock()
	err := e.remove(ref)
	return engine.ImageDeleteResult{}, err
}
func (e *cleanupEngine) RemoveVolume(ctx context.Context, name string) error { return e.remove(name) }
func (e *cleanupEngine) remove(ref string) error {
	e.mu.Lock()
	e.removed = append(e.removed, ref)
	fail, hook := e.fail, e.beforeRemove
	e.mu.Unlock()
	if hook != nil {
		hook(ref)
	}
	if ref == fail {
		return errors.New("Docker refusal")
	}
	return nil
}
func (e *cleanupEngine) BuildPruneByID(context.Context) (bool, error) { return e.exact, nil }
func (e *cleanupEngine) PruneBuildCache(_ context.Context, ids []string) (int64, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if ids == nil {
		e.pruneIDs = append(e.pruneIDs, nil)
	} else {
		e.pruneIDs = append(e.pruneIDs, append([]string{}, ids...))
	}
	e.snap.BuildCache = nil
	return 123, nil
}

type cleanupLog struct {
	mu   sync.Mutex
	rows []actionlog.Entry
	fail bool
}

func (l *cleanupLog) Append(_ context.Context, r actionlog.Entry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rows = append(l.rows, r)
	if l.fail {
		return errors.New("log offline")
	}
	return nil
}
func cleanupFixture(t *testing.T) (*Service, *cleanupEngine, *cleanupLog) {
	t.Helper()
	e := &cleanupEngine{snap: engine.DFSnapshot{Kinds: dfKinds}}
	l := &cleanupLog{}
	s := NewService(ServiceConfig{Engine: e, Ops: stacks.NewOps(time.Minute, nil), Actions: l})
	return s, e, l
}
func volume(name string) engine.Volume {
	return engine.Volume{Name: name, CreatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
}
func plan(t *testing.T, s *Service, r PlanRequest) Plan {
	t.Helper()
	p, err := s.CreatePlan(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func confirm(t *testing.T, s *Service, p Plan) stacks.Operation {
	t.Helper()
	o, err := s.ConfirmPlan(context.Background(), p.ID, p.ConfirmPhrase, "alice")
	if err != nil {
		t.Fatal(err)
	}
	waitService(t, func() bool { o, _ = s.cfg.Ops.Get(o.ID); return o.State != stacks.OpRunning })
	return o
}
func TestCleanupRequestValidation(t *testing.T) {
	s, _, _ := cleanupFixture(t)
	for _, r := range []PlanRequest{{}, {Kind: "containers", Selection: "unused"}, {Kind: "images", Selection: "unused"}, {Kind: "volumes", Selection: "dangling"}, {Kind: "build-cache", Selection: "dangling"}, {Kind: "volume", Name: "../x"}, {Kind: "volume", Name: "-x"}, {Kind: "image", Ref: "bad ref"}, {Kind: "image", Ref: ""}, {Kind: "images", Selection: "dangling", Ref: "x"}, {Kind: "volume", Name: "valid", Selection: "unused"}} {
		if _, err := s.CreatePlan(context.Background(), r); !errors.Is(err, ErrValidation) {
			t.Errorf("%+v: %v", r, err)
		}
	}
}
func TestCleanupPlansAndProtection(t *testing.T) {
	s, e, _ := cleanupFixture(t)
	e.images = []engine.Image{{ID: "sha256:aaaaaaaaaaaa", Size: 10}, {ID: "sha256:bbbbbbbbbbbb", RepoTags: []string{"nginx:1.25", "nginx:latest"}, Size: 20}}
	e.snap.Images = []engine.DFImage{{ID: e.images[0].ID, Size: int64ptr(10), SharedSize: int64ptr(3)}}
	p := plan(t, s, PlanRequest{Kind: "images", Selection: "dangling"})
	if len(p.Items) != 1 || p.Items[0].ID != e.images[0].ID || p.ConfirmPhrase != "delete 1 image" || *p.Reclaim.Min != 7 || *p.Reclaim.Max != 10 || p.Reclaim.Exact {
		t.Fatalf("%+v", p)
	}
	p = plan(t, s, PlanRequest{Kind: "image", Ref: "nginx:1.25"})
	if p.ConfirmPhrase != "nginx:1.25" || *p.Reclaim.Max != 0 {
		t.Fatalf("%+v", p)
	}
	p = plan(t, s, PlanRequest{Kind: "image", Ref: "aaaaaaaaaaaa"})
	if p.ConfirmPhrase != "aaaaaaaaaaaa" {
		t.Fatal(p)
	}
	e.volumes = []engine.Volume{volume("old_data"), volume("test_tmp"), volume("media_db")}
	s.cfg.Registry = serviceRegistry{{Name: "media", Project: "media"}}
	s.cfg.Declarations = serviceDeclarations(func(context.Context, stacks.Entry) StackDeclarations {
		return StackDeclarations{Name: "media", Project: "media", Known: true, Volumes: []DeclaredVolume{{Name: "media_db"}}}
	})
	p = plan(t, s, PlanRequest{Kind: "volumes", Selection: "unused"})
	if len(p.Items) != 2 || len(p.Protected) != 1 || p.Protected[0].Stack != "media" || p.Protected[0].StackState != "not-deployed" || p.ConfirmPhrase != "delete 2 volumes" {
		t.Fatalf("%+v", p)
	}
	if _, err := s.CreatePlan(context.Background(), PlanRequest{Kind: "volume", Name: "media_db"}); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "media") {
		t.Fatal(err)
	}
}
func int64ptr(n int64) *int64 { return &n }
func TestCleanupSingleRefusalsAndWarnings(t *testing.T) {
	s, e, _ := cleanupFixture(t)
	e.images = []engine.Image{{ID: "sha256:aaaaaaaaaaaa", RepoTags: []string{"nginx:1.25"}}}
	e.volumes = []engine.Volume{volume("data")}
	e.containers = []engine.Container{{ID: "c", Name: "consumer", ImageID: e.images[0].ID, Mounts: []engine.Mount{{Type: "volume", Name: "data"}}}}
	for _, r := range []PlanRequest{{Kind: "image", Ref: "nginx:1.25"}, {Kind: "volume", Name: "data"}} {
		_, err := s.CreatePlan(context.Background(), r)
		if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "consumer") {
			t.Fatal(err)
		}
	}
	for _, r := range []PlanRequest{{Kind: "image", Ref: "missing:tag"}, {Kind: "volume", Name: "missing"}} {
		if _, err := s.CreatePlan(context.Background(), r); !errors.Is(err, ErrPlanNotFound) {
			t.Fatal(err)
		}
	}
	e.containers = nil
	s.cfg.Registry = serviceRegistry{{Name: "media", Project: "media"}}
	s.cfg.Declarations = serviceDeclarations(func(context.Context, stacks.Entry) StackDeclarations {
		return StackDeclarations{Name: "media", Known: true, Images: []DeclaredImage{{Service: "web", Reference: "nginx:1.25"}}}
	})
	p := plan(t, s, PlanRequest{Kind: "image", Ref: "nginx:1.25"})
	if len(p.Warnings) == 0 || !strings.Contains(p.Warnings[0], "media") {
		t.Fatal(p)
	}
}
func TestCleanupImageByIDRefusedWhileMultiplyTagged(t *testing.T) {
	s, e, _ := cleanupFixture(t)
	e.images = []engine.Image{{ID: "sha256:aaaaaaaaaaaa1111", RepoTags: []string{"a:1", "a:2"}}, {ID: "sha256:bbbbbbbbbbbb2222", RepoTags: []string{"b:1"}}}
	for _, ref := range []string{"sha256:aaaaaaaaaaaa1111", "aaaaaaaaaaaa"} {
		_, err := s.CreatePlan(context.Background(), PlanRequest{Kind: "image", Ref: ref})
		if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "aaaaaaaaaaaa") || !strings.Contains(err.Error(), "a:1, a:2") || !strings.Contains(err.Error(), "one tag at a time") {
			t.Fatal(ref, err)
		}
	}
	// Deleting one tag, or an image with a single tag by id, is still allowed.
	plan(t, s, PlanRequest{Kind: "image", Ref: "a:1"})
	plan(t, s, PlanRequest{Kind: "image", Ref: "bbbbbbbbbbbb"})
}
func TestCleanupImageByIDSkippedWhenItGainsASecondTag(t *testing.T) {
	s, e, _ := cleanupFixture(t)
	e.images = []engine.Image{{ID: "sha256:bbbbbbbbbbbb2222", RepoTags: []string{"b:1"}}}
	p := plan(t, s, PlanRequest{Kind: "image", Ref: "bbbbbbbbbbbb"})
	e.mu.Lock()
	e.images[0].RepoTags = []string{"b:1", "c:1"}
	e.mu.Unlock()
	confirm(t, s, p)
	if len(e.removed) != 0 {
		t.Fatal(e.removed)
	}
}
func TestCleanupUnknownDeclarationsAndCreationIdentity(t *testing.T) {
	s, e, _ := cleanupFixture(t)
	e.volumes = []engine.Volume{volume("external")}
	s.cfg.Registry = serviceRegistry{{Name: "broken", Project: "broken"}}
	for _, r := range []PlanRequest{{Kind: "volume", Name: "external"}, {Kind: "volumes", Selection: "unused"}} {
		if _, err := s.CreatePlan(context.Background(), r); !errors.Is(err, ErrUnavailable) {
			t.Fatal(err)
		}
	}
	s.cfg.Registry = nil
	e.volumes[0].CreatedAt = time.Time{}
	for _, r := range []PlanRequest{{Kind: "volume", Name: "external"}, {Kind: "volumes", Selection: "unused"}} {
		if _, err := s.CreatePlan(context.Background(), r); !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
}
func TestCleanupPlanLifetimeAndConfirm(t *testing.T) {
	s, e, _ := cleanupFixture(t)
	e.volumes = []engine.Volume{volume("data")}
	now := time.Now()
	s.cfg.Now = func() time.Time { return now }
	p := plan(t, s, PlanRequest{Kind: "volume", Name: "data"})
	if len(p.ID) != 34 || !strings.HasPrefix(p.ID, "p_") || p.ExpiresAt.Sub(p.CreatedAt) != 5*time.Minute {
		t.Fatal(p)
	}
	if _, err := s.ConfirmPlan(context.Background(), p.ID, " data", "alice"); !errors.Is(err, ErrWrongPhrase) {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Minute)
	if _, err := s.ConfirmPlan(context.Background(), p.ID, p.ConfirmPhrase, "alice"); !errors.Is(err, ErrPlanNotFound) {
		t.Fatal(err)
	}
	p = plan(t, s, PlanRequest{Kind: "volume", Name: "data"})
	for range 32 {
		plan(t, s, PlanRequest{Kind: "volume", Name: "data"})
	}
	if _, err := s.ConfirmPlan(context.Background(), p.ID, p.ConfirmPhrase, "alice"); !errors.Is(err, ErrPlanNotFound) {
		t.Fatal(err)
	}
	p = plan(t, s, PlanRequest{Kind: "volume", Name: "data"})
	confirm(t, s, p)
	if _, err := s.ConfirmPlan(context.Background(), p.ID, p.ConfirmPhrase, "alice"); !errors.Is(err, ErrPlanNotFound) {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.volumes = nil
	e.mu.Unlock()
	p = plan(t, s, PlanRequest{Kind: "volumes", Selection: "unused"})
	if _, err := s.ConfirmPlan(context.Background(), p.ID, p.ConfirmPhrase, "alice"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}
func TestCleanupRechecks(t *testing.T) {
	for _, scenario := range []string{"used", "declared", "unknown", "replaced", "missing-time", "gone", "refcount"} {
		t.Run(scenario, func(t *testing.T) {
			s, e, l := cleanupFixture(t)
			e.volumes = []engine.Volume{volume("data")}
			p := plan(t, s, PlanRequest{Kind: "volume", Name: "data"})
			switch scenario {
			case "used":
				e.containers = []engine.Container{{Name: "new", Mounts: []engine.Mount{{Type: "volume", Name: "data"}}}}
			case "declared", "unknown":
				s.cfg.Registry = serviceRegistry{{Name: "media", Project: "media"}}
				s.cfg.Declarations = serviceDeclarations(func(context.Context, stacks.Entry) StackDeclarations {
					return StackDeclarations{Name: "media", Known: scenario == "declared", Volumes: []DeclaredVolume{{Name: "data"}}}
				})
			case "replaced":
				e.volumes[0].CreatedAt = e.volumes[0].CreatedAt.Add(time.Second)
			case "missing-time":
				e.volumes[0].CreatedAt = time.Time{}
			case "gone":
				e.mu.Lock()
				e.volumes = nil
				e.mu.Unlock()
			case "refcount":
				e.snap.Volumes = []engine.DFVolume{{Name: "data", UsageData: &engine.DFVolumeUsage{RefCount: 1}}}
			}
			o := confirm(t, s, p)
			if o.State != stacks.OpSucceeded || len(e.removed) != 0 || len(l.rows) != 1 || l.rows[0].Result != "skipped" {
				t.Fatalf("%+v removed=%v rows=%+v", o, e.removed, l.rows)
			}
		})
	}
}
func TestCleanupExplicitPreviewAndLogging(t *testing.T) {
	s, e, l := cleanupFixture(t)
	e.volumes = []engine.Volume{volume("a"), volume("b")}
	p := plan(t, s, PlanRequest{Kind: "volumes", Selection: "unused"})
	e.volumes = append(e.volumes, volume("late"))
	e.fail = "a"
	o := confirm(t, s, p)
	if o.State != stacks.OpFailed || !reflect.DeepEqual(e.removed, []string{"a", "b"}) || len(l.rows) != 2 {
		t.Fatalf("%+v %v %+v", o, e.removed, l.rows)
	}
	for _, r := range l.rows {
		if r.Kind != "storage.prune" || r.Scope != "host" || r.Username != "alice" || r.OperationID != o.ID {
			t.Fatal(r)
		}
	}
	s, e, l = cleanupFixture(t)
	e.images = []engine.Image{{ID: "sha256:aaaaaaaaaaaa", RepoTags: []string{"nginx:1.25", "nginx:latest"}}}
	p = plan(t, s, PlanRequest{Kind: "image", Ref: "nginx:1.25"})
	l.fail = true
	o = confirm(t, s, p)
	lines, _ := s.cfg.Ops.Lines(o.ID)
	if o.State != stacks.OpSucceeded || !reflect.DeepEqual(e.removed, []string{"nginx:1.25"}) || !strings.Contains(strings.Join(lines, "\n"), "untagged nginx:1.25") || !strings.Contains(strings.Join(lines, "\n"), "could not record action") {
		t.Fatalf("%+v %v %v", o, e.removed, lines)
	}
}
func TestCleanupImageFingerprintAndFreshSafety(t *testing.T) {
	s, e, _ := cleanupFixture(t)
	e.images = []engine.Image{{ID: "sha256:aaaaaaaaaaaa"}}
	p := plan(t, s, PlanRequest{Kind: "images", Selection: "dangling"})
	e.images[0].RepoTags = []string{"new:tag"}
	confirm(t, s, p)
	if len(e.removed) != 0 {
		t.Fatal(e.removed)
	}
	e.mu.Lock()
	e.images[0].RepoTags = nil
	e.mu.Unlock()
	p = plan(t, s, PlanRequest{Kind: "images", Selection: "dangling"})
	confirm(t, s, p)
	if !reflect.DeepEqual(e.removed, []string{"sha256:aaaaaaaaaaaa"}) {
		t.Fatal(e.removed)
	}
}
func TestCleanupUnavailablePreflightAndCancellation(t *testing.T) {
	s, e, _ := cleanupFixture(t)
	e.volumes = []engine.Volume{volume("a"), volume("b")}
	p := plan(t, s, PlanRequest{Kind: "volumes", Selection: "unused"})
	e.unavailable = true
	if _, err := s.ConfirmPlan(context.Background(), p.ID, p.ConfirmPhrase, "alice"); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	e.unavailable = false
	gate := make(chan struct{})
	entered := make(chan struct{})
	e.beforeRemove = func(string) { close(entered); <-gate }
	o, err := s.ConfirmPlan(context.Background(), p.ID, p.ConfirmPhrase, "alice")
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	if _, err := s.ConfirmPlan(context.Background(), plan(t, s, PlanRequest{Kind: "volume", Name: "a"}).ID, "a", "alice"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	s.cfg.Ops.Cancel(o.ID)
	close(gate)
	waitService(t, func() bool { v, _ := s.cfg.Ops.Get(o.ID); return v.State != stacks.OpRunning })
	if !reflect.DeepEqual(e.removed, []string{"a"}) {
		t.Fatal(e.removed)
	}
}
func TestCleanupBuildCacheModes(t *testing.T) {
	for _, exact := range []bool{false, true} {
		t.Run(fmt.Sprint(exact), func(t *testing.T) {
			s, e, l := cleanupFixture(t)
			e.exact = exact
			e.snap.BuildCache = []engine.DFBuildCacheRecord{{ID: "a", Size: int64ptr(10)}, {ID: "b", Size: int64ptr(20)}, {ID: "used", InUse: true}, {ID: "shared", Shared: true}}
			p := plan(t, s, PlanRequest{Kind: "build-cache", Selection: "unused"})
			if p.Reclaim.Exact != exact || p.ConfirmPhrase != "delete 2 build cache records" || len(p.Items) != 2 {
				t.Fatal(p)
			}
			if !exact && (p.Mode != "all-unused" || len(p.Warnings) != 1 || p.Warnings[0] != buildCacheWarning) {
				t.Fatal(p)
			}
			o := confirm(t, s, p)
			if o.State != stacks.OpSucceeded || len(e.pruneIDs) != 1 {
				t.Fatal(o, e.pruneIDs)
			}
			if exact {
				if !reflect.DeepEqual(e.pruneIDs[0], []string{"b", "a"}) || len(l.rows) != 2 || *l.rows[0].Bytes != 20 || *l.rows[1].Bytes != 10 {
					t.Fatal(e.pruneIDs, l.rows)
				}
			} else if e.pruneIDs[0] != nil || len(l.rows) != 1 || l.rows[0].Target != "build-cache/all-unused" || *l.rows[0].Bytes != 123 {
				t.Fatal(e.pruneIDs, l.rows)
			}
		})
	}
}

func TestCleanupExistingImageDeclarationWarnsButNewDeclarationSkips(t *testing.T) {
	for _, alreadyDeclared := range []bool{true, false} {
		t.Run(fmt.Sprint(alreadyDeclared), func(t *testing.T) {
			s, e, l := cleanupFixture(t)
			e.images = []engine.Image{{ID: "sha256:aaaaaaaaaaaa", RepoTags: []string{"nginx:1.25"}}}
			declared := func(context.Context, stacks.Entry) StackDeclarations {
				return StackDeclarations{Name: "media", Known: true, Images: []DeclaredImage{{Service: "web", Reference: "nginx:1.25"}}}
			}
			if alreadyDeclared {
				s.cfg.Registry = serviceRegistry{{Name: "media", Project: "media"}}
				s.cfg.Declarations = serviceDeclarations(declared)
			}
			p := plan(t, s, PlanRequest{Kind: "image", Ref: "nginx:1.25"})
			s.cfg.Registry = serviceRegistry{{Name: "media", Project: "media"}}
			s.cfg.Declarations = serviceDeclarations(declared)
			confirm(t, s, p)
			if alreadyDeclared {
				if len(e.removed) != 1 || len(p.Warnings) != 1 {
					t.Fatalf("removed=%v warnings=%v", e.removed, p.Warnings)
				}
			} else if len(e.removed) != 0 || l.rows[0].Result != "skipped" {
				t.Fatal(e.removed, l.rows)
			}
		})
	}
}
func TestCleanupStoppedProtectionAndUnknownSpecificRefusals(t *testing.T) {
	s, e, _ := cleanupFixture(t)
	e.volumes = []engine.Volume{volume("media_db"), volume("external")}
	e.volumes[0].Labels = map[string]string{composeProjectLabel: "media"}
	e.containers = []engine.Container{{Name: "db", State: "exited", Labels: map[string]string{composeProjectLabel: "media"}}}
	s.cfg.Registry = serviceRegistry{{Name: "media", Project: "media"}}
	s.cfg.Declarations = serviceDeclarations(func(context.Context, stacks.Entry) StackDeclarations {
		return StackDeclarations{Name: "media", Known: true, Volumes: []DeclaredVolume{{Name: "media_db"}}}
	})
	p := plan(t, s, PlanRequest{Kind: "volumes", Selection: "unused"})
	if len(p.Protected) != 1 || p.Protected[0].StackState != "stopped" {
		t.Fatal(p)
	}
	s.cfg.Declarations = nil
	if _, err := s.CreatePlan(context.Background(), PlanRequest{Kind: "volume", Name: "media_db"}); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "media") {
		t.Fatal(err)
	}
	e.containers = append(e.containers, engine.Container{Name: "external-user", Mounts: []engine.Mount{{Type: "volume", Name: "external"}}})
	if _, err := s.CreatePlan(context.Background(), PlanRequest{Kind: "volume", Name: "external"}); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "external-user") {
		t.Fatal(err)
	}
}
func TestCleanupFreshListsBypassCachedSafety(t *testing.T) {
	s, e, _ := cleanupFixture(t)
	e.volumes = []engine.Volume{volume("data")}
	if _, err := s.Inputs(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.containers = []engine.Container{{Name: "new-user", Mounts: []engine.Mount{{Type: "volume", Name: "data"}}}}
	if _, err := s.CreatePlan(context.Background(), PlanRequest{Kind: "volume", Name: "data"}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}
func TestCleanupConfirmAtomicAndPlanCopies(t *testing.T) {
	s, e, _ := cleanupFixture(t)
	e.volumes = []engine.Volume{volume("data")}
	p := plan(t, s, PlanRequest{Kind: "volume", Name: "data"})
	p.Items[0].ID = "forged"
	p.ConfirmPhrase = "data"
	var wg sync.WaitGroup
	var mu sync.Mutex
	success, missing := 0, 0
	for range 10 {
		wg.Go(func() {
			_, err := s.ConfirmPlan(context.Background(), p.ID, "data", "alice")
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				success++
			} else if errors.Is(err, ErrPlanNotFound) {
				missing++
			} else {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if success != 1 || missing != 9 {
		t.Fatal(success, missing)
	}
	waitService(t, func() bool { return s.cfg.Ops.Recent("")[0].State != stacks.OpRunning })
	if !reflect.DeepEqual(e.removed, []string{"data"}) {
		t.Fatal(e.removed)
	}
}

type preflightEngine struct {
	*cleanupEngine
	images func(context.Context) error
	df     func(context.Context, engine.DFKind) (engine.DFSnapshot, error)
}

func (e *preflightEngine) Images(ctx context.Context) ([]engine.Image, error) {
	if err := e.images(ctx); err != nil {
		return nil, err
	}
	return e.cleanupEngine.Images(ctx)
}

func (e *preflightEngine) DiskUsage(ctx context.Context, k engine.DFKind) (engine.DFSnapshot, error) {
	if e.df != nil {
		return e.df(ctx, k)
	}
	return e.cleanupEngine.DiskUsage(ctx, k)
}

func TestCleanupConfirmPreflightKeepsDFBrowsingAndPublicationAvailable(t *testing.T) {
	s, e, _ := cleanupFixture(t)
	e.volumes = []engine.Volume{volume("data")}
	p := plan(t, s, PlanRequest{Kind: "volume", Name: "data"})
	preflight, releasePreflight := make(chan struct{}), make(chan struct{})
	dfStarted, releaseDF := make(chan struct{}), make(chan struct{})
	var preflightOnce, dfOnce sync.Once
	defer preflightOnce.Do(func() { close(releasePreflight) })
	defer dfOnce.Do(func() { close(releaseDF) })
	s.cfg.Engine = &preflightEngine{cleanupEngine: e, images: func(context.Context) error {
		close(preflight)
		<-releasePreflight
		return errors.New("preflight offline")
	}, df: func(context.Context, engine.DFKind) (engine.DFSnapshot, error) {
		close(dfStarted)
		<-releaseDF
		return engine.DFSnapshot{Kinds: dfKinds, Volumes: []engine.DFVolume{{Name: "data", UsageData: &engine.DFVolumeUsage{Size: 42}}}}, nil
	}}
	fresh := make(chan error, 1)
	go func() { _, err := s.FreshDF(context.Background(), engine.DFVolumes); fresh <- err }()
	<-dfStarted
	confirmed := make(chan error, 1)
	go func() {
		_, err := s.ConfirmPlan(context.Background(), p.ID, p.ConfirmPhrase, "alice")
		confirmed <- err
	}()
	<-preflight
	for _, read := range []struct {
		name string
		run  func()
	}{
		{"DFReadings", func() { s.DFReadings() }},
		{"VolumeSummary", func() { s.VolumeSummary() }},
	} {
		done := make(chan struct{})
		go func() { read.run(); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Errorf("%s blocked behind confirmation preflight", read.name)
		}
	}
	dfOnce.Do(func() { close(releaseDF) })
	select {
	case err := <-fresh:
		if err != nil {
			t.Fatal(err)
		}
		if summary, ok := s.VolumeSummary(); !ok || summary.Size != 42 {
			t.Fatalf("fresh DF not published: %+v %v", summary, ok)
		}
	case <-time.After(time.Second):
		t.Error("FreshDF completion blocked behind confirmation preflight")
	}
	preflightOnce.Do(func() { close(releasePreflight) })
	if err := <-confirmed; !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}

func TestCleanupConfirmRevalidatesExpiryAfterPreflight(t *testing.T) {
	s, e, _ := cleanupFixture(t)
	e.volumes = []engine.Volume{volume("data")}
	base := time.Now()
	var ticks atomic.Int64
	s.cfg.Now = func() time.Time { return base.Add(time.Duration(ticks.Load())) }
	p := plan(t, s, PlanRequest{Kind: "volume", Name: "data"})
	preflight, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	s.cfg.Engine = &preflightEngine{cleanupEngine: e, images: func(context.Context) error {
		onceGate := ticks.Load() == 0
		if onceGate {
			close(preflight)
			<-release
		}
		return nil
	}}
	done := make(chan error, 1)
	go func() { _, err := s.ConfirmPlan(context.Background(), p.ID, p.ConfirmPhrase, "alice"); done <- err }()
	<-preflight
	ticks.Store(int64(5 * time.Minute))
	once.Do(func() { close(release) })
	if err := <-done; !errors.Is(err, ErrPlanNotFound) {
		t.Fatalf("expired during preflight: %v", err)
	}
	if ops := s.cfg.Ops.Recent(""); len(ops) != 0 {
		t.Fatalf("expired plan started operation: %+v", ops)
	}
}

func TestCleanupConfirmRevalidatesEvictionAfterPreflight(t *testing.T) {
	s, e, _ := cleanupFixture(t)
	e.volumes = []engine.Volume{volume("data")}
	p := plan(t, s, PlanRequest{Kind: "volume", Name: "data"})
	preflight, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	var once sync.Once
	defer once.Do(func() { close(release) })
	s.cfg.Engine = &preflightEngine{cleanupEngine: e, images: func(context.Context) error {
		if first.CompareAndSwap(false, true) {
			close(preflight)
			<-release
		}
		return nil
	}}
	done := make(chan error, 1)
	go func() { _, err := s.ConfirmPlan(context.Background(), p.ID, p.ConfirmPhrase, "alice"); done <- err }()
	<-preflight
	evicted := make(chan error, 1)
	go func() {
		for range 32 {
			if _, err := s.CreatePlan(context.Background(), PlanRequest{Kind: "volume", Name: "data"}); err != nil {
				evicted <- err
				return
			}
		}
		evicted <- nil
	}()
	select {
	case err := <-evicted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Error("plan eviction blocked behind confirmation preflight")
	}
	once.Do(func() { close(release) })
	if err := <-done; !errors.Is(err, ErrPlanNotFound) {
		t.Fatalf("evicted during preflight: %v", err)
	}
	if ops := s.cfg.Ops.Recent(""); len(ops) != 0 {
		t.Fatalf("evicted plan started operation: %+v", ops)
	}
}

func TestCleanupConfirmRelooksUpAfterConcurrentConsumption(t *testing.T) {
	for _, same := range []bool{true, false} {
		t.Run(fmt.Sprintf("same-plan=%v", same), func(t *testing.T) {
			s, e, _ := cleanupFixture(t)
			e.volumes = []engine.Volume{volume("data")}
			first := plan(t, s, PlanRequest{Kind: "volume", Name: "data"})
			target := first
			if !same {
				target = plan(t, s, PlanRequest{Kind: "volume", Name: "data"})
			}
			preflight, release := make(chan struct{}), make(chan struct{})
			var gated atomic.Bool
			var once sync.Once
			defer once.Do(func() { close(release) })
			s.cfg.Engine = &preflightEngine{cleanupEngine: e, images: func(context.Context) error {
				if gated.CompareAndSwap(false, true) {
					close(preflight)
					<-release
				}
				return nil
			}}
			type result struct {
				op  stacks.Operation
				err error
			}
			confirm := func(p Plan, done chan result) {
				o, err := s.ConfirmPlan(context.Background(), p.ID, p.ConfirmPhrase, "alice")
				done <- result{o, err}
			}
			blocked, concurrent := make(chan result, 1), make(chan result, 1)
			go confirm(target, blocked)
			<-preflight
			go confirm(first, concurrent)
			var other result
			select {
			case other = <-concurrent:
			case <-time.After(time.Second):
				t.Error("concurrent confirmation blocked by unrelated preflight")
				once.Do(func() { close(release) })
				other = <-concurrent
			}
			if other.err != nil {
				t.Error(other.err)
			} else {
				waitService(t, func() bool { o, _ := s.cfg.Ops.Get(other.op.ID); return o.State != stacks.OpRunning })
			}
			once.Do(func() { close(release) })
			got := <-blocked
			if same {
				if !errors.Is(got.err, ErrPlanNotFound) {
					t.Fatalf("consumed plan confirmation = %v", got.err)
				}
			} else {
				if got.err != nil {
					t.Fatal(got.err)
				}
				waitService(t, func() bool { o, _ := s.cfg.Ops.Get(got.op.ID); return o.State != stacks.OpRunning })
			}
		})
	}
}
func TestCleanupExactBuildRecheckSkipsAndEmptyNeverPrunes(t *testing.T) {
	for _, all := range []bool{false, true} {
		t.Run(fmt.Sprint(all), func(t *testing.T) {
			s, e, l := cleanupFixture(t)
			e.exact = true
			e.snap.BuildCache = []engine.DFBuildCacheRecord{{ID: "a", Size: int64ptr(1)}, {ID: "b", Size: int64ptr(1)}, {ID: "gone", Size: int64ptr(1)}}
			p := plan(t, s, PlanRequest{Kind: "build-cache", Selection: "unused"})
			e.snap.BuildCache = []engine.DFBuildCacheRecord{{ID: "a", InUse: true}, {ID: "b", Shared: all}, {ID: "late"}}
			o := confirm(t, s, p)
			if o.State != stacks.OpSucceeded || len(l.rows) != 3 {
				t.Fatal(o, l.rows)
			}
			if all {
				if len(e.pruneIDs) != 0 {
					t.Fatal(e.pruneIDs)
				}
			} else if !reflect.DeepEqual(e.pruneIDs, [][]string{{"b"}}) {
				t.Fatal(e.pruneIDs)
			}
		})
	}
}
func TestCleanupImageNowUsedAndAlreadyGone(t *testing.T) {
	for _, gone := range []bool{false, true} {
		t.Run(fmt.Sprint(gone), func(t *testing.T) {
			s, e, l := cleanupFixture(t)
			e.images = []engine.Image{{ID: "sha256:aaaaaaaaaaaa"}}
			p := plan(t, s, PlanRequest{Kind: "images", Selection: "dangling"})
			if gone {
				e.images = nil
			} else {
				e.containers = []engine.Container{{Name: "new", ImageID: e.images[0].ID, State: "exited"}}
			}
			confirm(t, s, p)
			if len(e.removed) != 0 || l.rows[0].Result != "skipped" {
				t.Fatal(e.removed, l.rows)
			}
		})
	}
}

func TestCleanupValidationFields(t *testing.T) {
	s, e, _ := cleanupFixture(t)
	for _, tc := range []struct {
		r     PlanRequest
		field string
	}{{PlanRequest{Kind: "bad"}, "kind"}, {PlanRequest{Kind: "images", Selection: "unused"}, "selection"}, {PlanRequest{Kind: "image", Ref: "a b"}, "ref"}, {PlanRequest{Kind: "volume", Name: "../bad"}, "name"}} {
		_, err := s.CreatePlan(context.Background(), tc.r)
		var fe interface{ ValidationField() string }
		if !errors.As(err, &fe) || fe.ValidationField() != tc.field {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
	e.volumes = []engine.Volume{volume("data")}
	p := plan(t, s, PlanRequest{Kind: "volume", Name: "data"})
	_, err := s.ConfirmPlan(context.Background(), p.ID, "wrong", "alice")
	var fe interface{ ValidationField() string }
	if !errors.As(err, &fe) || fe.ValidationField() != "confirm" || !errors.Is(err, ErrWrongPhrase) {
		t.Fatal(err)
	}
}

func TestCleanupFreshDiskFailureRefusesEveryKind(t *testing.T) {
	for _, r := range []PlanRequest{{Kind: "images", Selection: "dangling"}, {Kind: "volume", Name: "data"}, {Kind: "build-cache", Selection: "unused"}} {
		t.Run(r.Kind, func(t *testing.T) {
			s, e, _ := cleanupFixture(t)
			e.volumes = []engine.Volume{volume("data")}
			e.images = []engine.Image{{ID: "sha256:aaaaaaaaaaaa", Size: 10}}
			e.snap.BuildCache = []engine.DFBuildCacheRecord{{ID: "cache", Size: int64ptr(10)}}
			plan(t, s, r)
			e.dfFailure = true
			if _, err := s.CreatePlan(context.Background(), r); !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "sizes unavailable") {
				t.Fatal(err)
			}
		})
	}
}
func TestCleanupMissingMeasurementsRefuseEligibleItems(t *testing.T) {
	for _, kind := range []string{"images", "volumes", "build-cache"} {
		t.Run(kind, func(t *testing.T) {
			s, e, _ := cleanupFixture(t)
			e.images = []engine.Image{{ID: "sha256:aaaaaaaaaaaa"}}
			e.volumes = []engine.Volume{volume("data")}
			e.snap.Images = []engine.DFImage{}
			e.snap.Volumes = []engine.DFVolume{{Name: "data"}}
			e.snap.BuildCache = []engine.DFBuildCacheRecord{{ID: "cache"}}
			selection := "unused"
			if kind == "images" {
				selection = "dangling"
			}
			if _, err := s.CreatePlan(context.Background(), PlanRequest{Kind: kind, Selection: selection}); !errors.Is(err, ErrUnavailable) {
				t.Fatal(err)
			}
		})
	}
}

func TestCleanupMissingFreshVolumeUsageSkips(t *testing.T) {
	s, e, l := cleanupFixture(t)
	e.volumes = []engine.Volume{volume("data")}
	p := plan(t, s, PlanRequest{Kind: "volume", Name: "data"})
	e.snap.Volumes = []engine.DFVolume{{Name: "data"}}
	confirm(t, s, p)
	if len(e.removed) != 0 || l.rows[0].Result != "skipped" {
		t.Fatal(e.removed, l.rows)
	}
}

func TestCleanupInvalidatesListsAfterFailure(t *testing.T) {
	s, e, _ := cleanupFixture(t)
	e.volumes = []engine.Volume{volume("data")}
	if _, err := s.Inputs(context.Background()); err != nil {
		t.Fatal(err)
	}
	p := plan(t, s, PlanRequest{Kind: "volume", Name: "data"})
	e.volumes = append(e.volumes, volume("late"))
	e.fail = "data"
	o := confirm(t, s, p)
	if o.State != stacks.OpFailed {
		t.Fatal(o)
	}
	in, err := s.Inputs(context.Background())
	if err != nil || len(in.Volumes) != 2 {
		t.Fatal(in.Volumes, err)
	}
}
func TestCleanupLogFailureContinuesLaterItems(t *testing.T) {
	s, e, l := cleanupFixture(t)
	e.volumes = []engine.Volume{volume("a"), volume("b")}
	l.fail = true
	p := plan(t, s, PlanRequest{Kind: "volumes", Selection: "unused"})
	o := confirm(t, s, p)
	lines, _ := s.cfg.Ops.Lines(o.ID)
	if o.State != stacks.OpSucceeded || len(e.removed) != 2 || strings.Count(strings.Join(lines, "\n"), "could not record action") != 2 {
		t.Fatal(o, e.removed, lines)
	}
}
func TestCleanupRefusalNamesAtMostThreeConsumers(t *testing.T) {
	s, e, _ := cleanupFixture(t)
	e.images = []engine.Image{{ID: "sha256:aaaaaaaaaaaa"}}
	for i := range 4 {
		e.containers = append(e.containers, engine.Container{Name: fmt.Sprintf("consumer%d", i), ImageID: e.images[0].ID})
	}
	_, err := s.CreatePlan(context.Background(), PlanRequest{Kind: "image", Ref: "aaaaaaaaaaaa"})
	if err == nil || !strings.Contains(err.Error(), "consumer2") || strings.Contains(err.Error(), "consumer3") {
		t.Fatal(err)
	}
}

func TestPlanReadExpiryCopyAndNoEngineIO(t *testing.T) {
	now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	size := int64(12)
	s := NewService(ServiceConfig{Engine: &cleanupEngine{unavailable: true}, Now: func() time.Time { return now }})
	s.plans = []Plan{{ID: "p_test", ExpiresAt: now.Add(time.Minute), Items: []PlanItem{{ID: "one", Bytes: &size}}, Warnings: []string{"original"}, Reclaim: Reclaim{Min: &size, Max: &size}}}
	p, err := s.Plan("p_test")
	if err != nil {
		t.Fatal(err)
	}
	p.Items[0].ID = "changed"
	*p.Items[0].Bytes = 99
	*p.Reclaim.Min = 99
	p.Warnings[0] = "changed"
	again, err := s.Plan("p_test")
	if err != nil || again.Items[0].ID != "one" || *again.Items[0].Bytes != 12 || *again.Reclaim.Min != 12 || again.Warnings[0] != "original" {
		t.Fatalf("copy=%+v %v", again, err)
	}
	now = now.Add(time.Minute)
	if _, err = s.Plan("p_test"); !errors.Is(err, ErrPlanNotFound) {
		t.Fatalf("expiry=%v", err)
	}
	if _, err = s.Plan("missing"); !errors.Is(err, ErrPlanNotFound) {
		t.Fatalf("unknown=%v", err)
	}
}
