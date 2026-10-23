//go:build installer

package installer_test

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// fixture is one staged install: a fake release server, a --root directory
// and stubbed system commands.
type fixture struct {
	t      *testing.T
	r      *run
	dist   string
	base   string
	root   string
	stubs  map[string]string
	stubD  string
	tmpDir string
	path   string
}

// newFixture publishes the given versions and serves them with the last one
// as "latest". The staged root already has /run/systemd/system (a booted
// systemd) unless noSystemd is called before the first run.
func newFixture(t *testing.T, versions ...string) *fixture {
	t.Helper()
	r := newRun(t)
	var dist string
	for _, v := range versions {
		dist = fakeRelease(t, r, v, nil, releaseOpts{})
	}
	f := &fixture{t: t, r: r, dist: dist, stubs: defaultStubs(), root: filepath.Join(r.Dir, "root"), tmpDir: filepath.Join(r.Dir, "tmp")}
	f.base = serveRelease(t, dist, "v"+strings.TrimPrefix(versions[len(versions)-1], "v"))
	for _, d := range []string{f.root, f.tmpDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.booted(true)
	return f
}

func (f *fixture) booted(on bool) {
	dir := filepath.Join(f.root, "run", "systemd", "system")
	if on {
		_ = os.MkdirAll(dir, 0o755)
	} else {
		_ = os.RemoveAll(filepath.Join(f.root, "run"))
	}
}

// run executes install.sh with --root and --base-url preset.
func (f *fixture) run(args ...string) (string, string, int) {
	f.t.Helper()
	return f.runEnv(nil, args...)
}

func (f *fixture) runEnv(env map[string]string, args ...string) (string, string, int) {
	f.t.Helper()
	if f.stubD == "" {
		f.stubD = stubPath(f.t, f.stubs)
	}
	e := map[string]string{"STUBS_DIR": f.stubD, "TMPDIR": f.tmpDir}
	if f.path != "" {
		e["PATH"] = f.path
	}
	for k, v := range env {
		e[k] = v
	}
	full := append([]string{"--root", f.root, "--base-url", f.base}, args...)
	return runScript(f.t, full, e, nil)
}

func (f *fixture) p(rel string) string { return filepath.Join(f.root, filepath.FromSlash(rel)) }

func (f *fixture) installedVersion() string {
	f.t.Helper()
	out, err := exec.Command(f.p("usr/local/bin/stackstead"), "version", "--short").Output()
	if err != nil {
		f.t.Fatalf("installed binary: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func mustExit(t *testing.T, code, want int, stdout, stderr string) {
	t.Helper()
	if code != want {
		t.Fatalf("exit code = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, want, stdout, stderr)
	}
}

func contains(t *testing.T, name, haystack string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(haystack, n) {
			t.Errorf("%s does not contain %q:\n%s", name, n, haystack)
		}
	}
}

func notContains(t *testing.T, name, haystack string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			t.Errorf("%s unexpectedly contains %q:\n%s", name, n, haystack)
		}
	}
}

func assertMode(t *testing.T, path string, want fs.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	got := fi.Mode().Perm() | fi.Mode()&fs.ModeSetgid
	if got != want {
		t.Errorf("%s mode = %v, want %v", path, got, want)
	}
}

type entry struct {
	mode  fs.FileMode
	mtime int64
	sum   string
}

// snapshot records every file under dir (mode, mtime, content hash).
func snapshot(t *testing.T, dir string) map[string]entry {
	t.Helper()
	out := map[string]entry{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		e := entry{mode: fi.Mode(), mtime: fi.ModTime().UnixNano()}
		if fi.Mode().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			s := sha256.Sum256(b)
			e.sum = hex.EncodeToString(s[:])
		}
		out[p] = e
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func requireNonRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("test needs a non-root user")
	}
}

func TestScript(t *testing.T) {
	t.Run("HelpAndUsageErrors", func(t *testing.T) {
		out, errOut, code := runScript(t, []string{"--help"}, nil, defaultStubs())
		mustExit(t, code, 0, out, errOut)
		contains(t, "help", out, "--version", "--prefix", "--no-start", "--signers", "--force", "--base-url", "--root",
			"--dry-run", "--uninstall", "--purge", "--print-paths", "--help",
			"STACKSTEAD_VERSION", "STACKSTEAD_BASE_URL", "STACKSTEAD_INSTALL_SIGNERS_FILE")
		for _, c := range []string{"0", "1", "2", "3", "4", "5", "6", "7", "8"} {
			if !regexp.MustCompile(`(?m)^\s+` + c + `\s+\S`).MatchString(out) {
				t.Errorf("help lacks exit code %s:\n%s", c, out)
			}
		}
		out, errOut, code = runScript(t, []string{"--no-such-flag"}, nil, defaultStubs())
		mustExit(t, code, 2, out, errOut)
		out, errOut, code = runScript(t, []string{"--version"}, nil, defaultStubs())
		mustExit(t, code, 2, out, errOut)
	})

	t.Run("PrintPaths", func(t *testing.T) {
		out, errOut, code := runScript(t, []string{"--print-paths", "--prefix", "/opt/x", "--root", "/r"}, nil, defaultStubs())
		mustExit(t, code, 0, out, errOut)
		contains(t, "paths", out, "BIN=/r/opt/x/bin/stackstead\n", "UNIT=/r/etc/systemd/system/stackstead.service\n",
			"CONFIG=/r/etc/stackstead/config.yaml\n", "DATA_DIR=/r/var/lib/stackstead\n", "STACKS_DIR=/r/opt/stacks\n")
	})

	t.Run("UnsupportedPlatformExits3", func(t *testing.T) {
		f := newFixture(t, "1.0.0")
		f.stubs["uname"] = `case "$1" in -m) echo riscv64 ;; *) echo Linux ;; esac`
		out, errOut, code := f.run()
		mustExit(t, code, 3, out, errOut)
		contains(t, "stderr", errOut, "riscv64")

		f = newFixture(t, "1.0.0")
		f.stubs["uname"] = `case "$1" in -m) echo ` + hostArch() + ` ;; *) echo FreeBSD ;; esac`
		out, errOut, code = f.run()
		mustExit(t, code, 3, out, errOut)
		contains(t, "stderr", errOut, "FreeBSD")

	})

	t.Run("MissingToolExits4", func(t *testing.T) {
		f := newFixture(t, "1.0.0")
		f.path = minimalPath(t, "sha256sum", "shasum", "openssl")
		out, errOut, code := f.run()
		mustExit(t, code, 4, out, errOut)
		contains(t, "stderr", errOut, "sha256")

		// ssh-keygen is only needed with --signers.
		f = newFixture(t, "1.0.0")
		f.path = minimalPath(t, "ssh-keygen")
		out, errOut, code = f.run()
		mustExit(t, code, 0, out, errOut)

		f = newFixture(t, "1.0.0")
		f.path = minimalPath(t, "ssh-keygen")
		signers := filepath.Join(f.r.Dir, "signers")
		writeFile(t, signers, []byte("x\n"), 0o644)
		out, errOut, code = f.run("--signers", signers)
		mustExit(t, code, 4, out, errOut)
		contains(t, "stderr", errOut, "ssh-keygen", "openssh")
	})

	t.Run("RefusesNonRootWithoutRoot", func(t *testing.T) {
		requireNonRoot(t)
		f := newFixture(t, "1.0.0")
		f.stubD = stubPath(t, f.stubs)
		out, errOut, code := runScript(t, []string{"--base-url", f.base}, map[string]string{"STUBS_DIR": f.stubD}, nil)
		mustExit(t, code, 8, out, errOut)
		contains(t, "stderr", errOut, "sudo")
		if reqs := requestsTo(f.base); len(reqs) != 0 {
			t.Errorf("refusal must happen before any download, saw %v", reqs)
		}
	})

	t.Run("FreshInstallLayout", func(t *testing.T) {
		f := newFixture(t, "1.0.0")
		out, errOut, code := f.run()
		mustExit(t, code, 0, out, errOut)

		assertMode(t, f.p("usr/local/bin/stackstead"), 0o755)
		assertMode(t, f.p("usr/local/share/stackstead/install.sh"), 0o755)
		assertMode(t, f.p("etc/systemd/system/stackstead.service"), 0o644)
		assertMode(t, f.p("etc/stackstead/config.yaml"), 0o640)
		assertMode(t, f.p("etc/stackstead"), 0o755)
		assertMode(t, f.p("var/lib/stackstead"), 0o700)
		assertMode(t, f.p("opt/stacks"), 0o775|os.ModeSetgid)
		if f.installedVersion() != "v1.0.0" {
			t.Errorf("installed version = %q", f.installedVersion())
		}
		unit := string(readFile(t, f.p("etc/systemd/system/stackstead.service")))
		contains(t, "unit", unit, "ExecStart=/usr/local/bin/stackstead serve --config /etc/stackstead/config.yaml\n", "SupplementaryGroups=docker")
		notContains(t, "unit", unit, "ExecStart=/usr/local/bin/stackstead --config")
		// the deb/rpm pre-install scripts look for this marker to refuse installing over a script install (issue #33)
		contains(t, "unit", unit, "# Installed by stackstead install.sh\n")
		want := readFile(t, filepath.Join(repoRoot(t), "deploy", "config.example.yaml"))
		if string(readFile(t, f.p("etc/stackstead/config.yaml"))) != string(want) {
			t.Error("config.yaml differs from config.example.yaml")
		}
		// Under --root account and service commands are printed, in this order.
		idx := -1
		for _, needle := range []string{"would run: groupadd --system docker", "would run: useradd --system", "would run: systemctl daemon-reload", "would run: systemctl enable --now stackstead"} {
			i := strings.Index(out, needle)
			if i < 0 || i < idx {
				t.Fatalf("%q missing or out of order in:\n%s", needle, out)
			}
			idx = i
		}
		useradd := out[strings.Index(out, "would run: useradd"):]
		useradd = useradd[:strings.Index(useradd, "\n")]
		contains(t, "useradd", useradd, "--home-dir /var/lib/stackstead", "--no-create-home", "nologin", "stackstead")
	})

	t.Run("SecondRunIsNoop", func(t *testing.T) {
		f := newFixture(t, "1.0.0")
		out, errOut, code := f.run()
		mustExit(t, code, 0, out, errOut)
		before := snapshot(t, f.root)
		out, errOut, code = f.run()
		mustExit(t, code, 0, out, errOut)
		contains(t, "stdout", out, "already installed")
		notContains(t, "stdout", out, "restart", "enable")
		after := snapshot(t, f.root)
		if len(before) != len(after) {
			t.Errorf("file set changed: %d -> %d entries", len(before), len(after))
		}
		for p, b := range before {
			if a, ok := after[p]; !ok || a != b {
				t.Errorf("%s changed: %+v -> %+v", p, b, after[p])
			}
		}
	})

	t.Run("UpgradeKeepsConfigAndData", func(t *testing.T) {
		f := newFixture(t, "1.0.0", "1.1.0")
		out, errOut, code := f.run("--version", "v1.0.0")
		mustExit(t, code, 0, out, errOut)
		cfg := []byte("listen: 127.0.0.1:9999\n# edited\n")
		writeFile(t, f.p("etc/stackstead/config.yaml"), cfg, 0o640)
		writeFile(t, f.p("var/lib/stackstead/stackstead.db"), []byte("db"), 0o600)
		writeFile(t, f.p("opt/stacks/web/compose.yaml"), []byte("services: {}\n"), 0o644)
		unit := f.p("etc/systemd/system/stackstead.service")
		writeFile(t, unit, append(readFile(t, unit), []byte("# stale\n")...), 0o644)

		out, errOut, code = f.run("--version", "v1.1.0")
		mustExit(t, code, 0, out, errOut)
		if f.installedVersion() != "v1.1.0" {
			t.Errorf("installed version = %q, want v1.1.0", f.installedVersion())
		}
		notContains(t, "unit", string(readFile(t, unit)), "# stale")
		if string(readFile(t, f.p("etc/stackstead/config.yaml"))) != string(cfg) {
			t.Error("config was overwritten")
		}
		if string(readFile(t, f.p("var/lib/stackstead/stackstead.db"))) != "db" {
			t.Error("data was touched")
		}
		if n := strings.Count(out, "would run: systemctl restart stackstead"); n != 1 {
			t.Errorf("restart printed %d times, want 1:\n%s", n, out)
		}
		notContains(t, "stdout", out, "enable", "useradd", "groupadd")
	})

	t.Run("OlderVersionNeedsForce", func(t *testing.T) {
		f := newFixture(t, "1.0.0", "1.1.0")
		out, errOut, code := f.run("--version", "1.1.0")
		mustExit(t, code, 0, out, errOut)
		out, errOut, code = f.run("--version", "1.0.0")
		mustExit(t, code, 0, out, errOut)
		contains(t, "stdout", out, "newer")
		if f.installedVersion() != "v1.1.0" {
			t.Errorf("downgraded without --force: %s", f.installedVersion())
		}
		out, errOut, code = f.run("--version", "1.0.0", "--force")
		mustExit(t, code, 0, out, errOut)
		if f.installedVersion() != "v1.0.0" {
			t.Errorf("--force did not install: %s", f.installedVersion())
		}
	})

	t.Run("VersionPinSelectsAsset", func(t *testing.T) {
		f := newFixture(t, "1.0.0", "1.1.0")
		out, errOut, code := f.runEnv(map[string]string{"STACKSTEAD_VERSION": "v1.0.0"})
		mustExit(t, code, 0, out, errOut)
		if f.installedVersion() != "v1.0.0" {
			t.Errorf("env pin: installed %s", f.installedVersion())
		}
		f = newFixture(t, "1.0.0", "1.1.0")
		out, errOut, code = f.runEnv(map[string]string{"STACKSTEAD_VERSION": "v1.0.0"}, "--version", "v1.1.0")
		mustExit(t, code, 0, out, errOut)
		if f.installedVersion() != "v1.1.0" {
			t.Errorf("flag must win over env: installed %s", f.installedVersion())
		}
	})

	t.Run("LatestResolvesViaRedirect", func(t *testing.T) {
		f := newFixture(t, "1.0.0", "1.1.0")
		out, errOut, code := f.run()
		mustExit(t, code, 0, out, errOut)
		if f.installedVersion() != "v1.1.0" {
			t.Errorf("latest installed %s", f.installedVersion())
		}
		contains(t, "requests", strings.Join(requestsTo(f.base), "\n"), "/releases/latest/download/SHA256SUMS")
	})

	t.Run("RefusesChecksumMismatch", func(t *testing.T) {
		f := newFixture(t, "1.0.0")
		tb := filepath.Join(f.dist, "v1.0.0", "stackstead_1.0.0_linux_"+goarch()+".tar.gz")
		fh, err := os.OpenFile(tb, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fh.WriteString("tampered")
		_ = fh.Close()
		out, errOut, code := f.run()
		mustExit(t, code, 6, out, errOut)
		assertNothingStaged(t, f)
	})

	t.Run("AssetMissingFromSumsIsRefused", func(t *testing.T) {
		r := newRun(t)
		dist := fakeRelease(t, r, "1.0.0", nil, releaseOpts{OmitFromSums: true})
		f := &fixture{t: t, r: r, dist: dist, stubs: defaultStubs(), root: filepath.Join(r.Dir, "root"), tmpDir: filepath.Join(r.Dir, "tmp")}
		f.base = serveRelease(t, dist, "v1.0.0")
		_ = os.MkdirAll(f.root, 0o755)
		_ = os.MkdirAll(f.tmpDir, 0o755)
		out, errOut, code := f.run("--version", "v1.0.0")
		mustExit(t, code, 6, out, errOut)
		assertNothingStaged(t, f)
	})

	t.Run("NoSigNoSignersInstalls", func(t *testing.T) {
		f := newFixture(t, "1.0.0")
		out, errOut, code := f.run()
		mustExit(t, code, 0, out, errOut)
		notContains(t, "requests", strings.Join(requestsTo(f.base), "\n"), ".sig")
		notContains(t, "output", strings.ToLower(out+errOut), "signature")
	})

	signedFixture := func(t *testing.T, opts releaseOpts) *fixture {
		r := newRun(t)
		dist := fakeRelease(t, r, "1.0.0", nil, opts)
		if !opts.Sign && !opts.SignOtherKey {
			// a signers file is still needed to ask for verification
			_ = fakeRelease(t, newRunWithDir(r, filepath.Join(r.Dir, "keyonly")), "9.9.9", nil, releaseOpts{Sign: true})
			writeFile(t, r.signersFile(), readFile(t, filepath.Join(r.Dir, "keyonly", "allowed_signers")), 0o644)
		}
		f := &fixture{t: t, r: r, dist: dist, stubs: defaultStubs(), root: filepath.Join(r.Dir, "root"), tmpDir: filepath.Join(r.Dir, "tmp")}
		f.base = serveRelease(t, dist, "v1.0.0")
		_ = os.MkdirAll(f.root, 0o755)
		_ = os.MkdirAll(f.tmpDir, 0o755)
		f.booted(true)
		return f
	}

	t.Run("SignersRequiresValidSig", func(t *testing.T) {
		f := signedFixture(t, releaseOpts{Sign: true})
		out, errOut, code := f.run("--signers", f.r.signersFile())
		mustExit(t, code, 0, out, errOut)
		contains(t, "requests", strings.Join(requestsTo(f.base), "\n"), "SHA256SUMS.sig")

		f = signedFixture(t, releaseOpts{})
		out, errOut, code = f.run("--signers", f.r.signersFile())
		mustExit(t, code, 6, out, errOut)
		assertNothingStaged(t, f)
	})

	t.Run("BadSignatureRefusedWithSigners", func(t *testing.T) {
		f := signedFixture(t, releaseOpts{SignOtherKey: true})
		out, errOut, code := f.run("--signers", f.r.signersFile())
		mustExit(t, code, 6, out, errOut)
		assertNothingStaged(t, f)
	})

	t.Run("SignersEnvEqualsFlag", func(t *testing.T) {
		f := signedFixture(t, releaseOpts{SignOtherKey: true})
		out, errOut, code := f.runEnv(map[string]string{"STACKSTEAD_INSTALL_SIGNERS_FILE": f.r.signersFile()})
		mustExit(t, code, 6, out, errOut)
		f = signedFixture(t, releaseOpts{Sign: true})
		out, errOut, code = f.runEnv(map[string]string{"STACKSTEAD_INSTALL_SIGNERS_FILE": f.r.signersFile()})
		mustExit(t, code, 0, out, errOut)
	})

	t.Run("DryRunWritesNothing", func(t *testing.T) {
		requireNonRoot(t)
		f := newFixture(t, "1.0.0")
		f.booted(false)
		out, errOut, code := f.run("--dry-run")
		mustExit(t, code, 0, out, errOut)
		contains(t, "plan", out, "would", "/usr/local/bin/stackstead", "/etc/systemd/system/stackstead.service", "systemctl")
		entries, _ := os.ReadDir(f.root)
		if len(entries) != 0 {
			t.Errorf("--root dir not empty after --dry-run: %v", entries)
		}
		if tmp, _ := os.ReadDir(f.tmpDir); len(tmp) != 0 {
			t.Errorf("temp dir not cleaned: %v", tmp)
		}
		// dry-run needs neither root nor --root.
		stubD := stubPath(t, f.stubs)
		out, errOut, code = runScript(t, []string{"--dry-run", "--prefix", filepath.Join(f.r.Dir, "prefix"), "--base-url", f.base}, map[string]string{"STUBS_DIR": stubD, "TMPDIR": f.tmpDir}, nil)
		mustExit(t, code, 0, out, errOut)
		contains(t, "plan", out, "would")
		if tmp, _ := os.ReadDir(f.tmpDir); len(tmp) != 0 {
			t.Errorf("temp dir not cleaned: %v", tmp)
		}
	})

	t.Run("RootModeNeverCallsServiceCommands", func(t *testing.T) {
		f := newFixture(t, "1.0.0")
		out, errOut, code := f.run()
		mustExit(t, code, 0, out, errOut)
		for _, name := range []string{"systemctl", "useradd", "groupadd", "adduser", "addgroup", "userdel", "usermod"} {
			if l := stubLog(f.stubD, name); l != "" {
				t.Errorf("stub %s was called under --root: %q", name, l)
			}
		}
		contains(t, "stdout", out, "would run: systemctl")
	})

	t.Run("DownloadFailureExits5", func(t *testing.T) {
		for _, code := range []int{500, 404} {
			f := newFixture(t, "1.0.0")
			f.base = serveStatus(t, code)
			out, errOut, got := f.run("--version", "v1.0.0")
			mustExit(t, got, 5, out, errOut)
			contains(t, "stderr", errOut, f.base+"/releases/download/v1.0.0/SHA256SUMS")
			assertNothingStaged(t, f)
		}
		f := newFixture(t, "1.0.0")
		out, errOut, got := f.run("--version", "v2.0.0")
		mustExit(t, got, 5, out, errOut)
	})

	t.Run("NoSystemdWarnsAndExitsZero", func(t *testing.T) {
		f := newFixture(t, "1.0.0")
		f.booted(false)
		delete(f.stubs, "systemctl")
		f.path = minimalPath(t)
		out, errOut, code := f.run()
		mustExit(t, code, 0, out, errOut)
		contains(t, "stderr", errOut, "no systemd", "serve --config")
		notContains(t, "stdout", out, "systemctl")
		assertMode(t, f.p("usr/local/bin/stackstead"), 0o755)
		// no systemd (Alpine): no unit either
		if fileExists(f.p("etc/systemd/system/stackstead.service")) {
			t.Error("a unit was written on a host without systemd")
		}
		assertMode(t, f.p("etc/stackstead/config.yaml"), 0o640)
	})

	t.Run("SystemdNotBootedEnablesOnly", func(t *testing.T) {
		f := newFixture(t, "1.0.0")
		f.booted(false)
		out, errOut, code := f.run()
		mustExit(t, code, 0, out, errOut)
		contains(t, "stdout", out, "would run: systemctl enable stackstead")
		notContains(t, "stdout", out, "enable --now", "daemon-reload")
		contains(t, "stderr", errOut, "not booted")
	})

	t.Run("NoStartOnlyEnables", func(t *testing.T) {
		f := newFixture(t, "1.0.0")
		out, errOut, code := f.run("--no-start")
		mustExit(t, code, 0, out, errOut)
		contains(t, "stdout", out, "would run: systemctl enable stackstead")
		notContains(t, "stdout", out, "enable --now")
	})

	t.Run("BusyboxAccountCommands", func(t *testing.T) {
		f := newFixture(t, "1.0.0")
		delete(f.stubs, "useradd")
		delete(f.stubs, "groupadd")
		f.stubs["adduser"] = `exit 0`
		f.stubs["addgroup"] = `exit 0`
		f.path = minimalPath(t)
		out, errOut, code := f.run()
		mustExit(t, code, 0, out, errOut)
		contains(t, "stdout", out, "would run: addgroup -S docker\n", "would run: addgroup -S stackstead\n",
			"would run: adduser -S -D -H -h /var/lib/stackstead -s ", " -G stackstead stackstead\n")
		notContains(t, "stdout", out, "useradd", "groupadd")
	})

	t.Run("UninstallKeepsDataPurgeRemovesIt", func(t *testing.T) {
		f := newFixture(t, "1.0.0")
		out, errOut, code := f.run()
		mustExit(t, code, 0, out, errOut)
		writeFile(t, f.p("var/lib/stackstead/stackstead.db"), []byte("db"), 0o600)
		f.base = "http://127.0.0.1:1" // uninstall must work offline
		out, errOut, code = f.run("--uninstall")
		mustExit(t, code, 0, out, errOut)
		for _, gone := range []string{"usr/local/bin/stackstead", "etc/systemd/system/stackstead.service", "usr/local/share/stackstead"} {
			if _, err := os.Lstat(f.p(gone)); err == nil {
				t.Errorf("%s still exists after --uninstall", gone)
			}
		}
		for _, kept := range []string{"etc/stackstead/config.yaml", "var/lib/stackstead/stackstead.db", "opt/stacks"} {
			if _, err := os.Lstat(f.p(kept)); err != nil {
				t.Errorf("%s removed by --uninstall: %v", kept, err)
			}
		}
		contains(t, "stdout", out, "would run: systemctl disable", "kept")
		notContains(t, "stdout", out, "userdel", "deluser")

		out, errOut, code = f.run("--uninstall", "--purge")
		mustExit(t, code, 0, out, errOut)
		for _, gone := range []string{"etc/stackstead", "var/lib/stackstead"} {
			if _, err := os.Lstat(f.p(gone)); err == nil {
				t.Errorf("%s still exists after --purge", gone)
			}
		}
		contains(t, "stdout", out, "would run: userdel stackstead")
	})

	t.Run("UninstallNeverTouchesStacks", func(t *testing.T) {
		f := newFixture(t, "1.0.0")
		out, errOut, code := f.run()
		mustExit(t, code, 0, out, errOut)
		writeFile(t, f.p("opt/stacks/web/compose.yaml"), []byte("services: {}\n"), 0o644)
		before := snapshot(t, f.p("opt/stacks"))
		out, errOut, code = f.run("--uninstall", "--purge")
		mustExit(t, code, 0, out, errOut)
		after := snapshot(t, f.p("opt/stacks"))
		for p, b := range before {
			if a, ok := after[p]; !ok || a.sum != b.sum {
				t.Errorf("%s changed by --uninstall --purge", p)
			}
		}
		if len(before) != len(after) {
			t.Error("stacks dir contents changed")
		}
	})

	t.Run("PackageInstallRefused", func(t *testing.T) {
		f := newFixture(t, "1.0.0")
		f.stubs["dpkg"] = `exit 0`
		out, errOut, code := f.run()
		mustExit(t, code, 8, out, errOut)
		contains(t, "stderr", errOut, "package")
		assertNothingStaged(t, f)
	})
	t.Run("LegacyBinaryIsNeverStartedByTheProbe", func(t *testing.T) {
		f := newFixture(t, "1.0.0")
		marker := filepath.Join(f.r.Dir, "legacy-marker")
		writeFile(t, f.p("usr/local/bin/stackstead"), readFile(t, buildLegacyBinary(t)), 0o755)
		out, errOut, code := f.runEnv(map[string]string{"LEGACY_MARKER": marker})
		mustExit(t, code, 0, out, errOut)
		if b, _ := os.ReadFile(marker); len(b) != 0 {
			t.Fatalf("the installer started the legacy binary as a server: %q", b)
		}
		if f.installedVersion() != "v1.0.0" {
			t.Errorf("legacy binary was not replaced: %s", f.installedVersion())
		}
		// present but of unknown version = upgrade, never a fresh install
		contains(t, "stdout", out, "would run: systemctl restart stackstead")
		notContains(t, "stdout", out, "enable --now", "enable")
	})

	t.Run("VersionComparison", func(t *testing.T) {
		cases := []struct {
			installed, requested string
			replaced             bool
		}{
			{"1.2.3-rc.2", "1.2.3-rc.10", true}, // numeric pre-release identifiers
			{"1.2.3-rc.10", "1.2.3-rc.2", false},
			{"1.2.3-5-gabc1234", "1.2.3", false}, // a build after the tag is newer than the tag
			{"1.2.3-5-gabc1234", "1.2.4", true},
			{"1.2.3", "1.2.3-rc.1", false}, // release beats its pre-release
		}
		for _, c := range cases {
			f := newFixture(t, c.requested)
			writeFile(t, f.p("usr/local/bin/stackstead"), readFile(t, buildBinary(t, "linux", goarch(), c.installed)), 0o755)
			out, errOut, code := f.run("--version", c.requested)
			mustExit(t, code, 0, out, errOut)
			want := "v" + c.installed
			if c.replaced {
				want = "v" + c.requested
			}
			if got := f.installedVersion(); got != want {
				t.Errorf("installed %s, requested %s: now %s, want %s\n%s", c.installed, c.requested, got, want, out)
			}
		}
	})

	t.Run("NonDefaultBaseURLWarnsAndInsecureIsRefused", func(t *testing.T) {
		f := newFixture(t, "1.0.0")
		out, errOut, code := f.run("--version", "v1.0.0")
		mustExit(t, code, 0, out, errOut)
		contains(t, "stderr", errOut, "warning:", "non-default", f.base)

		f = newFixture(t, "1.0.0")
		before := len(requestsTo(f.base))
		out, errOut, code = f.run("--base-url", "http://example.com/stackstead")
		mustExit(t, code, 2, out, errOut)
		contains(t, "stderr", errOut, "https")
		if len(requestsTo(f.base)) != before {
			t.Error("refused URL must not be fetched")
		}
		for _, u := range []string{"http://localhost:1", "http://[::1]:1", "http://127.0.0.1:1"} {
			_, errOut, code = f.run("--base-url", u)
			if code != 5 {
				t.Errorf("loopback %s: exit %d, want 5 (download failure, not refusal)\n%s", u, code, errOut)
			}
		}
		// the default URL is not warned about (uninstall needs no network)
		out, errOut, code = f.run("--uninstall")
		_ = out
		notContains(t, "stderr", errOut, "non-default")
	})

	t.Run("RefusesRootSlash", func(t *testing.T) {
		out, errOut, code := runScript(t, []string{"--root", "/", "--dry-run"}, nil, defaultStubs())
		mustExit(t, code, 2, out, errOut)
		out, errOut, code = runScript(t, []string{"--root", "//", "--dry-run"}, nil, defaultStubs())
		mustExit(t, code, 2, out, errOut)
	})

	t.Run("ExistingStacksDirNotWritableWarnsAndIsNotChanged", func(t *testing.T) {
		f := newFixture(t, "1.0.0")
		if err := os.MkdirAll(f.p("opt/stacks"), 0o755); err != nil {
			t.Fatal(err)
		}
		_ = os.Chmod(f.p("opt/stacks"), 0o755)
		out, errOut, code := f.run()
		mustExit(t, code, 0, out, errOut)
		contains(t, "stderr", errOut, "warning:", "chgrp docker /opt/stacks && chmod 2775 /opt/stacks")
		assertMode(t, f.p("opt/stacks"), 0o755)
		notContains(t, "stdout", out, "chown")

		f = newFixture(t, "1.0.0")
		if err := os.MkdirAll(f.p("opt/stacks"), 0o777); err != nil {
			t.Fatal(err)
		}
		_ = os.Chmod(f.p("opt/stacks"), 0o777)
		out, errOut, code = f.run()
		mustExit(t, code, 0, out, errOut)
		notContains(t, "stderr", errOut, "chgrp docker")

		// a directory the installer just created needs no warning
		f = newFixture(t, "1.0.0")
		_, errOut, code = f.run()
		mustExit(t, code, 0, "", errOut)
		notContains(t, "stderr", errOut, "chgrp docker")
	})

	t.Run("PlantedTempSymlinksAreNotFollowed", func(t *testing.T) {
		// a DEST.new symlink planted next to a destination must not redirect the write
		f := newFixture(t, "1.0.0")
		victim := filepath.Join(t.TempDir(), "victim")
		writeFile(t, victim, []byte("secret"), 0o600)
		dests := []string{"usr/local/bin/stackstead", "usr/local/share/stackstead/install.sh",
			"etc/systemd/system/stackstead.service", "etc/stackstead/config.yaml"}
		for _, d := range dests {
			if err := os.MkdirAll(filepath.Dir(f.p(d)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(victim, f.p(d)+".new"); err != nil {
				t.Fatal(err)
			}
		}
		out, errOut, code := f.run()
		mustExit(t, code, 0, out, errOut)
		if string(readFile(t, victim)) != "secret" {
			t.Error("a planted .new symlink was followed: the victim file was overwritten")
		}
		assertMode(t, victim, 0o600)
		for _, d := range dests {
			if fi, err := os.Lstat(f.p(d)); err != nil || !fi.Mode().IsRegular() {
				t.Errorf("%s is not a regular file: %v", d, err)
			}
		}
	})

	t.Run("ServiceUserJoinsDockerGroup", func(t *testing.T) {
		f := newFixture(t, "1.0.0")
		out, errOut, code := f.run()
		mustExit(t, code, 0, out, errOut)
		line := out[strings.Index(out, "would run: useradd"):]
		line = line[:strings.Index(line, "\n")]
		contains(t, "useradd", line, "-G docker")

		// existing user, not in docker: usermod -aG
		f = newFixture(t, "1.0.0")
		f.stubs["id"] = `case "$1" in -u) echo 1000 ;; -nG) echo stackstead ;; esac; exit 0`
		out, errOut, code = f.run()
		mustExit(t, code, 0, out, errOut)
		contains(t, "stdout", out, "would run: usermod -aG docker stackstead\n")
		notContains(t, "stdout", out, "useradd")

		// existing user already in docker: nothing to do
		f = newFixture(t, "1.0.0")
		f.stubs["id"] = `case "$1" in -u) echo 1000 ;; -nG) echo "stackstead docker" ;; esac; exit 0`
		out, errOut, code = f.run()
		mustExit(t, code, 0, out, errOut)
		notContains(t, "stdout", out, "usermod", "useradd")

		// busybox
		f = newFixture(t, "1.0.0")
		delete(f.stubs, "usermod")
		delete(f.stubs, "useradd")
		delete(f.stubs, "groupadd")
		f.stubs["adduser"] = `exit 0`
		f.stubs["addgroup"] = `exit 0`
		f.path = minimalPath(t)
		out, errOut, code = f.run()
		mustExit(t, code, 0, out, errOut)
		contains(t, "stdout", out, "would run: addgroup stackstead docker\n")
	})

	t.Run("UnsafeTarballMembersAreRefused", func(t *testing.T) {
		for name, opts := range map[string]releaseOpts{"dotdot": {UnsafeMember: true}, "symlink": {SymlinkUnit: true}} {
			r := newRun(t)
			dist := fakeRelease(t, r, "1.0.0", nil, opts)
			f := &fixture{t: t, r: r, dist: dist, stubs: defaultStubs(), root: filepath.Join(r.Dir, "root"), tmpDir: filepath.Join(r.Dir, "tmp")}
			f.base = serveRelease(t, dist, "v1.0.0")
			_ = os.MkdirAll(f.root, 0o755)
			_ = os.MkdirAll(f.tmpDir, 0o755)
			out, errOut, code := f.run("--version", "v1.0.0")
			if code != 6 {
				t.Errorf("%s: exit %d, want 6\n%s%s", name, code, out, errOut)
			}
			assertNothingStaged(t, f)
			if _, err := os.Stat(filepath.Join(r.Dir, "evil")); err == nil {
				t.Errorf("%s: member escaped the extraction directory", name)
			}
		}
	})

	t.Run("PartialNameInSumsDoesNotMatch", func(t *testing.T) {
		f := newFixture(t, "1.0.0")
		sums := filepath.Join(f.dist, "v1.0.0", "SHA256SUMS")
		var kept []string
		for _, l := range strings.Split(strings.TrimSpace(string(readFile(t, sums))), "\n") {
			if strings.Contains(l, "_linux_") {
				// only a longer, different asset name remains, with a plausible hash
				l = l[:64] + "  x" + strings.TrimSpace(l[64:])
			}
			kept = append(kept, l)
		}
		writeFile(t, sums, []byte(strings.Join(kept, "\n")+"\n"), 0o644)
		out, errOut, code := f.run("--version", "v1.0.0")
		mustExit(t, code, 6, out, errOut)
		assertNothingStaged(t, f)
	})

	t.Run("StarNameInSumsIsAccepted", func(t *testing.T) {
		f := newFixture(t, "1.0.0")
		sums := filepath.Join(f.dist, "v1.0.0", "SHA256SUMS")
		b := strings.ReplaceAll(string(readFile(t, sums)), "  stackstead_", " *stackstead_")
		writeFile(t, sums, []byte(b), 0o644)
		out, errOut, code := f.run()
		mustExit(t, code, 0, out, errOut)
		contains(t, "latest", out, "v1.0.0")
	})
}

func goarch() string {
	if strings.Contains(hostArch(), "aarch64") {
		return "arm64"
	}
	return "amd64"
}

// assertNothingStaged checks that no installed file exists under the root.
func assertNothingStaged(t *testing.T, f *fixture) {
	t.Helper()
	var found []string
	_ = filepath.WalkDir(f.root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			found = append(found, p)
		}
		return nil
	})
	for _, top := range []string{"usr", "etc", "var", "opt"} {
		if _, err := os.Stat(f.p(top)); err == nil {
			found = append(found, f.p(top))
		}
	}
	sort.Strings(found)
	if len(found) > 0 {
		t.Errorf("files were staged before verification passed: %v", found)
	}
	if tmp, _ := os.ReadDir(f.tmpDir); len(tmp) != 0 {
		t.Errorf("temp dir not cleaned: %v", tmp)
	}
}

func newRunWithDir(r *run, dir string) *run {
	_ = os.MkdirAll(dir, 0o755)
	return &run{ID: r.ID, Dir: dir}
}

func TestShellcheckClean(t *testing.T) {
	script := installScript(t)
	if _, err := os.Stat(script); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("sh", "-n", script).CombinedOutput(); err != nil {
		t.Fatalf("sh -n: %v\n%s", err, out)
	}
	if p, err := exec.LookPath("dash"); err == nil {
		if out, err := exec.Command(p, "-n", script).CombinedOutput(); err != nil {
			t.Fatalf("dash -n: %v\n%s", err, out)
		}
	}
	if p, err := exec.LookPath("shellcheck"); err == nil {
		if out, err := exec.Command(p, "--shell=sh", "--severity=style", script).CombinedOutput(); err != nil {
			t.Fatalf("shellcheck:\n%s", out)
		}
		return
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("neither shellcheck nor docker is available")
	}
	r := newRun(t)
	const image = "koalaman/shellcheck:v0.10.0"
	if exec.Command(docker, "image", "inspect", image).Run() != nil {
		if out, err := exec.Command(docker, "pull", "-q", image).CombinedOutput(); err != nil {
			t.Skipf("cannot pull %s: %s", image, out)
		}
	}
	name := "stackstead-itest-" + r.ID + "-shellcheck"
	t.Cleanup(func() { _ = exec.Command(docker, "rm", "-f", name).Run() })
	out, err := exec.Command(docker, "run", "--rm", "--name", name, "--label", "stackstead.test="+r.ID,
		"-v", filepath.Dir(script)+":/mnt:ro", image, "--shell=sh", "--severity=style", "/mnt/install.sh").CombinedOutput()
	if err != nil {
		t.Fatalf("shellcheck:\n%s", out)
	}
	if exec.Command(docker, "image", "inspect", "busybox:1.36").Run() == nil {
		bname := name + "-busybox"
		t.Cleanup(func() { _ = exec.Command(docker, "rm", "-f", bname).Run() })
		out, err := exec.Command(docker, "run", "--rm", "--name", bname, "--label", "stackstead.test="+r.ID,
			"-v", filepath.Dir(script)+":/mnt:ro", "busybox:1.36", "sh", "-n", "/mnt/install.sh").CombinedOutput()
		if err != nil {
			t.Fatalf("busybox sh -n:\n%s", out)
		}
	}
}

// codesInHelp returns the exit codes install.sh --help lists.
func codesInHelp(t *testing.T) []string {
	t.Helper()
	out, errOut, code := runScript(t, []string{"--help"}, nil, defaultStubs())
	mustExit(t, code, 0, out, errOut)
	_, tail, ok := strings.Cut(out, "Exit codes:")
	if !ok {
		t.Fatalf("--help has no Exit codes section:\n%s", out)
	}
	var codes []string
	for _, l := range strings.Split(tail, "\n") {
		if m := regexp.MustCompile(`^\s+(\d+)\s`).FindStringSubmatch(l); m != nil {
			codes = append(codes, m[1])
		}
	}
	sort.Strings(codes)
	return codes
}

// The exit-code table in docs/install.md and the codes in install.sh --help are the same set.
func TestInstallDocExitCodesMatchHelp(t *testing.T) {
	doc := string(readFile(t, filepath.Join(repoRoot(t), "docs", "install.md")))
	_, section, ok := strings.Cut(doc, "## Exit codes")
	if !ok {
		t.Fatal("docs/install.md has no '## Exit codes' section")
	}
	if i := strings.Index(section, "\n## "); i >= 0 {
		section = section[:i]
	}
	var docCodes []string
	for _, l := range strings.Split(section, "\n") {
		if m := regexp.MustCompile(`^\|\s*(\d+)\s*\|`).FindStringSubmatch(l); m != nil {
			docCodes = append(docCodes, m[1])
		}
	}
	sort.Strings(docCodes)
	help := codesInHelp(t)
	if strings.Join(docCodes, ",") != strings.Join(help, ",") {
		t.Errorf("docs/install.md exit codes %v differ from install.sh --help %v", docCodes, help)
	}
}

// The binary path the docs give for packaged installs is the one the shipped unit runs.
func TestDocsNameTheUnitBinaryPath(t *testing.T) {
	root := repoRoot(t)
	m := regexp.MustCompile(`(?m)^ExecStart=(\S+) serve`).FindSubmatch(readFile(t, filepath.Join(root, "deploy", "systemd", "stackstead.service")))
	if m == nil {
		t.Fatal("no ExecStart=<bin> serve in the unit")
	}
	for _, doc := range []string{"README.md", "docs/install.md", "docs/cli.md"} {
		if !strings.Contains(string(readFile(t, filepath.Join(root, doc))), string(m[1])) {
			t.Errorf("%s does not mention the unit's binary path %s", doc, m[1])
		}
	}
}

// install.sh pins PATH before anything else runs when it is root on macOS, where sudo passes
// the user's PATH on (Homebrew's user-owned bin directory ahead of /usr/bin). The runtime effect
// cannot be tested without real root (rootSim fakes id), so this checks the source: the first
// executable lines use absolute /usr/bin/id and /usr/bin/uname and set a system-only PATH before
// the script defines or runs anything else. Linux is deliberately not pinned: sudo there applies
// secure_path, and a root login shell with a user-writable PATH is the administrator's own problem.
func TestScriptPinsRootPATHOnMacFirst(t *testing.T) {
	src := string(readFile(t, installScript(t)))
	pin := strings.Index(src, "PATH=/usr/bin:/bin:/usr/sbin:/sbin")
	first := strings.Index(src, "\nDEFAULT_BASE_URL=")
	if pin < 0 || first < 0 || pin > first {
		t.Fatalf("PATH is not pinned before the script's first definition (pin at %d, DEFAULT_BASE_URL at %d)", pin, first)
	}
	head := src[:first]
	for _, want := range []string{`/usr/bin/id -u`, `/usr/bin/uname -s`, "Darwin"} {
		if !strings.Contains(head, want) {
			t.Errorf("the PATH pin must use %q", want)
		}
	}
	for _, l := range strings.Split(head, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") || strings.HasPrefix(l, "set -") {
			continue
		}
		if strings.Contains(l, "id -u") && !strings.Contains(l, "/usr/bin/id") || strings.Contains(l, "uname") && !strings.Contains(l, "/usr/bin/uname") {
			t.Errorf("PATH lookup before the pin: %q", l)
		}
	}
}
