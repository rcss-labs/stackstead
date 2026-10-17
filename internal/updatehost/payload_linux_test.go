package updatehost

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/rcss-labs/stackstead/internal/update"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
)

// Native Linux execution must close every staging write handle before exec;
// otherwise the kernel refuses an otherwise valid ELF with ETXTBSY.
func TestPayloadVerifiesAllThreeELFsWithoutOpenStagingWriter(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("payload probes require an unprivileged UID")
	}
	repo, e := filepath.Abs("../..")
	if e != nil {
		t.Fatal(e)
	}
	src, e := os.MkdirTemp(filepath.Join(repo, ".superpowers"), "elf-fixture-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(src)
	code := `package main
import("encoding/json";"os";"github.com/rcss-labs/stackstead/internal/version")
func main(){json.NewEncoder(os.Stdout).Encode(version.Get())}`
	if e = os.WriteFile(filepath.Join(src, "main.go"), []byte(code), 0600); e != nil {
		t.Fatal(e)
	}
	commit := strings.Repeat("a", 40)
	exe := filepath.Join(t.TempDir(), "fixture")
	c := exec.Command("go", "build", "-ldflags", "-X github.com/rcss-labs/stackstead/internal/version.Version=1.2.3 -X github.com/rcss-labs/stackstead/internal/version.Commit="+commit, "-o", exe, filepath.Join(src, "main.go"))
	if b, e := c.CombinedOutput(); e != nil {
		t.Fatalf("ELF fixture: %v %s", e, b)
	}
	binary, e := os.ReadFile(exe)
	if e != nil {
		t.Fatal(e)
	}
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	for _, name := range []string{"stackstead", "stackstead-updater", "stackstead-updater-bootstrap"} {
		if e = tw.WriteHeader(&tar.Header{Name: name, Mode: 0755, Size: int64(len(binary)), Format: tar.FormatUSTAR}); e != nil {
			t.Fatal(e)
		}
		if _, e = tw.Write(binary); e != nil {
			t.Fatal(e)
		}
	}
	if e = tw.Close(); e != nil {
		t.Fatal(e)
	}
	if e = gz.Close(); e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(t.TempDir(), "payload.tar.gz")
	if e = os.WriteFile(p, archive.Bytes(), 0600); e != nil {
		t.Fatal(e)
	}
	f, e := os.Open(p)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	id := update.Identity{Version: "1.2.3", Commit: commit, OS: "linux", Arch: goruntime.GOARCH}
	a := update.VerifiedArtifact{Name: "fixture.tar.gz", Path: p, Size: int64(archive.Len()), SHA256: fmt.Sprintf("%x", sha256.Sum256(archive.Bytes()))}
	proof, e := inspectChildPayload(context.Background(), payloadRequest{Artifact: a, Installer: update.InstallerScript, Identity: id}, f)
	if e != nil {
		t.Fatalf("actual staged ELF execution failed: %v", e)
	}
	if proof.App.Identity != id || proof.Helper.Identity != id || proof.Bootstrap.Identity != id {
		t.Fatalf("incomplete ELF proof: %+v", proof)
	}
}
