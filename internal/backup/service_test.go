package backup

import (
	"context"
	"errors"
	"testing"

	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

type fakeSizes struct{ snap engine.DFSnapshot }

func (f fakeSizes) DiskUsage(context.Context, engine.DFKind) (engine.DFSnapshot, error) {
	return f.snap, nil
}

func TestServiceSelectionsListMountsWithSizes(t *testing.T) {
	env := newRunnerEnv(t, localOnly(t))
	env.addStack(t, "web", stacks.StatusRunning)
	env.stacks.mounts["web"] = []stacks.Mount{
		{Kind: "volume", Source: "web_data", Target: "/data"},
		{Kind: "bind", Source: "/srv/media", Target: "/media"},
	}
	svc := &Service{
		Settings: env.deps.Config, History: env.deps.Runs, Runner: env.runner, Stacks: env.stacks,
		Sizes: fakeSizes{engine.DFSnapshot{Volumes: []engine.DFVolume{{Name: "web_data", UsageData: &engine.DFVolumeUsage{Size: 2048}}}}},
	}
	ctx := context.Background()

	views, err := svc.Selections(ctx)
	if err != nil || len(views) != 1 {
		t.Fatalf("Selections = %+v, %v", views, err)
	}
	v := views[0]
	if v.Volumes || v.Images || !v.StopDuring {
		t.Errorf("unsaved stack defaults = %+v", v.StackSelection)
	}
	if len(v.Mounts) != 2 || v.Mounts[0].Bytes != 2048 || v.Mounts[1].Bytes != -1 {
		t.Errorf("mounts = %+v", v.Mounts)
	}

	if err := svc.SetSelection(ctx, StackSelection{Stack: "web", Volumes: true}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetSelection(ctx, StackSelection{Stack: "ghost"}); !errors.Is(err, ErrUnknownStack) {
		t.Errorf("unknown stack err = %v", err)
	}
	views, _ = svc.Selections(ctx)
	if !views[0].Volumes {
		t.Error("saved selection not returned")
	}
}
