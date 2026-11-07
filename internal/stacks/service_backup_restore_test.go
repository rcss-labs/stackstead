package stacks_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rcss-labs/stackstead/internal/stacks"
)

func TestRegisterRestoredAddsEntry(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	path := filepath.Join(h.dir, "app", "compose.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("services: {a: {image: nginx}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := stacks.Entry{Name: "app", ComposePath: path, Origin: stacks.OriginManaged, Project: "app"}
	if err := h.svc.RegisterRestored(ctx, e); err != nil {
		t.Fatal(err)
	}
	got, err := h.reg.Get(ctx, "app")
	if err != nil || got.ComposePath != path || got.Origin != stacks.OriginManaged || got.Project != "app" {
		t.Fatalf("registered = %+v (%v)", got, err)
	}
	if err := h.svc.RegisterRestored(ctx, e); !errors.Is(err, stacks.ErrStackExists) {
		t.Errorf("second register err = %v, want ErrStackExists", err)
	}
}

func TestRegisterRestoredRejectsBadNameAndTakenProject(t *testing.T) {
	h := newHarness(t)
	setupWeb(t, h)
	ctx := context.Background()
	if err := h.svc.RegisterRestored(ctx, stacks.Entry{Name: "../evil", ComposePath: "/x/compose.yaml", Project: "evil"}); err == nil {
		t.Error("an unsafe name was registered")
	}
	err := h.svc.RegisterRestored(ctx, stacks.Entry{Name: "other", ComposePath: filepath.Join(h.dir, "other", "compose.yaml"), Origin: stacks.OriginManaged, Project: "web"})
	if !errors.Is(err, stacks.ErrProjectTaken) {
		t.Errorf("err = %v, want ErrProjectTaken", err)
	}
}

func TestStopAndStartForRestoreBypassOps(t *testing.T) {
	h := newHarness(t)
	setupWeb(t, h)
	ctx := context.Background()
	// The restore holds the stack's operation slot, and its restart must also
	// work during shutdown, so neither may go through Ops.
	h.ops.Shutdown(ctx)

	if err := h.svc.StopForRestore(ctx, "web", func(string) {}); err != nil {
		t.Fatalf("StopForRestore: %v", err)
	}
	if got := h.compose.lastRun(); got.File != h.path("web") || strings.Join(got.Args, " ") != "stop" {
		t.Errorf("stop run = %+v", got)
	}
	if err := h.svc.StartRestored(ctx, "web", func(string) {}); err != nil {
		t.Fatalf("StartRestored: %v", err)
	}
	if got := h.compose.lastRun(); got.File != h.path("web") || strings.Join(got.Args, " ") != "up -d --build --remove-orphans" {
		t.Errorf("start run = %+v", got)
	}
}
