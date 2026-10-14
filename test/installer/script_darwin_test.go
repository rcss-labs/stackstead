//go:build installer

package installer_test

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

const darwinLabel = "io.github.rcss-labs.stackstead"

// publishRelease builds <r.Dir>/dist/v<version>/ for goos on amd64 and arm64
// like fakeRelease does, but lets a test drop or add archive members, and
// ships launchagent.plist.in in the darwin archives (what the Makefile's
// mkrelease -extra darwin:... does). It returns the dist directory.
func publishRelease(t *testing.T, r *run, version, goos string, drop []string, add map[string][]byte) string {
	t.Helper()
	root := repoRoot(t)
	files := map[string][]byte{
		"install.sh":          readFile(t, installScript(t)),
		"config.example.yaml": readFile(t, filepath.Join(root, "deploy", "config.example.yaml")),
		"README.md":           []byte("# stackstead\n"),
		"LICENSE":             []byte("GNU AFFERO GENERAL PUBLIC LICENSE\n"),
	}
	if goos == "darwin" {
		files["launchagent.plist.in"] = readFile(t, filepath.Join(root, "deploy", "macos", "launchagent.plist.in"))
	} else {
		files["stackstead.service"] = readFile(t, filepath.Join(root, "deploy", "systemd", "stackstead.service"))
	}
	for _, d := range drop {
		delete(files, d)
	}
	for k, v := range add {
		files[k] = v
	}
	vdir := filepath.Join(r.Dir, "dist", "v"+version)
	if err := os.MkdirAll(vdir, 0o755); err != nil {
		t.Fatal(err)
	}
	var sums strings.Builder
	for _, arch := range []string{"amd64", "arm64"} {
		all := map[string][]byte{"stackstead": readFile(t, buildBinary(t, goos, arch, version))}
		for k, v := range files {
			all[k] = v
		}
		name := fmt.Sprintf("stackstead_%s_%s_%s.tar.gz", version, goos, arch)
		data := tarball(t, all, map[string]int64{"stackstead": 0o755, "install.sh": 0o755}, releaseOpts{})
		writeFile(t, filepath.Join(vdir, name), data, 0o644)
		fmt.Fprintf(&sums, "%s  %s\n", hexSum(data), name)
	}
	writeFile(t, filepath.Join(vdir, "SHA256SUMS"), []byte(sums.String()), 0o644)
	return filepath.Join(r.Dir, "dist")
}

func fixtureFromDist(t *testing.T, r *run, dist, latest string, stubs map[string]string) *fixture {
	t.Helper()
	f := &fixture{t: t, r: r, dist: dist, stubs: stubs, root: filepath.Join(r.Dir, "root"), tmpDir: filepath.Join(r.Dir, "tmp")}
	f.base = serveRelease(t, dist, "v"+latest)
	for _, d := range []string{f.root, f.tmpDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// darwinStubs are the commands the macOS flow calls. asRoot makes `id` report
// uid 0 (a sudo run); otherwise the invoker is "alice" (uid 501). Nothing
// here touches the real launchd, /usr/local or Docker.
func darwinStubs(asRoot bool) map[string]string {
	idBody := `case "$*" in -u) echo 501 ;; -un) echo alice ;; "-u "*) echo 501 ;; "-gn "*) echo staff ;; *) exit 1 ;; esac`
	if asRoot {
		idBody = `case "$*" in -u) echo 0 ;; -un) echo root ;; "-u "*) echo 501 ;; "-gn "*) echo staff ;; *) exit 1 ;; esac`
	}
	return map[string]string{
		"uname": `case "$1" in -m) echo "${FAKE_UNAME_M:-arm64}" ;; *) echo Darwin ;; esac`,
		"id":    idBody,
		// real dscl prints the value on an indented second line when it contains a space
		"dscl": `if [ -n "${FAKE_DSCL_MULTILINE:-}" ]; then echo "NFSHomeDirectory:"; echo " ${FAKE_HOME:-/Users/alice}"; else echo "NFSHomeDirectory: ${FAKE_HOME:-/Users/alice}"; fi`,
		"docker": `[ -z "${FAKE_DOCKER_FAIL:-}" ] || exit 1
case "$1 $2" in
"context inspect") echo "${FAKE_DOCKER_ENDPOINT:-unix:///Users/alice/.docker/run/docker.sock}" ;;
"compose version") echo "Docker Compose version v2.0.0" ;;
esac`,
		"launchctl": `exit 0`,
		"sudo":      `exec "$@"`,
		"chown":     `exit 0`,
		"pkgutil":   `[ "$1" != "--pkg-info" ] || [ -n "${FAKE_RECEIPT:-}" ]`,
	}
}

func newDarwinFixture(t *testing.T, asRoot bool, versions ...string) *fixture {
	t.Helper()
	r := newRun(t)
	var dist string
	for _, v := range versions {
		dist = publishRelease(t, r, v, "darwin", nil, nil)
	}
	return fixtureFromDist(t, r, dist, versions[len(versions)-1], darwinStubs(asRoot))
}

var sudoUser = map[string]string{"SUDO_USER": "alice"}

const aliceSupport = "Users/alice/Library/Application Support/Stackstead"

func TestDarwin(t *testing.T) {
	t.Run("TargetsSudoUser", func(t *testing.T) {
		f := newDarwinFixture(t, true, "1.0.0")
		out, errOut, code := f.runEnv(sudoUser)
		mustExit(t, code, 0, out, errOut)

		assertMode(t, f.p("usr/local/bin/stackstead"), 0o755)
		assertMode(t, f.p("usr/local/share/stackstead/install.sh"), 0o755)
		assertMode(t, f.p("usr/local/share/stackstead/launchagent.plist.in"), 0o644)
		assertMode(t, f.p(aliceSupport+"/config.yaml"), 0o600)
		for _, d := range []string{aliceSupport + "/data", aliceSupport + "/stacks", "Users/alice/Library/Logs/Stackstead", "Users/alice/Library/LaunchAgents"} {
			if fi, err := os.Stat(f.p(d)); err != nil || !fi.IsDir() {
				t.Errorf("%s is not a directory: %v", d, err)
			}
		}
		if _, err := os.Stat(f.p("Users/alice/Library/LaunchAgents/" + darwinLabel + ".plist")); err != nil {
			t.Error(err)
		}
		if f.installedVersion() != "v1.0.0" {
			t.Errorf("installed version = %q", f.installedVersion())
		}
		// nothing outside the two trees of the staged root
		entries, _ := os.ReadDir(f.root)
		for _, e := range entries {
			if e.Name() != "Users" && e.Name() != "usr" {
				t.Errorf("unexpected top-level entry %s in the staged root", e.Name())
			}
		}
		// we are "root": no sudo, ownership handed to alice, the agent runs as alice in gui/501
		for _, name := range []string{"sudo", "launchctl", "chown"} {
			if l := stubLog(f.stubD, name); l != "" {
				t.Errorf("stub %s was called under --root: %q", name, l)
			}
		}
		contains(t, "stdout", out,
			"launchctl bootout gui/501/"+darwinLabel,
			"launchctl bootstrap gui/501 /Users/alice/Library/LaunchAgents/"+darwinLabel+".plist",
			"-u alice")
		notContains(t, "stdout", out, "chown")
	})

	t.Run("RefusesPlainRoot", func(t *testing.T) {
		f := newDarwinFixture(t, true, "1.0.0")
		out, errOut, code := f.run()
		mustExit(t, code, 8, out, errOut)
		contains(t, "stderr", errOut, "sudo")
		if reqs := requestsTo(f.base); len(reqs) != 0 {
			t.Errorf("refusal must happen before any download, saw %v", reqs)
		}
		out, errOut, code = f.runEnv(map[string]string{"SUDO_USER": "root"})
		mustExit(t, code, 8, out, errOut)
		out, errOut, code = f.run("--user", "bad name")
		mustExit(t, code, 2, out, errOut)
		// an explicit --user is fine for root (an admin installing for someone)
		out, errOut, code = f.run("--user", "alice")
		mustExit(t, code, 0, out, errOut)
		if _, err := os.Stat(f.p(aliceSupport + "/config.yaml")); err != nil {
			t.Error(err)
		}
	})

	t.Run("NonRootCannotTargetAnotherUser", func(t *testing.T) {
		f := newDarwinFixture(t, false, "1.0.0")
		out, errOut, code := f.run("--user", "bob")
		mustExit(t, code, 8, out, errOut)
	})

	t.Run("SelectsAssetByArch", func(t *testing.T) {
		f := newDarwinFixture(t, false, "1.0.0")
		out, errOut, code := f.runEnv(map[string]string{"FAKE_UNAME_M": "x86_64"})
		mustExit(t, code, 0, out, errOut)
		reqs := strings.Join(requestsTo(f.base), "\n")
		contains(t, "requests", reqs, "stackstead_1.0.0_darwin_amd64.tar.gz")
		notContains(t, "requests", reqs, "darwin_arm64")
	})

	t.Run("PlistHasAbsolutePaths", func(t *testing.T) {
		f := newDarwinFixture(t, true, "1.0.0")
		out, errOut, code := f.runEnv(sudoUser)
		mustExit(t, code, 0, out, errOut)
		plistPath := f.p("Users/alice/Library/LaunchAgents/" + darwinLabel + ".plist")
		plist := string(readFile(t, plistPath))
		assertWellFormedXML(t, plist)
		if runtime.GOOS == "darwin" {
			if o, err := exec.Command("plutil", "-lint", plistPath).CombinedOutput(); err != nil {
				t.Errorf("plutil -lint: %v\n%s", err, o)
			}
		}
		notContains(t, "plist", plist, "~", "$HOME", "@LABEL@", "@BIN@", "@CONFIG@", "@LOG@", "@HOME@", f.root)
		flat := regexp.MustCompile(`>\s+<`).ReplaceAllString(plist, "><")
		contains(t, "plist", flat,
			"<key>Label</key><string>"+darwinLabel+"</string>",
			"<key>ProgramArguments</key><array><string>/usr/local/bin/stackstead</string><string>serve</string><string>--config</string><string>/Users/alice/Library/Application Support/Stackstead/config.yaml</string></array>",
			"<key>RunAtLoad</key><true/>",
			"<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>",
			"<key>ThrottleInterval</key><integer>5</integer>",
			"<key>StandardOutPath</key><string>/Users/alice/Library/Logs/Stackstead/stackstead.log</string>",
			"<key>StandardErrorPath</key><string>/Users/alice/Library/Logs/Stackstead/stackstead.log</string>",
			"<key>HOME</key><string>/Users/alice</string>",
			"/opt/homebrew/bin", "/usr/local/bin", "<key>ProcessType</key><string>Background</string>")
	})

	t.Run("PlistEscapesXML", func(t *testing.T) {
		f := newDarwinFixture(t, true, "1.0.0")
		out, errOut, code := f.runEnv(map[string]string{"SUDO_USER": "alice", "FAKE_HOME": "/Users/a&b <x>"})
		mustExit(t, code, 0, out, errOut)
		plist := string(readFile(t, f.p("Users/a&b <x>/Library/LaunchAgents/"+darwinLabel+".plist")))
		assertWellFormedXML(t, plist)
		contains(t, "plist", plist, "/Users/a&amp;b &lt;x&gt;/Library/Application Support/Stackstead/config.yaml")
	})

	t.Run("ConfigUsesDetectedSocket", func(t *testing.T) {
		f := newDarwinFixture(t, true, "1.0.0")
		out, errOut, code := f.runEnv(sudoUser)
		mustExit(t, code, 0, out, errOut)
		cfg := string(readFile(t, f.p(aliceSupport+"/config.yaml")))
		contains(t, "config", cfg,
			"\nlisten: 127.0.0.1:8420\n",
			"\nstacks_dir: /Users/alice/Library/Application Support/Stackstead/stacks\n",
			"\ndata_dir: /Users/alice/Library/Application Support/Stackstead/data\n",
			"\ndocker_socket: /Users/alice/.docker/run/docker.sock\n",
			"# allowed_roots:")
		if n := strings.Count(cfg, "docker_socket:"); n != 1 {
			t.Errorf("docker_socket appears %d times:\n%s", n, cfg)
		}
		contains(t, "docker log", stubLog(f.stubD, "docker"), "context inspect --format")
		notContains(t, "stderr", errOut, "docker")
		// a socket under a path that needs YAML quoting is quoted
		f = newDarwinFixture(t, true, "1.0.0")
		out, errOut, code = f.runEnv(map[string]string{"SUDO_USER": "alice", "FAKE_DOCKER_ENDPOINT": "unix:///Users/al ice/it's: #1/docker.sock"})
		mustExit(t, code, 0, out, errOut)
		contains(t, "config", string(readFile(t, f.p(aliceSupport+"/config.yaml"))), "\ndocker_socket: '/Users/al ice/it''s: #1/docker.sock'\n")
	})

	t.Run("ConfigFallbackSocketWarns", func(t *testing.T) {
		for name, env := range map[string]map[string]string{
			"docker fails":        {"FAKE_DOCKER_FAIL": "1"},
			"not a unix endpoint": {"FAKE_DOCKER_ENDPOINT": "tcp://10.0.0.5:2375"},
			"empty endpoint":      {"FAKE_DOCKER_ENDPOINT": " "},
		} {
			f := newDarwinFixture(t, true, "1.0.0")
			env["SUDO_USER"] = "alice"
			out, errOut, code := f.runEnv(env)
			mustExit(t, code, 0, out, errOut)
			cfg := string(readFile(t, f.p(aliceSupport+"/config.yaml")))
			if !strings.Contains(cfg, "\ndocker_socket: /var/run/docker.sock\n") {
				t.Errorf("%s: fallback socket missing:\n%s", name, cfg)
			}
			if !strings.Contains(errOut, "warning:") || !strings.Contains(errOut, "/var/run/docker.sock") {
				t.Errorf("%s: no fallback warning in stderr:\n%s", name, errOut)
			}
		}
	})

	t.Run("FirstRunBootsOutThenBootstraps", func(t *testing.T) {
		f := newDarwinFixture(t, true, "1.0.0")
		out, errOut, code := f.runEnv(sudoUser)
		mustExit(t, code, 0, out, errOut)
		i, j := strings.Index(out, "launchctl bootout"), strings.Index(out, "launchctl bootstrap")
		if i < 0 || j < i {
			t.Errorf("want bootout before bootstrap:\n%s", out)
		}
		notContains(t, "stdout", out, "kickstart")
	})

	t.Run("NoStartWritesOnlyThePlist", func(t *testing.T) {
		f := newDarwinFixture(t, true, "1.0.0")
		out, errOut, code := f.runEnv(sudoUser, "--no-start")
		mustExit(t, code, 0, out, errOut)
		notContains(t, "stdout", out, "launchctl")
		if _, err := os.Stat(f.p("Users/alice/Library/LaunchAgents/" + darwinLabel + ".plist")); err != nil {
			t.Error(err)
		}
	})

	t.Run("UpgradeKickstarts", func(t *testing.T) {
		f := newDarwinFixture(t, true, "1.0.0", "1.1.0")
		out, errOut, code := f.runEnv(sudoUser, "--version", "v1.0.0")
		mustExit(t, code, 0, out, errOut)
		cfg := []byte("listen: 127.0.0.1:9999\n# edited\n")
		writeFile(t, f.p(aliceSupport+"/config.yaml"), cfg, 0o600)
		writeFile(t, f.p(aliceSupport+"/data/stackstead.db"), []byte("db"), 0o600)
		writeFile(t, f.p(aliceSupport+"/stacks/web/compose.yaml"), []byte("services: {}\n"), 0o644)

		out, errOut, code = f.runEnv(sudoUser, "--version", "v1.1.0")
		mustExit(t, code, 0, out, errOut)
		if f.installedVersion() != "v1.1.0" {
			t.Errorf("installed version = %q, want v1.1.0", f.installedVersion())
		}
		contains(t, "stdout", out, "launchctl kickstart -k gui/501/"+darwinLabel)
		notContains(t, "stdout", out, "launchctl bootstrap")
		if string(readFile(t, f.p(aliceSupport+"/config.yaml"))) != string(cfg) {
			t.Error("config was overwritten")
		}
		if string(readFile(t, f.p(aliceSupport+"/data/stackstead.db"))) != "db" || !fileExists(f.p(aliceSupport+"/stacks/web/compose.yaml")) {
			t.Error("data or stacks were touched")
		}
	})

	t.Run("UpgradeReloadsAChangedPlist", func(t *testing.T) {
		f := newDarwinFixture(t, true, "1.0.0", "1.1.0")
		out, errOut, code := f.runEnv(sudoUser, "--version", "v1.0.0")
		mustExit(t, code, 0, out, errOut)
		plist := f.p("Users/alice/Library/LaunchAgents/" + darwinLabel + ".plist")
		writeFile(t, plist, append(readFile(t, plist), []byte("<!-- stale -->\n")...), 0o644)
		out, errOut, code = f.runEnv(sudoUser, "--version", "v1.1.0")
		mustExit(t, code, 0, out, errOut)
		contains(t, "stdout", out, "launchctl bootout", "launchctl bootstrap")
		notContains(t, "plist", string(readFile(t, plist)), "stale")
	})

	t.Run("SecondRunIsNoop", func(t *testing.T) {
		f := newDarwinFixture(t, true, "1.0.0")
		out, errOut, code := f.runEnv(sudoUser)
		mustExit(t, code, 0, out, errOut)
		before := snapshot(t, f.root)
		out, errOut, code = f.runEnv(sudoUser)
		mustExit(t, code, 0, out, errOut)
		contains(t, "stdout", out, "already installed")
		notContains(t, "stdout", out, "launchctl")
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

	t.Run("UninstallAndPurge", func(t *testing.T) {
		f := newDarwinFixture(t, true, "1.0.0")
		out, errOut, code := f.runEnv(sudoUser)
		mustExit(t, code, 0, out, errOut)
		writeFile(t, f.p(aliceSupport+"/stacks/web/compose.yaml"), []byte("services: {}\n"), 0o644)
		writeFile(t, f.p("Users/alice/Library/Logs/Stackstead/stackstead.log"), []byte("log\n"), 0o644)
		plist := f.p("Users/alice/Library/LaunchAgents/" + darwinLabel + ".plist")

		// no receipt: no pkgutil --forget; Application Support and Logs survive
		out, errOut, code = f.runEnv(sudoUser, "--uninstall")
		mustExit(t, code, 0, out, errOut)
		for _, gone := range []string{plist, f.p("usr/local/bin/stackstead"), f.p("usr/local/share/stackstead")} {
			if fileExists(gone) {
				t.Errorf("%s still exists after --uninstall", gone)
			}
		}
		for _, kept := range []string{aliceSupport + "/config.yaml", aliceSupport + "/stacks/web/compose.yaml", "Users/alice/Library/Logs/Stackstead/stackstead.log"} {
			if !fileExists(f.p(kept)) {
				t.Errorf("%s was removed by --uninstall", kept)
			}
		}
		contains(t, "stdout", out, "launchctl bootout gui/501/"+darwinLabel)
		notContains(t, "stdout", out, "pkgutil --forget")
		contains(t, "stdout", out, "kept")

		// with a receipt: forgotten
		out, errOut, code = f.runEnv(map[string]string{"SUDO_USER": "alice", "FAKE_RECEIPT": "1"}, "--uninstall")
		mustExit(t, code, 0, out, errOut)
		contains(t, "stdout", out, "pkgutil --forget "+darwinLabel)

		// purge needs --yes (no terminal here); without it nothing is removed
		out, errOut, code = f.runEnv(sudoUser, "--uninstall", "--purge")
		mustExit(t, code, 2, out, errOut)
		contains(t, "stderr", errOut, "--yes", "stacks")
		if !fileExists(f.p(aliceSupport + "/stacks/web/compose.yaml")) {
			t.Error("--purge without --yes removed the stacks")
		}
		out, errOut, code = f.runEnv(sudoUser, "--uninstall", "--purge", "--yes")
		mustExit(t, code, 0, out, errOut)
		for _, gone := range []string{aliceSupport, "Users/alice/Library/Logs/Stackstead"} {
			if fileExists(f.p(gone)) {
				t.Errorf("%s still exists after --purge --yes", gone)
			}
		}
		if !fileExists(f.p("Users/alice/Library")) {
			t.Error("the user's Library itself must stay")
		}
	})

	t.Run("DryRunWritesNothing", func(t *testing.T) {
		requireNonRoot(t)
		f := newDarwinFixture(t, false, "1.0.0")
		out, errOut, code := f.run("--dry-run")
		mustExit(t, code, 0, out, errOut)
		contains(t, "plan", out, "would", "/usr/local/bin/stackstead", "/Users/alice/Library/LaunchAgents/"+darwinLabel+".plist", "launchctl")
		notContains(t, "plan", out, "systemctl")
		if entries, _ := os.ReadDir(f.root); len(entries) != 0 {
			t.Errorf("--root dir not empty after --dry-run: %v", entries)
		}
		if tmp, _ := os.ReadDir(f.tmpDir); len(tmp) != 0 {
			t.Errorf("temp dir not cleaned: %v", tmp)
		}
		for _, name := range []string{"sudo", "launchctl", "chown"} {
			if l := stubLog(f.stubD, name); l != "" {
				t.Errorf("stub %s was called in dry-run: %q", name, l)
			}
		}
		// without --root: nothing is written under the user's home or the prefix
		home, prefix := t.TempDir(), filepath.Join(t.TempDir(), "prefix")
		stubD := stubPath(t, f.stubs)
		out, errOut, code = runScript(t, []string{"--dry-run", "--prefix", prefix, "--base-url", f.base},
			map[string]string{"STUBS_DIR": stubD, "TMPDIR": f.tmpDir, "FAKE_HOME": home}, nil)
		mustExit(t, code, 0, out, errOut)
		if entries, _ := os.ReadDir(home); len(entries) != 0 {
			t.Errorf("home not empty after --dry-run: %v", entries)
		}
		if fileExists(prefix) {
			t.Error("prefix created by --dry-run")
		}
		if l := stubLog(stubD, "sudo"); l != "" {
			t.Errorf("sudo called in dry-run: %q", l)
		}
	})

	t.Run("PrintPathsShowsPerUserPaths", func(t *testing.T) {
		f := newDarwinFixture(t, true, "1.0.0")
		out, errOut, code := runScript(t, []string{"--print-paths", "--root", "/r"}, map[string]string{"STUBS_DIR": stubPath(t, f.stubs), "SUDO_USER": "alice"}, nil)
		mustExit(t, code, 0, out, errOut)
		contains(t, "paths", out, "BIN=/r/usr/local/bin/stackstead\n",
			"CONFIG=/r/Users/alice/Library/Application Support/Stackstead/config.yaml\n",
			"PLIST=/r/Users/alice/Library/LaunchAgents/"+darwinLabel+".plist\n",
			"LOG=/r/Users/alice/Library/Logs/Stackstead/stackstead.log\n")
	})

	t.Run("ExtractionDropsArchiveOwnership", func(t *testing.T) {
		// bsdtar's --help does not list --no-same-owner but accepts it; as root
		// it would otherwise restore the archive's uid/gid.
		real, err := exec.LookPath("tar")
		if err != nil {
			t.Skip("no tar")
		}
		f := newDarwinFixture(t, false, "1.0.0")
		f.stubs["tar"] = `case "$1" in --help) echo "tar(bsdtar-like): -x extract -t list"; exit 0 ;; esac
exec ` + real + ` "$@"`
		out, errOut, code := f.run()
		mustExit(t, code, 0, out, errOut)
		var extract string
		for _, l := range strings.Split(stubLog(f.stubD, "tar"), "\n") {
			if strings.Contains(l, "-xzf") {
				extract = l
			}
		}
		if !strings.Contains(extract, "--no-same-owner") {
			t.Errorf("extraction does not drop ownership; tar calls:\n%s", stubLog(f.stubD, "tar"))
		}
	})
}

// TestDarwinRealMode runs without --root against a throwaway home and prefix:
// sudo, launchctl and docker are stubs, the home comes from the dscl stub, and
// the service answers on a port of the test's own (never the demo's 8420).
func TestDarwinRealMode(t *testing.T) {
	requireNonRoot(t)
	setup := func(t *testing.T) (f *fixture, home, prefix string, env map[string]string) {
		t.Helper()
		r := newRun(t)
		dist := publishRelease(t, r, "1.0.0", "darwin", nil, nil)
		f = fixtureFromDist(t, r, dist, "1.0.0", darwinStubs(false))
		delete(f.stubs, "id") // the real id: this test runs as the real, non-root user
		home = filepath.Join(r.Dir, "home")
		if err := os.MkdirAll(home, 0o755); err != nil {
			t.Fatal(err)
		}
		prefix = filepath.Join(r.Dir, "prefix")
		env = map[string]string{"FAKE_HOME": home, "FAKE_STACKSTEAD_LOG": filepath.Join(r.Dir, "stackstead.log")}
		f.stubD = stubPath(t, f.stubs)
		return f, home, prefix, env
	}
	runReal := func(f *fixture, env map[string]string, args ...string) (string, string, int) {
		e := map[string]string{"STUBS_DIR": f.stubD, "TMPDIR": f.tmpDir}
		for k, v := range env {
			e[k] = v
		}
		return runScript(t, append([]string{"--base-url", f.base}, args...), e, nil)
	}

	t.Run("SudoOnlyForThePrefixAndReadinessProbe", func(t *testing.T) {
		f, home, prefix, env := setup(t)
		// the first probe is the installer's check that nobody else holds the port, made before it
		// starts the agent; the service "comes up" after that
		var probes atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.URL.Path == "/api/health" && probes.Add(1) > 1 {
				_, _ = io.WriteString(w, `{"ok":true}`)
				return
			}
			http.NotFound(w, req)
		}))
		t.Cleanup(srv.Close)
		u, _ := url.Parse(srv.URL)
		support := filepath.Join(home, "Library", "Application Support", "Stackstead")
		writeFile(t, filepath.Join(support, "config.yaml"), []byte("listen: "+u.Host+"\n"), 0o600)

		out, errOut, code := runReal(f, env, "--prefix", prefix)
		mustExit(t, code, 0, out, errOut)
		contains(t, "stdout", out, "sudo", "Open http://"+u.Host+"/",
			"Logs: "+filepath.Join(home, "Library", "Logs", "Stackstead", "stackstead.log"), "grep setup_token")

		lines := strings.Split(strings.TrimSpace(stubLog(f.stubD, "sudo")), "\n")
		if len(lines) < 3 {
			t.Fatalf("expected sudo to be used for the prefix, log: %q", stubLog(f.stubD, "sudo"))
		}
		for _, l := range lines {
			fields := strings.Fields(l)
			if target := fields[len(fields)-1]; !strings.HasPrefix(target, prefix) {
				t.Errorf("sudo used outside the prefix: %q", l)
			}
		}
		assertMode(t, filepath.Join(prefix, "bin", "stackstead"), 0o755)
		assertMode(t, filepath.Join(prefix, "share", "stackstead", "install.sh"), 0o755)
		plist := filepath.Join(home, "Library", "LaunchAgents", darwinLabel+".plist")
		if !fileExists(plist) {
			t.Fatal("plist missing")
		}
		uid := fmt.Sprint(os.Getuid())
		contains(t, "launchctl log", stubLog(f.stubD, "launchctl"), "bootout gui/"+uid+"/"+darwinLabel, "bootstrap gui/"+uid+" "+plist)
		contains(t, "plist", string(readFile(t, plist)), filepath.Join(prefix, "bin", "stackstead"))
		contains(t, "stackstead log", string(readFile(t, env["FAKE_STACKSTEAD_LOG"])), "health --config "+filepath.Join(support, "config.yaml"))
	})

	t.Run("NoStartStillValidatesAGeneratedConfig", func(t *testing.T) {
		f, home, prefix, env := setup(t)
		out, errOut, code := runReal(f, env, "--prefix", prefix, "--no-start")
		mustExit(t, code, 0, out, errOut)
		cfg := filepath.Join(home, "Library", "Application Support", "Stackstead", "config.yaml")
		assertMode(t, cfg, 0o600)
		contains(t, "stackstead log", string(readFile(t, env["FAKE_STACKSTEAD_LOG"])), "config validate --config "+cfg)
		if l := stubLog(f.stubD, "launchctl"); l != "" {
			t.Errorf("launchctl called with --no-start: %q", l)
		}
		notContains(t, "stderr", errOut, "config validate did not pass")
	})

	t.Run("BootstrapFailureIsAWarning", func(t *testing.T) {
		f, home, prefix, env := setup(t)
		f.stubs["launchctl"] = `[ "$1" != bootstrap ] || { echo "Bootstrap failed: 125: Domain does not support specified action" >&2; exit 125; }`
		f.stubD = stubPath(t, f.stubs)
		out, errOut, code := runReal(f, env, "--prefix", prefix)
		mustExit(t, code, 0, out, errOut)
		contains(t, "stderr", errOut, "warning:", "next login")
		if !fileExists(filepath.Join(home, "Library", "LaunchAgents", darwinLabel+".plist")) {
			t.Error("plist must still be written")
		}
	})
}

// TestScriptHardening covers install.sh fixes that are not macOS specific.
func TestScriptHardening(t *testing.T) {
	t.Run("BaseURLWithUserinfoIsRefused", func(t *testing.T) {
		for _, u := range []string{
			"http://127.0.0.1:80@evil.example.com/x",
			"http://localhost@evil.example.com",
			"https://user:pw@example.com/x",
			"https://example.com/x@y",
		} {
			f := newFixture(t, "1.0.0")
			out, errOut, code := runScript(t, []string{"--root", f.root, "--base-url", u}, map[string]string{"STUBS_DIR": stubPath(t, f.stubs), "TMPDIR": f.tmpDir}, nil)
			mustExit(t, code, 2, out, errOut)
			contains(t, "stderr", errOut, "@")
		}
	})

	t.Run("UninstallIgnoresTheBaseURL", func(t *testing.T) {
		f := newFixture(t, "1.0.0")
		out, errOut, code := f.run()
		mustExit(t, code, 0, out, errOut)
		// an unusable source in the environment must not block removal
		out, errOut, code = f.runEnv(map[string]string{"STACKSTEAD_BASE_URL": "ftp://nowhere.example.com"}, "--base-url", "ftp://nowhere.example.com", "--uninstall")
		mustExit(t, code, 0, out, errOut)
		if fileExists(f.p("usr/local/bin/stackstead")) {
			t.Error("binary still installed")
		}
	})

	t.Run("MemberLookupIsAFixedStringMatch", func(t *testing.T) {
		// "installXsh" matches the regular expression install.sh; it must not
		// be taken for the member install.sh.
		r := newRun(t)
		dist := publishRelease(t, r, "1.0.0", "linux", []string{"install.sh"}, map[string][]byte{"installXsh": []byte("x\n")})
		f := fixtureFromDist(t, r, dist, "1.0.0", defaultStubs())
		f.booted(true)
		out, errOut, code := f.run()
		mustExit(t, code, 0, out, errOut)
		contains(t, "stderr", errOut, "no install.sh")
		if f.installedVersion() != "v1.0.0" {
			t.Errorf("installed version = %q", f.installedVersion())
		}
	})

	t.Run("StacksDirGroupWritableThroughServiceUserGroupDoesNotWarn", func(t *testing.T) {
		requireNonRoot(t)
		f := newDarwinOrLinuxFixtureWithStacksDir(t)
		out, errOut, code := f.run()
		mustExit(t, code, 0, out, errOut)
		notContains(t, "stderr", errOut, "cannot write")
	})
}

// newDarwinOrLinuxFixtureWithStacksDir prepares a Linux fixture whose
// /opt/stacks is group-writable (0775) by the test user's own primary group,
// and whose stackstead user (per the id stub) is a member of that group.
func newDarwinOrLinuxFixtureWithStacksDir(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t, "1.0.0")
	if err := os.MkdirAll(f.p("opt/stacks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f.p("opt/stacks"), 0o775); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("ls", "-ld", f.p("opt/stacks")).Output()
	if err != nil {
		t.Fatal(err)
	}
	group := strings.Fields(string(out))[3]
	if group == "docker" {
		t.Skip("the test user's primary group is called docker")
	}
	f.stubs["id"] = `case "$*" in
stackstead) echo "uid=900(stackstead) gid=900(stackstead) groups=900(stackstead)" ;;
"-nG stackstead") echo "stackstead docker ` + group + `" ;;
*) exec /usr/bin/id "$@" ;;
esac`
	return f
}

func fileExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func assertWellFormedXML(t *testing.T, s string) {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(s))
	for {
		_, err := dec.Token()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatalf("plist is not well-formed XML: %v\n%s", err, s)
		}
	}
}

// rootSim runs install.sh for real (no --root) with `id -u` reporting 0, so
// the script behaves as under sudo: per-user work must go through
// `sudo -H -u <user>`. The sudo stub logs and then runs the command as the
// test's own user in a throwaway home and prefix.
func rootSim(t *testing.T) (f *fixture, home, prefix string, env map[string]string) {
	t.Helper()
	requireNonRoot(t)
	r := newRun(t)
	dist := publishRelease(t, r, "1.0.0", "darwin", nil, nil)
	stubs := darwinStubs(true)
	stubs["id"] = `case "$*" in -u) echo 0 ;; -un) echo root ;; "-u "*) exec /usr/bin/id -u ;; "-gn "*) exec /usr/bin/id -gn ;; *) exit 1 ;; esac`
	stubs["sudo"] = `while [ $# -gt 0 ]; do case "$1" in -H) shift ;; -u) shift 2 ;; *) break ;; esac; done
exec "$@"`
	f = fixtureFromDist(t, r, dist, "1.0.0", stubs)
	f.stubD = stubPath(t, f.stubs)
	home = filepath.Join(r.Dir, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	prefix = filepath.Join(r.Dir, "prefix")
	env = map[string]string{"SUDO_USER": "alice", "FAKE_HOME": home, "FAKE_STACKSTEAD_LOG": filepath.Join(r.Dir, "stackstead.log"), "STUBS_DIR": f.stubD, "TMPDIR": f.tmpDir}
	return f, home, prefix, env
}

func runRootSim(t *testing.T, f *fixture, env map[string]string, prefix string, args ...string) (string, string, int) {
	t.Helper()
	return runScript(t, append([]string{"--base-url", f.base, "--prefix", prefix, "--no-start"}, args...), env, nil)
}

// Root must never write into a user's home with its own privileges: every
// per-user file operation runs as that user, through unpredictable temp names.
func TestDarwinRootNeverActsInTheUsersHome(t *testing.T) {
	t.Run("PlantedTempSymlinksAreNotFollowed", func(t *testing.T) {
		f, home, prefix, env := rootSim(t)
		victim := filepath.Join(home, "victim")
		writeFile(t, victim, []byte("secret"), 0o600)
		agents := filepath.Join(home, "Library", "LaunchAgents")
		support := filepath.Join(home, "Library", "Application Support", "Stackstead")
		for _, p := range []string{filepath.Join(agents, darwinLabel+".plist.new"), filepath.Join(support, "config.yaml.new")} {
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(victim, p); err != nil {
				t.Fatal(err)
			}
		}
		out, errOut, code := runRootSim(t, f, env, prefix)
		mustExit(t, code, 0, out, errOut)
		if string(readFile(t, victim)) != "secret" {
			t.Error("a planted .new symlink was followed: the victim file was overwritten")
		}
		assertMode(t, victim, 0o600)
		if fi, err := os.Lstat(filepath.Join(agents, darwinLabel+".plist")); err != nil || !fi.Mode().IsRegular() {
			t.Errorf("plist is not a regular file: %v", err)
		}
	})

	t.Run("EveryPerUserWriteGoesThroughTheUser", func(t *testing.T) {
		f, home, prefix, env := rootSim(t)
		out, errOut, code := runRootSim(t, f, env, prefix)
		mustExit(t, code, 0, out, errOut)
		log := stubLog(f.stubD, "sudo")
		var sawMkdir, sawMv bool
		for _, l := range strings.Split(strings.TrimSpace(log), "\n") {
			if !strings.HasPrefix(l, "-H -u alice ") {
				t.Errorf("sudo line is not a plain 'sudo -H -u alice ...': %q", l)
			}
			if strings.HasPrefix(l, "-H -u alice mkdir -p "+filepath.Join(home, "Library", "Logs", "Stackstead")) {
				sawMkdir = true
			}
			if strings.HasPrefix(l, "-H -u alice mv -f ") && strings.HasSuffix(l, " "+filepath.Join(home, "Library", "LaunchAgents", darwinLabel+".plist")) {
				sawMv = true
			}
		}
		if !sawMkdir || !sawMv {
			t.Errorf("missing user-run mkdir/mv for the plist (mkdir=%v mv=%v):\n%s", sawMkdir, sawMv, log)
		}
		if l := stubLog(f.stubD, "chown"); l != "" {
			t.Errorf("chown must not be used: %q", l)
		}
		notContains(t, "stdout", out, "chown")
		// writes under the prefix stay with root, not the user
		assertMode(t, filepath.Join(prefix, "bin", "stackstead"), 0o755)
	})

	t.Run("DockerRunsAsTheUserWithAnExtendedPATH", func(t *testing.T) {
		f, _, prefix, env := rootSim(t)
		out, errOut, code := runRootSim(t, f, env, prefix)
		mustExit(t, code, 0, out, errOut)
		found := false
		for _, l := range strings.Split(stubLog(f.stubD, "sudo"), "\n") {
			if strings.HasPrefix(l, "-H -u alice env PATH=") && strings.Contains(l, "/opt/homebrew/bin") && strings.HasSuffix(l, " docker context inspect --format {{(index .Endpoints \"docker\").Host}}") {
				found = true
			}
		}
		if !found {
			t.Errorf("docker context inspect did not run via sudo -H -u alice env PATH=...:\n%s", stubLog(f.stubD, "sudo"))
		}
	})

	t.Run("PlantedToolsInTheHomeNeverRunAsRoot", func(t *testing.T) {
		f, home, prefix, env := rootSim(t)
		marker := filepath.Join(home, "marker")
		for _, tool := range []string{"timeout", "sha256sum", "awk", "tar", "sed"} {
			writeFile(t, filepath.Join(home, ".docker", "bin", tool), []byte("#!/bin/sh\necho "+tool+" >> "+marker+"\nexit 1\n"), 0o755)
		}
		out, errOut, code := runRootSim(t, f, env, prefix)
		mustExit(t, code, 0, out, errOut)
		if fileExists(marker) {
			t.Errorf("a tool planted in the user's home ran as root: %s", readFile(t, marker))
		}
	})
}

// sudoLines returns the sudo stub's log, one invocation per line.
func sudoLines(f *fixture) []string {
	return strings.Split(strings.TrimSpace(stubLog(f.stubD, "sudo")), "\n")
}

// hasSudoLine reports whether some `sudo` line starts with prefix and ends with suffix.
func hasSudoLine(f *fixture, prefix, suffix string) bool {
	for _, l := range sudoLines(f) {
		if strings.HasPrefix(l, prefix) && strings.HasSuffix(l, suffix) {
			return true
		}
	}
	return false
}

// Everything that touches the user's files or runs their tools goes through
// `sudo -H -u <user>`: config validate, health, reading the config and the
// plist, and uninstall/purge removals.
func TestDarwinRootSimRunsUserWorkAsTheUser(t *testing.T) {
	t.Run("ConfigValidateRunsAsTheUser", func(t *testing.T) {
		f, home, prefix, env := rootSim(t)
		out, errOut, code := runRootSim(t, f, env, prefix)
		mustExit(t, code, 0, out, errOut)
		cfg := filepath.Join(home, "Library", "Application Support", "Stackstead", "config.yaml")
		if !hasSudoLine(f, "-H -u alice env PATH=", " "+filepath.Join(prefix, "bin", "stackstead")+" config validate --config "+cfg) {
			t.Errorf("config validate did not run as 'sudo -H -u alice env PATH=... stackstead config validate --config ...':\n%s", stubLog(f.stubD, "sudo"))
		}
	})

	t.Run("HealthRunsAsTheUser", func(t *testing.T) {
		f, home, prefix, env := rootSim(t)
		var probes atomic.Int32 // the first probe is the installer's pre-start port check
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if probes.Add(1) == 1 {
				http.NotFound(w, req)
				return
			}
			_, _ = io.WriteString(w, `{"ok":true}`)
		}))
		t.Cleanup(srv.Close)
		u, _ := url.Parse(srv.URL)
		cfg := filepath.Join(home, "Library", "Application Support", "Stackstead", "config.yaml")
		writeFile(t, cfg, []byte("listen: "+u.Host+"\n"), 0o600)
		out, errOut, code := runScript(t, []string{"--base-url", f.base, "--prefix", prefix}, env, nil)
		mustExit(t, code, 0, out, errOut)
		contains(t, "stdout", out, "Open http://"+u.Host+"/")
		if !hasSudoLine(f, "-H -u alice env PATH=", " "+filepath.Join(prefix, "bin", "stackstead")+" health --config "+cfg) {
			t.Errorf("health did not run as 'sudo -H -u alice env PATH=... stackstead health --config ...':\n%s", stubLog(f.stubD, "sudo"))
		}
	})

	t.Run("ConfigAndPlistAreReadAsTheUser", func(t *testing.T) {
		f, home, prefix, env := rootSim(t)
		cfg := filepath.Join(home, "Library", "Application Support", "Stackstead", "config.yaml")
		plist := filepath.Join(home, "Library", "LaunchAgents", darwinLabel+".plist")
		for i := 0; i < 2; i++ { // the second run compares the rendered plist with the installed one
			out, errOut, code := runRootSim(t, f, env, prefix, "--force")
			mustExit(t, code, 0, out, errOut)
		}
		if !hasSudoLine(f, "-H -u alice sed ", " "+cfg) {
			t.Errorf("the config was not read as the user (sudo -H -u alice sed ... config.yaml):\n%s", stubLog(f.stubD, "sudo"))
		}
		if !hasSudoLine(f, "-H -u alice cmp ", " "+plist) {
			t.Errorf("the installed plist was not compared as the user (sudo -H -u alice cmp ... plist):\n%s", stubLog(f.stubD, "sudo"))
		}
		if !hasSudoLine(f, "-H -u alice test -e ", " "+cfg) {
			t.Errorf("the config existence check must run as the user:\n%s", stubLog(f.stubD, "sudo"))
		}
		if !hasSudoLine(f, "-H -u alice test -f ", " "+plist) {
			t.Errorf("the plist existence check must run as the user:\n%s", stubLog(f.stubD, "sudo"))
		}
	})

	t.Run("UninstallAndPurgeRemoveThroughTheUser", func(t *testing.T) {
		f, home, prefix, env := rootSim(t)
		out, errOut, code := runRootSim(t, f, env, prefix)
		mustExit(t, code, 0, out, errOut)
		support := filepath.Join(home, "Library", "Application Support", "Stackstead")
		logs := filepath.Join(home, "Library", "Logs", "Stackstead")
		plist := filepath.Join(home, "Library", "LaunchAgents", darwinLabel+".plist")
		out, errOut, code = runScript(t, []string{"--uninstall", "--purge", "--yes", "--prefix", prefix}, env, nil)
		mustExit(t, code, 0, out, errOut)
		if !hasSudoLine(f, "-H -u alice rm -f ", " "+plist) {
			t.Errorf("the plist was not removed as the user:\n%s", stubLog(f.stubD, "sudo"))
		}
		if !hasSudoLine(f, "-H -u alice rm -rf ", " "+support+" "+logs) {
			t.Errorf("the purge did not remove the support and log directories as the user:\n%s", stubLog(f.stubD, "sudo"))
		}
		for _, p := range []string{plist, support, logs, filepath.Join(prefix, "bin", "stackstead")} {
			if fileExists(p) {
				t.Errorf("%s still exists after --uninstall --purge", p)
			}
		}
	})
}

// A user-controlled PATH reaching a root run (sudo -E, a login shell) must not
// let a tool in the user's home bin run as root.
func TestDarwinRootIgnoresHomeBinInIncomingPATH(t *testing.T) {
	f, home, prefix, env := rootSim(t)
	marker := filepath.Join(home, "marker")
	bin := filepath.Join(home, "bin")
	for _, tool := range []string{"timeout", "sha256sum", "shasum", "awk", "tar", "sed", "cmp", "mktemp", "grep"} {
		writeFile(t, filepath.Join(bin, tool), []byte("#!/bin/sh\necho "+tool+" >> "+marker+"\nexit 1\n"), 0o755)
	}
	env["PATH"] = bin + ":" + os.Getenv("PATH") // after the stub dir, ahead of the system directories
	out, errOut, code := runRootSim(t, f, env, prefix)
	mustExit(t, code, 0, out, errOut)
	if fileExists(marker) {
		t.Errorf("a tool from the home bin directory ran: %s", readFile(t, marker))
	}
}

// A home with a space: dscl prints the value on a second, indented line.
func TestDarwinHomeWithASpaceFromDscl(t *testing.T) {
	f := newDarwinFixture(t, true, "1.0.0")
	f.stubD = stubPath(t, f.stubs)
	out, errOut, code := runScript(t, []string{"--print-paths", "--root", "/r"}, map[string]string{
		"STUBS_DIR": f.stubD, "SUDO_USER": "alice", "FAKE_HOME": "/Users/Jane Doe", "FAKE_DSCL_MULTILINE": "1"}, nil)
	mustExit(t, code, 0, out, errOut)
	contains(t, "paths", out, "CONFIG=/r/Users/Jane Doe/Library/Application Support/Stackstead/config.yaml")
}

// Something else already answering on the listen address must not be reported as our service.
func TestDarwinWarnsWhenAnotherProcessHoldsThePort(t *testing.T) {
	requireNonRoot(t)
	r := newRun(t)
	dist := publishRelease(t, r, "1.0.0", "darwin", nil, nil)
	f := fixtureFromDist(t, r, dist, "1.0.0", darwinStubs(false))
	delete(f.stubs, "id")
	home := filepath.Join(r.Dir, "home")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { _, _ = io.WriteString(w, `{"ok":true}`) }))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	writeFile(t, filepath.Join(home, "Library", "Application Support", "Stackstead", "config.yaml"), []byte("listen: "+u.Host+"\n"), 0o600)
	f.stubD = stubPath(t, f.stubs)
	env := map[string]string{"STUBS_DIR": f.stubD, "TMPDIR": f.tmpDir, "FAKE_HOME": home, "FAKE_STACKSTEAD_LOG": filepath.Join(r.Dir, "ss.log")}
	out, errOut, code := runScript(t, []string{"--base-url", f.base, "--prefix", filepath.Join(r.Dir, "prefix")}, env, nil)
	mustExit(t, code, 0, out, errOut)
	contains(t, "stderr", errOut, "already answering on "+u.Host)
	notContains(t, "stdout", out, "Stackstead is running")
}

func TestDarwinRefusesUIDZeroTarget(t *testing.T) {
	f := newDarwinFixture(t, true, "1.0.0")
	f.stubs["id"] = `case "$*" in -u) echo 0 ;; -un) echo root ;; "-u "*) echo 0 ;; "-gn "*) echo wheel ;; *) exit 1 ;; esac`
	out, errOut, code := f.run("--user", "alice")
	mustExit(t, code, 8, out, errOut)
	if reqs := requestsTo(f.base); len(reqs) != 0 {
		t.Errorf("refusal must precede downloads: %v", reqs)
	}
}

func TestDarwinVerification(t *testing.T) {
	t.Run("ChecksumMismatchIsRefused", func(t *testing.T) {
		f := newDarwinFixture(t, false, "1.0.0")
		fh, err := os.OpenFile(filepath.Join(f.dist, "v1.0.0", "stackstead_1.0.0_darwin_arm64.tar.gz"), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fh.WriteString("tampered")
		_ = fh.Close()
		out, errOut, code := f.run("--version", "v1.0.0")
		mustExit(t, code, 6, out, errOut)
		assertNothingStaged(t, f)
	})
	t.Run("AssetMissingFromSumsIsRefused", func(t *testing.T) {
		f := newDarwinFixture(t, false, "1.0.0")
		writeFile(t, filepath.Join(f.dist, "v1.0.0", "SHA256SUMS"), []byte(strings.Repeat("0", 64)+"  something_else.tar.gz\n"), 0o644)
		out, errOut, code := f.run("--version", "v1.0.0")
		mustExit(t, code, 6, out, errOut)
		assertNothingStaged(t, f)
	})
}

func TestDarwinPlistSubstitutesOnce(t *testing.T) {
	f := newDarwinFixture(t, true, "1.0.0")
	out, errOut, code := f.runEnv(map[string]string{"SUDO_USER": "alice", "FAKE_HOME": "/Users/a@LOG@b"})
	mustExit(t, code, 0, out, errOut)
	plist := string(readFile(t, f.p("Users/a@LOG@b/Library/LaunchAgents/"+darwinLabel+".plist")))
	contains(t, "plist", plist, "<string>/Users/a@LOG@b/Library/Application Support/Stackstead/config.yaml</string>", "<string>/Users/a@LOG@b</string>")
}

func TestStacksDirOwnedByDockerGroupDoesNotWarn(t *testing.T) {
	f := newFixture(t, "1.0.0")
	if err := os.MkdirAll(f.p("opt/stacks"), 0o775); err != nil {
		t.Fatal(err)
	}
	f.stubs["ls"] = `echo "drwxrwsr-x 2 root docker 64 Jan  1 00:00 /x"`
	out, errOut, code := f.run()
	mustExit(t, code, 0, out, errOut)
	notContains(t, "stderr", errOut, "cannot write")
}
