package images

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
