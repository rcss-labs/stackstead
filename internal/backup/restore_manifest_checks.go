package backup

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/rcss-labs/stackstead/internal/images"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

// errVolumeKeyMismatch refuses a volume entry whose archive key is not the key
// of the name the manifest gives it. Only the manifest (or its sidecar) says
// where the data goes, so an edited name would redirect it.
var errVolumeKeyMismatch = errors.New("archive key does not match the volume name in the manifest")

// checkVolumeKey requires mv.Key to be VolumeKey of mv's own kind and name.
func checkVolumeKey(mv ManifestVolume) error {
	if want := VolumeKey(stacks.Mount{Kind: mv.Kind, Source: mv.Name}); mv.Key != want {
		return fmt.Errorf("%w: %s %q has key %s, expected %s", errVolumeKeyMismatch, mv.Kind, mv.Name, mv.Key, want)
	}
	return nil
}

// loadImageChecked loads an archived image tar, refusing one that carries a
// tag other than ref so a crafted archive cannot retag an unrelated local
// image. It reuses the streaming inspector of the peer image fetch: the tar is
// parsed as it flows to docker and never buffered, and the stream fails before
// docker sees the end of an archive that names a foreign tag.
func loadImageChecked(ctx context.Context, eng Engine, ref string, stream io.Reader) error {
	guard := images.NewTarGuard(stream, ref)
	defer guard.Close()
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	loadErr := eng.LoadImage(ctx, guard)
	if verr := guard.Violation(); verr != nil {
		cancel(verr) // an engine that ignored the read error must not finish the load
		return fmt.Errorf("refused image %s: %w", ref, verr)
	}
	return loadErr
}
