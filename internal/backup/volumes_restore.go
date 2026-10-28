package backup

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

// helperCleanupTimeout bounds removing a helper container after its context ended.
const helperCleanupTimeout = 30 * time.Second

// RestoreVolume replaces the contents of one named volume with inner, a tar
// stream with paths relative to the mount. A missing volume is created with
// labels. A throwaway helper container mounts it read-write at /data, empties
// it, and inner is extracted into it through Docker. The helper is removed on
// every path. Bind mounts go through RestoreBind. The caller makes sure the
// helper image is present.
func RestoreVolume(ctx context.Context, eng Engine, stack string, mount stacks.Mount, labels map[string]string, inner io.Reader) error {
	if mount.Kind != "volume" {
		return fmt.Errorf("unknown mount kind %q", mount.Kind)
	}
	_, err := eng.InspectVolume(ctx, mount.Source)
	if errors.Is(err, engine.ErrVolumeNotFound) {
		err = eng.CreateVolume(ctx, mount.Source, labels)
	}
	if err != nil {
		return err
	}
	return runHelper(ctx, eng, helperRun{
		stack:  stack,
		mount:  engine.HelperMount{Type: mount.Kind, Source: mount.Source, Target: "/data"},
		cmd:    []string{"find", "/data", "-mindepth", "1", "-delete"},
		label:  mount.Source,
		putDir: "/",
	}, inner)
}

// bindMountPoint is where RestoreBind mounts the vetted root in the helper.
const bindMountPoint = "/mnt"

// emptyBindScript walks $2 below $1 one component at a time, refusing any
// symlink and creating missing directories, then empties the directory; a
// final realpath check fails closed if anything resolves elsewhere. Docker
// follows host symlinks in a bind source, so only the vetted root is mounted
// and this runs on the container's view, where a symlink can only point at the
// helper's own filesystem.
const emptyBindScript = `set -eu
set -f
p=$1
IFS=/
for c in $2; do
  [ -n "$c" ] || continue
  p="$p/$c"
  if [ -L "$p" ]; then echo "refusing symlink $p" >&2; exit 3; fi
  [ -e "$p" ] || mkdir "$p"
done
unset IFS
[ -d "$p" ] || exit 4
[ "$(realpath "$p")" = "$p" ] || { echo "$p resolves elsewhere" >&2; exit 5; }
find "$p" -mindepth 1 -delete`

// RestoreBind replaces the contents of the vetted bind source target with
// inner (a tar stream whose entries are under data/). The helper mounts only
// target.Root, never the bind source itself, and empties and fills
// <mount>/<target.Rel> inside the container; see emptyBindScript.
func RestoreBind(ctx context.Context, eng Engine, stack string, target bindTarget, inner io.Reader) error {
	rel := path.Clean(filepath.ToSlash(target.Rel))
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || path.IsAbs(rel) {
		return fmt.Errorf("bind source %s is not below its root", target)
	}
	stripped, stop := stripDataPrefix(inner)
	defer stop() // inner is not read once this returns
	return runHelper(ctx, eng, helperRun{
		stack:  stack,
		mount:  engine.HelperMount{Type: volumeKindBind, Source: target.Root, Target: bindMountPoint},
		cmd:    []string{"sh", "-c", emptyBindScript, "sh", bindMountPoint, rel},
		label:  target.String(),
		putDir: bindMountPoint + "/" + rel,
	}, stripped)
}

type helperRun struct {
	stack  string
	mount  engine.HelperMount
	cmd    []string
	label  string // names the target in errors
	putDir string
}

func runHelper(ctx context.Context, eng Engine, run helperRun, inner io.Reader) error {
	id, err := eng.CreateContainer(ctx, engine.HelperSpec{
		Image:  HelperImage,
		Mounts: []engine.HelperMount{run.mount},
		Cmd:    run.cmd,
		Labels: map[string]string{"dev.stackstead.restore-stack": run.stack},
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
		return fmt.Errorf("empty %s: %w", run.label, err)
	}
	if code != 0 {
		return fmt.Errorf("empty %s: helper exited with %d", run.label, code)
	}
	if err := eng.PutArchive(ctx, id, run.putDir, inner); err != nil {
		return fmt.Errorf("write %s: %w", run.label, err)
	}
	return nil
}

// stripDataPrefix rewrites a tar stream of data/... entries to entries
// relative to the data directory itself ("data" becomes "./"), so it extracts
// straight into the bind directory. An entry outside data/ fails the stream.
// stop ends the rewrite and waits for it, after which in is no longer read.
func stripDataPrefix(in io.Reader) (out io.Reader, stop func()) {
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		pw.CloseWithError(rewriteTar(in, pw))
	}()
	return pr, func() {
		pr.Close()
		<-done
	}
}

func rewriteTar(in io.Reader, out io.Writer) error {
	tr, tw := tar.NewReader(in), tar.NewWriter(out)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return tw.Close()
		}
		if err != nil {
			return err
		}
		if hdr.Name, err = stripData(hdr.Name); err != nil {
			return err
		}
		if hdr.Typeflag == tar.TypeLink {
			if hdr.Linkname, err = stripData(hdr.Linkname); err != nil {
				return err
			}
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := io.Copy(tw, tr); err != nil {
			return err
		}
	}
}

func stripData(name string) (string, error) {
	name = path.Clean(name)
	switch {
	case name == "data":
		return "./", nil
	case strings.HasPrefix(name, "data/"):
		return strings.TrimPrefix(name, "data/"), nil
	}
	return "", fmt.Errorf("archive entry %q is outside data/", name)
}
