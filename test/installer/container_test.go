//go:build installer

package installer_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Container tests of install.sh: the script runs as root inside a fresh Linux
// container against a fake release served over a private Docker network, and
// every assertion is a second `docker exec` inside the same container.

var linuxDistros = []string{"debian", "ubuntu", "rocky"}

func expectCode(t *testing.T, what string, got, want int, stdout, stderr string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: exit code %d, want %d\nstdout:\n%s\nstderr:\n%s", what, got, want, stdout, stderr)
	}
}

func expectContains(t *testing.T, what, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("%s: missing %q in:\n%s", what, needle, haystack)
	}
}

func expectNotContains(t *testing.T, what, haystack, needle string) {
	t.Helper()
	if strings.Contains(haystack, needle) {
		t.Fatalf("%s: unexpected %q in:\n%s", what, needle, haystack)
	}
}

func expectEqual(t *testing.T, what, got, want string) {
	t.Helper()
	if strings.TrimSpace(got) != want {
		t.Fatalf("%s: got %q, want %q", what, strings.TrimSpace(got), want)
	}
}

func TestFreshInstallLayoutInContainer(t *testing.T) {
	for _, distro := range linuxDistros {
		t.Run(distro, func(t *testing.T) {
			t.Parallel()
			s := newScenario(t, distro, scenarioOpts{})
			// no --version: the script resolves "latest" through the release server
			stdout, stderr, code := s.install()
			expectCode(t, "install", code, 0, stdout, stderr)

			expectEqual(t, "version", s.shOK("/usr/local/bin/stackstead version --short"), "v1.0.0")
			passwd := strings.Split(strings.TrimSpace(s.shOK("getent passwd stackstead")), ":")
			if len(passwd) != 7 || passwd[5] != "/var/lib/stackstead" || !strings.HasSuffix(passwd[6], "nologin") {
				t.Fatalf("passwd entry %v: want home /var/lib/stackstead and a nologin shell", passwd)
			}
			s.shOK("getent group docker")
			expectContains(t, "groups of stackstead", " "+strings.Join(strings.Fields(s.shOK("id -nG stackstead")), " ")+" ", " docker ")
			for path, want := range map[string]string{
				"/opt/stacks":                 "2775 stackstead:docker",
				"/var/lib/stackstead":         "700 stackstead:",
				"/etc/stackstead/config.yaml": "640 root:stackstead",
				"/etc/stackstead":             "755 root:",
				"/usr/local/bin/stackstead":   "755 root:root",
			} {
				got := strings.TrimSpace(s.shOK("stat -c '%a %U:%G' " + path))
				if !strings.HasPrefix(got, want) {
					t.Fatalf("stat %s: got %q, want %q (group not checked when empty)", path, got, want)
				}
			}
			unit := s.shOK("cat /etc/systemd/system/stackstead.service")
			expectContains(t, "unit", unit, "ExecStart=/usr/local/bin/stackstead serve --config /etc/stackstead/config.yaml")
			expectContains(t, "unit", unit, "ExecStartPre=/usr/local/bin/stackstead config validate --config /etc/stackstead/config.yaml")
			s.shOK("test -L /etc/systemd/system/multi-user.target.wants/stackstead.service")
			s.shOK("test -x /usr/local/share/stackstead/install.sh")
			out := stdout + stderr
			expectContains(t, "output", out, "nothing was started")
			expectContains(t, "output", out, "Docker is not installed")
		})
	}
}

func TestAlpineNoSystemdInContainer(t *testing.T) {
	t.Parallel()
	s := newScenario(t, "alpine", scenarioOpts{})
	stdout, stderr, code := s.install("--version", "v1.0.0")
	expectCode(t, "install", code, 0, stdout, stderr)
	expectEqual(t, "version", s.shOK("/usr/local/bin/stackstead version --short"), "v1.0.0")
	passwd := strings.Split(strings.TrimSpace(s.shOK("grep '^stackstead:' /etc/passwd")), ":")
	if len(passwd) != 7 || passwd[5] != "/var/lib/stackstead" || !strings.HasSuffix(passwd[6], "nologin") {
		t.Fatalf("passwd entry %v: want home /var/lib/stackstead and a nologin shell", passwd)
	}
	expectContains(t, "groups of stackstead", " "+strings.Join(strings.Fields(s.shOK("id -Gn stackstead")), " ")+" ", " docker ")
	expectEqual(t, "stacks dir", s.shOK("stat -c '%a %U:%G' /opt/stacks"), "2775 stackstead:docker")
	expectEqual(t, "config", s.shOK("stat -c '%a %U:%G' /etc/stackstead/config.yaml"), "640 root:stackstead")
	out := stdout + stderr
	expectContains(t, "output", out, "no systemd")
	expectContains(t, "output", out, "/usr/local/bin/stackstead serve --config /etc/stackstead/config.yaml")
}

// The plan's T6 spec says no unit is installed where there is no systemd; the
// script writes it unconditionally.
func TestAlpineWritesNoUnitInContainer(t *testing.T) {
	t.Parallel()
	s := newScenario(t, "alpine", scenarioOpts{})
	stdout, stderr, code := s.install("--version", "v1.0.0")
	expectCode(t, "install", code, 0, stdout, stderr)
	s.shOK("test ! -e /etc/systemd/system/stackstead.service")
}

func TestUpgradeInContainer(t *testing.T) {
	t.Parallel()
	s := newScenario(t, "debian", scenarioOpts{Versions: []string{"1.0.0", "2.0.0"}})
	stdout, stderr, code := s.install("--version", "v1.0.0")
	expectCode(t, "install v1", code, 0, stdout, stderr)
	s.shOK("echo marker > /var/lib/stackstead/marker && echo '# edited by admin' >> /etc/stackstead/config.yaml && echo '# tampered' >> /etc/systemd/system/stackstead.service && echo old > /usr/local/share/stackstead/install.sh")

	stdout, stderr, code = s.install("--version", "v2.0.0")
	expectCode(t, "install v2", code, 0, stdout, stderr)
	expectEqual(t, "version", s.shOK("/usr/local/bin/stackstead version --short"), "v2.0.0")
	expectEqual(t, "data marker", s.shOK("cat /var/lib/stackstead/marker"), "marker")
	expectContains(t, "config", s.shOK("cat /etc/stackstead/config.yaml"), "# edited by admin")
	unit := s.shOK("cat /etc/systemd/system/stackstead.service")
	expectNotContains(t, "unit", unit, "# tampered")
	expectContains(t, "unit", unit, "ExecStart=/usr/local/bin/stackstead serve --config /etc/stackstead/config.yaml")
	s.shOK("cmp /src/install.sh /usr/local/share/stackstead/install.sh")
}

func TestSameVersionIsNoopInContainer(t *testing.T) {
	t.Parallel()
	s := newScenario(t, "ubuntu", scenarioOpts{})
	stdout, stderr, code := s.install("--version", "v1.0.0")
	expectCode(t, "first install", code, 0, stdout, stderr)
	s.shOK("sleep 1; touch /tmp/sentinel; sleep 1")
	stdout, stderr, code = s.install("--version", "v1.0.0")
	expectCode(t, "second install", code, 0, stdout, stderr)
	expectContains(t, "output", stdout+stderr, "already installed")
	changed := strings.TrimSpace(s.shOK("find /usr/local /etc/stackstead /var/lib/stackstead /etc/systemd/system /opt/stacks -newer /tmp/sentinel"))
	if changed != "" {
		t.Fatalf("second run changed files:\n%s", changed)
	}
}

func TestChecksumMismatchInContainer(t *testing.T) {
	t.Parallel()
	s := newScenario(t, "rocky", scenarioOpts{Tamper: func(t *testing.T, www string) { appendToTarballs(t, www) }})
	stdout, stderr, code := s.install("--version", "v1.0.0")
	expectCode(t, "install", code, 6, stdout, stderr)
	expectContains(t, "stderr", stderr, "checksum mismatch")
	s.shOK("test ! -e /usr/local/bin/stackstead")
	s.shOK("test ! -e /etc/stackstead")
}

func TestSignersPathInContainer(t *testing.T) {
	t.Run("valid signature installs", func(t *testing.T) {
		t.Parallel()
		s := newScenario(t, "debian", scenarioOpts{Release: releaseOpts{Sign: true}})
		stdout, stderr, code := s.install("--version", "v1.0.0", "--signers", "/release/allowed_signers")
		expectCode(t, "install", code, 0, stdout, stderr)
		expectContains(t, "output", stdout, "signature OK")
		s.shOK("test -x /usr/local/bin/stackstead")
	})
	t.Run("tampered signature is refused", func(t *testing.T) {
		t.Parallel()
		s := newScenario(t, "debian", scenarioOpts{
			Release: releaseOpts{Sign: true},
			Tamper:  func(t *testing.T, www string) { corruptSignatures(t, www) },
		})
		stdout, stderr, code := s.install("--version", "v1.0.0", "--signers", "/release/allowed_signers")
		expectCode(t, "install", code, 6, stdout, stderr)
		s.shOK("test ! -e /usr/local/bin/stackstead")
	})
	t.Run("unsigned release installs without signers", func(t *testing.T) {
		t.Parallel()
		s := newScenario(t, "debian", scenarioOpts{})
		stdout, stderr, code := s.install("--version", "v1.0.0")
		expectCode(t, "install", code, 0, stdout, stderr)
	})
}

func TestUninstallAndPurgeInContainer(t *testing.T) {
	t.Parallel()
	s := newScenario(t, "debian", scenarioOpts{})
	stdout, stderr, code := s.install("--version", "v1.0.0")
	expectCode(t, "install", code, 0, stdout, stderr)
	s.shOK("mkdir -p /opt/stacks/keep && echo services: > /opt/stacks/keep/compose.yaml && echo db > /var/lib/stackstead/stackstead.db")

	stdout, stderr, code = s.install("--uninstall")
	expectCode(t, "uninstall", code, 0, stdout, stderr)
	s.shOK("test ! -e /usr/local/bin/stackstead && test ! -e /etc/systemd/system/stackstead.service && test ! -e /usr/local/share/stackstead && test ! -L /etc/systemd/system/multi-user.target.wants/stackstead.service")
	s.shOK("test -f /etc/stackstead/config.yaml && test -f /var/lib/stackstead/stackstead.db && getent passwd stackstead")
	s.shOK("test -f /opt/stacks/keep/compose.yaml")

	stdout, stderr, code = s.install("--uninstall", "--purge")
	expectCode(t, "purge", code, 0, stdout, stderr)
	s.shOK("test ! -e /etc/stackstead && test ! -e /var/lib/stackstead && ! getent passwd stackstead")
	s.shOK("test -f /opt/stacks/keep/compose.yaml")
}

func TestUninstallStopsServiceInContainer(t *testing.T) {
	t.Parallel()
	s := newScenario(t, "debian", scenarioOpts{FakeSystemd: true})
	stdout, stderr, code := s.install("--version", "v1.0.0")
	expectCode(t, "install", code, 0, stdout, stderr)
	s.shOK("curl -fsS http://127.0.0.1:8420/api/health && : > /var/log/fake-systemctl.log")

	stdout, stderr, code = s.install("--uninstall")
	expectCode(t, "uninstall", code, 0, stdout, stderr)
	calls := s.shOK("cat /var/log/fake-systemctl.log")
	disable := strings.Index(calls, "disable --now stackstead")
	reload := strings.LastIndex(calls, "daemon-reload")
	if disable < 0 || reload < 0 || disable > reload {
		t.Fatalf("want disable --now stackstead then daemon-reload, got:\n%s", calls)
	}
	s.shOK("test ! -e /run/fake-stackstead.pid && test ! -L /etc/systemd/system/multi-user.target.wants/stackstead.service")
	if _, c := s.sh("curl -fsS -m 2 http://127.0.0.1:8420/api/health"); c == 0 {
		t.Fatal("the service still answers after uninstall")
	}
}

func TestDryRunInContainer(t *testing.T) {
	t.Parallel()
	s := newScenario(t, "ubuntu", scenarioOpts{})
	stdout, stderr, code := s.install("--version", "v1.0.0", "--dry-run")
	expectCode(t, "dry run", code, 0, stdout, stderr)
	expectContains(t, "output", stdout, "Plan")
	s.shOK("test ! -e /usr/local/bin/stackstead && test ! -e /etc/stackstead && test ! -e /opt/stacks && ! getent passwd stackstead")
}

func TestRefusesOverPackageInContainer(t *testing.T) {
	t.Run("dpkg", func(t *testing.T) {
		t.Parallel()
		s := newScenario(t, "debian", scenarioOpts{})
		// a real (empty-ish) package that owns /usr/bin/stackstead
		s.shOK("mkdir -p /tmp/pkg/DEBIAN /tmp/pkg/usr/bin && printf 'Package: stackstead\\nVersion: 0.0.1\\nArchitecture: all\\nMaintainer: t <t@example.com>\\nDescription: test\\n' > /tmp/pkg/DEBIAN/control && echo '#!/bin/sh' > /tmp/pkg/usr/bin/stackstead && chmod 755 /tmp/pkg/usr/bin/stackstead && dpkg-deb --build /tmp/pkg /tmp/stackstead.deb >/dev/null && dpkg -i /tmp/stackstead.deb >/dev/null && dpkg -S /usr/bin/stackstead")
		stdout, stderr, code := s.install("--version", "v1.0.0")
		expectCode(t, "install", code, 8, stdout, stderr)
		expectContains(t, "stderr", stderr, "dpkg")
		s.shOK("test ! -e /usr/local/bin/stackstead")
	})
	t.Run("rpm", func(t *testing.T) {
		t.Parallel()
		s := newScenario(t, "rocky", scenarioOpts{})
		// a stand-in rpm that owns /usr/bin/stackstead: building a real rpm database entry is T8's job
		s.shOK("printf '#!/bin/sh\\ncase \"$*\" in *-qf*/usr/bin/stackstead*) echo stackstead-1.0.0; exit 0;; esac\\nexit 1\\n' > /usr/local/bin/rpm && chmod 755 /usr/local/bin/rpm")
		stdout, stderr, code := s.install("--version", "v1.0.0")
		expectCode(t, "install", code, 8, stdout, stderr)
		expectContains(t, "stderr", stderr, "rpm")
		s.shOK("test ! -e /usr/local/bin/stackstead")
	})
}

func TestDashAndBusyboxParseInContainer(t *testing.T) {
	t.Run("dash", func(t *testing.T) {
		t.Parallel()
		s := newScenario(t, "debian", scenarioOpts{})
		s.shOK("dash -n /src/install.sh")
		out := s.shOK("dash /src/install.sh --help")
		expectContains(t, "help", out, "Exit codes:")
	})
	t.Run("busybox ash", func(t *testing.T) {
		t.Parallel()
		s := newScenario(t, "alpine", scenarioOpts{})
		s.shOK("busybox sh -n /src/install.sh")
		out := s.shOK("busybox sh /src/install.sh --help")
		expectContains(t, "help", out, "Exit codes:")
	})
	t.Run("checkbashisms", func(t *testing.T) {
		t.Parallel()
		s := newScenario(t, "debian", scenarioOpts{})
		out, code := s.sh("checkbashisms /src/install.sh 2>&1; echo rc=$?")
		if code != 0 || strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(out), "rc=0")) != "" {
			t.Fatalf("checkbashisms reported something (code %d):\n%s", code, out)
		}
	})
}

// ---- real-mode paths the host tests (--root) cannot reach ----

func TestServiceStartAndHealthSummaryInContainer(t *testing.T) {
	t.Parallel()
	s := newScenario(t, "debian", scenarioOpts{FakeSystemd: true})
	stdout, stderr, code := s.install("--version", "v1.0.0")
	expectCode(t, "install", code, 0, stdout, stderr)
	expectContains(t, "stdout", stdout, "Stackstead is running")
	expectContains(t, "stdout", stdout, "fake health summary")
	expectContains(t, "stdout", stdout, "stackstead health exit code: 7")
	expectContains(t, "stdout", stdout, "journalctl -u stackstead | grep setup_token")
	expectNotContains(t, "output", stdout+stderr, "setup_token=")

	calls := s.shOK("cat /var/log/fake-systemctl.log")
	reload := strings.Index(calls, "daemon-reload")
	enable := strings.Index(calls, "enable --now stackstead")
	if reload < 0 || enable < 0 || reload > enable {
		t.Fatalf("systemctl calls must be daemon-reload then enable --now stackstead, got:\n%s", calls)
	}
	expectEqual(t, "health", s.shOK("curl -fsS http://127.0.0.1:8420/api/health"), `{"ok":true}`)
	// the summary runs as the service user, never as root
	uid := strings.TrimSpace(s.shOK("id -u stackstead"))
	expectContains(t, "health call", s.shOK("cat /var/log/fake-stackstead-calls.log"), "uid="+uid+" health")
	expectContains(t, "service process", s.shOK("ps -eo uid=,args="), uid+" /usr/local/bin/stackstead serve --config /etc/stackstead/config.yaml")
}

func TestReadyFallsBackToSetupStatusInContainer(t *testing.T) {
	t.Parallel()
	s := newScenario(t, "debian", scenarioOpts{FakeSystemd: true, Mode: "setup-only"})
	stdout, stderr, code := s.install("--version", "v1.0.0")
	expectCode(t, "install", code, 0, stdout, stderr)
	expectContains(t, "stdout", stdout, "Stackstead is running")
	out, _ := s.sh("curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8420/api/health")
	expectEqual(t, "health status of a binary without /api/health", out, "404")
}

func TestReadyTimeoutExit7InContainer(t *testing.T) {
	t.Parallel()
	s := newScenario(t, "debian", scenarioOpts{FakeSystemd: true, Mode: "never"})
	start := time.Now()
	stdout, stderr, code := s.install("--version", "v1.0.0")
	took := time.Since(start)
	expectCode(t, "install", code, 7, stdout, stderr)
	expectContains(t, "stderr", stderr, "did not answer")
	expectContains(t, "output", stdout+stderr, "journalctl -u stackstead")
	if took < 14*time.Second || took > 60*time.Second {
		t.Fatalf("readiness wait took %v, want about 15 s", took)
	}
	// a failed readiness probe leaves the installation in place for diagnosis
	s.shOK("test -x /usr/local/bin/stackstead")
}

func TestUpgradeRestartsRunningServiceInContainer(t *testing.T) {
	t.Parallel()
	s := newScenario(t, "debian", scenarioOpts{FakeSystemd: true, Versions: []string{"1.0.0", "2.0.0"}})
	stdout, stderr, code := s.install("--version", "v1.0.0")
	expectCode(t, "install v1", code, 0, stdout, stderr)
	oldPID := strings.TrimSpace(s.shOK("cat /run/fake-stackstead.pid"))
	s.shOK(": > /var/log/fake-systemctl.log")

	stdout, stderr, code = s.install("--version", "v2.0.0")
	expectCode(t, "install v2", code, 0, stdout, stderr)
	calls := s.shOK("cat /var/log/fake-systemctl.log")
	expectContains(t, "systemctl calls", calls, "restart stackstead")
	expectNotContains(t, "systemctl calls", calls, "enable")
	newPID := strings.TrimSpace(s.shOK("cat /run/fake-stackstead.pid"))
	if newPID == oldPID {
		t.Fatalf("service was not restarted (pid %s)", newPID)
	}
	expectContains(t, "stdout", stdout, "Stackstead is running")
}

func TestUpgradeDoesNotStartStoppedServiceInContainer(t *testing.T) {
	t.Parallel()
	s := newScenario(t, "debian", scenarioOpts{FakeSystemd: true, Versions: []string{"1.0.0", "2.0.0"}})
	stdout, stderr, code := s.install("--version", "v1.0.0")
	expectCode(t, "install v1", code, 0, stdout, stderr)
	// the admin stopped and disabled the service
	s.shOK("systemctl disable --now stackstead; : > /var/log/fake-systemctl.log")

	stdout, stderr, code = s.install("--version", "v2.0.0")
	expectCode(t, "install v2", code, 0, stdout, stderr)
	calls := s.shOK("cat /var/log/fake-systemctl.log")
	expectNotContains(t, "systemctl calls", calls, "restart")
	expectNotContains(t, "systemctl calls", calls, "enable")
	s.shOK("test ! -L /etc/systemd/system/multi-user.target.wants/stackstead.service")
	expectNotContains(t, "stdout", stdout, "Stackstead is running")
}

func TestNoStartInContainer(t *testing.T) {
	t.Parallel()
	s := newScenario(t, "debian", scenarioOpts{FakeSystemd: true})
	stdout, stderr, code := s.install("--version", "v1.0.0", "--no-start")
	expectCode(t, "install", code, 0, stdout, stderr)
	calls := s.shOK("cat /var/log/fake-systemctl.log")
	expectContains(t, "systemctl calls", calls, "daemon-reload")
	expectContains(t, "systemctl calls", calls, "enable stackstead")
	expectNotContains(t, "systemctl calls", calls, "--now")
	s.shOK("test ! -e /run/fake-stackstead.pid")
	s.shOK("test -L /etc/systemd/system/multi-user.target.wants/stackstead.service")
}

func TestValidateConfigRunsInContainer(t *testing.T) {
	t.Parallel()
	s := newScenario(t, "debian", scenarioOpts{})
	stdout, stderr, code := s.install("--version", "v1.0.0")
	expectCode(t, "install", code, 0, stdout, stderr)
	// a fresh config is validated with the installed binary, and a valid one is silent
	expectContains(t, "binary calls", s.shOK("cat /var/log/fake-stackstead-calls.log"), "config validate --config /etc/stackstead/config.yaml")
	expectNotContains(t, "output", stdout+stderr, "config validate did not pass")
}

func TestValidateConfigWarnsInContainer(t *testing.T) {
	t.Parallel()
	s := newScenario(t, "debian", scenarioOpts{Mode: "validate-fail"})
	stdout, stderr, code := s.install("--version", "v1.0.0")
	expectCode(t, "install", code, 0, stdout, stderr)
	expectContains(t, "stderr", stderr, "config validate did not pass")
	expectContains(t, "binary calls", s.shOK("cat /var/log/fake-stackstead-calls.log"), "config validate --config /etc/stackstead/config.yaml")
}

func TestDockerCheckInContainer(t *testing.T) {
	t.Run("compose plugin and socket missing", func(t *testing.T) {
		t.Parallel()
		s := newScenario(t, "debian", scenarioOpts{})
		s.shOK("install -m 755 /release/helpers/docker /usr/local/bin/docker && touch /etc/fake-docker-no-compose")
		stdout, stderr, code := s.install("--version", "v1.0.0")
		expectCode(t, "install", code, 0, stdout, stderr)
		expectContains(t, "stderr", stderr, "docker compose plugin is missing")
		expectContains(t, "stderr", stderr, "Docker socket /var/run/docker.sock not found")
		expectNotContains(t, "stderr", stderr, "Docker is not installed")
	})
	t.Run("docker present is silent", func(t *testing.T) {
		t.Parallel()
		s := newScenario(t, "debian", scenarioOpts{})
		s.shOK("install -m 755 /release/helpers/docker /usr/local/bin/docker")
		s.shOK("(setsid /release/helpers/fakehelper listen-unix /var/run/docker.sock </dev/null >/dev/null 2>&1 &) ; for i in 1 2 3 4 5 6 7 8 9 10; do test -S /var/run/docker.sock && exit 0; sleep 0.5; done; exit 1")
		stdout, stderr, code := s.install("--version", "v1.0.0")
		expectCode(t, "install", code, 0, stdout, stderr)
		expectNotContains(t, "stderr", stderr, "Docker")
		expectNotContains(t, "stderr", stderr, "docker compose")
	})
	t.Run("socket from the config", func(t *testing.T) {
		t.Parallel()
		s := newScenario(t, "debian", scenarioOpts{})
		s.shOK("install -m 755 /release/helpers/docker /usr/local/bin/docker && mkdir -p /etc/stackstead && printf 'listen: 127.0.0.1:8420\\ndocker_socket: /run/custom-docker.sock\\n' > /etc/stackstead/config.yaml && chmod 640 /etc/stackstead/config.yaml")
		stdout, stderr, code := s.install("--version", "v1.0.0")
		expectCode(t, "install", code, 0, stdout, stderr)
		expectContains(t, "stderr", stderr, "Docker socket /run/custom-docker.sock not found")
	})
}

func TestAccountsInContainer(t *testing.T) {
	t.Run("existing docker group is reused", func(t *testing.T) {
		t.Parallel()
		s := newScenario(t, "debian", scenarioOpts{})
		s.shOK("groupadd -g 4242 docker")
		stdout, stderr, code := s.install("--version", "v1.0.0")
		expectCode(t, "install", code, 0, stdout, stderr)
		expectEqual(t, "docker gid", s.shOK("getent group docker | cut -d: -f3"), "4242")
		expectEqual(t, "stacks group", s.shOK("stat -c %g /opt/stacks"), "4242")
	})
	t.Run("existing user joins the docker group", func(t *testing.T) {
		t.Parallel()
		s := newScenario(t, "rocky", scenarioOpts{})
		s.shOK("useradd --system --user-group --home-dir /var/lib/stackstead --no-create-home --shell /sbin/nologin stackstead")
		expectNotContains(t, "groups before", " "+s.shOK("id -nG stackstead")+" ", " docker ")
		stdout, stderr, code := s.install("--version", "v1.0.0")
		expectCode(t, "install", code, 0, stdout, stderr)
		expectContains(t, "groups after", " "+strings.Join(strings.Fields(s.shOK("id -nG stackstead")), " ")+" ", " docker ")
		expectEqual(t, "uid kept", s.shOK("getent passwd stackstead | cut -d: -f1,6"), "stackstead:/var/lib/stackstead")
	})
	t.Run("existing stacks dir is not touched", func(t *testing.T) {
		t.Parallel()
		s := newScenario(t, "ubuntu", scenarioOpts{})
		s.shOK("mkdir -p /opt/stacks && chmod 755 /opt/stacks && echo mine > /opt/stacks/file")
		stdout, stderr, code := s.install("--version", "v1.0.0")
		expectCode(t, "install", code, 0, stdout, stderr)
		expectEqual(t, "stacks dir", s.shOK("stat -c '%a %U:%G' /opt/stacks"), "755 root:root")
		expectContains(t, "stderr", stderr, "/opt/stacks exists but stackstead cannot write to it")
	})
}

func TestSignalCleanupInContainer(t *testing.T) {
	t.Parallel()
	s := newScenario(t, "debian", scenarioOpts{})
	// a slow tarball download gives the test a window to interrupt the script
	s.shOK("mkdir -p /tmp/itest && printf '#!/bin/sh\\ncase \"$*\" in *.tar.gz*) sleep 60 ;; esac\\nexec /usr/bin/curl \"$@\"\\n' > /usr/local/bin/curl && chmod 755 /usr/local/bin/curl")
	out := s.shOK(`TMPDIR=/tmp/itest setsid sh /src/install.sh --base-url ` + s.base + ` --version v1.0.0 >/tmp/install.out 2>&1 &
pid=$!
for i in 1 2 3 4 5 6 7 8 9 10; do ls /tmp/itest | grep -q stackstead-install && break; sleep 0.5; done
ls /tmp/itest | grep -q stackstead-install || { echo "script never created its temp dir"; cat /tmp/install.out; exit 1; }
kill -s TERM -- -$pid
rc=0; wait $pid || rc=$?
echo "rc=$rc"
ls /tmp/itest`)
	expectContains(t, "signal run", out, "rc=130")
	expectNotContains(t, "temp dir left behind", out, "stackstead-install")
	s.shOK("test ! -e /usr/local/bin/stackstead && test ! -e /etc/stackstead")
}

// ---- docker helpers (T6 owns dockerRun, dockerBuildImage, withNetwork) ----

const (
	serverImage = "busybox:1.36"
	serverPort  = "8099"
)

var (
	imageMu    sync.Mutex
	imageBuilt = map[string]bool{}
	seq        atomic.Int64
	// releaseServers maps a private network to the release server container
	// whose network namespace test containers join, so that the base URL can be
	// http://127.0.0.1:<port> (the script only accepts plain http for loopback).
	serversMu      sync.Mutex
	releaseServers = map[string]string{}
)

func docker(args ...string) (stdout, stderr string, code int) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			return so.String(), se.String() + err.Error(), 127
		}
		code = ee.ExitCode()
	}
	return so.String(), se.String(), code
}

func requireDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not installed")
	}
	if _, _, code := docker("info", "--format", "{{.ServerVersion}}"); code != 0 {
		t.Skip("the docker daemon is not reachable")
	}
}

// dockerArch is the Go architecture of the docker server (what containers run).
func dockerArch(t *testing.T) string {
	t.Helper()
	out, errOut, code := docker("version", "--format", "{{.Server.Arch}}")
	if code != 0 {
		t.Fatalf("docker version: %s", errOut)
	}
	switch strings.TrimSpace(out) {
	case "arm64", "aarch64":
		return "arm64"
	case "amd64", "x86_64":
		return "amd64"
	}
	t.Skipf("unsupported docker server architecture %q", strings.TrimSpace(out))
	return ""
}

// registerSweep is the safety net: whatever a test leaves behind under its own
// run label is reported and removed, and nothing else is ever touched. It is
// registered first, so it runs after every other cleanup.
func registerSweep(t *testing.T, r *run) {
	t.Helper()
	label := "label=stackstead.test=" + r.ID
	t.Cleanup(func() {
		var left []string
		for i := 0; i < 20; i++ { // --rm removal is asynchronous
			out, _, _ := docker("ps", "-a", "-q", "--filter", label)
			left = strings.Fields(out)
			if len(left) == 0 {
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
		if len(left) > 0 {
			t.Errorf("containers left behind by run %s: %v", r.ID, left)
			docker(append([]string{"rm", "-f"}, left...)...)
		}
		if out, _, _ := docker("network", "ls", "-q", "--filter", label); strings.TrimSpace(out) != "" {
			t.Errorf("networks left behind by run %s: %s", r.ID, strings.Fields(out))
			docker(append([]string{"network", "rm"}, strings.Fields(out)...)...)
		}
	})
}

// dockerBuildImage builds test/installer/images/<dockerfile> once per content
// hash and returns its tag. Images are kept as a cache and labelled.
func dockerBuildImage(t *testing.T, r *run, dockerfile string) string {
	t.Helper()
	dir := filepath.Join(repoRoot(t), "test", "installer", "images")
	content := readFile(t, filepath.Join(dir, dockerfile))
	sum := sha256.Sum256(content)
	tag := "stackstead-itest/" + strings.TrimSuffix(dockerfile, ".Dockerfile") + ":" + hex.EncodeToString(sum[:])[:12]
	imageMu.Lock()
	defer imageMu.Unlock()
	if imageBuilt[tag] {
		return tag
	}
	if _, _, code := docker("image", "inspect", tag); code != 0 {
		out, errOut, code := docker("build", "--label", "stackstead.test=image", "-t", tag, "-f", filepath.Join(dir, dockerfile), dir)
		if code != 0 {
			t.Fatalf("docker build %s failed:\n%s\n%s", dockerfile, out, errOut)
		}
	}
	imageBuilt[tag] = true
	return tag
}

// withNetwork creates a private (internal: no route out) labelled network and
// registers its removal.
func withNetwork(t *testing.T, r *run) string {
	t.Helper()
	name := fmt.Sprintf("stackstead-itest-%s-net-%d", r.ID, seq.Add(1))
	t.Cleanup(func() { docker("network", "rm", name) })
	if _, errOut, code := docker("network", "create", "--internal", "--label", "stackstead.test="+r.ID, name); code != 0 {
		t.Fatalf("docker network create: %s", errOut)
	}
	return name
}

type runOpts struct {
	Mounts        []string // host:container[:ro]
	Env           []string // KEY=VALUE
	Privileged    bool
	NoInit        bool     // no docker-init: the command (systemd) must be PID 1
	ExtraArgs     []string // more `docker run` flags, before the image
	Detach        bool     // stdout is the container name
	KeepContainer bool     // no --rm
}

// dockerRun runs a labelled, uniquely named container; cleanup is registered
// before the container exists. A network with a release server is joined
// through the server's network namespace.
func dockerRun(t *testing.T, r *run, image, network string, cmd []string, opts runOpts) (stdout, stderr string, code int) {
	t.Helper()
	name := fmt.Sprintf("stackstead-itest-%s-c%d", r.ID, seq.Add(1))
	t.Cleanup(func() { docker("rm", "-f", name) })
	args := []string{"run", "--name", name, "--label", "stackstead.test=" + r.ID}
	if !opts.NoInit {
		args = append(args, "--init")
	}
	args = append(args, opts.ExtraArgs...)
	if !opts.KeepContainer {
		args = append(args, "--rm")
	}
	if opts.Detach {
		args = append(args, "-d")
	}
	if opts.Privileged {
		args = append(args, "--privileged")
	}
	serversMu.Lock()
	srv := releaseServers[network]
	serversMu.Unlock()
	if srv != "" {
		args = append(args, "--network", "container:"+srv)
	} else if network != "" {
		args = append(args, "--network", network)
	}
	for _, m := range opts.Mounts {
		args = append(args, "-v", m)
	}
	for _, e := range opts.Env {
		args = append(args, "-e", e)
	}
	args = append(args, image)
	args = append(args, cmd...)
	stdout, stderr, code = docker(args...)
	if opts.Detach && code == 0 {
		stdout = name
	}
	return stdout, stderr, code
}

func dockerExec(container string, env []string, argv ...string) (stdout, stderr string, code int) {
	args := []string{"exec"}
	for _, e := range env {
		args = append(args, "-e", e)
	}
	args = append(args, container)
	return docker(append(args, argv...)...)
}

// serveReleaseInNetwork serves the versions in dist (see fakeRelease) from a
// pinned busybox httpd with GitHub's URL layout, releases/download/v<ver>/ and
// releases/latest/download/ (the highest version), and returns the base URL as
// seen by containers started on the same network.
func serveReleaseInNetwork(t *testing.T, r *run, dist string, network string) string {
	t.Helper()
	return serveReleaseTampered(t, r, dist, network, nil)
}

func serveReleaseTampered(t *testing.T, r *run, dist, network string, tamper func(t *testing.T, www string)) string {
	t.Helper()
	www := filepath.Join(r.Dir, "www")
	dl := filepath.Join(www, "releases", "download")
	if err := os.MkdirAll(dl, 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dist)
	if err != nil {
		t.Fatal(err)
	}
	var versions []string
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "v") {
			continue
		}
		versions = append(versions, e.Name())
		if err := os.MkdirAll(filepath.Join(dl, e.Name()), 0o755); err != nil {
			t.Fatal(err)
		}
		files, _ := os.ReadDir(filepath.Join(dist, e.Name()))
		for _, f := range files {
			writeFile(t, filepath.Join(dl, e.Name(), f.Name()), readFile(t, filepath.Join(dist, e.Name(), f.Name())), 0o644)
		}
	}
	if len(versions) == 0 {
		t.Fatal("no release versions in " + dist)
	}
	sort.Strings(versions)
	latest := filepath.Join(www, "releases", "latest")
	if err := os.MkdirAll(latest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "download", versions[len(versions)-1]), filepath.Join(latest, "download")); err != nil {
		t.Fatal(err)
	}
	if tamper != nil {
		tamper(t, www)
	}
	wwwMount, err := filepath.EvalSymlinks(www)
	if err != nil {
		t.Fatal(err)
	}
	out, errOut, code := dockerRun(t, r, serverImage, network, []string{"httpd", "-f", "-p", serverPort, "-h", "/www"},
		runOpts{Detach: true, Mounts: []string{wwwMount + ":/www:ro"}})
	if code != 0 {
		t.Fatalf("starting the release server: %s", errOut)
	}
	server := strings.TrimSpace(out)
	serversMu.Lock()
	releaseServers[network] = server
	serversMu.Unlock()
	t.Cleanup(func() {
		serversMu.Lock()
		delete(releaseServers, network)
		serversMu.Unlock()
	})
	base := "http://127.0.0.1:" + serverPort
	url := base + "/releases/latest/download/SHA256SUMS"
	for i := 0; ; i++ {
		if _, _, code := dockerExec(server, nil, "wget", "-q", "-O", "/dev/null", url); code == 0 {
			break
		}
		if i >= 40 {
			t.Fatal("the release server did not come up")
		}
		time.Sleep(250 * time.Millisecond)
	}
	return base
}

// ---- fake binary and helpers ----

// The fake stackstead is a small static Go program (the real CLI is Plan 6 and
// not merged here). It follows the "Contract for Plan 7" and, unlike the host
// harness fake, really serves: /api/health, /api/setup/status. Behaviour is
// switched by the file /etc/fake-stackstead-mode: "setup-only" (no
// /api/health), "never" (serve never listens), "validate-fail" (config
// validate exits 6). Every call is appended to
// /var/log/fake-stackstead-calls.log as "uid=<uid> <args>".
const fakeServerSource = `package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

var version = "v0.0.0"

func mode() string {
	b, _ := os.ReadFile("/etc/fake-stackstead-mode")
	return strings.TrimSpace(string(b))
}

func logCall() {
	f, err := os.OpenFile("/var/log/fake-stackstead-calls.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o666)
	if err == nil {
		fmt.Fprintf(f, "uid=%d %s\n", os.Getuid(), strings.Join(os.Args[1:], " "))
		f.Close()
	}
}

func listenFromConfig(path string) string {
	b, _ := os.ReadFile(path)
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "listen:"); ok {
			return strings.TrimSpace(v)
		}
	}
	return "127.0.0.1:8420"
}

func main() {
	args := os.Args[1:]
	logCall()
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: stackstead <command>")
		os.Exit(2)
	}
	switch args[0] {
	case "--help", "-h", "help":
		fmt.Println("Usage: stackstead <command>\n  serve     run the server\n  version   print the version\n  config    configuration tools\n  health    health summary")
	case "version":
		if len(args) > 1 && args[1] == "--short" {
			fmt.Println(version)
		} else if len(args) > 2 && args[1] == "-o" {
			fmt.Printf("{\"version\":%q,\"commit\":\"fake\",\"date\":\"x\",\"go\":\"go\",\"os\":\"linux\",\"arch\":\"x\",\"license\":\"AGPL-3.0\"}\n", version)
		} else {
			fmt.Printf("stackstead %s (fake x) go linux/x\nLicense: AGPL-3.0\n", version)
		}
	case "config":
		if len(args) > 1 && args[1] == "validate" {
			if mode() == "validate-fail" {
				fmt.Fprintln(os.Stderr, "config: invalid key: listen")
				os.Exit(6)
			}
			return
		}
		os.Exit(2)
	case "health":
		fmt.Println("fake health summary")
		os.Exit(7)
	case "listen-unix":
		l, err := net.Listen("unix", args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		_ = l
		select {}
	case "serve":
		cfg := "/etc/stackstead/config.yaml"
		for i, a := range args {
			if a == "--config" && i+1 < len(args) {
				cfg = args[i+1]
			}
		}
		if mode() == "never" {
			time.Sleep(time.Hour)
			return
		}
		mux := http.NewServeMux()
		if mode() != "setup-only" {
			mux.HandleFunc("/api/health", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "{\"ok\":true}") })
		}
		mux.HandleFunc("/api/setup/status", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "{\"needs_setup\":true}") })
		if err := http.ListenAndServe(listenFromConfig(cfg), mux); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: stackstead <command>")
		os.Exit(2)
	}
}
`

// fakeSystemctl stands in for a booted systemd: it records its calls and
// really starts/stops the fake service as the stackstead user, so wait_ready
// and the health summary run for real.
const fakeSystemctl = `#!/bin/sh
echo "$*" >> /var/log/fake-systemctl.log
PIDF=/run/fake-stackstead.pid
LINK=/etc/systemd/system/multi-user.target.wants/stackstead.service
alive() { [ -f "$PIDF" ] && kill -0 "$(cat "$PIDF")" 2>/dev/null; }
start() {
	alive && return 0
	setsid runuser -u stackstead -- /usr/local/bin/stackstead serve --config /etc/stackstead/config.yaml </dev/null >/var/log/fake-stackstead.log 2>&1 &
	echo $! > "$PIDF"
}
stop() {
	old=
	if alive; then old=$(cat "$PIDF"); kill -s TERM -- "-$old" 2>/dev/null || true; fi
	rm -f "$PIDF"
	i=0
	while [ -n "$old" ] && kill -0 "$old" 2>/dev/null && [ "$i" -lt 50 ]; do i=$((i + 1)); sleep 0.1; done
}
now=
verb=
for a in "$@"; do
	case $a in
	--now) now=1 ;;
	-*) ;;
	*) [ -n "$verb" ] || verb=$a ;;
	esac
done
case $verb in
enable) mkdir -p "$(dirname "$LINK")"; ln -sf /etc/systemd/system/stackstead.service "$LINK"; [ -z "$now" ] || start ;;
disable) rm -f "$LINK"; [ -z "$now" ] || stop ;;
restart) stop; start ;;
is-active) alive; exit $? ;;
esac
exit 0
`

const fakeDocker = `#!/bin/sh
case "$1 $2" in
"compose version") [ ! -e /etc/fake-docker-no-compose ] || exit 1; echo "Docker Compose version fake" ;;
esac
exit 0
`

// buildFakeServer cross-compiles the fake for linux/arch with the version baked in.
func buildFakeServer(t *testing.T, r *run, arch, version string) string {
	t.Helper()
	dir := filepath.Join(r.Dir, "fakesrc")
	writeFile(t, filepath.Join(dir, "main.go"), []byte(fakeServerSource), 0o644)
	writeFile(t, filepath.Join(dir, "go.mod"), []byte("module fakestackstead\n\ngo 1.22\n"), 0o644)
	out := filepath.Join(r.Dir, "fakebin-"+version, "stackstead")
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-X main.version=v"+version, "-o", out, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch, "GOFLAGS=")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the fake stackstead: %v\n%s", err, b)
	}
	return out
}

// ---- scenario ----

type scenarioOpts struct {
	Versions    []string // default {"1.0.0"}
	Release     releaseOpts
	FakeSystemd bool   // /run/systemd/system exists and systemctl is a recording fake that starts the fake service
	Mode        string // contents of /etc/fake-stackstead-mode
	Tamper      func(t *testing.T, www string)
	// Binary builds the stackstead for linux/arch of a version; default: the fast fake server.
	// The opt-in systemd smoke passes the real binary.
	Binary func(t *testing.T, r *run, arch, version string) string
	// Booted starts the container with systemd as PID 1 (privileged; see bootedRunOpts).
	Booted bool
}

// scenario is one container on its own private network with a release server.
type scenario struct {
	t         *testing.T
	r         *run
	base      string
	container string
	bin       string // installed stackstead for the upgrade tests; "" means /usr/local/bin/stackstead
}

func newScenario(t *testing.T, distro string, o scenarioOpts) *scenario {
	t.Helper()
	requireDocker(t)
	r := newRun(t)
	registerSweep(t, r)
	arch := dockerArch(t)
	if len(o.Versions) == 0 {
		o.Versions = []string{"1.0.0"}
	}
	image := dockerBuildImage(t, r, distro+".Dockerfile")
	var firstBin string
	var dist string
	for _, v := range o.Versions {
		build := buildFakeServer
		if o.Binary != nil {
			build = o.Binary
		}
		bin := build(t, r, arch, v)
		if firstBin == "" {
			firstBin = bin
		}
		dist = fakeRelease(t, r, v, map[string]string{"linux/" + arch: bin}, o.Release)
	}
	helpers := filepath.Join(r.Dir, "helpers")
	writeFile(t, filepath.Join(helpers, "fakehelper"), readFile(t, firstBin), 0o755)
	writeFile(t, filepath.Join(helpers, "systemctl"), []byte(fakeSystemctl), 0o755)
	writeFile(t, filepath.Join(helpers, "docker"), []byte(fakeDocker), 0o755)

	network := withNetwork(t, r)
	s := &scenario{t: t, r: r}
	s.base = serveReleaseTampered(t, r, dist, network, o.Tamper)
	rootDir, err := filepath.EvalSymlinks(r.Dir)
	if err != nil {
		t.Fatal(err)
	}
	ro := runOpts{
		Detach: true,
		Mounts: []string{rootDir + ":/release:ro", installScript(t) + ":/src/install.sh:ro"},
	}
	cmd := []string{"sleep", "infinity"}
	if o.Booted {
		ro, cmd = bootedRunOpts(ro), []string{"/lib/systemd/systemd"}
	}
	out, errOut, code := dockerRun(t, r, image, network, cmd, ro)
	if code != 0 {
		t.Fatalf("starting the test container: %s", errOut)
	}
	s.container = strings.TrimSpace(out)
	if o.Booted {
		s.waitBooted()
	}
	setup := "touch /var/log/fake-stackstead-calls.log && chmod 666 /var/log/fake-stackstead-calls.log"
	if o.Mode != "" {
		setup += " && echo " + o.Mode + " > /etc/fake-stackstead-mode"
	}
	if o.FakeSystemd {
		setup += " && mkdir -p /run/systemd/system && install -m 755 /release/helpers/systemctl /usr/local/bin/systemctl"
	}
	s.shOK(setup)
	return s
}

// install runs the script as root in the container against the release server.
func (s *scenario) install(args ...string) (stdout, stderr string, code int) {
	s.t.Helper()
	argv := append([]string{"sh", "/src/install.sh", "--base-url", s.base}, args...)
	return dockerExec(s.container, nil, argv...)
}

func (s *scenario) sh(script string) (string, int) {
	s.t.Helper()
	out, errOut, code := dockerExec(s.container, nil, "sh", "-c", script)
	return out + errOut, code
}

// shOK runs a check and fails the test unless it exits 0; it returns stdout.
func (s *scenario) shOK(script string) string {
	s.t.Helper()
	out, errOut, code := dockerExec(s.container, nil, "sh", "-c", script)
	if code != 0 {
		s.t.Fatalf("in container: %s\nexit %d\nstdout:\n%s\nstderr:\n%s", script, code, out, errOut)
	}
	return out
}

// appendToTarballs alters every release tarball after SHA256SUMS was written.
func appendToTarballs(t *testing.T, www string) {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(www, "releases", "download", "v*", "*.tar.gz"))
	if len(files) == 0 {
		t.Fatal("no tarballs to alter")
	}
	for _, f := range files {
		fh, err := os.OpenFile(f, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fh.WriteString("tampered after the checksums were written\n")
		_ = fh.Close()
	}
}

// tamperInstallSh alters the install.sh asset of every release after SHA256SUMS was written.
func tamperInstallSh(t *testing.T, www string) {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(www, "releases", "download", "v*", "install.sh"))
	if len(files) == 0 {
		t.Fatal("no install.sh assets to alter")
	}
	for _, f := range files {
		fh, err := os.OpenFile(f, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fh.WriteString("\n# tampered after the checksums were written\n")
		_ = fh.Close()
	}
}

// corruptSignatures replaces every SHA256SUMS.sig with a same-shaped but invalid one.
func corruptSignatures(t *testing.T, www string) {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(www, "releases", "download", "v*", "SHA256SUMS.sig"))
	if len(files) == 0 {
		t.Fatal("no signatures to corrupt")
	}
	for _, f := range files {
		b := readFile(t, f)
		lines := strings.Split(string(b), "\n")
		for i, l := range lines {
			if len(l) > 20 && !strings.HasPrefix(l, "-----") {
				lines[i] = strings.ToUpper(l[:10]) + strings.ToLower(l[10:])
				if lines[i] == l {
					lines[i] = "A" + l[1:]
				}
				break
			}
		}
		writeFile(t, f, []byte(strings.Join(lines, "\n")), 0o644)
	}
}
