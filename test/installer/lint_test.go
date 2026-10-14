//go:build installer

package installer_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	lintTestVersion = "v0.0.1"
	linuxTargets    = "linux/amd64 linux/arm64"
)

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

// lintMake runs make in the repo root and returns its combined output.
func lintMake(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("make", args...)
	cmd.Dir = lintRepoRoot(t)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

type lintDirs struct{ dist, bin string }

func newLintDirs(t *testing.T) lintDirs {
	t.Helper()
	tmp := t.TempDir()
	return lintDirs{dist: filepath.Join(tmp, "dist"), bin: filepath.Join(tmp, "bin")}
}

func (d lintDirs) args(extra ...string) []string {
	return append([]string{"DIST=" + d.dist, "BIN=" + d.bin}, extra...)
}

// buildBinaries runs the shared `make binaries` step. SKIP_UI=1 leaves out the
// Node-based UI build, which does not influence the Go packaging under test.
func buildBinaries(t *testing.T, d lintDirs, version, targets string) {
	t.Helper()
	out, err := lintMake(t, d.args("binaries", "SKIP_UI=1", "VERSION="+version, "ALLOW_DEV_RELEASE=1", "RELEASE_TARGETS="+targets)...)
	if err != nil {
		t.Fatalf("make binaries failed: %v\n%s", err, out)
	}
}

// buildPackages builds binaries then packages into fresh dirs; the repo's dist/ and
// bin/ are never touched.
func buildPackages(t *testing.T, version, targets, arches string) lintDirs {
	t.Helper()
	d := newLintDirs(t)
	buildBinaries(t, d, version, targets)
	out, err := lintMake(t, d.args("packages", "VERSION="+version, "PKG_ARCHES="+arches)...)
	if err != nil {
		t.Fatalf("make packages failed: %v\n%s", err, out)
	}
	return d
}

func expectedPackages() []string {
	return []string{
		"stackstead_0.0.1_amd64.deb",
		"stackstead_0.0.1_arm64.deb",
		"stackstead-0.0.1-1.x86_64.rpm",
		"stackstead-0.0.1-1.aarch64.rpm",
	}
}

func lintImage(t *testing.T) string {
	t.Helper()
	if out, err := lintMake(t, "lint-image"); err != nil {
		t.Fatalf("make lint-image failed: %v\n%s", err, out)
	}
	out, err := lintMake(t, "-s", "print-lint-image")
	if err != nil {
		t.Fatalf("make print-lint-image: %v\n%s", err, out)
	}
	return strings.TrimSpace(out)
}

// lintContainer runs a shell script in the lint image with dist mounted read-only.
// The container is uniquely named and labelled, removed with --rm, and force-removed
// on cleanup (registered before it is created); nothing else is touched.
func lintContainer(t *testing.T, dist, script string) (string, error) {
	t.Helper()
	image := lintImage(t)
	id := lintRunID(t)
	name := "stackstead-itest-" + id + "-lint"
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	cmd := exec.Command("docker", "run", "--rm", "--name", name, "--label", "stackstead.test="+id,
		"-v", dist+":/dist:ro", "-v", filepath.Join(lintRepoRoot(t), "deploy/pkg")+":/pkg:ro",
		image, "sh", "-c", script)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
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

// section returns the part of out that follows the "=== <name>" marker up to the next marker.
func section(t *testing.T, out, name string) string {
	t.Helper()
	_, rest, ok := strings.Cut(out, "=== "+name+"\n")
	if !ok {
		t.Fatalf("no section %q in output:\n%s", name, out)
	}
	body, _, _ := strings.Cut(rest, "=== ")
	return body
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
	d := buildPackages(t, lintTestVersion, linuxTargets, "amd64 arm64")
	for _, name := range expectedPackages() {
		st, err := os.Stat(filepath.Join(d.dist, name))
		if err != nil || st.Size() == 0 {
			t.Fatalf("expected non-empty %s in dist: %v", name, err)
		}
	}
	lintRequireDocker(t)
	out, err := lintContainer(t, d.dist, `set -eu
cd /dist
for f in *.deb; do
  echo "=== $f"
  dpkg-deb --info "$f"
  dpkg-deb --contents "$f"
  echo "ARCH $(dpkg-deb -f "$f" Architecture)"
  echo "CONFFILES"; dpkg-deb -I "$f" conffiles
done
for f in *.rpm; do
  echo "=== $f"
  rpm -qpl "$f"
  echo "CONFIG"; rpm -qpc "$f"
  echo "ARCH $(rpm -qp --qf '%{ARCH}' "$f")"
  rpm -qp --scripts "$f"
done`)
	if err != nil {
		t.Fatalf("package inspection failed: %v\n%s", err, out)
	}
	opt := regexp.MustCompile(`(?m)(\./opt/|^/opt(/|$))`)
	for _, pkg := range []struct{ file, arch string }{
		{"stackstead_0.0.1_amd64.deb", "amd64"}, {"stackstead_0.0.1_arm64.deb", "arm64"},
	} {
		s := section(t, out, pkg.file)
		for _, want := range []string{
			"./usr/bin/stackstead", "./usr/lib/systemd/system/stackstead.service",
			"./etc/stackstead/config.yaml", "./usr/share/doc/stackstead/copyright",
			"ARCH " + pkg.arch, "CONFFILES\n/etc/stackstead/config.yaml",
		} {
			if !strings.Contains(s, want) {
				t.Errorf("%s lacks %q\n%s", pkg.file, want, s)
			}
		}
		for _, script := range []string{"preinst", "postinst", "prerm", "postrm"} {
			if !regexp.MustCompile(`(?m)^\s*\d+ bytes,\s+\d+ lines\s+\*\s+` + script + `\s`).MatchString(s) {
				t.Errorf("%s control info lacks maintainer script %s\n%s", pkg.file, script, s)
			}
		}
		if opt.MatchString(s) {
			t.Errorf("%s: /opt/stacks must not be in the payload\n%s", pkg.file, s)
		}
	}
	for _, pkg := range []struct{ file, arch string }{
		{"stackstead-0.0.1-1.x86_64.rpm", "x86_64"}, {"stackstead-0.0.1-1.aarch64.rpm", "aarch64"},
	} {
		s := section(t, out, pkg.file)
		for _, want := range []string{
			"/usr/bin/stackstead\n", "/usr/lib/systemd/system/stackstead.service\n",
			"/usr/share/licenses/stackstead/LICENSE", "CONFIG\n/etc/stackstead/config.yaml",
			"ARCH " + pkg.arch,
			"preinstall scriptlet", "postinstall scriptlet", "preuninstall scriptlet", "postuninstall scriptlet",
		} {
			if !strings.Contains(s, want) {
				t.Errorf("%s lacks %q\n%s", pkg.file, want, s)
			}
		}
		if opt.MatchString(s) {
			t.Errorf("%s: /opt/stacks must not be in the payload\n%s", pkg.file, s)
		}
	}
}

func TestPackagesFailWithoutBinaries(t *testing.T) {
	d := newLintDirs(t)
	out, err := lintMake(t, d.args("packages", "VERSION="+lintTestVersion)...)
	if err == nil {
		t.Fatalf("make packages must fail when no binaries were built\n%s", out)
	}
	if !strings.Contains(out, "missing binary") || !strings.Contains(out, "make binaries") {
		t.Errorf("failure must name the missing binary and say to run make binaries\n%s", out)
	}
	if entries, _ := os.ReadDir(d.dist); len(entries) != 0 {
		t.Errorf("no package may be produced without binaries: %v", entries)
	}
}

func TestPackagesRefuseDevVersion(t *testing.T) {
	d := newLintDirs(t)
	buildBinaries(t, d, lintTestVersion, "linux/amd64")
	for _, v := range []string{"abc1234", "v0.0.1-3-gabc1234", "v0.0.1-dirty"} {
		out, err := lintMake(t, d.args("packages", "VERSION="+v, "PKG_ARCHES=amd64")...)
		if err == nil {
			t.Errorf("make packages VERSION=%s must fail without ALLOW_DEV_RELEASE=1\n%s", v, out)
		}
		if !strings.Contains(out, "dev version") {
			t.Errorf("VERSION=%s: failure should say it is a dev version\n%s", v, out)
		}
	}
	if entries, _ := os.ReadDir(d.dist); len(entries) != 0 {
		t.Errorf("no package may be produced for a dev version: %v", entries)
	}
	out, err := lintMake(t, d.args("packages", "VERSION=abc1234", "ALLOW_DEV_RELEASE=1", "PKG_ARCHES=amd64")...)
	if err != nil {
		t.Fatalf("ALLOW_DEV_RELEASE=1 must permit a dev package: %v\n%s", err, out)
	}
}

// An unparseable version is not a dev version: the message must be mkrelease's own.
func TestPackagesReportTheRealVersionError(t *testing.T) {
	d := newLintDirs(t)
	buildBinaries(t, d, lintTestVersion, "linux/amd64")
	out, err := lintMake(t, d.args("packages", "VERSION=not a version", "PKG_ARCHES=amd64")...)
	if err == nil {
		t.Fatalf("make packages with an unparseable VERSION must fail\n%s", out)
	}
	if !strings.Contains(out, "cannot normalise version") {
		t.Errorf("the real mkrelease error must be shown\n%s", out)
	}
	if strings.Contains(out, "is a dev version") {
		t.Errorf("an unparseable version must not be blamed on being a dev version\n%s", out)
	}
}

func TestPackagesPrereleaseUsesTilde(t *testing.T) {
	d := buildPackages(t, "v0.0.1-rc1", "linux/amd64", "amd64")
	deb, rpm := "stackstead_0.0.1~rc1_amd64.deb", "stackstead-0.0.1~rc1-1.x86_64.rpm"
	for _, n := range []string{deb, rpm} {
		if _, err := os.Stat(filepath.Join(d.dist, n)); err != nil {
			t.Fatalf("missing %s: %v", n, err)
		}
	}
	lintRequireDocker(t)
	out, err := lintContainer(t, d.dist, `set -eu
cd /dist
echo "DEB $(dpkg-deb -f stackstead_*.deb Version)"
echo "RPM $(rpm -qp --qf '%{VERSION}-%{RELEASE}' stackstead-*.rpm)"
dpkg --compare-versions "$(dpkg-deb -f stackstead_*.deb Version)" lt 0.0.1-1 && echo "SORTS-BEFORE-FINAL"`)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"DEB 0.0.1~rc1-1", "RPM 0.0.1~rc1-1", "SORTS-BEFORE-FINAL"} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q in\n%s", want, out)
		}
	}
}

func TestPackagesArchOverride(t *testing.T) {
	d := buildPackages(t, lintTestVersion, "linux/arm64", "arm64")
	ents, err := os.ReadDir(d.dist)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range ents {
		got = append(got, e.Name())
	}
	want := "stackstead-0.0.1-1.aarch64.rpm stackstead_0.0.1_arm64.deb"
	if strings.Join(got, " ") != want {
		t.Fatalf("PKG_ARCHES=arm64 produced %v, want only %q", got, want)
	}
}

// tarMember returns the contents of the member `name` of a (possibly gzipped) tar stream.
func tarMember(t *testing.T, r io.Reader, name string) []byte {
	t.Helper()
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			t.Fatalf("member %s not found", name)
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimPrefix(h.Name, "./") == name {
			b, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
			return b
		}
	}
}

// debMember reads `member` (an ar entry, e.g. data.tar.gz) out of a .deb.
func debMember(t *testing.T, path, member string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(b, []byte("!<arch>\n")) {
		t.Fatalf("%s is not an ar archive", path)
	}
	b = b[8:]
	for len(b) >= 60 {
		name := strings.TrimSpace(string(b[:16]))
		size, err := strconv.Atoi(strings.TrimSpace(string(b[48:58])))
		if err != nil {
			t.Fatal(err)
		}
		body := b[60 : 60+size]
		if name == member {
			return body
		}
		if size%2 == 1 {
			size++
		}
		if 60+size > len(b) {
			break
		}
		b = b[60+size:]
	}
	t.Fatalf("%s has no %s", path, member)
	return nil
}

// The tarball and the packages must ship the same bytes: both consume `make binaries`.
func TestTarballAndDebShipIdenticalBinary(t *testing.T) {
	d := buildPackages(t, lintTestVersion, linuxTargets, "amd64 arm64")
	cmd := exec.Command("go", "run", "./tools/mkrelease", "-version", lintTestVersion,
		"-dist", d.dist, "-bin", d.bin, "-targets", strings.ReplaceAll(linuxTargets, " ", ","))
	cmd.Dir = lintRepoRoot(t)
	cmd.Env = append(os.Environ(), "SOURCE_DATE_EPOCH=1700000000")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mkrelease failed: %v\n%s", err, out)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		tgz, err := os.Open(filepath.Join(d.dist, fmt.Sprintf("stackstead_0.0.1_linux_%s.tar.gz", arch)))
		if err != nil {
			t.Fatal(err)
		}
		gz, err := gzip.NewReader(tgz)
		if err != nil {
			t.Fatal(err)
		}
		fromTar := tarMember(t, gz, "stackstead")
		_ = tgz.Close()

		data := debMember(t, filepath.Join(d.dist, fmt.Sprintf("stackstead_0.0.1_%s.deb", arch)), "data.tar.gz")
		dgz, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		fromDeb := tarMember(t, dgz, "usr/bin/stackstead")
		if len(fromTar) == 0 || !bytes.Equal(fromTar, fromDeb) {
			t.Errorf("%s: tarball binary (%d bytes) and deb binary (%d bytes) differ", arch, len(fromTar), len(fromDeb))
		}
	}
}

func TestPackagesAreCoveredBySums(t *testing.T) {
	d := buildPackages(t, lintTestVersion, linuxTargets, "amd64 arm64")
	// `make release` also needs install.sh and the UI; the checksum step is mkrelease,
	// so run it directly over the same dist/ and bin/.
	cmd := exec.Command("go", "run", "./tools/mkrelease", "-version", lintTestVersion,
		"-dist", d.dist, "-bin", d.bin, "-targets", strings.ReplaceAll(linuxTargets, " ", ","))
	cmd.Dir = lintRepoRoot(t)
	cmd.Env = append(os.Environ(), "SOURCE_DATE_EPOCH=1700000000")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mkrelease failed: %v\n%s", err, out)
	}
	sums, err := os.ReadFile(filepath.Join(d.dist, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range expectedPackages() {
		if !strings.Contains(string(sums), "  "+name+"\n") {
			t.Errorf("SHA256SUMS does not list %s\n%s", name, sums)
		}
	}
}

// `make dist` must build the binaries once, then packages, then release (which
// computes the checksums over everything). A dry run shows the order.
func TestDistOrder(t *testing.T) {
	d := newLintDirs(t)
	out, err := lintMake(t, d.args("-n", "dist", "SKIP_UI=1", "VERSION="+lintTestVersion, "RELEASE_TARGETS="+linuxTargets)...)
	if err != nil {
		t.Fatalf("make -n dist: %v\n%s", err, out)
	}
	idx := func(s string) int {
		i := strings.Index(out, s)
		if i < 0 {
			t.Fatalf("dry run lacks %q\n%s", s, out)
		}
		return i
	}
	build, pkg, rel := idx("go build"), idx("nfpm/v2/cmd/nfpm@"), idx("-targets linux/amd64,linux/arm64")
	if !(build < pkg && pkg < rel) {
		t.Errorf("want binaries (%d) < packages (%d) < release (%d)\n%s", build, pkg, rel, out)
	}
	if n := strings.Count(out, "go build"); n != 1 {
		t.Errorf("binaries must be built once (one build loop), found %d go build recipes\n%s", n, out)
	}
}

func TestPackageLintClean(t *testing.T) {
	lintRequireDocker(t)
	d := buildPackages(t, lintTestVersion, linuxTargets, "amd64 arm64")
	out, err := lintMake(t, d.args("package-lint")...)
	if err != nil {
		t.Fatalf("make package-lint failed: %v\n%s", err, out)
	}
	t.Logf("%s", out)
}

func TestPackageLintNeedsPackages(t *testing.T) {
	d := newLintDirs(t)
	if err := os.MkdirAll(d.dist, 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := lintMake(t, d.args("package-lint")...)
	if err == nil || !strings.Contains(out, "no packages") {
		t.Fatalf("package-lint must fail early with an empty dist: %v\n%s", err, out)
	}
	if strings.Contains(out, "docker build") {
		t.Errorf("must not build the image before checking for packages\n%s", out)
	}
}

// rpmlint must actually see the payload: with absolute cpio names it extracted nothing and
// reported no-binary. Also nothing may be written into the container's real /usr/bin.
func TestRpmlintInspectsPayload(t *testing.T) {
	lintRequireDocker(t)
	d := buildPackages(t, lintTestVersion, "linux/amd64", "amd64")
	out, err := lintContainer(t, d.dist, `set -u
test ! -e /usr/bin/stackstead || { echo "PRECONDITION: /usr/bin/stackstead exists in the image"; exit 3; }
rpmlint /dist/*.rpm; echo "EXIT $?"
test ! -e /usr/bin/stackstead && echo "NOTHING-LANDED-IN-USR-BIN"`)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out, "no-binary") {
		t.Errorf("rpmlint still reports no-binary (payload not extracted)\n%s", out)
	}
	// a positive marker: an rpmlint crash or an empty run must not pass
	if !strings.Contains(out, "statically-linked-binary /usr/bin/stackstead") {
		t.Errorf("rpmlint did not inspect the binary in the payload\n%s", out)
	}
	if !strings.Contains(out, "NOTHING-LANDED-IN-USR-BIN") {
		t.Errorf("rpmlint extraction wrote into the container's /usr/bin\n%s", out)
	}
}

// rpmlint exits 0 on warnings unless --strict is given, so a warning would slip through.
func TestPackageLintRunsRpmlintStrict(t *testing.T) {
	d := newLintDirs(t)
	if err := os.MkdirAll(d.dist, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.dist, "x.rpm"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := lintMake(t, d.args("-n", "package-lint")...)
	if err != nil {
		t.Fatalf("make -n package-lint: %v\n%s", err, out)
	}
	if !strings.Contains(out, "rpmlint --strict") {
		t.Errorf("package-lint must run rpmlint --strict\n%s", out)
	}
}
