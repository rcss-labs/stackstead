//go:build installer && darwin

package installer_test

import (
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// Structural tests of the unsigned macOS .pkg (Plan 7 T7). Nothing here runs the
// real installer, launchctl or touches ~/Library, /usr/local or /Library: the
// package is only expanded into a temp dir, and the postinstall script runs in a
// sandbox of stubs.

const (
	pkgTestVersion = "0.0.1"
	pkgTestID      = "io.github.rcss-labs.stackstead"
)

var (
	macPkgOnce sync.Once
	macPkgFile string
	macPkgOut  string
	macPkgDist string
	macPkgErr  error
)

// builtPkg builds the pkg once per test run into a temp DIST/BIN around real
// `make binaries` output (SKIP_UI: the UI does not influence the packaging) and
// returns the path and the build output.
func builtPkg(t *testing.T) (pkg, buildOut string) {
	t.Helper()
	macPkgOnce.Do(func() {
		tmp, err := os.MkdirTemp("", "stackstead-pkgtest-")
		if err != nil {
			macPkgErr = err
			return
		}
		macPkgDist = tmp
		d := lintDirs{dist: filepath.Join(tmp, "dist"), bin: filepath.Join(tmp, "bin")}
		out, err := lintMake(t, d.args("binaries", "SKIP_UI=1", "VERSION=v"+pkgTestVersion, "ALLOW_DEV_RELEASE=1", "RELEASE_TARGETS=darwin/amd64 darwin/arm64")...)
		if err != nil {
			macPkgErr = fmt.Errorf("make binaries: %v\n%s", err, out)
			return
		}
		macPkgOut, macPkgErr = lintMake(t, d.args("pkg", "VERSION=v"+pkgTestVersion, "ALLOW_DEV_RELEASE=1")...)
		macPkgFile = filepath.Join(d.dist, "stackstead_"+pkgTestVersion+"_darwin.pkg")
	})
	if macPkgErr != nil {
		t.Fatalf("make pkg failed: %v\n%s", macPkgErr, macPkgOut)
	}
	return macPkgFile, macPkgOut
}

func init() {
	extraCleanups = append(extraCleanups, func() {
		if macPkgDist != "" {
			_ = os.RemoveAll(macPkgDist)
		}
	})
}

func runTool(t *testing.T, name string, args ...string) (string, error) {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

// expandedPkg expands the built pkg with pkgutil --expand-full and returns the
// expansion root and the component package directory inside it.
func expandedPkg(t *testing.T) (root, component string) {
	t.Helper()
	pkg, _ := builtPkg(t)
	root = filepath.Join(t.TempDir(), "expanded")
	if out, err := runTool(t, "pkgutil", "--expand-full", pkg, root); err != nil {
		t.Fatalf("pkgutil --expand-full: %v\n%s", err, out)
	}
	comps, _ := filepath.Glob(filepath.Join(root, "*.pkg"))
	if len(comps) != 1 {
		t.Fatalf("want exactly one component package in %s, got %v", root, comps)
	}
	return root, comps[0]
}

func walkFiles(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	err := filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	return files
}

func TestPkgPayloadAndScripts(t *testing.T) {
	root, comp := expandedPkg(t)
	payload := filepath.Join(comp, "Payload")

	want := []string{
		"usr/local/bin/stackstead",
		"usr/local/share/stackstead/LICENSE",
		"usr/local/share/stackstead/config.example.yaml",
		"usr/local/share/stackstead/install.sh",
		"usr/local/share/stackstead/launchagent.plist.in",
	}
	if got := walkFiles(t, payload); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("payload files =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	modes := map[string]os.FileMode{
		"usr/local/bin/stackstead":                        0o755,
		"usr/local/share/stackstead/install.sh":           0o755,
		"usr/local/share/stackstead/LICENSE":              0o644,
		"usr/local/share/stackstead/config.example.yaml":  0o644,
		"usr/local/share/stackstead/launchagent.plist.in": 0o644,
	}
	for rel, m := range modes {
		assertMode(t, filepath.Join(payload, rel), m)
	}

	// the helper files are the repository's, byte for byte
	repo := repoRoot(t)
	for rel, src := range map[string]string{
		"usr/local/share/stackstead/install.sh":           filepath.Join(repo, "install.sh"),
		"usr/local/share/stackstead/launchagent.plist.in": filepath.Join(repo, "deploy", "macos", "launchagent.plist.in"),
		"usr/local/share/stackstead/config.example.yaml":  filepath.Join(repo, "deploy", "config.example.yaml"),
		"usr/local/share/stackstead/LICENSE":              filepath.Join(repo, "LICENSE"),
	} {
		if string(readFile(t, filepath.Join(payload, rel))) != string(readFile(t, src)) {
			t.Errorf("%s differs from %s", rel, src)
		}
	}

	// universal, ad-hoc signed binary
	bin := filepath.Join(payload, "usr/local/bin/stackstead")
	if out, err := runTool(t, "lipo", "-archs", bin); err != nil || strings.TrimSpace(out) != "x86_64 arm64" {
		t.Errorf("lipo -archs = %q (%v), want \"x86_64 arm64\"", out, err)
	}
	out, err := runTool(t, "codesign", "-dv", bin)
	if err != nil {
		t.Errorf("codesign -dv: %v\n%s", err, out)
	}
	contains(t, "codesign -dv", out, "Signature=adhoc")

	// ownership recorded in the Bom: everything root:wheel
	bom, err := runTool(t, "lsbom", "-s", "-p", "MUGf", filepath.Join(comp, "Bom"))
	if err != nil {
		t.Fatalf("lsbom: %v\n%s", err, bom)
	}
	for _, line := range strings.Split(strings.TrimSpace(bom), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		if f[1] != "0" || f[2] != "0" {
			t.Errorf("bom entry not owned by root:wheel (uid gid = %s %s): %s", f[1], f[2], line)
		}
	}
	contains(t, "bom", bom, "./usr/local/bin/stackstead")

	// scripts: the postinstall and its own copy of install.sh and the templates. installer
	// extracts Scripts into a root-only temp dir, so root runs and reads nothing from
	// /usr/local/share (which a user may own) during the installation.
	scripts := walkFiles(t, filepath.Join(comp, "Scripts"))
	wantScripts := "config.example.yaml,install.sh,launchagent.plist.in,postinstall"
	if strings.Join(scripts, ",") != wantScripts {
		t.Errorf("Scripts = %v, want %s", scripts, wantScripts)
	}
	assertMode(t, filepath.Join(comp, "Scripts", "postinstall"), 0o755)
	assertMode(t, filepath.Join(comp, "Scripts", "install.sh"), 0o755)
	for name, src := range map[string]string{
		"install.sh":           filepath.Join(repo, "install.sh"),
		"launchagent.plist.in": filepath.Join(repo, "deploy", "macos", "launchagent.plist.in"),
		"config.example.yaml":  filepath.Join(repo, "deploy", "config.example.yaml"),
		"postinstall":          postinstallScript(t),
	} {
		if string(readFile(t, filepath.Join(comp, "Scripts", name))) != string(readFile(t, src)) {
			t.Errorf("Scripts/%s differs from %s", name, src)
		}
	}
	// no quarantine/provenance attribute and no AppleDouble leftovers in the payload
	for _, rel := range want {
		if out, _ := runTool(t, "xattr", filepath.Join(payload, rel)); strings.Contains(out, "com.apple.quarantine") {
			t.Errorf("%s carries a quarantine attribute: %s", rel, out)
		}
	}

	// PackageInfo
	info := string(readFile(t, filepath.Join(comp, "PackageInfo")))
	contains(t, "PackageInfo", info, `identifier="`+pkgTestID+`"`, `version="`+pkgTestVersion+`"`, `install-location="/"`)

	// Distribution
	dist := readFile(t, filepath.Join(root, "Distribution"))
	var doc struct {
		Title   string `xml:"title"`
		Welcome struct {
			File string `xml:"file,attr"`
		} `xml:"welcome"`
		Conclusion struct {
			File string `xml:"file,attr"`
		} `xml:"conclusion"`
		Options struct {
			Hosts      string `xml:"hostArchitectures,attr"`
			RootVolume string `xml:"rootVolumeOnly,attr"`
		} `xml:"options"`
		MinOS []struct {
			Min string `xml:"min,attr"`
		} `xml:"allowed-os-versions>os-version"`
		Refs []struct {
			ID      string `xml:"id,attr"`
			Version string `xml:"version,attr"`
		} `xml:"pkg-ref"`
	}
	if err := xml.Unmarshal(dist, &doc); err != nil {
		t.Fatalf("Distribution is not well-formed XML: %v\n%s", err, dist)
	}
	if doc.Title != "Stackstead" {
		t.Errorf("title = %q", doc.Title)
	}
	if doc.Welcome.File != "welcome.html" || doc.Conclusion.File != "conclusion.html" {
		t.Errorf("welcome/conclusion = %q/%q", doc.Welcome.File, doc.Conclusion.File)
	}
	if doc.Options.RootVolume != "false" {
		t.Errorf("rootVolumeOnly = %q, want false", doc.Options.RootVolume)
	}
	hosts := strings.Split(doc.Options.Hosts, ",")
	sort.Strings(hosts)
	if strings.Join(hosts, ",") != "arm64,x86_64" {
		t.Errorf("hostArchitectures = %q", doc.Options.Hosts)
	}
	if len(doc.MinOS) != 1 || doc.MinOS[0].Min != "12.0" {
		t.Errorf("allowed-os-versions = %+v, want min 12.0", doc.MinOS)
	}
	found := false
	for _, r := range doc.Refs {
		if r.ID == pkgTestID && r.Version == pkgTestVersion {
			found = true
		}
	}
	if !found {
		t.Errorf("no pkg-ref %s version %s in\n%s", pkgTestID, pkgTestVersion, dist)
	}

	// resources
	concl := string(readFile(t, filepath.Join(root, "Resources", "conclusion.html")))
	contains(t, "conclusion.html", concl, "http://127.0.0.1:8420", "sudo /usr/local/share/stackstead/install.sh --uninstall",
		"unidentified")
	if len(readFile(t, filepath.Join(root, "Resources", "welcome.html"))) == 0 {
		t.Error("welcome.html is empty")
	}
}

func TestPkgUnsignedPrintsGatekeeperNote(t *testing.T) {
	pkg, out := builtPkg(t)
	contains(t, "make pkg output", out, "Gatekeeper", "unidentified", "right-click", "sudo installer -pkg", "spctl")
	notContains(t, "make pkg output", out, "notarytool")

	sig, _ := runTool(t, "pkgutil", "--check-signature", pkg)
	if !strings.Contains(strings.ToLower(sig), "no signature") {
		t.Errorf("pkgutil --check-signature should report no signature:\n%s", sig)
	}
	// the expected rejection of an unsigned package; with assessments disabled on this
	// host (spctl --status says so) the verdict is "accepted" by override, but the
	// reason is still "no usable signature"
	spctl, err := runTool(t, "spctl", "--assess", "--type", "install", "-v", pkg)
	contains(t, "spctl", spctl, "no usable signature")
	if status, _ := runTool(t, "spctl", "--status"); strings.Contains(status, "assessments enabled") {
		if err == nil {
			t.Errorf("spctl accepted an unsigned pkg:\n%s", spctl)
		}
		contains(t, "spctl", spctl, "rejected")
	} else {
		t.Logf("Gatekeeper assessments are disabled on this host; only the no-usable-signature reason is asserted:\n%s", spctl)
	}
}

func TestPkgPartialIdentityFails(t *testing.T) {
	for name, env := range map[string][]string{
		"app only":       {"MACOS_APP_IDENTITY=Developer ID Application: X"},
		"installer only": {"MACOS_INSTALLER_IDENTITY=Developer ID Installer: X"},
		"notary only":    {"NOTARY_PROFILE=prof"},
		"two of three":   {"MACOS_APP_IDENTITY=a", "NOTARY_PROFILE=prof"},
	} {
		t.Run(name, func(t *testing.T) {
			tmp := t.TempDir()
			dist := filepath.Join(tmp, "dist")
			cmd := exec.Command("make", "pkg", "DIST="+dist, "BIN="+filepath.Join(tmp, "bin"), "VERSION=v"+pkgTestVersion, "ALLOW_DEV_RELEASE=1")
			cmd.Dir = repoRoot(t)
			cmd.Env = append(os.Environ(), env...)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("make pkg succeeded with a partial identity set:\n%s", out)
			}
			contains(t, "output", string(out), "MACOS_APP_IDENTITY", "MACOS_INSTALLER_IDENTITY", "NOTARY_PROFILE", "all three")
			if pkgs, _ := filepath.Glob(filepath.Join(dist, "*.pkg")); len(pkgs) != 0 {
				t.Errorf("a pkg was produced: %v", pkgs)
			}
		})
	}
}

func TestPkgTargetIsMacOnlyAndDistSkipsElsewhere(t *testing.T) {
	root := repoRoot(t)
	run := func(args ...string) (string, error) {
		cmd := exec.Command("make", args...)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := run("pkg", "UNAME_S=Linux", "DIST="+t.TempDir())
	if err == nil {
		t.Errorf("make pkg succeeded on a non-macOS host:\n%s", out)
	}
	contains(t, "make pkg (Linux)", out, "macOS")

	out, err = run("-n", "dist", "UNAME_S=Linux", "VERSION=v0.0.1", "ALLOW_DEV_RELEASE=1")
	if err != nil {
		t.Fatalf("make -n dist (Linux): %v\n%s", err, out)
	}
	notContains(t, "dist (Linux)", out, "pkgbuild")
	contains(t, "dist (Linux)", out, "skipping the macOS .pkg")

	out, err = run("-n", "dist", "UNAME_S=Darwin", "VERSION=v0.0.1", "ALLOW_DEV_RELEASE=1")
	if err != nil {
		t.Fatalf("make -n dist (Darwin): %v\n%s", err, out)
	}
	contains(t, "dist (Darwin)", out, "pkgbuild")
	// pkg runs after packages and before release (which writes SHA256SUMS)
	ip, ik, ir := strings.Index(out, "nfpm"), strings.Index(out, "pkgbuild"), strings.Index(out, "-dist ")
	if !(ip >= 0 && ip < ik && ik < ir) {
		t.Errorf("want packages (nfpm) < pkg (pkgbuild) < release (mkrelease) in dist order, got %d %d %d", ip, ik, ir)
	}
}

// ---------------------------------------------------------------- postinstall sandbox

func postinstallScript(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "deploy", "macos", "scripts", "postinstall")
}

type postinstallRun struct {
	out, errOut string
	code        int
	stubs       string // directory of logging stubs (also the PATH seam directory)
	dir         string // the "scripts dir": postinstall and its sibling stub install.sh
	helperLog   string
	helperEnv   string
}

// runPostinstall runs a copy of the script as the invoking (non-root) user next
// to a stub install.sh (the real one is shipped beside it in the pkg). The PATH
// seam is honoured only when not root, so it cannot redirect a real installer
// run; assumeRoot sets the test-only "behave as root" switch (which can only
// restrict). consoleUser is what `stat -f %Su /dev/console` reports; helperExit
// is the stub install.sh's exit status.
func runPostinstall(t *testing.T, volume, consoleUser string, uid0 bool, helperExit int, assumeRoot bool) postinstallRun {
	t.Helper()
	dir := t.TempDir()
	stubs := stubPath(t, map[string]string{
		"stat":      fmt.Sprintf(`echo '%s'`, consoleUser),
		"id":        fmt.Sprintf(`case "$1" in -u) if [ -n "$2" ] && [ %t = true ]; then echo 0; elif [ -n "$2" ]; then echo 501; else echo 4242; fi ;; *) exit 1 ;; esac`, uid0),
		"launchctl": `exit 0`,
		"sudo":      `exit 0`,
		"env":       `exit 0`,
	})
	writeFile(t, filepath.Join(dir, "postinstall"), readFile(t, postinstallScript(t)), 0o755)
	writeFile(t, filepath.Join(dir, "install.sh"), []byte(fmt.Sprintf("#!/bin/sh\necho \"$*\" >> %q\n/usr/bin/env > %q\nexit %d\n", filepath.Join(dir, "helper.log"), filepath.Join(dir, "helper.env"), helperExit)), 0o755)
	cmd := exec.Command("/bin/sh", filepath.Join(dir, "postinstall"), "/tmp/fake.pkg", volume+"/", volume, volume)
	cmd.Env = []string{
		"PATH=/usr/bin:/bin",
		"STACKSTEAD_PKG_PATH=" + stubs + ":/usr/bin:/bin",
		"TMPDIR=/evil-tmp",
		"SUDO_USER=mallory",
		"HOME=" + t.TempDir(),
	}
	if assumeRoot {
		cmd.Env = append(cmd.Env, "STACKSTEAD_PKG_ASSUME_ROOT=1")
	}
	var so, se strings.Builder
	cmd.Stdout, cmd.Stderr = &so, &se
	code := 0
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		code = ee.ExitCode()
	}
	hl, _ := os.ReadFile(filepath.Join(dir, "helper.log"))
	he, _ := os.ReadFile(filepath.Join(dir, "helper.env"))
	return postinstallRun{so.String(), se.String(), code, stubs, dir, string(hl), string(he)}
}

func TestPostinstallStagesOnlyOnTestVolume(t *testing.T) {
	r := runPostinstall(t, "/Volumes/sstest", "alice", false, 0, false)
	mustExit(t, r.code, 0, r.out, r.errOut)
	contains(t, "stdout", r.out, "staged only")
	if r.helperLog != "" {
		t.Errorf("install.sh was run for a non-boot volume: %q", r.helperLog)
	}
	for _, name := range []string{"launchctl", "sudo", "stat"} {
		if l := stubLog(r.stubs, name); l != "" {
			t.Errorf("%s was called: %q", name, l)
		}
	}
}

func TestPostinstallNeedsARegularConsoleUser(t *testing.T) {
	for name, user := range map[string]string{
		"root":                 "root",
		"empty":                "",
		"setup assistant":      "_mbsetupuser",
		"option-like":          "-evil",
		"shell metacharacters": "a;b",
		"space":                "al ice",
	} {
		t.Run(name, func(t *testing.T) {
			r := runPostinstall(t, "/", user, false, 0, false)
			mustExit(t, r.code, 0, r.out, r.errOut) // never fail the installation
			contains(t, "stdout", r.out, "install.sh --from-pkg --user")
			if r.helperLog != "" {
				t.Errorf("install.sh ran for console user %q: %q", user, r.helperLog)
			}
		})
	}
}

func TestPostinstallRefusesUIDZeroUser(t *testing.T) {
	r := runPostinstall(t, "/", "toor", true, 0, false)
	mustExit(t, r.code, 0, r.out, r.errOut)
	contains(t, "stdout", r.out, "install.sh --from-pkg --user")
	if r.helperLog != "" {
		t.Errorf("install.sh ran for a uid 0 account: %q", r.helperLog)
	}
}

func TestPostinstallDelegatesWithCleanEnvironment(t *testing.T) {
	r := runPostinstall(t, "/", "alice", false, 0, false)
	mustExit(t, r.code, 0, r.out, r.errOut)
	// the sibling install.sh and the templates next to it, never anything under /usr/local/share
	want := "--from-pkg --payload-dir " + evalDir(t, r.dir) + " --user alice"
	if got := strings.TrimSpace(r.helperLog); got != want {
		t.Errorf("install.sh args = %q, want %q", got, want)
	}
	// the installer's environment (a user-influenced TMPDIR, SUDO_USER, HOME) is not passed on
	notContains(t, "helper env", r.helperEnv, "TMPDIR=/evil-tmp", "SUDO_USER=mallory", "HOME=")
	contains(t, "helper env", r.helperEnv, "PATH=")
}

func TestPostinstallToleratesAFailingHelper(t *testing.T) {
	r := runPostinstall(t, "/", "alice", false, 7, false)
	mustExit(t, r.code, 0, r.out, r.errOut)
	contains(t, "stderr", r.errOut, "--from-pkg --user alice", "7")
}

func evalDir(t *testing.T, d string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(d)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// As root the PATH seam must be ignored: the stubs in it (stat, id, env, ...)
// are never called, and the helper gets the fixed system PATH.
func TestPostinstallIgnoresSandboxOverridesAsRoot(t *testing.T) {
	r := runPostinstall(t, "/", "alice", false, 0, true)
	mustExit(t, r.code, 0, r.out, r.errOut)
	for _, name := range []string{"stat", "id", "env", "launchctl", "sudo"} {
		if l := stubLog(r.stubs, name); l != "" {
			t.Errorf("stub %s from the override PATH was called as root: %q", name, l)
		}
	}
	if r.helperLog != "" {
		notContains(t, "helper env", r.helperEnv, r.stubs)
		contains(t, "helper env", r.helperEnv, "PATH=/usr/bin:/bin:/usr/sbin:/sbin")
	}
}

// As a non-root user the seam works (so the tests above exercise the real script).
func TestPostinstallPathSeamWorksWhenNotRoot(t *testing.T) {
	r := runPostinstall(t, "/", "alice", false, 0, false)
	mustExit(t, r.code, 0, r.out, r.errOut)
	if stubLog(r.stubs, "stat") == "" {
		t.Error("the PATH seam stub was not used")
	}
}

func TestShellcheckPostinstall(t *testing.T) {
	script := postinstallScript(t)
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
	name := "stackstead-itest-" + r.ID + "-shellcheck-pkg"
	t.Cleanup(func() { _ = exec.Command(docker, "rm", "-f", name).Run() })
	out, err := exec.Command(docker, "run", "--rm", "--name", name, "--label", "stackstead.test="+r.ID,
		"-v", filepath.Dir(script)+":/mnt:ro", image, "--shell=sh", "--severity=style", "/mnt/postinstall").CombinedOutput()
	if err != nil {
		t.Fatalf("shellcheck:\n%s", out)
	}
}

// TestPkgInstallOnTestVolume installs onto a separately mounted volume. It is
// skipped unless STACKSTEAD_PKG_TEST_VOLUME names one (see RELEASING.md): it
// runs `sudo installer`, which is never done implicitly.
// requireScratchVolume refuses anything that is, or lives on the same device as,
// the boot volume or its data volume: /Volumes/Macintosh HD is a symlink to /.
func requireScratchVolume(t *testing.T, vol string) {
	t.Helper()
	real, err := filepath.EvalSymlinks(vol)
	if err != nil {
		t.Fatalf("STACKSTEAD_PKG_TEST_VOLUME %q: %v", vol, err)
	}
	if !strings.HasPrefix(real, "/Volumes/") {
		t.Fatalf("STACKSTEAD_PKG_TEST_VOLUME must resolve to a mounted volume under /Volumes, got %q", real)
	}
	vs, err := os.Stat(real)
	if err != nil {
		t.Fatal(err)
	}
	for _, sys := range []string{"/", "/System/Volumes/Data"} {
		ss, err := os.Stat(sys)
		if err != nil {
			continue
		}
		if os.SameFile(vs, ss) {
			t.Fatalf("%q is the system volume (%s); refusing", vol, sys)
		}
		a, aok := vs.Sys().(*syscall.Stat_t)
		b, bok := ss.Sys().(*syscall.Stat_t)
		if aok && bok && a.Dev == b.Dev {
			t.Fatalf("%q is on the same device as %s; refusing", vol, sys)
		}
	}
}

func TestPkgInstallOnTestVolume(t *testing.T) {
	vol := os.Getenv("STACKSTEAD_PKG_TEST_VOLUME")
	if vol == "" {
		t.Skip("set STACKSTEAD_PKG_TEST_VOLUME to a mounted scratch volume (not / and not a real volume) to run this")
	}
	requireScratchVolume(t, vol)
	pkg, _ := builtPkg(t)
	t.Cleanup(func() { _, _ = runTool(t, "sudo", "pkgutil", "--forget", pkgTestID, "--volume", vol) })
	out, err := runTool(t, "sudo", "installer", "-pkg", pkg, "-target", vol, "-dumplog")
	if err != nil {
		t.Fatalf("installer: %v\n%s", err, out)
	}
	contains(t, "installer -dumplog", out, "staged only")
	if _, err := os.Stat(filepath.Join(vol, "usr/local/bin/stackstead")); err != nil {
		t.Errorf("binary did not land under the volume: %v", err)
	}
	out, _ = runTool(t, "pkgutil", "--pkgs", "--volume", vol)
	contains(t, "pkgutil --pkgs", out, pkgTestID)
}

// ---------------------------------------------------------------- install.sh --from-pkg

// fromPkgFixture stages what the .pkg payload installs under --root, plus
// logging network stubs, and returns the fixture (alice, running as root).
func fromPkgFixture(t *testing.T) *fixture {
	t.Helper()
	r := newRun(t)
	stubs := darwinStubs(true)
	stubs["curl"] = `exit 22`
	stubs["wget"] = `exit 4`
	f := &fixture{t: t, r: r, stubs: stubs, root: filepath.Join(r.Dir, "root"), tmpDir: filepath.Join(r.Dir, "tmp"), base: "http://127.0.0.1:1"}
	for _, d := range []string{f.root, f.tmpDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	repo := repoRoot(t)
	writeFile(t, f.p("usr/local/bin/stackstead"), readFile(t, buildBinary(t, "darwin", "arm64", "1.0.0")), 0o755)
	helper := "usr/local/share/stackstead/"
	writeFile(t, f.p(helper+"install.sh"), readFile(t, installScript(t)), 0o755)
	writeFile(t, f.p(helper+"launchagent.plist.in"), readFile(t, filepath.Join(repo, "deploy", "macos", "launchagent.plist.in")), 0o644)
	writeFile(t, f.p(helper+"config.example.yaml"), readFile(t, filepath.Join(repo, "deploy", "config.example.yaml")), 0o644)
	writeFile(t, f.p(helper+"LICENSE"), []byte("AGPL\n"), 0o644)
	return f
}

func TestInstallShFromPkg(t *testing.T) {
	plist := "Users/alice/Library/LaunchAgents/" + darwinLabel + ".plist"

	t.Run("FreshInstallForTheNamedUserWithoutDownloading", func(t *testing.T) {
		f := fromPkgFixture(t)
		binBefore := readFile(t, f.p("usr/local/bin/stackstead"))
		out, errOut, code := f.run("--from-pkg", "--user", "alice")
		mustExit(t, code, 0, out, errOut)
		for _, name := range []string{"curl", "wget"} {
			if l := stubLog(f.stubD, name); l != "" {
				t.Errorf("%s was called: %q", name, l)
			}
		}
		if string(readFile(t, f.p("usr/local/bin/stackstead"))) != string(binBefore) {
			t.Error("the installed binary was replaced")
		}
		assertMode(t, f.p(aliceSupport+"/config.yaml"), 0o600)
		contains(t, "config", string(readFile(t, f.p(aliceSupport+"/config.yaml"))), "docker_socket: /Users/alice/.docker/run/docker.sock")
		contains(t, "plist", string(readFile(t, f.p(plist))), "<string>/usr/local/bin/stackstead</string>", "/Users/alice/Library/Logs/Stackstead/stackstead.log")
		contains(t, "stdout", out, "-u alice", "launchctl bootout gui/501/"+darwinLabel, "launchctl bootstrap gui/501 /Users/alice/Library/LaunchAgents/"+darwinLabel+".plist")
		notContains(t, "stdout", out, "kickstart")
	})

	t.Run("ExistingAgentIsKickstartedAndConfigKept", func(t *testing.T) {
		f := fromPkgFixture(t)
		out, errOut, code := f.run("--from-pkg", "--user", "alice")
		mustExit(t, code, 0, out, errOut)
		cfg := []byte("listen: 127.0.0.1:9999\n# edited\n")
		writeFile(t, f.p(aliceSupport+"/config.yaml"), cfg, 0o600)
		out, errOut, code = f.run("--from-pkg", "--user", "alice")
		mustExit(t, code, 0, out, errOut)
		contains(t, "stdout", out, "launchctl kickstart -k gui/501/"+darwinLabel)
		notContains(t, "stdout", out, "launchctl bootstrap")
		if string(readFile(t, f.p(aliceSupport+"/config.yaml"))) != string(cfg) {
			t.Error("config was overwritten")
		}
	})

	t.Run("RefusesRootAndUnknownTargets", func(t *testing.T) {
		f := fromPkgFixture(t)
		out, errOut, code := f.run("--from-pkg")
		mustExit(t, code, 8, out, errOut) // plain root without --user
		out, errOut, code = f.run("--from-pkg", "--user", "root")
		mustExit(t, code, 8, out, errOut)
		out, errOut, code = f.run("--from-pkg", "--uninstall", "--user", "alice")
		mustExit(t, code, 2, out, errOut)
		out, errOut, code = f.run("--from-pkg", "--version", "v1.0.0", "--user", "alice")
		mustExit(t, code, 2, out, errOut)
		if fileExists(f.p(aliceSupport)) {
			t.Error("something was written for a refused run")
		}
	})

	t.Run("IncompletePayloadFailsBeforeWriting", func(t *testing.T) {
		f := fromPkgFixture(t)
		if err := os.Remove(f.p("usr/local/share/stackstead/config.example.yaml")); err != nil {
			t.Fatal(err)
		}
		out, errOut, code := f.run("--from-pkg", "--user", "alice")
		mustExit(t, code, 7, out, errOut)
		contains(t, "stderr", errOut, "config.example.yaml")
		if fileExists(f.p(aliceSupport)) {
			t.Error("something was written for an incomplete payload")
		}
	})

	t.Run("NoStartWritesButDoesNotLoad", func(t *testing.T) {
		f := fromPkgFixture(t)
		out, errOut, code := f.run("--from-pkg", "--user", "alice", "--no-start")
		mustExit(t, code, 0, out, errOut)
		notContains(t, "stdout", out, "launchctl")
		if !fileExists(f.p(plist)) {
			t.Error("plist missing")
		}
	})
}

// fromPkgSim runs install.sh --from-pkg for real (no --root) with uid 0 (rootSim's
// sudo stub logs every `sudo -H -u alice ...` and runs it as the test user), with
// the payload staged under a throwaway prefix.
func fromPkgSim(t *testing.T) (f *fixture, home, prefix string, env map[string]string) {
	t.Helper()
	f, home, prefix, env = rootSim(t)
	share := filepath.Join(prefix, "share", "stackstead")
	repo := repoRoot(t)
	writeFile(t, filepath.Join(prefix, "bin", "stackstead"), readFile(t, buildBinary(t, "darwin", "arm64", "1.0.0")), 0o755)
	writeFile(t, filepath.Join(share, "launchagent.plist.in"), readFile(t, filepath.Join(repo, "deploy", "macos", "launchagent.plist.in")), 0o644)
	writeFile(t, filepath.Join(share, "config.example.yaml"), readFile(t, filepath.Join(repo, "deploy", "config.example.yaml")), 0o644)
	return f, home, prefix, env
}

func runFromPkgSim(t *testing.T, f *fixture, env map[string]string, prefix string, args ...string) (string, string, int) {
	t.Helper()
	return runScript(t, append([]string{"--from-pkg", "--prefix", prefix, "--no-start"}, args...), env, nil)
}

// A payload directory that a user may own (/usr/local/share on an Intel Homebrew
// Mac) is read as that user, never by root: a swapped directory or a hard link to
// a root-only file cannot be turned into root-written config.
func TestFromPkgReadsUserControlledTemplatesAsTheUser(t *testing.T) {
	f, _, prefix, env := fromPkgSim(t)
	out, errOut, code := runFromPkgSim(t, f, env, prefix)
	mustExit(t, code, 0, out, errOut)
	log := stubLog(f.stubD, "sudo")
	for _, tpl := range []string{"launchagent.plist.in", "config.example.yaml"} {
		want := "-H -u alice cat " + filepath.Join(prefix, "share", "stackstead", tpl)
		if !strings.Contains(log, want) {
			t.Errorf("template %s was not read as the user (want %q in the sudo log):\n%s", tpl, want, log)
		}
	}
}

// With --payload-dir (the pkg's root-only scripts dir) root reads the templates
// itself, from that dir and nowhere else.
func TestFromPkgPayloadDirIsUsedInsteadOfThePrefix(t *testing.T) {
	f, home, prefix, env := fromPkgSim(t)
	dir := t.TempDir()
	repo := repoRoot(t)
	tpl := string(readFile(t, filepath.Join(repo, "deploy", "macos", "launchagent.plist.in")))
	writeFile(t, filepath.Join(dir, "launchagent.plist.in"), []byte(tpl+"<!-- from-payload-dir -->\n"), 0o644)
	writeFile(t, filepath.Join(dir, "config.example.yaml"), readFile(t, filepath.Join(repo, "deploy", "config.example.yaml")), 0o644)
	// poison the prefix copy: it must not be read
	writeFile(t, filepath.Join(prefix, "share", "stackstead", "launchagent.plist.in"), []byte("POISON @LABEL@\n"), 0o644)
	out, errOut, code := runFromPkgSim(t, f, env, prefix, "--payload-dir", dir)
	mustExit(t, code, 0, out, errOut)
	plist := string(readFile(t, filepath.Join(home, "Library", "LaunchAgents", darwinLabel+".plist")))
	contains(t, "plist", plist, "from-payload-dir")
	notContains(t, "plist", plist, "POISON")
	if strings.Contains(stubLog(f.stubD, "sudo"), " cat "+prefix) {
		t.Errorf("templates were read through the user although a payload dir was given:\n%s", stubLog(f.stubD, "sudo"))
	}

	// a symlinked or missing template in the payload dir is refused
	if err := os.Remove(filepath.Join(dir, "config.example.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hosts", filepath.Join(dir, "config.example.yaml")); err != nil {
		t.Fatal(err)
	}
	out, errOut, code = runFromPkgSim(t, f, env, prefix, "--payload-dir", dir)
	mustExit(t, code, 7, out, errOut)
	// the flag needs --from-pkg and an absolute path
	out, errOut, code = runScript(t, []string{"--payload-dir", dir, "--prefix", prefix}, env, nil)
	mustExit(t, code, 2, out, errOut)
	out, errOut, code = runFromPkgSim(t, f, env, prefix, "--payload-dir", "relative")
	mustExit(t, code, 2, out, errOut)
}

func TestFromPkgRefusedOnLinux(t *testing.T) {
	f := newFixture(t, "1.0.0")
	out, errOut, code := f.run("--from-pkg")
	mustExit(t, code, 2, out, errOut)
	contains(t, "stderr", errOut, "macOS")
}

func TestHelpMentionsFromPkg(t *testing.T) {
	out, errOut, code := runScript(t, []string{"--help"}, nil, defaultStubs())
	mustExit(t, code, 0, out, errOut)
	contains(t, "help", out, "--from-pkg")
}

// A bin/ left over from another version must not be packaged.
func TestPkgRefusesStaleBinaries(t *testing.T) {
	tmp := t.TempDir()
	d := lintDirs{dist: filepath.Join(tmp, "dist"), bin: filepath.Join(tmp, "bin")}
	buildBinaries(t, d, "v0.0.1", "darwin/amd64 darwin/arm64")
	out, err := lintMake(t, d.args("pkg", "VERSION=v0.0.2", "ALLOW_DEV_RELEASE=1")...)
	if err == nil {
		t.Fatalf("make pkg accepted binaries built for another version:\n%s", out)
	}
	contains(t, "output", out, "stale", "0.0.1", "0.0.2")
	if pkgs, _ := filepath.Glob(filepath.Join(d.dist, "*.pkg")); len(pkgs) != 0 {
		t.Errorf("a pkg was produced: %v", pkgs)
	}
}
