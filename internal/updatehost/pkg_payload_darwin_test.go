package updatehost

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/rcss-labs/stackstead/internal/update"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func realPKGFixture(t *testing.T, badHelper ...bool) (payloadRequest, *os.File) {
	t.Helper()
	w := t.TempDir()
	// Build under the repository so Go's internal-package boundary is respected.
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.MkdirTemp(filepath.Join(repo, ".superpowers"), "payload-fixture-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(src) })
	code := `package main
import("encoding/json";"os";"github.com/rcss-labs/stackstead/internal/version")
func main(){json.NewEncoder(os.Stdout).Encode(version.Get())}
`
	if err = os.WriteFile(filepath.Join(src, "main.go"), []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("a", 40)
	run := func(path string, args ...string) {
		t.Helper()
		c := exec.Command(path, args...)
		c.Dir = repo
		c.Env = append(os.Environ(), "COPYFILE_DISABLE=1")
		if b, e := c.CombinedOutput(); e != nil {
			t.Fatalf("fixture %s %v: %v: %s", path, args, e, b)
		}
	}
	for _, arch := range []string{"amd64", "arm64"} {
		c := exec.Command("go", "build", "-ldflags", "-X github.com/rcss-labs/stackstead/internal/version.Version=1.2.3 -X github.com/rcss-labs/stackstead/internal/version.Commit="+commit, "-o", filepath.Join(w, arch), filepath.Join(src, "main.go"))
		c.Env = append(os.Environ(), "GOOS=darwin", "GOARCH="+arch)
		if b, e := c.CombinedOutput(); e != nil {
			t.Fatalf("fixture build: %v %s", e, b)
		}
	}
	if len(badHelper) > 0 && badHelper[0] {
		c := exec.Command("go", "build", "-ldflags", "-X github.com/rcss-labs/stackstead/internal/version.Version=1.2.3 -X github.com/rcss-labs/stackstead/internal/version.Commit="+strings.Repeat("b", 40), "-o", filepath.Join(w, "wrong-amd64"), filepath.Join(src, "main.go"))
		c.Env = append(os.Environ(), "GOOS=darwin", "GOARCH=amd64")
		if b, e := c.CombinedOutput(); e != nil {
			t.Fatalf("wrong slice fixture: %v %s", e, b)
		}
	}
	for _, p := range []string{"usr/local/bin/stackstead", "Library/PrivilegedHelperTools/stackstead-updater", "Library/PrivilegedHelperTools/stackstead-updater-bootstrap"} {
		dest := filepath.Join(w, "root", p)
		if err = os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			t.Fatal(err)
		}
		amd64 := filepath.Join(w, "amd64")
		if len(badHelper) > 0 && badHelper[0] && p == "Library/PrivilegedHelperTools/stackstead-updater" {
			amd64 = filepath.Join(w, "wrong-amd64")
		}
		run("/usr/bin/lipo", "-create", "-output", dest, amd64, filepath.Join(w, "arm64"))
	}
	run("/usr/bin/xattr", "-cr", filepath.Join(w, "root"))
	os.Mkdir(filepath.Join(w, "pkgs"), 0700)
	run("/usr/bin/pkgbuild", "--root", filepath.Join(w, "root"), "--identifier", pkgIdentifier, "--version", "1.2.3", "--install-location", "/", "--ownership", "recommended", "--compression", "legacy", "--scripts", filepath.Join(repo, "deploy/macos/scripts"), filepath.Join(w, "pkgs", "stackstead-component.pkg"))
	distBytes, e := os.ReadFile(filepath.Join(repo, "deploy/macos/distribution.xml.in"))
	if e != nil {
		t.Fatal(e)
	}
	dist := strings.NewReplacer("@IDENTIFIER@", pkgIdentifier, "@VERSION@", "1.2.3", "@COMPONENT@", pkgComponent).Replace(string(distBytes))
	os.WriteFile(filepath.Join(w, "distribution.xml"), []byte(dist), 0600)
	run("/usr/bin/productbuild", "--distribution", filepath.Join(w, "distribution.xml"), "--resources", filepath.Join(repo, "deploy/macos/resources"), "--package-path", filepath.Join(w, "pkgs"), filepath.Join(w, "fixture.pkg"))
	raw, err := os.ReadFile(filepath.Join(w, "fixture.pkg"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(w, "fixture.pkg"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return payloadRequest{Artifact: update.VerifiedArtifact{Name: "fixture.pkg", Path: f.Name(), Size: int64(len(raw)), SHA256: fmt.Sprintf("%x", sha256.Sum256(raw))}, Installer: update.InstallerPkg, Identity: update.Identity{Version: "1.2.3", Commit: commit, OS: "darwin", Arch: "arm64"}}, f
}
func TestPKGProducerFromRealLegacyProductbuild(t *testing.T) {
	req, f := realPKGFixture(t)
	got, err := inspectChildPayload(context.Background(), req, f)
	if err != nil {
		t.Fatalf("actual builder-produced PKG proof unavailable: %v", err)
	}
	if got.Artifact != req.Artifact || got.Identifier != pkgIdentifier || got.Version != "1.2.3" || got.InstallLocation != "/" || len(got.Entries) < 9 {
		t.Fatalf("incomplete inventory: %+v", got)
	}
	for _, binary := range []executableInspection{got.App, got.Helper, got.Bootstrap} {
		if binary.Identity != req.Identity || len(binary.Slices) != 2 {
			t.Fatalf("missing full binary/slice identities: %+v", binary)
		}
	}
}

func TestPKGProducerEnvelopeUsesOneExactComponentEncoding(t *testing.T) {
	for _, variant := range []string{"canonical", "duplicated", "resources", "mixed", "collision", "unknown"} {
		t.Run(variant, func(t *testing.T) {
			var b bytes.Buffer
			tw := tar.NewWriter(&b)
			tw.WriteHeader(&tar.Header{Name: pkgComponent + "/", Typeflag: tar.TypeDir, Mode: 0755, Format: tar.FormatUSTAR})
			for i, role := range []string{"Bom", "Payload", "PackageInfo"} {
				p := pkgComponent + "/" + role
				if variant == "duplicated" || (variant == "mixed" && i == 1) {
					p += p
				}
				tw.WriteHeader(&tar.Header{Name: p, Mode: 0644, Size: 1, Format: tar.FormatUSTAR})
				tw.Write([]byte("x"))
			}
			if variant == "resources" {
				tw.WriteHeader(&tar.Header{Name: "Resources/", Typeflag: tar.TypeDir, Mode: 0755, Format: tar.FormatUSTAR})
				tw.WriteHeader(&tar.Header{Name: "Resources/welcome.html", Mode: 0644, Size: 1, Format: tar.FormatUSTAR})
				tw.Write([]byte("x"))
			}
			if variant == "collision" {
				p := pkgComponent + "/Payload"
				tw.WriteHeader(&tar.Header{Name: p + p, Mode: 0644, Size: 1, Format: tar.FormatUSTAR})
				tw.Write([]byte("x"))
			}
			if variant == "unknown" {
				tw.WriteHeader(&tar.Header{Name: "other.pkg/Payload", Mode: 0644, Size: 1, Format: tar.FormatUSTAR})
				tw.Write([]byte("x"))
			}
			tw.WriteHeader(&tar.Header{Name: "Distribution", Mode: 0644, Size: 1, Format: tar.FormatUSTAR})
			tw.Write([]byte("x"))
			tw.Close()
			err := readPKGEnvelope(bytes.NewReader(b.Bytes()), func(_ string, h *tar.Header, r io.Reader) error { _, e := io.CopyN(io.Discard, r, h.Size); return e })
			positive := variant == "canonical" || variant == "duplicated" || variant == "resources"
			if positive && err != nil {
				t.Fatalf("actual supported layout refused: %v", err)
			}
			if !positive && err == nil {
				t.Fatal("ambiguous component accepted")
			}
		})
	}
}
func TestPKGProducerChildUsesVerifiedInputDescriptor(t *testing.T) {
	req, src := realPKGFixture(t)
	executor, in := childFixture(t)
	staged := filepath.Join(filepath.Dir(executor.workerPath), "payload.pkg")
	dst, e := os.OpenFile(staged, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = io.Copy(dst, src); e != nil {
		t.Fatal(e)
	}
	if e = dst.Close(); e != nil {
		t.Fatal(e)
	}
	req.Artifact.Path = staged
	_, _, p := pkgFixture()
	p.Target.Identity = req.Identity
	p.TargetArtifact = req.Artifact
	p.Registration.Instances = []update.Instance{in}
	got, e := executor.inspectPayload(context.Background(), "actual-job", p, req.Artifact)
	if e != nil {
		t.Fatalf("fixed UID payload child could not inspect verified FD: %v", e)
	}
	if got.Artifact != req.Artifact || got.Helper.Identity != req.Identity || got.Bootstrap.Identity != req.Identity {
		t.Fatalf("incomplete child proof: %+v", got)
	}
}

func TestPayloadVerifiesAllThreeExecutableIdentitiesWrongIncompatibleHelperSlice(t *testing.T) {
	req, f := realPKGFixture(t, true)
	if _, e := inspectChildPayload(context.Background(), req, f); e == nil {
		t.Fatal("wrong full commit in helper amd64 slice accepted on arm64 host")
	}
}

func TestPKGProducerDistributionRejectsExternalOrExecutableConstructs(t *testing.T) {
	base := `<?xml version="1.0"?><installer-gui-script minSpecVersion="2"><options customize="never" require-scripts="false"/><choice id="io.github.rcss-labs.stackstead"><pkg-ref id="io.github.rcss-labs.stackstead"/></choice><pkg-ref id="io.github.rcss-labs.stackstead" version="1.2.3">#stackstead-component.pkg</pkg-ref><pkg-ref id="io.github.rcss-labs.stackstead"><bundle-version/></pkg-ref></installer-gui-script>`
	for _, bad := range []string{"", "external", "wrong-id", "DTD", "script-attribute", "installation-check", "namespace", "duplicate-attribute", "absent", "duplicate-locator", "version"} {
		t.Run(bad, func(t *testing.T) {
			s := base
			switch bad {
			case "external":
				s = strings.Replace(s, "#stackstead-component.pkg", "https://example.invalid/other.pkg", 1)
			case "wrong-id":
				s = strings.Replace(s, pkgIdentifier, "other.id", 1)
			case "DTD":
				s = strings.Replace(s, "<installer-gui-script", `<!DOCTYPE x [<!ENTITY x SYSTEM "file:///tmp/secret">]><installer-gui-script`, 1)
			case "script-attribute":
				s = strings.Replace(s, `customize="never"`, `customize="never" script="evil()"`, 1)
			case "installation-check":
				s = strings.Replace(s, "</installer-gui-script>", `<installation-check script="evil()"/></installer-gui-script>`, 1)
			case "namespace":
				s = strings.Replace(s, "<choice", `<choice xmlns="evil"`, 1)
			case "duplicate-attribute":
				s = strings.Replace(s, `minSpecVersion="2"`, `minSpecVersion="2" minSpecVersion="2"`, 1)
			case "absent":
				s = strings.Replace(s, "#stackstead-component.pkg", "", 1)
			case "duplicate-locator":
				s = strings.Replace(s, "</installer-gui-script>", `<pkg-ref id="io.github.rcss-labs.stackstead">#stackstead-component.pkg</pkg-ref></installer-gui-script>`, 1)
			case "version":
				s = strings.Replace(s, `version="1.2.3"`, `version="1.2.4"`, 1)
			}
			e := validatePKGDistribution([]byte(s), "1.2.3")
			if bad == "" && e != nil {
				t.Fatal(e)
			}
			if bad != "" && e == nil {
				t.Fatalf("unsafe distribution %s accepted", bad)
			}
		})
	}
}
