package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

// helperCleanupTimeout bounds removing a helper container after its context ended.
const helperCleanupTimeout = 30 * time.Second

// RestoreVolume replaces the contents of one volume or bind mount with inner,
// a tar stream with paths relative to the mount. A missing named volume is
// created with labels. A throwaway helper container mounts it read-write at
// /data, empties it, and inner is extracted into it through Docker. The
// helper is removed on every path. mount.Source is what Docker mounts: the
// volume name, or the (already vetted) host path of a bind. The caller makes
// sure the helper image is present.
func RestoreVolume(ctx context.Context, eng Engine, stack string, mount stacks.Mount, labels map[string]string, inner io.Reader) error {
	switch mount.Kind {
	case "volume":
		_, err := eng.InspectVolume(ctx, mount.Source)
		if errors.Is(err, engine.ErrVolumeNotFound) {
			err = eng.CreateVolume(ctx, mount.Source, labels)
		}
		if err != nil {
			return err
		}
	case volumeKindBind:
	default:
		return fmt.Errorf("unknown mount kind %q", mount.Kind)
	}
	id, err := eng.CreateContainer(ctx, engine.HelperSpec{
		Image:  HelperImage,
		Mounts: []engine.HelperMount{{Type: mount.Kind, Source: mount.Source, Target: "/data"}},
		Cmd:    []string{"find", "/data", "-mindepth", "1", "-delete"},
		Labels: map[string]string{"dev.stackstead.restore-stack": stack},
	})
	if err != nil {
		return fmt.Errorf("create helper: %w", err)
	}
	defer func() {
		// The caller's context may be cancelled; the helper must still go.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), helperCleanupTimeout)
		defer cancel()
		_ = eng.RemoveContainer(cleanupCtx, id, true)
	}()
	if err := eng.StartContainer(ctx, id); err != nil {
		return fmt.Errorf("start helper: %w", err)
	}
	code, err := eng.WaitContainer(ctx, id)
	if err != nil {
		return fmt.Errorf("empty %s: %w", mount.Source, err)
	}
	if code != 0 {
		return fmt.Errorf("empty %s: helper exited with %d", mount.Source, code)
	}
	if err := eng.PutArchive(ctx, id, "/", inner); err != nil {
		return fmt.Errorf("write %s: %w", mount.Source, err)
	}
	return nil
}
