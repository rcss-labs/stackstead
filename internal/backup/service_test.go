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

func TestSaveConfigWithScheduleOnClearsPausedFlag(t *testing.T) {
	cs, db := newTestConfigStore(t)
	svc := &Service{Settings: cs}
	ctx := context.Background()
	if err := PauseScheduleAfterRestore(ctx, db); err != nil {
		t.Fatal(err)
	}
	if paused, err := svc.PausedAfterRestore(ctx); err != nil || !paused {
		t.Fatalf("PausedAfterRestore = %v, %v; want true after a restore", paused, err)
	}

	off := defaultConfig()
	if err := svc.SaveConfig(ctx, off); err != nil {
		t.Fatal(err)
	}
	if paused, _ := svc.PausedAfterRestore(ctx); !paused {
		t.Error("saving with the schedule still off cleared the flag")
	}

	on := defaultConfig()
	on.Schedule.Enabled = true
	if err := svc.SaveConfig(ctx, on); err != nil {
		t.Fatal(err)
	}
	if paused, _ := svc.PausedAfterRestore(ctx); paused {
		t.Error("turning the schedule on kept the paused flag")
	}
}
