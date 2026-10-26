package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

// VolumeKey is the path-safe id of a mount inside the archive: v-<volume name>
// for named volumes, b-<first 12 hex of sha256(path)> for bind mounts.
func VolumeKey(m stacks.Mount) string {
	if m.Kind == "bind" {
		sum := sha256.Sum256([]byte(m.Source))
		return "b-" + hex.EncodeToString(sum[:])[:12]
	}
	return "v-" + m.Source
}

// CopyVolume streams one volume or bind mount into the archive under
// volumes/<stack>/<key>/ through a throwaway helper container that mounts it
// read-only at /data. The helper is always removed.
func CopyVolume(ctx context.Context, eng Engine, w *Writer, stack string, m stacks.Mount) (mv ManifestVolume, err error) {
	key := VolumeKey(m)
	mv = ManifestVolume{Key: key, Kind: m.Kind, Name: m.Source, Target: m.Target}
	id, err := eng.CreateContainer(ctx, engine.HelperSpec{
		Image:  HelperImage,
		Mounts: []engine.HelperMount{{Type: m.Kind, Source: m.Source, Target: "/data", ReadOnly: true}},
		Cmd:    []string{"true"},
		Labels: map[string]string{"dev.stackstead.backup-stack": stack},
	})
	if err != nil {
		return mv, fmt.Errorf("create helper for %s: %w", key, err)
	}
	defer func() {
		// The caller's context may be cancelled; the helper must still go.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = eng.RemoveContainer(cleanupCtx, id, true)
	}()
	body, err := eng.GetArchive(ctx, id, "/data")
	if err != nil {
		return mv, fmt.Errorf("read %s: %w", key, err)
	}
	defer body.Close()
	mv.Bytes, err = w.AddTarStream("volumes/"+stack+"/"+key+"/", body)
	if err != nil {
		return mv, fmt.Errorf("archive %s: %w", key, err)
	}
	return mv, nil
}
