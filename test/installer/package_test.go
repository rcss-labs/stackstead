//go:build installer

package installer_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Container tests of the deb and rpm packages: real dpkg and rpm databases, the
// real maintainer scripts and the real nfpm-built payload, around the fake
// stackstead binary of container_test.go (the real CLI is Plan 6). systemd is
// not booted; where a test needs the service manager it uses the T6 stand-in
// systemctl, and the booted-systemd checks are T9's.

const (
	pkgV1 = "0.0.1"
	pkgV2 = "0.0.2"
)

var (
	pkgOnce sync.Once
	pkgRoot string // <pkgRoot>/<version>/{stackstead_*.deb,stackstead-*.rpm}
	pkgErr  error
	pkgArch string // Go architecture of the docker server the packages are built for
)

// extraCleanups are run by TestMain after the tests (used by other files of this
// package that build once per run, such as the macOS .pkg).
var extraCleanups []func()

func TestMain(m *testing.M) {
	code := m.Run()
	for _, f := range extraCleanups {
		f()
	}
	if pkgRoot != "" {
		_ = os.RemoveAll(pkgRoot)
	}
	os.Exit(code)
}

// testPackages builds the v0.0.1 and v0.0.2 packages for the docker server
// architecture once per test run, with `make packages` around the fake binary
// (the recipe only needs bin/stackstead-linux-<arch>, so `make binaries` and
// the real server are not involved).
func testPackages(t *testing.T) string {
	t.Helper()
	pkgOnce.Do(func() {
		// cleared only when everything below completed, so a t.Fatal inside the
		// Once cannot leave later tests running on a partial package set
		pkgErr = errors.New("package build did not complete")
		r := newRun(t)
		arch := dockerArch(t)
		pkgArch = arch
		root, err := os.MkdirTemp("", "stackstead-pkgs-")
		if err != nil {
			pkgErr = err
			return
		}
		pkgRoot = root
		for _, v := range []string{pkgV1, pkgV2} {
			bin := filepath.Join(root, "bin-"+v)
			if err := os.MkdirAll(bin, 0o755); err != nil {
				pkgErr = err
				return
			}
			writeFile(t, filepath.Join(bin, "stackstead-linux-"+arch), readFile(t, buildFakeServer(t, r, arch, v)), 0o755)
			dist := filepath.Join(root, v)
			if out, err := lintMake(t, "packages", "DIST="+dist, "BIN="+bin, "VERSION=v"+v, "ALLOW_DEV_RELEASE=1", "PKG_ARCHES="+arch); err != nil {
				pkgErr = fmt.Errorf("make packages v%s: %v\n%s", v, err, out)
				return
			}
			_ = os.RemoveAll(bin)
			_ = filepath.Walk(dist, func(p string, info os.FileInfo, err error) error {
				if err == nil {
					if info.IsDir() {
						_ = os.Chmod(p, 0o755)
					} else {
						_ = os.Chmod(p, 0o644)
					}
				}
				return nil
			})
		}
		_ = os.Chmod(root, 0o755)
		pkgErr = nil
	})
	if pkgErr != nil {
		t.Fatalf("building the test packages: %v", pkgErr)
	}
	return pkgRoot
}

// pkgPath is where the package of this family and version sits inside a container.
func pkgPath(distro, version string) string {
	if distro == "rocky" {
		arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[pkgArch]
		return fmt.Sprintf("/pkgs/%s/stackstead-%s-1.%s.rpm", version, version, arch)
	}
	return fmt.Sprintf("/pkgs/%s/stackstead_%s_%s.deb", version, version, pkgArch)
}

// installPackage installs (or upgrades to) the package at path inside the
// container with the family's own tool and returns its combined output.
func installPackage(t *testing.T, r *run, container, path string) (out string, code int) {
	t.Helper()
	var script string
	switch {
	case strings.HasSuffix(path, ".deb"):
		script = "DEBIAN_FRONTEND=noninteractive apt-get install -y " + path + " 2>&1"
	case strings.HasSuffix(path, ".rpm"):
		// rpm -U installs when nothing is installed and upgrades otherwise
		script = "rpm -U " + path + " 2>&1"
	default:
		t.Fatalf("unknown package type %q", path)
	}
	stdout, stderr, code := dockerExec(container, nil, "sh", "-c", script)
	return stdout + stderr, code
}

// packageVersion is the installed package's version as dpkg or rpm report it.
func packageVersion(t *testing.T, r *run, container string) string {
	t.Helper()
	stdout, stderr, code := dockerExec(container, nil, "sh", "-c",
		`if command -v dpkg-query >/dev/null; then dpkg-query -W -f '${source:Upstream-Version}' stackstead; else rpm -q --qf '%{VERSION}' stackstead; fi`)
	if code != 0 {
		t.Fatalf("package version: exit %d\n%s%s", code, stdout, stderr)
	}
	return strings.TrimSpace(stdout)
}

// pkgScenario is a container of the distro with the test packages copied in.
// booted adds the stand-in systemctl (replacing /usr/bin/systemctl, since rpm
// scriptlets do not have /usr/local/bin in PATH) that really starts/stops the fake service.
func pkgScenario(t *testing.T, distro string, booted bool) *scenario {
	t.Helper()
	requireDocker(t)
	root := testPackages(t)
	s := newScenario(t, distro, scenarioOpts{})
	s.shOK("mkdir -p /pkgs")
	if _, errOut, code := docker("cp", root+"/.", s.container+":/pkgs"); code != 0 {
		t.Fatalf("docker cp packages: %s", errOut)
	}
	if booted {
		unit := strings.ReplaceAll(fakeSystemctl, "/usr/local/bin/stackstead", "/usr/bin/stackstead")
		writeFile(t, filepath.Join(s.r.Dir, "pkgsystemctl"), []byte(unit), 0o755)
		s.shOK("mkdir -p /run/systemd/system && install -m 755 /release/pkgsystemctl /usr/bin/systemctl")
	}
	return s
}

func (s *scenario) pkgInstall(distro, version string) string {
	s.t.Helper()
	out, code := installPackage(s.t, s.r, s.container, pkgPath(distro, version))
	if code != 0 {
		s.t.Fatalf("installing %s %s: exit %d\n%s", distro, version, code, out)
	}
	return out
}

// ---- tests ----

var debDistros = []string{"debian", "ubuntu"}

func TestDebFreshInstallInContainer(t *testing.T) {
	for _, distro := range debDistros {
		t.Run(distro, func(t *testing.T) {
			t.Parallel()
			s := pkgScenario(t, distro, true)
			out := s.pkgInstall(distro, pkgV1)
			expectEqual(t, "dpkg version", packageVersion(t, s.r, s.container), pkgV1)
			checkPackageLayout(t, s, pkgV1)
			expectContains(t, "conffiles", s.shOK("dpkg-query -W -f '${Conffiles}\\n' stackstead"), "/etc/stackstead/config.yaml")
			expectContains(t, "output", out, "http://127.0.0.1:8420")
			expectContains(t, "output", out, "journalctl -u stackstead")
			expectContains(t, "output", out, "Docker was not found")
			s.shOK("dpkg -S /usr/lib/systemd/system/stackstead.service")
			expectContains(t, "systemctl calls", s.shOK("cat /var/log/fake-systemctl.log"), "enable --now stackstead.service")
		})
	}
}

func TestRpmFreshInstallInContainer(t *testing.T) {
	t.Parallel()
	s := pkgScenario(t, "rocky", true)
	out := s.pkgInstall("rocky", pkgV1)
	expectEqual(t, "rpm version", packageVersion(t, s.r, s.container), pkgV1)
	checkPackageLayout(t, s, pkgV1)
	expectContains(t, "rpm -qc", s.shOK("rpm -qc stackstead"), "/etc/stackstead/config.yaml")
	expectEqual(t, "rpm -V", s.shOK("rpm -V stackstead; echo done"), "done")
	expectContains(t, "output", out, "http://127.0.0.1:8420")
	expectContains(t, "output", out, "Docker was not found")
	expectContains(t, "systemctl calls", s.shOK("cat /var/log/fake-systemctl.log"), "enable --now stackstead.service")
}

// Without a booted systemd (a chroot, an image build) the scripts must still
// succeed and must not pretend to have enabled anything.
func TestPackageInstallWithoutSystemdInContainer(t *testing.T) {
	for _, distro := range []string{"debian", "rocky"} {
		t.Run(distro, func(t *testing.T) {
			t.Parallel()
			s := pkgScenario(t, distro, false)
			s.pkgInstall(distro, pkgV1)
			checkPackageLayout(t, s, pkgV1)
		})
	}
}

// checkPackageLayout is the layout every package install must produce: the same
// rules as install.sh, with the packaged paths (/usr/bin, /usr/lib/systemd).
// With the stand-in systemctl present it also checks the enable policy.
func checkPackageLayout(t *testing.T, s *scenario, version string) {
	t.Helper()
	expectEqual(t, "version", s.shOK("/usr/bin/stackstead version --short"), "v"+version)
	passwd := strings.Split(strings.TrimSpace(s.shOK("getent passwd stackstead")), ":")
	if len(passwd) != 7 || passwd[5] != "/var/lib/stackstead" || !strings.HasSuffix(passwd[6], "nologin") {
		t.Fatalf("passwd entry %v: want home /var/lib/stackstead and a nologin shell", passwd)
	}
	s.shOK("getent group docker")
	expectContains(t, "groups of stackstead", " "+strings.Join(strings.Fields(s.shOK("id -nG stackstead")), " ")+" ", " docker ")
	for path, want := range map[string]string{
		"/opt/stacks":                                "2775 stackstead:docker",
		"/var/lib/stackstead":                        "700 stackstead:stackstead",
		"/etc/stackstead/config.yaml":                "640 root:stackstead",
		"/usr/bin/stackstead":                        "755 root:root",
		"/usr/lib/systemd/system/stackstead.service": "644 root:root",
	} {
		expectEqual(t, "stat "+path, s.shOK("stat -c '%a %U:%G' "+path), want)
	}
	expectEqual(t, "stat /etc/stackstead", s.shOK("stat -c '%a %U:%G' /etc/stackstead"), "755 root:root")
	s.shOK("test ! -e /usr/local/bin/stackstead")
	unit := s.shOK("cat /usr/lib/systemd/system/stackstead.service")
	expectContains(t, "unit", unit, "ExecStart=/usr/bin/stackstead serve --config /etc/stackstead/config.yaml")
	expectContains(t, "unit", unit, "UMask=0007")
	if out, code := s.sh("systemd-analyze verify /usr/lib/systemd/system/stackstead.service 2>&1"); code != 0 || strings.TrimSpace(out) != "" {
		t.Fatalf("systemd-analyze verify: exit %d\n%s", code, out)
	}
	if _, code := s.sh("test -d /run/systemd/system"); code == 0 {
		s.shOK("test -L /etc/systemd/system/multi-user.target.wants/stackstead.service")
		s.shOK("kill -0 $(cat /run/fake-stackstead.pid)")
	} else {
		s.shOK("test ! -e /etc/systemd/system/multi-user.target.wants/stackstead.service")
	}
}

// seedLocalState is what an admin and the running service leave behind: a
// modified config, a database marker and a compose file in /opt/stacks.
func seedLocalState(t *testing.T, s *scenario) (configSum string) {
	t.Helper()
	s.shOK(`echo '# local change' >> /etc/stackstead/config.yaml &&
		echo data > /var/lib/stackstead/stackstead.db && chown stackstead:stackstead /var/lib/stackstead/stackstead.db &&
		mkdir -p /opt/stacks/app && echo 'services: {}' > /opt/stacks/app/compose.yaml &&
		echo unpackaged > /etc/stackstead/local-notes.txt &&
		: > /var/log/fake-systemctl.log`)
	return strings.Fields(s.shOK("sha256sum /etc/stackstead/config.yaml"))[0]
}

func checkLocalStateKept(t *testing.T, s *scenario, configSum string) {
	t.Helper()
	expectEqual(t, "config unchanged", strings.Fields(s.shOK("sha256sum /etc/stackstead/config.yaml"))[0], configSum)
	expectEqual(t, "database marker", s.shOK("cat /var/lib/stackstead/stackstead.db"), "data")
	s.shOK("test -f /opt/stacks/app/compose.yaml")
}

// countLines counts the lines of the stand-in systemctl log equal to want.
func countLines(t *testing.T, s *scenario, want string) int {
	t.Helper()
	n := 0
	for _, l := range strings.Split(s.shOK("cat /var/log/fake-systemctl.log"), "\n") {
		if strings.TrimSpace(l) == want {
			n++
		}
	}
	return n
}

func TestDebUpgradeKeepsDataInContainer(t *testing.T) {
	for _, distro := range debDistros {
		t.Run(distro, func(t *testing.T) {
			t.Parallel()
			s := pkgScenario(t, distro, true)
			s.pkgInstall(distro, pkgV1)
			sum := seedLocalState(t, s)
			s.pkgInstall(distro, pkgV2)

			expectEqual(t, "dpkg version", packageVersion(t, s.r, s.container), pkgV2)
			expectEqual(t, "binary", s.shOK("/usr/bin/stackstead version --short"), "v"+pkgV2)
			checkLocalStateKept(t, s, sum)
			s.shOK("test -z \"$(ls /etc/stackstead/*.dpkg-* 2>/dev/null)\"")
			// the service stays enabled and is restarted once, never stopped or disabled
			s.shOK("test -L /etc/systemd/system/multi-user.target.wants/stackstead.service")
			expectEqual(t, "try-restart calls", fmt.Sprint(countLines(t, s, "try-restart stackstead.service")), "1")
			log := s.shOK("cat /var/log/fake-systemctl.log")
			for _, verb := range []string{"stop ", "disable ", "enable "} {
				expectNotContains(t, "systemctl calls during upgrade", log, verb)
			}
		})
	}
}

func TestRpmUpgradeRestartsOnceInContainer(t *testing.T) {
	t.Parallel()
	s := pkgScenario(t, "rocky", true)
	s.pkgInstall("rocky", pkgV1)
	sum := seedLocalState(t, s)
	s.pkgInstall("rocky", pkgV2)

	expectEqual(t, "rpm version", packageVersion(t, s.r, s.container), pkgV2)
	expectEqual(t, "binary", s.shOK("/usr/bin/stackstead version --short"), "v"+pkgV2)
	checkLocalStateKept(t, s, sum)
	s.shOK("test -z \"$(ls /etc/stackstead/*.rpmsave /etc/stackstead/*.rpmnew 2>/dev/null)\"")
	s.shOK("test -L /etc/systemd/system/multi-user.target.wants/stackstead.service")
	expectEqual(t, "try-restart calls", fmt.Sprint(countLines(t, s, "try-restart stackstead.service")), "1")
	log := s.shOK("cat /var/log/fake-systemctl.log")
	for _, verb := range []string{"stop ", "disable ", "enable "} {
		expectNotContains(t, "systemctl calls during upgrade", log, verb)
	}
}

// An admin who disabled the service keeps it disabled across an upgrade.
func TestUpgradeKeepsDisabledServiceInContainer(t *testing.T) {
	for _, distro := range []string{"debian", "rocky"} {
		t.Run(distro, func(t *testing.T) {
			t.Parallel()
			s := pkgScenario(t, distro, true)
			s.pkgInstall(distro, pkgV1)
			s.shOK("systemctl disable stackstead.service && : > /var/log/fake-systemctl.log")
			s.pkgInstall(distro, pkgV2)
			s.shOK("test ! -e /etc/systemd/system/multi-user.target.wants/stackstead.service")
			expectNotContains(t, "systemctl calls", s.shOK("cat /var/log/fake-systemctl.log"), "enable ")
		})
	}
}

func TestDebRemoveKeepsConfigAndDataInContainer(t *testing.T) {
	for _, distro := range debDistros {
		t.Run(distro, func(t *testing.T) {
			t.Parallel()
			s := pkgScenario(t, distro, true)
			s.pkgInstall(distro, pkgV1)
			sum := seedLocalState(t, s)
			s.shOK("DEBIAN_FRONTEND=noninteractive apt-get remove -y stackstead")

			checkLocalStateKept(t, s, sum)
			s.shOK("getent passwd stackstead")
			s.shOK("test ! -e /usr/bin/stackstead && test ! -e /usr/lib/systemd/system/stackstead.service")
			s.shOK("test ! -e /etc/systemd/system/multi-user.target.wants/stackstead.service")
			log := s.shOK("cat /var/log/fake-systemctl.log")
			expectContains(t, "systemctl calls", log, "stop stackstead.service")
			expectContains(t, "systemctl calls", log, "disable stackstead.service")
			expectEqual(t, "dpkg state", s.shOK("dpkg -l stackstead | tail -n 1 | cut -c1-2"), "rc")
		})
	}
}

func TestDebPurgeKeepsStacksInContainer(t *testing.T) {
	for _, via := range []string{"remove then purge", "purge directly"} {
		t.Run(via, func(t *testing.T) {
			t.Parallel()
			s := pkgScenario(t, "debian", true)
			s.pkgInstall("debian", pkgV1)
			seedLocalState(t, s)
			if via == "remove then purge" {
				s.shOK("DEBIAN_FRONTEND=noninteractive apt-get remove -y stackstead")
			}
			s.shOK("DEBIAN_FRONTEND=noninteractive apt-get purge -y stackstead")

			s.shOK("test ! -e /etc/stackstead && test ! -e /var/lib/stackstead")
			if out, code := s.sh("getent passwd stackstead"); code == 0 {
				t.Fatalf("user stackstead survived the purge: %s", out)
			}
			s.shOK("getent group docker")
			s.shOK("test -f /opt/stacks/app/compose.yaml")
			if out, code := s.sh("dpkg -l stackstead"); code == 0 {
				t.Fatalf("dpkg still knows stackstead after purge:\n%s", out)
			}
		})
	}
}

// rpm has no purge: erase keeps data, the user and /opt/stacks (the README
// documents the manual purge); the config follows rpm's own rule, a modified
// %config file is saved as .rpmsave and an unmodified one is removed.
func TestRpmRemoveKeepsDataInContainer(t *testing.T) {
	t.Run("modified config", func(t *testing.T) {
		t.Parallel()
		s := pkgScenario(t, "rocky", true)
		s.pkgInstall("rocky", pkgV1)
		seedLocalState(t, s)
		s.shOK("rpm -e stackstead")
		expectContains(t, "saved config", s.shOK("cat /etc/stackstead/config.yaml.rpmsave"), "# local change")
		s.shOK("test ! -e /etc/stackstead/config.yaml")
		expectEqual(t, "database marker", s.shOK("cat /var/lib/stackstead/stackstead.db"), "data")
		s.shOK("test -f /opt/stacks/app/compose.yaml")
		s.shOK("getent passwd stackstead")
		s.shOK("test ! -e /usr/bin/stackstead && test ! -e /usr/lib/systemd/system/stackstead.service")
		s.shOK("test ! -e /etc/systemd/system/multi-user.target.wants/stackstead.service")
		log := s.shOK("cat /var/log/fake-systemctl.log")
		expectContains(t, "systemctl calls", log, "stop stackstead.service")
		expectContains(t, "systemctl calls", log, "disable stackstead.service")
		if out, code := s.sh("rpm -q stackstead"); code == 0 {
			t.Fatalf("rpm still reports the package:\n%s", out)
		}
	})
	t.Run("unmodified config", func(t *testing.T) {
		t.Parallel()
		s := pkgScenario(t, "rocky", true)
		s.pkgInstall("rocky", pkgV1)
		s.shOK("echo data > /var/lib/stackstead/stackstead.db && chown stackstead:stackstead /var/lib/stackstead/stackstead.db")
		s.shOK("rpm -e stackstead")
		s.shOK("test ! -e /etc/stackstead/config.yaml && test ! -e /etc/stackstead/config.yaml.rpmsave && test ! -e /etc/stackstead")
		expectEqual(t, "database marker", s.shOK("cat /var/lib/stackstead/stackstead.db"), "data")
		s.shOK("getent passwd stackstead")
	})
}

// dpkg passes the old version to postinst configure when a removed (not purged)
// package is installed again, so a fresh-install check on $2 alone would skip the
// enable; the prerm marker is what makes the reinstall enable the service.
func TestReinstallAfterRemoveEnablesServiceInContainer(t *testing.T) {
	for _, distro := range []string{"debian", "rocky"} {
		t.Run(distro, func(t *testing.T) {
			t.Parallel()
			s := pkgScenario(t, distro, true)
			s.pkgInstall(distro, pkgV1)
			sum := seedLocalState(t, s)
			if distro == "rocky" {
				s.shOK("rpm -e stackstead")
				s.shOK("mv /etc/stackstead/config.yaml.rpmsave /tmp/config.saved")
			} else {
				s.shOK("DEBIAN_FRONTEND=noninteractive apt-get remove -y stackstead")
			}
			s.shOK("test ! -e /etc/systemd/system/multi-user.target.wants/stackstead.service")
			s.pkgInstall(distro, pkgV1)
			s.shOK("test -L /etc/systemd/system/multi-user.target.wants/stackstead.service")
			expectEqual(t, "enable calls", fmt.Sprint(countLines(t, s, "enable --now stackstead.service")), "1")
			s.shOK("test ! -e /var/lib/stackstead/.disabled-by-remove")
			s.shOK("kill -0 $(cat /run/fake-stackstead.pid)")
			if distro == "debian" {
				checkLocalStateKept(t, s, sum)
			}
			checkPackageLayout(t, s, pkgV1)
		})
	}
}

func TestReinstallSameVersionIsCleanInContainer(t *testing.T) {
	for _, distro := range []string{"debian", "rocky"} {
		t.Run(distro, func(t *testing.T) {
			t.Parallel()
			s := pkgScenario(t, distro, true)
			s.pkgInstall(distro, pkgV1)
			sum := seedLocalState(t, s)
			var out string
			var code int
			if distro == "rocky" {
				out, code = installPackage(t, s.r, s.container, "--replacepkgs "+pkgPath(distro, pkgV1))
			} else {
				stdout, stderr, c := dockerExec(s.container, nil, "sh", "-c", "DEBIAN_FRONTEND=noninteractive apt-get install --reinstall -y "+pkgPath(distro, pkgV1)+" 2>&1")
				out, code = stdout+stderr, c
			}
			if code != 0 {
				t.Fatalf("reinstall: exit %d\n%s", code, out)
			}
			expectNotContains(t, "reinstall output", out, "already exists")
			expectEqual(t, "passwd entries", s.shOK("grep -c '^stackstead:' /etc/passwd"), "1")
			expectEqual(t, "group entries", s.shOK("grep -c '^stackstead:' /etc/group"), "1")
			expectEqual(t, "docker membership entries", s.shOK("id -nG stackstead | tr ' ' '\\n' | grep -cx docker"), "1")
			checkLocalStateKept(t, s, sum)
			checkPackageLayout(t, s, pkgV1)
		})
	}
}

// install.sh must refuse to overlay a binary a real package manager owns.
func TestInstallScriptRefusesOverRealPackageInContainer(t *testing.T) {
	for _, tc := range []struct{ distro, tool string }{{"debian", "dpkg"}, {"rocky", "rpm"}} {
		t.Run(tc.tool, func(t *testing.T) {
			t.Parallel()
			s := pkgScenario(t, tc.distro, false)
			s.pkgInstall(tc.distro, pkgV1)
			stdout, stderr, code := s.install("--version", "v1.0.0")
			expectCode(t, "install.sh", code, 8, stdout, stderr)
			expectContains(t, "stderr", stderr, tc.tool)
			s.shOK("test ! -e /usr/local/bin/stackstead")
			expectEqual(t, "package untouched", s.shOK("/usr/bin/stackstead version --short"), "v"+pkgV1)
		})
	}
}

// A user created before the package (an earlier manual setup) still joins the docker group.
func TestPackageAddsExistingUserToDockerGroupInContainer(t *testing.T) {
	for _, distro := range []string{"debian", "rocky"} {
		t.Run(distro, func(t *testing.T) {
			t.Parallel()
			s := pkgScenario(t, distro, false)
			s.shOK("groupadd --system stackstead && useradd --system --gid stackstead stackstead")
			expectNotContains(t, "groups before", " "+strings.Join(strings.Fields(s.shOK("id -nG stackstead")), " ")+" ", " docker ")
			s.pkgInstall(distro, pkgV1)
			expectContains(t, "groups after", " "+strings.Join(strings.Fields(s.shOK("id -nG stackstead")), " ")+" ", " docker ")
		})
	}
}
