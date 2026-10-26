//go:build integration

package engine_test

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/engine"
)

// helperImage mirrors backup.HelperImage; engine must not import backup.
const helperImage = "busybox:1.37.0@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e"

func TestHelperRoundTripVolume(t *testing.T) {
	run, eng := itDocker(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err := eng.PullImage(ctx, helperImage, nil); err != nil {
		t.Fatalf("pull: %v", err)
	}
	vol := itName(t, run)
	t.Cleanup(func() { run("volume", "rm", "-f", vol) })
	if err := eng.CreateVolume(ctx, vol, map[string]string{"dev.stackstead.test": "true"}); err != nil {
		t.Fatalf("create volume: %v", err)
	}
	id, err := eng.CreateContainer(ctx, engine.HelperSpec{
		Image:  helperImage,
		Mounts: []engine.HelperMount{{Type: "volume", Source: vol, Target: "/data"}},
		Cmd:    []string{"true"},
	})
	if err != nil {
		t.Fatalf("create container: %v", err)
	}
	t.Cleanup(func() { eng.RemoveContainer(context.Background(), id, true) })

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	content := "hello backup"
	if err := tw.WriteHeader(&tar.Header{Name: "hello.txt", Mode: 0o644, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	tw.Write([]byte(content))
	tw.Close()
	if err := eng.PutArchive(ctx, id, "/data", &buf); err != nil {
		t.Fatalf("put archive: %v", err)
	}

	rc, err := eng.GetArchive(ctx, id, "/data/.")
	if err != nil {
		t.Fatalf("get archive: %v", err)
	}
	defer rc.Close()
	tr := tar.NewReader(rc)
	found := false
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Name == "hello.txt" || h.Name == "./hello.txt" {
			b, _ := io.ReadAll(tr)
			found = string(b) == content
		}
	}
	if !found {
		t.Fatal("hello.txt not read back from the volume")
	}

	if err := eng.ContainerAction(ctx, id, "start", 0); err != nil {
		t.Fatalf("start: %v", err)
	}
	if code, err := eng.WaitContainer(ctx, id); err != nil || code != 0 {
		t.Fatalf("wait: code=%d err=%v", code, err)
	}
	if err := eng.RemoveContainer(ctx, id, false); err != nil {
		t.Fatalf("remove container: %v", err)
	}
}
