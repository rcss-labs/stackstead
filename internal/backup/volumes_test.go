package backup

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/rcss-labs/stackstead/internal/stacks"
)

func TestCopyVolumeRemovesHelperOnError(t *testing.T) {
	eng := &fakeEngine{archiveErr: errors.New("boom")}
	var buf bytes.Buffer
	_, err := CopyVolume(context.Background(), eng, NewWriter(&buf), "web", stacks.Mount{Kind: "volume", Source: "web_db", Target: "/db"})
	if err == nil {
		t.Fatal("want error")
	}
	if len(eng.removed) != 1 || eng.removed[0] != "helper1" || !eng.removeForce[0] {
		t.Fatalf("helper not force-removed: %v %v", eng.removed, eng.removeForce)
	}
}

func TestCopyVolumeStreamsIntoArchive(t *testing.T) {
	eng := &fakeEngine{archive: buildInnerTar(t, []innerEntry{{name: "data/", dir: true, mode: 0o755}, {name: "data/f", body: "hello", mode: 0o644}})}
	var buf bytes.Buffer
	w := NewWriter(&buf)
	mv, err := CopyVolume(context.Background(), eng, w, "web", stacks.Mount{Kind: "volume", Source: "web_db", Target: "/db"})
	if err != nil {
		t.Fatal(err)
	}
	if mv.Key != "v-web_db" || mv.Kind != "volume" || mv.Name != "web_db" || mv.Target != "/db" || mv.Bytes != 5 {
		t.Fatalf("mv = %+v", mv)
	}
	m := eng.spec.Mounts
	if len(m) != 1 || m[0].Target != "/data" || !m[0].ReadOnly || m[0].Source != "web_db" || m[0].Type != "volume" || eng.archivePath != "/data" {
		t.Fatalf("spec = %+v path %q", eng.spec, eng.archivePath)
	}
	var found bool
	for _, n := range archiveNames(t, w, &buf) {
		found = found || n == "volumes/web/v-web_db/data/f"
	}
	if !found {
		t.Fatal("volume file missing from archive")
	}
	if len(eng.removed) != 1 {
		t.Fatal("helper not removed")
	}
}

func TestVolumeKey(t *testing.T) {
	if got := VolumeKey(stacks.Mount{Kind: "volume", Source: "db"}); got != "v-db" {
		t.Fatal(got)
	}
	k := VolumeKey(stacks.Mount{Kind: "bind", Source: "/srv/x"})
	if !strings.HasPrefix(k, "b-") || len(k) != 14 {
		t.Fatal(k)
	}
}
