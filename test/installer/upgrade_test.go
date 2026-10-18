//go:build installer

package installer_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Container tests of `stackstead upgrade` against the real installers (install.sh, apt-get, rpm).
// The installed v1 is the real binary, because only it has the upgrade command; the release it
// upgrades to (v2) is the fast fake, which answers `version --short`. The release comes from the
// scenario's local release server through --base-url, never from GitHub.

const (
	upV1 = "0.0.1"
	upV2 = "0.0.2"
	upV0 = "0.0.0" // older than v1, for downgrade refusals
)

// upgradeBinary is real for v1 and fake for every other version.
func upgradeBinary(t *testing.T, r *run, arch, version string) string {
	t.Helper()
	if version == upV1 {
		return buildRealServer(t, r, arch, version)
	}
	return buildFakeServer(t, r, arch, version)
}

var (
	upPkgOnce sync.Once
	upPkgRoot string
	upPkgErr  error
)

// upgradePackages builds the v1 (real) and v2 (fake) deb and rpm once per run.
func upgradePackages(t *testing.T) string {
	t.Helper()
	upPkgOnce.Do(func() {
		upPkgRoot, upPkgErr = buildPackagesWith(t, []string{upV1, upV2}, upgradeBinary)
	})
	if upPkgErr != nil {
		t.Fatalf("building the upgrade test packages: %v", upPkgErr)
	}
	return upPkgRoot
}

// upgradeScenario is a script-install container: fake systemctl, real v1, fake newer releases.
func upgradeScenario(t *testing.T, distro string, o scenarioOpts) *scenario {
	t.Helper()
	o.FakeSystemd = true
	o.Binary = upgradeBinary
	if len(o.Versions) == 0 {
		o.Versions = []string{upV1, upV2}
	}
	s := newScenario(t, distro, o)
	// upgrade runs the installer with a fixed PATH that has no /usr/local/bin: the stand-in must sit where systemctl does
	s.shOK("install -m 755 /release/helpers/systemctl /usr/bin/systemctl")
	return s
}

// upgradePkgScenario is a container with the v1 packages in /pkgs and a release server that
// also holds the v2 packages (and plain releases of the extra versions). systemctl is the
// stand-in at /usr/bin, where maintainer scripts look.
func upgradePkgScenario(t *testing.T, distro string, extra ...string) *scenario {
	t.Helper()
	return upgradePkgScenarioWith(t, distro, nil, extra...)
}

// upgradePkgScenarioWith is upgradePkgScenario where the release serves override[asset] (a local
// file) instead of the built package of that name.
func upgradePkgScenarioWith(t *testing.T, distro string, override map[string]string, extra ...string) *scenario {
	t.Helper()
	requireDocker(t)
	root := upgradePackages(t)
	assets := map[string]string{}
	for _, v := range []string{upV1, upV2} {
		files, _ := filepath.Glob(filepath.Join(root, v, "stackstead*"))
		for _, f := range files {
			assets[filepath.Base(f)] = f
		}
	}
	for name, f := range override {
		assets[name] = f
	}
	s := newScenario(t, distro, scenarioOpts{
		Versions: append([]string{upV1, upV2}, extra...),
		Binary:   upgradeBinary,
		Release:  releaseOpts{Packages: assets},
	})
	s.bin = "/usr/bin/stackstead"
	s.shOK("mkdir -p /pkgs")
	if _, errOut, code := docker("cp", filepath.Join(root, upV1)+"/.", s.container+":/pkgs/"+upV1); code != 0 {
		t.Fatalf("docker cp packages: %s", errOut)
	}
	unit := strings.ReplaceAll(fakeSystemctl, "/usr/local/bin/stackstead", "/usr/bin/stackstead")
	writeFile(t, filepath.Join(s.r.Dir, "pkgsystemctl"), []byte(unit), 0o755)
	s.shOK("mkdir -p /run/systemd/system && install -m 755 /release/pkgsystemctl /usr/bin/systemctl")
	return s
}

func (s *scenario) stackstead() string {
	if s.bin != "" {
		return s.bin
	}
	return "/usr/local/bin/stackstead"
}

func (s *scenario) upgradeArgv(args []string) []string {
	argv := append([]string{s.stackstead(), "upgrade", "--base-url", s.base}, args...)
	for _, a := range args {
		if a == "--check" {
			return argv
		}
	}
	return append(argv, "--yes")
}

// upgrade runs `stackstead upgrade --base-url <release server> [--yes]` as root.
func (s *scenario) upgrade(args ...string) (stdout, stderr string, code int) {
	s.t.Helper()
	return dockerExec(s.container, nil, s.upgradeArgv(args)...)
}

// upgradeAs is upgrade as another user (util-linux runuser is in every image).
func (s *scenario) upgradeAs(user string, args ...string) (stdout, stderr string, code int) {
	s.t.Helper()
	return dockerExec(s.container, nil, append([]string{"runuser", "-u", user, "--"}, s.upgradeArgv(args)...)...)
}

func (s *scenario) installedVersion() string {
	s.t.Helper()
	return strings.TrimSpace(s.shOK(s.stackstead() + " version --short"))
}

func (s *scenario) clearSystemctlLog() { s.shOK(": > /var/log/fake-systemctl.log") }

func (s *scenario) noUpgradeTempDir() {
	s.t.Helper()
	s.shOK("! ls -d /tmp/stackstead-upgrade.* 2>/dev/null")
}

func TestUpgradeScriptInContainer(t *testing.T) {
	t.Parallel()
	s := upgradeScenario(t, "debian", scenarioOpts{})
	stdout, stderr, code := s.install("--version", "v"+upV1)
	expectCode(t, "install v1", code, 0, stdout, stderr)
	expectEqual(t, "installed version", s.installedVersion(), "v"+upV1)
	s.shOK("echo marker > /var/lib/stackstead/marker && echo '# edited by admin' >> /etc/stackstead/config.yaml")
	s.clearSystemctlLog()

	stdout, stderr, code = s.upgrade()
	expectCode(t, "upgrade", code, 0, stdout, stderr)
	expectEqual(t, "version after upgrade", s.installedVersion(), "v"+upV2)
	calls := s.shOK("cat /var/log/fake-systemctl.log")
	expectContains(t, "systemctl calls", calls, "restart stackstead")
	expectNotContains(t, "systemctl calls", calls, "enable")
	expectEqual(t, "data marker", s.shOK("cat /var/lib/stackstead/marker"), "marker")
	expectContains(t, "config", s.shOK("cat /etc/stackstead/config.yaml"), "# edited by admin")
	s.noUpgradeTempDir()
}

func TestUpgradeDebInContainer(t *testing.T) {
	t.Parallel()
	s := upgradePkgScenario(t, "ubuntu")
	if out, code := installPackage(t, s.r, s.container, pkgPath("ubuntu", upV1)); code != 0 {
		t.Fatalf("apt-get install v1: exit %d\n%s", code, out)
	}
	expectEqual(t, "dpkg version", packageVersion(t, s.r, s.container), upV1)
	s.clearSystemctlLog()

	stdout, stderr, code := s.upgrade()
	expectCode(t, "upgrade", code, 0, stdout, stderr)
	expectEqual(t, "dpkg version after upgrade", packageVersion(t, s.r, s.container), upV2)
	expectContains(t, "systemctl calls", s.shOK("cat /var/log/fake-systemctl.log"), "try-restart")
	// apt-get itself unpacked the verified file the upgrade downloaded
	expectContains(t, "output", stdout+stderr, "Unpacking stackstead (0.0.2-1) over (0.0.1-1)")
	expectContains(t, "output", stdout+stderr, "/tmp/stackstead-upgrade.")
	s.noUpgradeTempDir()
}

func TestUpgradeRpmInContainer(t *testing.T) {
	t.Parallel()
	s := upgradePkgScenario(t, "rocky")
	if out, code := installPackage(t, s.r, s.container, pkgPath("rocky", upV1)); code != 0 {
		t.Fatalf("rpm -U v1: exit %d\n%s", code, out)
	}
	expectEqual(t, "rpm version", packageVersion(t, s.r, s.container), upV1)
	s.shOK("! command -v dnf") // the image ships microdnf only: the rpm -U fallback runs

	stdout, stderr, code := s.upgrade()
	expectCode(t, "upgrade", code, 0, stdout, stderr)
	expectEqual(t, "rpm version after upgrade", packageVersion(t, s.r, s.container), upV2)
	s.noUpgradeTempDir()
}

func TestUpgradeChecksumMismatchInContainer(t *testing.T) {
	t.Parallel()
	s := upgradeScenario(t, "debian", scenarioOpts{Tamper: tamperInstallSh})
	stdout, stderr, code := s.install("--version", "v"+upV1)
	// the tampered install.sh asset is not what the script itself runs from the tarball, so v1 installs
	expectCode(t, "install v1", code, 0, stdout, stderr)
	s.clearSystemctlLog()

	stdout, stderr, code = s.upgrade()
	expectCode(t, "upgrade", code, 6, stdout, stderr)
	expectContains(t, "stderr", stderr, "checksum")
	expectEqual(t, "version unchanged", s.installedVersion(), "v"+upV1)
	expectEqual(t, "systemctl calls", strings.TrimSpace(s.shOK("cat /var/log/fake-systemctl.log")), "")
	s.noUpgradeTempDir()
}

func TestUpgradeSameVersionIsNoopInContainer(t *testing.T) {
	t.Parallel()
	s := upgradeScenario(t, "debian", scenarioOpts{Versions: []string{upV1}})
	stdout, stderr, code := s.install("--version", "v"+upV1)
	expectCode(t, "install v1", code, 0, stdout, stderr)
	s.clearSystemctlLog()

	stdout, stderr, code = s.upgrade()
	expectCode(t, "upgrade", code, 0, stdout, stderr)
	expectContains(t, "output", stdout+stderr, "already installed")
	expectEqual(t, "systemctl calls", strings.TrimSpace(s.shOK("cat /var/log/fake-systemctl.log")), "")
	expectEqual(t, "version", s.installedVersion(), "v"+upV1)
	s.noUpgradeTempDir()
}

func TestUpgradeRefusesDowngradeInContainer(t *testing.T) {
	t.Parallel()
	s := upgradePkgScenario(t, "ubuntu", upV0)
	if out, code := installPackage(t, s.r, s.container, pkgPath("ubuntu", upV1)); code != 0 {
		t.Fatalf("apt-get install v1: exit %d\n%s", code, out)
	}

	stdout, stderr, code := s.upgrade("--version", "v"+upV0)
	expectCode(t, "downgrade", code, 5, stdout, stderr)
	expectContains(t, "stderr", stderr, "apt-get install -y --allow-downgrades")
	expectEqual(t, "dpkg version", packageVersion(t, s.r, s.container), upV1)

	// packages are never downgraded, not even with --force: the operator gets the manual command
	stdout, stderr, code = s.upgrade("--version", "v"+upV0, "--force")
	expectCode(t, "forced downgrade", code, 5, stdout, stderr)
	expectEqual(t, "dpkg version", packageVersion(t, s.r, s.container), upV1)
	expectContains(t, "output", stdout+stderr, "apt-get install -y --allow-downgrades")
	s.noUpgradeTempDir()
}

func TestUpgradeForceDowngradeScriptInContainer(t *testing.T) {
	t.Parallel()
	s := upgradeScenario(t, "debian", scenarioOpts{Versions: []string{upV0, upV1, upV2}})
	stdout, stderr, code := s.install("--version", "v"+upV1)
	expectCode(t, "install v1", code, 0, stdout, stderr)

	stdout, stderr, code = s.upgrade("--version", "v"+upV0)
	expectCode(t, "downgrade", code, 5, stdout, stderr)
	expectContains(t, "stderr", stderr, "use --force")
	expectEqual(t, "version", s.installedVersion(), "v"+upV1)

	stdout, stderr, code = s.upgrade("--version", "v"+upV0, "--force")
	expectCode(t, "forced downgrade", code, 0, stdout, stderr)
	expectEqual(t, "version after --force", s.installedVersion(), "v"+upV0)
	s.noUpgradeTempDir()
}

func TestUpgradeNonRootRefusedInContainer(t *testing.T) {
	t.Parallel()
	s := upgradeScenario(t, "debian", scenarioOpts{})
	stdout, stderr, code := s.install("--version", "v"+upV1)
	expectCode(t, "install v1", code, 0, stdout, stderr)
	s.clearSystemctlLog()

	stdout, stderr, code = s.upgradeAs("nobody")
	expectCode(t, "upgrade as nobody", code, 3, stdout, stderr)
	expectContains(t, "stderr", stderr, "sudo /usr/local/bin/stackstead upgrade")
	expectEqual(t, "version", s.installedVersion(), "v"+upV1)
	expectEqual(t, "systemctl calls", strings.TrimSpace(s.shOK("cat /var/log/fake-systemctl.log")), "")
	s.noUpgradeTempDir()
}

func TestUpgradeCheckAsNonRootInContainer(t *testing.T) {
	t.Parallel()
	s := upgradeScenario(t, "debian", scenarioOpts{})
	stdout, stderr, code := s.install("--version", "v"+upV1)
	expectCode(t, "install v1", code, 0, stdout, stderr)

	stdout, stderr, code = s.upgradeAs("nobody", "--check", "-o", "json")
	expectCode(t, "check as nobody", code, 0, stdout, stderr)
	compact := strings.ReplaceAll(stdout, " ", "")
	expectContains(t, "json", compact, `"state":"available"`)
	expectContains(t, "json", compact, `"latest":"`+upV2+`"`)

	stdout, stderr, code = s.upgradeAs("nobody", "--check")
	expectCode(t, "text check as nobody", code, 0, stdout, stderr)
	expectContains(t, "text", stdout, "update: available")
	expectEqual(t, "version", s.installedVersion(), "v"+upV1)
}

// holdLockSource takes the POSIX (fcntl) write lock dpkg and apt take on lock-frontend, which a
// flock(1) in the container would not conflict with, and holds it until killed.
const holdLockSource = `package main

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

func main() {
	f, err := os.OpenFile(os.Args[1], os.O_RDWR|os.O_CREATE, 0o640)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	l := syscall.Flock_t{Type: syscall.F_WRLCK}
	if err := syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &l); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("locked")
	time.Sleep(time.Minute)
}
`

func buildHoldLock(t *testing.T, r *run, arch string) string {
	t.Helper()
	dir := filepath.Join(r.Dir, "holdlocksrc")
	writeFile(t, filepath.Join(dir, "main.go"), []byte(holdLockSource), 0o644)
	writeFile(t, filepath.Join(dir, "go.mod"), []byte("module holdlock\n\ngo 1.22\n"), 0o644)
	out := filepath.Join(r.Dir, "helpers", "holdlock")
	cmd := exec.Command("go", "build", "-trimpath", "-o", out, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch, "GOFLAGS=")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building holdlock: %v\n%s", err, b)
	}
	return out
}

func TestUpgradeDpkgLockBusyInContainer(t *testing.T) {
	t.Parallel()
	s := upgradePkgScenario(t, "ubuntu")
	if out, code := installPackage(t, s.r, s.container, pkgPath("ubuntu", upV1)); code != 0 {
		t.Fatalf("apt-get install v1: exit %d\n%s", code, out)
	}
	buildHoldLock(t, s.r, dockerArch(t))
	s.shOK("install -m 755 /release/helpers/holdlock /usr/local/bin/holdlock")
	if _, errOut, code := docker("exec", "-d", s.container, "sh", "-c", "exec holdlock /var/lib/dpkg/lock-frontend >/tmp/holdlock.out 2>&1"); code != 0 {
		t.Fatalf("starting holdlock: %s", errOut)
	}
	s.waitFor("lock held", "grep -q locked /tmp/holdlock.out", 10*time.Second)

	start := time.Now()
	stdout, stderr, code := s.upgrade()
	expectCode(t, "upgrade with the dpkg lock held", code, 5, stdout, stderr)
	if d := time.Since(start); d > 30*time.Second {
		t.Errorf("upgrade took %s to report the busy lock", d)
	}
	expectContains(t, "stderr", stderr, "dpkg lock is held")
	expectEqual(t, "dpkg version", packageVersion(t, s.r, s.container), upV1)
}

// debWithNewConfigDefault repacks the built v2 deb (in a throwaway container of the distro, which
// has dpkg-deb) so that its packaged /etc/stackstead/config.yaml differs from v1's, and returns
// the asset name and the local file.
func debWithNewConfigDefault(t *testing.T, distro string) (name, file string) {
	t.Helper()
	requireDocker(t)
	root := upgradePackages(t)
	name = filepath.Base(pkgPath(distro, upV2))
	r := newRun(t)
	registerSweep(t, r)
	out := filepath.Join(r.Dir, "repack")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(out, 0o777); err != nil {
		t.Fatal(err)
	}
	image := dockerBuildImage(t, r, distro+".Dockerfile")
	script := "set -eu; dpkg-deb -R /in/" + name + " /tmp/x; echo '# new packaged default' >> /tmp/x/etc/stackstead/config.yaml; " +
		"dpkg-deb -b /tmp/x /out/" + name + " >/dev/null"
	inDir, _ := filepath.EvalSymlinks(filepath.Join(root, upV2))
	outDir, _ := filepath.EvalSymlinks(out)
	if so, se, code := dockerRun(t, r, image, "", []string{"sh", "-c", script}, runOpts{Mounts: []string{inDir + ":/in:ro", outDir + ":/out"}}); code != 0 {
		t.Fatalf("repacking the deb: exit %d\n%s%s", code, so, se)
	}
	return name, filepath.Join(out, name)
}

func TestUpgradeDebKeepsEditedConfigInContainer(t *testing.T) {
	t.Parallel()
	name, file := debWithNewConfigDefault(t, "ubuntu")
	s := upgradePkgScenarioWith(t, "ubuntu", map[string]string{name: file})
	if out, code := installPackage(t, s.r, s.container, pkgPath("ubuntu", upV1)); code != 0 {
		t.Fatalf("apt-get install v1: exit %d\n%s", code, out)
	}
	s.shOK("echo '# edited by admin' >> /etc/stackstead/config.yaml")

	stdout, stderr, code := s.upgrade()
	expectCode(t, "upgrade over an edited conffile", code, 0, stdout, stderr)
	expectEqual(t, "dpkg version", packageVersion(t, s.r, s.container), upV2)
	expectContains(t, "config", s.shOK("cat /etc/stackstead/config.yaml"), "# edited by admin")
	expectNotContains(t, "config", s.shOK("cat /etc/stackstead/config.yaml"), "# new packaged default")
	expectContains(t, ".dpkg-dist", s.shOK("cat /etc/stackstead/config.yaml.dpkg-dist"), "# new packaged default")
}
