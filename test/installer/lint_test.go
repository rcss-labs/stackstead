//go:build installer

package installer_test

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const lintTestVersion = "v0.0.1"

// lintRepoRoot is the repository root (this package lives in test/installer).
func lintRepoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func lintRunID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func lintRequireDocker(t *testing.T) {
	t.Helper()
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("docker unavailable: %v", err)
	}
}

// buildPackages runs `make packages` into a fresh DIST and BIN so the test never
// touches the developer's dist/ or bin/.
func buildPackages(t *testing.T) (dist string) {
	t.Helper()
	root := lintRepoRoot(t)
	tmp := t.TempDir()
	dist = filepath.Join(tmp, "dist")
	cmd := exec.Command("make", "packages", "VERSION="+lintTestVersion, "ALLOW_DEV_RELEASE=1", "DIST="+dist, "BIN="+filepath.Join(tmp, "bin"))
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make packages failed: %v\n%s", err, out)
	}
	return dist
}

func expectedPackages() []string {
	return []string{
		"stackstead_0.0.1_amd64.deb",
		"stackstead_0.0.1_arm64.deb",
		"stackstead-0.0.1-1.x86_64.rpm",
		"stackstead-0.0.1-1.aarch64.rpm",
	}
}

// lintContainer runs a shell script in the lint image with dist mounted read-only.
// The container is uniquely named and labelled, removed with --rm, and force-removed
// on cleanup (registered before it is created); nothing else is touched.
func lintContainer(t *testing.T, dist, script string) (string, error) {
	t.Helper()
	root := lintRepoRoot(t)
	id := lintRunID(t)
	name := "stackstead-itest-" + id + "-lint"
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", name).Run()
	})
	build := exec.Command("make", "lint-image")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("make lint-image failed: %v\n%s", err, out)
	}
	imgOut, err := exec.Command("make", "-s", "-C", root, "print-lint-image").Output()
	if err != nil {
		t.Fatalf("make print-lint-image: %v", err)
	}
	image := strings.TrimSpace(string(imgOut))
	cmd := exec.Command("docker", "run", "--rm", "--name", name, "--label", "stackstead.test="+id,
		"-v", dist+":/dist:ro", "-v", filepath.Join(root, "deploy/pkg")+":/pkg:ro",
		image, "sh", "-c", script)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return buf.String(), err
	case <-time.After(5 * time.Minute):
		_ = cmd.Process.Kill()
		t.Fatalf("lint container timed out:\n%s", buf.String())
	}
	return "", nil
}

func TestNfpmVersionPinned(t *testing.T) {
	mk, err := os.ReadFile(filepath.Join(lintRepoRoot(t), "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^NFPM_VERSION\s*:?=\s*(\S+)\s*$`).FindSubmatch(mk)
	if m == nil {
		t.Fatal("Makefile has no NFPM_VERSION := <version> line")
	}
	if !regexp.MustCompile(`^v\d+\.\d+\.\d+$`).Match(m[1]) {
		t.Fatalf("NFPM_VERSION %q is not an exact vX.Y.Z", m[1])
	}
	if !strings.Contains(string(mk), "nfpm/v2/cmd/nfpm@$(NFPM_VERSION)") {
		t.Fatal("NFPM default must be `go run github.com/goreleaser/nfpm/v2/cmd/nfpm@$(NFPM_VERSION)`")
	}
}

func TestPackagesBuild(t *testing.T) {
	dist := buildPackages(t)
	for _, name := range expectedPackages() {
		st, err := os.Stat(filepath.Join(dist, name))
		if err != nil || st.Size() == 0 {
			t.Fatalf("expected non-empty %s in dist: %v", name, err)
		}
	}
	lintRequireDocker(t)
	out, err := lintContainer(t, dist, `set -eu
cd /dist
for f in *.deb; do
  dpkg-deb --info "$f"
  dpkg-deb --contents "$f"
  echo "ARCH $f $(dpkg-deb -f "$f" Architecture)"
  echo "CONFFILES $f"; dpkg-deb -I "$f" conffiles
done
for f in *.rpm; do
  rpm -qpl "$f"
  echo "CONFIG $f"; rpm -qpc "$f"
  echo "ARCH $f $(rpm -qp --qf '%{ARCH}' "$f")"
  rpm -qp --scripts "$f"
done`)
	if err != nil {
		t.Fatalf("package inspection failed: %v\n%s", err, out)
	}
	for _, want := range []string{
		"./usr/bin/stackstead", "./usr/lib/systemd/system/stackstead.service",
		"./etc/stackstead/config.yaml", "./usr/share/doc/stackstead/copyright",
		"preinst", "postinst", "prerm", "postrm",
		"/usr/bin/stackstead\n", "/usr/lib/systemd/system/stackstead.service\n",
		"/usr/share/licenses/stackstead/LICENSE",
		"preinstall scriptlet", "postinstall scriptlet", "preuninstall scriptlet", "postuninstall scriptlet",
		"ARCH stackstead_0.0.1_amd64.deb amd64", "ARCH stackstead_0.0.1_arm64.deb arm64",
		"ARCH stackstead-0.0.1-1.x86_64.rpm x86_64", "ARCH stackstead-0.0.1-1.aarch64.rpm aarch64",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("package inspection output lacks %q\n%s", want, out)
		}
	}
	if !strings.Contains(out, "CONFFILES stackstead_0.0.1_amd64.deb\n/etc/stackstead/config.yaml") {
		t.Errorf("deb conffiles must list /etc/stackstead/config.yaml\n%s", out)
	}
	if !strings.Contains(out, "CONFIG stackstead-0.0.1-1.x86_64.rpm\n/etc/stackstead/config.yaml") {
		t.Errorf("rpm config files must list /etc/stackstead/config.yaml\n%s", out)
	}
	if regexp.MustCompile(`(?m)(\./opt/|^/opt(/|$))`).MatchString(out) {
		t.Errorf("/opt/stacks must not be in the payload\n%s", out)
	}
}

func TestPackagesAreCoveredBySums(t *testing.T) {
	dist := buildPackages(t)
	root := lintRepoRoot(t)
	// `make release` also builds the UI (Node); the checksum step is mkrelease, so run it
	// directly over the same dist/ and bin/ that `make packages` produced.
	cmd := exec.Command("go", "run", "./tools/mkrelease", "-version", lintTestVersion, "-allow-dev",
		"-dist", dist, "-bin", filepath.Join(filepath.Dir(dist), "bin"), "-targets", "linux/amd64,linux/arm64")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "SOURCE_DATE_EPOCH=1700000000")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mkrelease failed: %v\n%s", err, out)
	}
	sums, err := os.ReadFile(filepath.Join(dist, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range expectedPackages() {
		if !strings.Contains(string(sums), "  "+name+"\n") {
			t.Errorf("SHA256SUMS does not list %s\n%s", name, sums)
		}
	}
}

func TestPackageLintClean(t *testing.T) {
	lintRequireDocker(t)
	dist := buildPackages(t)
	cmd := exec.Command("make", "package-lint", "DIST="+dist)
	cmd.Dir = lintRepoRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make package-lint failed: %v\n%s", err, out)
	}
	t.Logf("%s", out)
}
