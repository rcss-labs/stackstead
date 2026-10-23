package images

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/engine"
)

func TestBuildTagTaken(t *testing.T) {
	h := newHarness(t)
	h.create(t, "app")
	h.build(t, "app", "1", nil)
	_, _, err := h.svc.Build(context.Background(), "alice", "app", BuildRequest{Tag: "1"})
	if !errors.Is(err, ErrTagTaken) {
		t.Fatalf("err = %v", err)
	}
	if got, _ := h.svc.store.Versions(context.Background(), mustID(t, h, "app")); len(got) != 1 {
		t.Fatalf("versions = %d", len(got))
	}
}

func mustID(t *testing.T, h *harness, slug string) string {
	t.Helper()
	img, err := h.svc.Get(context.Background(), slug)
	if err != nil {
		t.Fatal(err)
	}
	return img.ID
}

func TestBuildFailedTagReusable(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.create(t, "app")
	h.eng.buildErr = &engine.BuildError{Message: "boom"}
	v := h.build(t, "app", "1", nil)
	if v.Status != VersionFailed || !strings.Contains(v.Error, "boom") {
		t.Fatalf("version = %+v", v)
	}
	if e, ok := h.log.last("image.build"); !ok || e.Result != "failed" {
		t.Fatalf("log = %+v", e)
	}
	if _, err := os.Stat(filepath.Join(h.dir, "app", "logs", v.ID+".log")); err != nil {
		t.Fatal("failed build has no log file")
	}
	h.eng.buildErr = nil
	if again := h.build(t, "app", "1", nil); again.Status != VersionSucceeded {
		t.Fatalf("retry = %+v", again)
	}
	if d, _ := h.svc.Detail(ctx, "app"); d.SuggestedTag != "2" || len(d.Versions) != 2 {
		t.Fatalf("detail = %+v", d)
	}
}

func TestBuildBusy(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.create(t, "app")
	h.eng.block = make(chan struct{})
	h.eng.started = make(chan struct{})
	v, op, err := h.svc.Build(ctx, "alice", "app", BuildRequest{Tag: "1"})
	if err != nil {
		t.Fatal(err)
	}
	<-h.eng.started
	if _, _, err := h.svc.Build(ctx, "alice", "app", BuildRequest{Tag: "2"}); !errors.Is(err, ErrBuildRunning) {
		t.Fatalf("err = %v", err)
	}
	if got, _ := h.svc.store.Versions(ctx, v.ImageID); len(got) != 1 {
		t.Fatalf("versions = %d", len(got))
	}
	if err := h.svc.DeleteVersion(ctx, "alice", "app", v.ID); !errors.Is(err, ErrBuildRunning) {
		t.Fatalf("delete building err = %v", err)
	}
	close(h.eng.block)
	h.waitOp(t, op.ID)
	if got, _ := h.svc.store.VersionByID(ctx, v.ID); got.Status != VersionSucceeded {
		t.Fatalf("status = %s", got.Status)
	}
}

func TestBuildCancelledMarksFailed(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.create(t, "app")
	h.eng.block = make(chan struct{})
	h.eng.started = make(chan struct{})
	v, op, err := h.svc.Build(ctx, "alice", "app", BuildRequest{Tag: "1"})
	if err != nil {
		t.Fatal(err)
	}
	<-h.eng.started
	h.ops.Cancel(op.ID)
	h.waitOp(t, op.ID)
	got, _ := h.svc.store.VersionByID(ctx, v.ID)
	if got.Status != VersionFailed || !strings.Contains(got.Error, "cancel") {
		t.Fatalf("version = %+v", got)
	}
}

func TestBuildSaveArgsUpdatesDefaults(t *testing.T) {
	h := newHarness(t)
	h.create(t, "app")
	_, op, err := h.svc.Build(context.Background(), "alice", "app", BuildRequest{Tag: "1", BuildArgs: map[string]string{"B": "9"}, SaveArgs: true})
	if err != nil {
		t.Fatal(err)
	}
	h.waitOp(t, op.ID)
	img, _ := h.svc.Get(context.Background(), "app")
	if img.BuildArgs["B"] != "9" || img.BuildArgs["A"] != "1" {
		t.Fatalf("defaults = %v", img.BuildArgs)
	}
}

func TestBuildRejectsBadTagAndUnknownImage(t *testing.T) {
	h := newHarness(t)
	h.create(t, "app")
	var ve *ValidationError
	if _, _, err := h.svc.Build(context.Background(), "alice", "app", BuildRequest{Tag: "bad tag"}); !errors.As(err, &ve) {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := h.svc.Build(context.Background(), "alice", "nope", BuildRequest{Tag: "1"}); !errors.Is(err, ErrImageNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildSaveArgsKeepsConcurrentUpdate(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.create(t, "app")
	h.svc.beforeBuildLock = func() {
		if _, err := h.svc.Update(ctx, "bob", "app", UpdateRequest{Name: "Renamed", BuildArgs: map[string]string{"A": "1", "Z": "26"}}); err != nil {
			t.Error(err)
		}
	}
	_, op, err := h.svc.Build(ctx, "alice", "app", BuildRequest{Tag: "1", BuildArgs: map[string]string{"B": "9"}, SaveArgs: true})
	if err != nil {
		t.Fatal(err)
	}
	h.waitOp(t, op.ID)
	img, _ := h.svc.Get(ctx, "app")
	if img.Name != "Renamed" || img.BuildArgs["Z"] != "26" || img.BuildArgs["B"] != "9" {
		t.Fatalf("image = %+v", img)
	}
}

func TestBuildSaveArgsFailureIsReturned(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	img := h.create(t, "app")
	if _, err := h.svc.store.db.Exec(`CREATE TRIGGER block_image_update BEFORE UPDATE ON images BEGIN SELECT RAISE(ABORT, 'blocked'); END`); err != nil {
		t.Fatal(err)
	}
	_, _, err := h.svc.Build(ctx, "alice", "app", BuildRequest{Tag: "1", BuildArgs: map[string]string{"B": "9"}, SaveArgs: true})
	if err == nil || !strings.Contains(err.Error(), "save build defaults") {
		t.Fatalf("err = %v", err)
	}
	if got, _ := h.svc.store.Versions(ctx, img.ID); len(got) != 0 {
		t.Fatalf("version rows left: %d", len(got))
	}
}

func TestBuildOpsBusyLeavesNoVersionAndRestoresDefaults(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	img := h.create(t, "app")
	release := make(chan struct{})
	if _, err := h.ops.StartTimeout(OpKey(testOwner, "app"), "push", time.Minute, func(ctx context.Context, _ func(string)) error {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	defer close(release)
	_, _, err := h.svc.Build(ctx, "alice", "app", BuildRequest{Tag: "1", BuildArgs: map[string]string{"B": "9"}, SaveArgs: true})
	if !errors.Is(err, ErrImageBusy) {
		t.Fatalf("err = %v", err)
	}
	if got, _ := h.svc.store.Versions(ctx, img.ID); len(got) != 0 {
		t.Fatalf("version rows left: %d", len(got))
	}
	if got, _ := h.svc.Get(ctx, "app"); got.BuildArgs["B"] != "2" {
		t.Fatalf("defaults not restored: %v", got.BuildArgs)
	}
}

func TestBuildFailedStartKeepsArgsChangedMeanwhile(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.create(t, "app")
	release := make(chan struct{})
	if _, err := h.ops.StartTimeout(OpKey(testOwner, "app"), "push", time.Minute, func(ctx context.Context, _ func(string)) error {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	defer close(release)
	h.svc.beforeBuildOp = func() {
		if _, err := h.svc.Update(ctx, "bob", "app", UpdateRequest{Name: "app", BuildArgs: map[string]string{"C": "3"}}); err != nil {
			t.Error(err)
		}
	}
	_, _, err := h.svc.Build(ctx, "alice", "app", BuildRequest{Tag: "1", BuildArgs: map[string]string{"B": "9"}, SaveArgs: true})
	if !errors.Is(err, ErrImageBusy) {
		t.Fatalf("err = %v", err)
	}
	got, _ := h.svc.Get(ctx, "app")
	if len(got.BuildArgs) != 1 || got.BuildArgs["C"] != "3" {
		t.Fatalf("build args = %v; want the concurrent update {C:3}", got.BuildArgs)
	}
}

func TestBuildDeletedImageDuringStart(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.create(t, "app")
	h.svc.beforeBuildLock = func() { h.svc.Delete(ctx, "alice", "app") }
	if _, _, err := h.svc.Build(ctx, "alice", "app", BuildRequest{Tag: "1"}); !errors.Is(err, ErrImageNotFound) {
		t.Fatalf("err = %v", err)
	}
}
