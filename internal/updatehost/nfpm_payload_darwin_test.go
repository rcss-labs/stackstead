package updatehost

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"github.com/rcss-labs/stackstead/internal/update"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestNativePayloadAcceptsActualPinnedNFPMGNUAndNamedData(t *testing.T) {
	repo, e := filepath.Abs("../..")
	if e != nil {
		t.Fatal(e)
	}
	w := t.TempDir()
	bin := filepath.Join(w, "binary")
	if e = os.WriteFile(bin, []byte("fixture\n"), 0755); e != nil {
		t.Fatal(e)
	}
	out := filepath.Join(w, "fixture.deb")
	c := exec.Command("go", "run", "github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.43.1", "package", "--config", "deploy/nfpm.yaml", "--packager", "deb", "--target", out)
	c.Dir = repo
	c.Env = append(os.Environ(), "ARCH=amd64", "VERSION=1.2.3", "PRERELEASE=", "BIN="+bin)
	if b, e := c.CombinedOutput(); e != nil {
		t.Fatalf("actual nfpm fixture: %v %s", e, b)
	}
	b, e := exec.Command("/usr/bin/bsdtar", "-xOf", out, "data.tar.gz").Output()
	if e != nil {
		t.Fatal(e)
	}
	gz, e := gzip.NewReader(bytes.NewReader(b))
	if e != nil {
		t.Fatal(e)
	}
	defer gz.Close()
	proof, e := inventoryTar(context.Background(), gz, payloadRequest{Installer: update.InstallerDeb}, defaultPayloadBounds, false)
	if e != nil {
		t.Fatalf("actual pinned nfpm GNU payload rejected: %v", e)
	}
	data, config := false, false
	for _, e := range proof.Entries {
		switch e.Path {
		case "/var/lib/stackstead":
			data = e.UID == 0 && e.GID == 0 && e.Uname == "stackstead" && e.Gname == "stackstead" && e.Mode == 0700
		case "/etc/stackstead/config.yaml":
			config = e.UID == 0 && e.GID == 0 && e.Uname == "root" && e.Gname == "stackstead" && e.Mode == 0640
		}
	}
	if !data || !config {
		t.Fatal("native named ownership or mode was discarded")
	}
}
func TestNativeRPMAcceptsActualPinnedNFPMAbsoluteCPIO(t *testing.T) {
	repo, e := filepath.Abs("../..")
	if e != nil {
		t.Fatal(e)
	}
	w := t.TempDir()
	bin := filepath.Join(w, "binary")
	if e = os.WriteFile(bin, []byte("fixture\n"), 0755); e != nil {
		t.Fatal(e)
	}
	out := filepath.Join(w, "fixture.rpm")
	c := exec.Command("go", "run", "github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.43.1", "package", "--config", "deploy/nfpm.yaml", "--packager", "rpm", "--target", out)
	c.Dir = repo
	c.Env = append(os.Environ(), "ARCH=amd64", "VERSION=1.2.3", "PRERELEASE=", "BIN="+bin)
	if b, e := c.CombinedOutput(); e != nil {
		t.Fatalf("actual nfpm RPM fixture: %v %s", e, b)
	}
	b, e := os.ReadFile(out)
	if e != nil {
		t.Fatal(e)
	}
	// Test-only bounded extraction of the known RPM lead/two headers, followed
	// by its genuine gzip/newc bytes. Production uses fixed rpm/rpm2cpio tools.
	off := 96
	for i := 0; i < 2; i++ {
		if off+16 > len(b) || !bytes.Equal(b[off:off+3], []byte{0x8e, 0xad, 0xe8}) {
			t.Fatal("RPM fixture header")
		}
		n := int(binary.BigEndian.Uint32(b[off+8 : off+12]))
		size := int(binary.BigEndian.Uint32(b[off+12 : off+16]))
		if n > 4096 || size > 1<<20 {
			t.Fatal("RPM fixture header limit")
		}
		off += 16 + 16*n + size
		if i == 0 {
			off = (off + 7) / 8 * 8
		}
		if off > len(b) {
			t.Fatal("RPM fixture header truncated")
		}
	}
	gz, e := gzip.NewReader(bytes.NewReader(b[off:]))
	if e != nil {
		t.Fatal(e)
	}
	defer gz.Close()
	raw, e := io.ReadAll(io.LimitReader(gz, 1<<20))
	if e != nil || len(raw) >= 1<<20 {
		t.Fatal("RPM fixture CPIO bound")
	}
	if len(raw) < 111 || string(raw[:6]) != "070701" || raw[110] != '/' {
		t.Fatal("fixture did not exercise actual absolute newc names")
	}
	var converted bytes.Buffer
	if e = newcToTar(bytes.NewReader(raw), &converted); e != nil {
		t.Fatalf("actual pinned nfpm absolute CPIO rejected: %v", e)
	}
	tr := tar.NewReader(&converted)
	found := false
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		if h.Name == "var/lib/stackstead" {
			found = h.Mode == 0700 && h.Uid == 0 && h.Gid == 0 && h.Typeflag == tar.TypeDir
		}
	}
	if !found {
		t.Fatal("actual nfpm data directory mode/numeric placeholder lost")
	}
}
