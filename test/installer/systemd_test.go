//go:build installer

package installer_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Booted-systemd smoke (opt-in: STACKSTEAD_SYSTEMD_TEST=1). The real stackstead binary (built
// without the web UI, which the service does not need to answer /api/health) runs as the real
// systemd unit inside a privileged container whose PID 1 is systemd, installed by install.sh and
// by the real .deb. This is the one place the hardened unit, the service user, the docker group
// and the maintainer scripts meet a live service manager; every other test uses a fake.
//
// It needs --privileged (systemd as PID 1 manages its own cgroup subtree; with cgroup v2 the
// container's default private cgroup namespace is enough, no host cgroup mount), which works on
// Docker Desktop and on Linux hosts with cgroup v2. A cgroup v1 host would need --cgroupns=host
// and a rw /sys/fs/cgroup mount; that is not supported here. Nothing is published to the host: the
// service listens on 127.0.0.1:8420 inside the container (a private network namespace shared
// only with the release server container), so a stackstead already running on the host cannot
// clash. Containers and networks follow the usual stackstead-itest-<run> naming and labels.

const (
	smokeV1 = "0.0.1"
	smokeV2 = "0.0.2"
)

func requireSystemdOptIn(t *testing.T) {
	t.Helper()
	if os.Getenv("STACKSTEAD_SYSTEMD_TEST") != "1" {
		t.Skip("set STACKSTEAD_SYSTEMD_TEST=1 to run the booted-systemd smoke (privileged containers)")
	}
	requireDocker(t)
}

// bootedRunOpts turns o into a container that can run systemd as PID 1.
func bootedRunOpts(o runOpts) runOpts {
	o.Privileged = true
	o.NoInit = true
	o.ExtraArgs = append(o.ExtraArgs, "--tmpfs", "/run", "--tmpfs", "/run/lock", "--tmpfs", "/tmp")
	return o
}

// waitBooted waits until systemd reports running (or degraded: a failed unrelated unit in a
// container does not matter here).
func (s *scenario) waitBooted() {
	s.t.Helper()
	var last string
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); time.Sleep(time.Second) {
		out, _ := s.sh("systemctl is-system-running")
		last = strings.TrimSpace(out)
		if last == "running" || last == "degraded" {
			return
		}
	}
	s.t.Fatalf("systemd did not finish booting (is-system-running: %q); is this Docker host able to run systemd containers?", last)
}

// waitFor polls a shell check for up to d and returns its stdout; it fails the test with the last output.
func (s *scenario) waitFor(what, script string, d time.Duration) string {
	s.t.Helper()
	var out string
	var code int
	for deadline := time.Now().Add(d); ; time.Sleep(500 * time.Millisecond) {
		out, code = s.sh(script)
		if code == 0 {
			return out
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("%s: not true after %s: %s\nlast output: %s", what, d, script, out)
		}
	}
}

func (s *scenario) mainPID() string {
	s.t.Helper()
	return strings.TrimSpace(s.shOK("systemctl show -p MainPID --value stackstead"))
}

var (
	realBinMu sync.Mutex
	realBins  = map[string]string{}
)

// buildRealServer cross-compiles the real stackstead (no UI) for linux/arch with the version stamped in.
func buildRealServer(t *testing.T, r *run, arch, version string) string {
	t.Helper()
	realBinMu.Lock()
	defer realBinMu.Unlock()
	key := arch + "/" + version
	if p := realBins[key]; p != "" {
		return p
	}
	out := filepath.Join(os.TempDir(), fmt.Sprintf("stackstead-smoke-%s-%d", strings.ReplaceAll(key, "/", "-"), os.Getpid()), "stackstead")
	extraCleanups = append(extraCleanups, func() { _ = os.RemoveAll(filepath.Dir(out)) })
	pkg := "github.com/rcss-labs/stackstead/internal/version"
	ld := fmt.Sprintf("-s -w -X %[1]s.Version=v%[2]s -X %[1]s.Commit=smoke -X %[1]s.Date=2026-01-01T00:00:00Z", pkg, version)
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags", ld, "-o", out, "./cmd/stackstead")
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the real stackstead: %v\n%s", err, b)
	}
	realBins[key] = out
	return out
}

var (
	smokePkgOnce sync.Once
	smokePkgRoot string
	smokePkgErr  error
)

// smokePackages builds the two test debs from the real binary with `make packages`.
func smokePackages(t *testing.T, r *run, arch string) string {
	t.Helper()
	smokePkgOnce.Do(func() {
		root, err := os.MkdirTemp("", "stackstead-smoke-pkgs-")
		if err != nil {
			smokePkgErr = err
			return
		}
		smokePkgRoot = root
		extraCleanups = append(extraCleanups, func() { _ = os.RemoveAll(root) })
		for _, v := range []string{smokeV1, smokeV2} {
			bin := filepath.Join(root, "bin-"+v)
			if err := os.MkdirAll(bin, 0o755); err != nil {
				smokePkgErr = err
				return
			}
			writeFile(t, filepath.Join(bin, "stackstead-linux-"+arch), readFile(t, buildRealServer(t, r, arch, v)), 0o755)
			dist := filepath.Join(root, v)
			if out, err := lintMake(t, "packages", "DIST="+dist, "BIN="+bin, "VERSION=v"+v, "ALLOW_DEV_RELEASE=1", "PKG_ARCHES="+arch); err != nil {
				smokePkgErr = fmt.Errorf("make packages v%s: %v\n%s", v, err, out)
				return
			}
		}
		_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err == nil {
				if info.IsDir() {
					_ = os.Chmod(p, 0o755)
				} else {
					_ = os.Chmod(p, 0o644)
				}
			}
			return nil
		})
	})
	if smokePkgErr != nil {
		t.Fatalf("building the smoke packages: %v", smokePkgErr)
	}
	return smokePkgRoot
}

func newBootedScenario(t *testing.T, versions ...string) *scenario {
	t.Helper()
	return newScenario(t, "debian", scenarioOpts{Versions: versions, Binary: buildRealServer, Booted: true})
}

// assertServiceRunning checks everything a working install must show: the unit is active within
// 15 s without Docker on the host, the API answers, the process runs as the service user in the
// docker group, the hardening reached the running unit, and `stackstead health` works as that user.
func assertServiceRunning(t *testing.T, s *scenario, bin, version string, fresh bool) {
	t.Helper()
	s.waitFor("service active", "systemctl is-active --quiet stackstead", 15*time.Second)
	health := s.waitFor("/api/health", "curl -fsS http://127.0.0.1:8420/api/health", 15*time.Second)
	expectEqual(t, "/api/health", strings.ReplaceAll(health, " ", ""), `{"ok":true}`)
	setup := s.shOK("curl -fsS http://127.0.0.1:8420/api/setup/status")
	expectContains(t, "/api/setup/status", strings.ReplaceAll(setup, " ", ""), `"needsSetup":true`)
	if fresh {
		// the value is neither asserted nor printed
		s.shOK("journalctl -u stackstead --no-pager | grep -q setup_token")
	}
	pid := s.mainPID()
	ps := strings.Fields(s.shOK("ps -o user:32=,group:32=,supgrp:64= -p " + pid))
	if len(ps) < 3 || ps[0] != "stackstead" || ps[1] != "stackstead" || !strings.Contains(ps[2], "docker") {
		t.Errorf("service process user/group/supgrp = %v, want stackstead stackstead ...docker...", ps)
	}
	props := s.shOK("systemctl show stackstead -p ProtectSystem -p NoNewPrivileges -p PrivateTmp -p UMask")
	expectContains(t, "unit properties", props, "ProtectSystem=full")
	expectContains(t, "unit properties", props, "NoNewPrivileges=yes")
	expectContains(t, "unit properties", props, "PrivateTmp=yes")
	expectContains(t, "unit properties", props, "UMask=0007")
	expectEqual(t, "installed version", s.shOK(bin+" version --short"), "v"+version)
	// as the service user: config, database and server must be fine; Docker is absent here, so exit 7 is the healthy answer
	out, code := s.sh("runuser -u stackstead -- " + bin + " health --config /etc/stackstead/config.yaml")
	if code != 0 && code != 7 {
		t.Errorf("stackstead health as the service user exited %d:\n%s", code, out)
	}
	expectNotContains(t, "stackstead health", out, "refusing")
	if code == 7 {
		expectContains(t, "stackstead health", strings.ToLower(out), "docker")
	}
}

func assertServiceGone(t *testing.T, s *scenario, unitPath string) {
	t.Helper()
	if out, code := s.sh("systemctl is-active --quiet stackstead"); code == 0 {
		t.Errorf("service still active after removal: %s", out)
	}
	s.shOK("test ! -e " + unitPath + " && ! systemctl cat stackstead >/dev/null 2>&1")
	if out, code := s.sh("curl -fsS --max-time 3 http://127.0.0.1:8420/api/health"); code == 0 {
		t.Errorf("the API still answers after removal: %s", out)
	}
}

func TestSystemdSmokeScriptInContainer(t *testing.T) {
	requireSystemdOptIn(t)
	s := newBootedScenario(t, smokeV1, smokeV2)

	out, errOut, code := s.install("--version", "v"+smokeV1)
	expectCode(t, "install v"+smokeV1, code, 0, out, errOut)
	expectContains(t, "install output", out, "http://127.0.0.1:8420")
	assertServiceRunning(t, s, "/usr/local/bin/stackstead", smokeV1, true)

	s.shOK("echo marker > /var/lib/stackstead/marker")
	before := s.mainPID()
	out, errOut, code = s.install() // latest is v2: an in-place upgrade
	expectCode(t, "upgrade", code, 0, out, errOut)
	assertServiceRunning(t, s, "/usr/local/bin/stackstead", smokeV2, false)
	if after := s.mainPID(); after == before || after == "0" {
		t.Errorf("the service was not restarted by the upgrade: main pid %s before and after", before)
	}
	s.shOK("test \"$(cat /var/lib/stackstead/marker)\" = marker && test -s /var/lib/stackstead/stackstead.db")

	out, errOut, code = dockerExec(s.container, nil, "sh", "/usr/local/share/stackstead/install.sh", "--uninstall")
	expectCode(t, "uninstall", code, 0, out, errOut)
	assertServiceGone(t, s, "/etc/systemd/system/stackstead.service")
	s.shOK("test -s /etc/stackstead/config.yaml && test -d /var/lib/stackstead && id stackstead")
}

func TestSystemdSmokeDebInContainer(t *testing.T) {
	requireSystemdOptIn(t)
	arch := dockerArch(t)
	root := smokePackages(t, newRun(t), arch)
	s := newBootedScenario(t, smokeV1)
	s.shOK("mkdir -p /pkgs")
	if _, errOut, code := docker("cp", root+"/.", s.container+":/pkgs"); code != 0 {
		t.Fatalf("docker cp packages: %s", errOut)
	}
	deb := func(v string) string { return fmt.Sprintf("/pkgs/%s/stackstead_%s_%s.deb", v, v, arch) }

	if out, code := installPackage(t, s.r, s.container, deb(smokeV1)); code != 0 {
		t.Fatalf("apt-get install v%s: exit %d\n%s", smokeV1, code, out)
	}
	assertServiceRunning(t, s, "/usr/bin/stackstead", smokeV1, true)

	s.shOK("echo marker > /var/lib/stackstead/marker && echo '# admin edit' >> /etc/stackstead/config.yaml")
	before := s.mainPID()
	if out, code := installPackage(t, s.r, s.container, deb(smokeV2)); code != 0 {
		t.Fatalf("apt-get install v%s: exit %d\n%s", smokeV2, code, out)
	}
	assertServiceRunning(t, s, "/usr/bin/stackstead", smokeV2, false)
	if after := s.mainPID(); after == before || after == "0" {
		t.Errorf("the service was not restarted by the upgrade: main pid %s before and after", before)
	}
	s.shOK("test \"$(cat /var/lib/stackstead/marker)\" = marker && grep -q '# admin edit' /etc/stackstead/config.yaml && ! ls /etc/stackstead/*.dpkg-* 2>/dev/null")

	s.shOK("DEBIAN_FRONTEND=noninteractive apt-get remove -y stackstead >/dev/null 2>&1")
	assertServiceGone(t, s, "/usr/lib/systemd/system/stackstead.service")
	s.shOK("test -s /etc/stackstead/config.yaml && test -f /var/lib/stackstead/marker")

	s.shOK("mkdir -p /opt/stacks && echo keep > /opt/stacks/keep.txt")
	s.shOK("DEBIAN_FRONTEND=noninteractive apt-get purge -y stackstead >/dev/null 2>&1")
	s.shOK("test ! -e /etc/stackstead && test ! -e /var/lib/stackstead && ! id stackstead 2>/dev/null && test \"$(cat /opt/stacks/keep.txt)\" = keep")
}
