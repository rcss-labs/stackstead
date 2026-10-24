//go:build integration

package images

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/engine"
)

func probeSocket() string {
	var candidates []string
	if h := os.Getenv("DOCKER_HOST"); strings.HasPrefix(h, "unix://") {
		candidates = append(candidates, strings.TrimPrefix(h, "unix://"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".docker", "run", "docker.sock"))
	}
	candidates = append(candidates, "/var/run/docker.sock")
	for _, c := range candidates {
		conn, err := net.DialTimeout("unix", c, time.Second)
		if err != nil {
			continue
		}
		conn.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err = engine.New(c, 3*time.Second).Ping(ctx)
		cancel()
		if err == nil {
			return c
		}
	}
	return ""
}

// minimalArchive builds a tiny docker-save style image (one layer holding a
// single file) tagged name, plus extra entries appended after the manifest.
func minimalArchive(t *testing.T, name string, extra ...archiveFile) []byte {
	t.Helper()
	var layer bytes.Buffer
	lw := tar.NewWriter(&layer)
	lw.WriteHeader(&tar.Header{Name: "hello", Mode: 0o644, Size: 2, Typeflag: tar.TypeReg})
	lw.Write([]byte("hi"))
	lw.Close()
	diff := sha256.Sum256(layer.Bytes())
	config, _ := json.Marshal(map[string]any{
		"architecture": runtime.GOARCH, "os": "linux", "config": map[string]any{},
		"rootfs": map[string]any{"type": "layers", "diff_ids": []string{"sha256:" + hex.EncodeToString(diff[:])}},
	})
	cfgSum := sha256.Sum256(config)
	cfgName := hex.EncodeToString(cfgSum[:]) + ".json"
	manifest, _ := json.Marshal([]map[string]any{{"Config": cfgName, "RepoTags": []string{name}, "Layers": []string{"layer0/layer.tar"}}})
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	for _, f := range []archiveFile{
		{name: "layer0/layer.tar", content: layer.String()},
		{name: cfgName, content: string(config)},
		{name: "manifest.json", content: string(manifest)},
	} {
		tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.content)), Typeflag: tar.TypeReg})
		tw.Write([]byte(f.content))
	}
	for _, f := range extra {
		tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.content)), Typeflag: tar.TypeReg})
		tw.Write([]byte(f.content))
	}
	tw.Close()
	return out.Bytes()
}

// nestedIndexArchive builds an OCI layout whose root index.json names only own
// but references a nested index blob whose manifest descriptor carries the
// foreign name in both annotations. Blobs come before index.json.
func nestedIndexArchive(t *testing.T, own, foreign string) []byte {
	t.Helper()
	blobs := map[string][]byte{}
	var order []string
	add := func(data []byte) (string, int) {
		sum := sha256.Sum256(data)
		digest := "sha256:" + hex.EncodeToString(sum[:])
		blobs[digest] = data
		order = append(order, digest)
		return digest, len(data)
	}
	var layer bytes.Buffer
	lw := tar.NewWriter(&layer)
	lw.WriteHeader(&tar.Header{Name: "hello", Mode: 0o644, Size: 2, Typeflag: tar.TypeReg})
	lw.Write([]byte("hi"))
	lw.Close()
	diff := sha256.Sum256(layer.Bytes())
	layerDigest, layerSize := add(layer.Bytes())
	config, _ := json.Marshal(map[string]any{
		"architecture": runtime.GOARCH, "os": "linux", "config": map[string]any{},
		"rootfs": map[string]any{"type": "layers", "diff_ids": []string{"sha256:" + hex.EncodeToString(diff[:])}},
	})
	cfgDigest, cfgSize := add(config)
	manifest, _ := json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": cfgDigest, "size": cfgSize},
		"layers": []map[string]any{{"mediaType": "application/vnd.oci.image.layer.v1.tar", "digest": layerDigest, "size": layerSize}},
	})
	mDigest, mSize := add(manifest)
	nested, _ := json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json",
		"manifests": []map[string]any{{
			"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": mDigest, "size": mSize,
			"annotations": map[string]string{
				"io.containerd.image.name":          foreign,
				"org.opencontainers.image.ref.name": foreign,
			},
		}},
	})
	nDigest, nSize := add(nested)
	root, _ := json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json",
		"manifests": []map[string]any{{
			"mediaType": "application/vnd.oci.image.index.v1+json", "digest": nDigest, "size": nSize,
			"annotations": map[string]string{"io.containerd.image.name": own},
		}},
	})
	files := []archiveFile{}
	for _, d := range order {
		files = append(files, archiveFile{name: "blobs/sha256/" + strings.TrimPrefix(d, "sha256:"), content: string(blobs[d])})
	}
	files = append(files,
		archiveFile{name: "oci-layout", content: `{"imageLayoutVersion":"1.0.0"}`},
		archiveFile{name: "index.json", content: string(root)})
	return buildArchive(files...)
}

// TestIntegrationLoadNeverCreatesForeignTags loads crafted archives into a
// real Docker through the inspector. Archives that name another tag must be
// refused before Docker tags anything; a clean archive must load.
func TestIntegrationLoadNeverCreatesForeignTags(t *testing.T) {
	sock := probeSocket()
	if sock == "" {
		t.Skip("no reachable Docker socket")
	}
	docker := func(args ...string) (string, error) {
		cmd := exec.Command("docker", args...)
		cmd.Env = append(os.Environ(), "DOCKER_HOST=unix://"+sock)
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI not installed")
	}
	raw := make([]byte, 4)
	rand.Read(raw)
	id := hex.EncodeToString(raw)
	own := "stackstead.invalid/probe" + id + "/probe:1"
	foreign := "stackstead-probe-" + id + "/nginx:latest"
	t.Cleanup(func() { docker("rmi", own); docker("rmi", foreign) })
	eng := engine.New(sock, 30*time.Second)
	ctx := context.Background()

	// Control: without the inspector Docker does apply the foreign tag, so the
	// refusals below are the inspector's doing.
	if err := eng.LoadImage(ctx, bytes.NewReader(minimalArchive(t, foreign))); err != nil {
		t.Fatalf("control load: %v", err)
	}
	if _, err := docker("image", "inspect", foreign); err != nil {
		t.Fatalf("control: docker did not tag %s", foreign)
	}
	docker("rmi", foreign)

	crafted := map[string][]byte{
		"foreign tag in manifest.json": minimalArchive(t, foreign),
		"absolute-name metadata appended": minimalArchive(t, own,
			archiveFile{name: "/manifest.json", content: manifestJSON(own, foreign)}),
	}
	for name, archive := range crafted {
		insp := newTarInspector(bytes.NewReader(archive), own)
		start := time.Now()
		lctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := eng.LoadImage(lctx, insp)
		cancel()
		insp.close()
		t.Logf("%s: load returned after %v: %v (inspector verdict: %v)", name, time.Since(start).Round(time.Millisecond), err, insp.violation())
		if err == nil {
			t.Errorf("%s: load succeeded", name)
		}
		for _, ref := range []string{foreign, own} {
			if _, ierr := docker("image", "inspect", ref); ierr == nil {
				t.Errorf("%s: %s exists after a refused load", name, ref)
				docker("rmi", ref)
			}
		}
	}

	// A nested index blob under blobs/ that names another tag is not read by
	// the inspector. Docker must not tag it either; this is the regression
	// guard for that assumption. What Docker does with the archive depends on
	// its image store: the containerd store loads it and applies only the root
	// index's own name, while the classic overlay2 store cannot read a nested
	// OCI index and rejects the load. Both are safe, so the property asserted
	// is that no foreign tag ever appears.
	nested := newTarInspector(bytes.NewReader(nestedIndexArchive(t, own, foreign)), own)
	nctx, ncancel := context.WithTimeout(ctx, 30*time.Second)
	nerr := eng.LoadImage(nctx, nested)
	ncancel()
	nested.close()
	t.Logf("nested-index load result (depends on the daemon's image store): %v", nerr)
	if _, err := docker("image", "inspect", foreign); err == nil {
		t.Errorf("docker tagged %s from a nested index: the inspector must parse nested index blobs", foreign)
	}
	if listed, _ := docker("images", "--format", "{{.Repository}}:{{.Tag}}"); strings.Contains(listed, "stackstead-probe-"+id) {
		t.Errorf("docker listed a foreign probe image after the nested-index load:\n%s", listed)
	}
	if nerr == nil {
		if _, err := docker("image", "inspect", own); err != nil {
			t.Errorf("nested-index archive loaded but did not create %s", own)
		}
	}
	docker("rmi", own)

	clean := newTarInspector(bytes.NewReader(minimalArchive(t, own)), own)
	defer clean.close()
	cctx, ccancel := context.WithTimeout(ctx, 30*time.Second)
	defer ccancel()
	if err := eng.LoadImage(cctx, clean); err != nil {
		t.Fatalf("clean archive refused: %v", err)
	}
	if _, err := docker("image", "inspect", own); err != nil {
		t.Fatalf("clean archive did not create %s", own)
	}
}
