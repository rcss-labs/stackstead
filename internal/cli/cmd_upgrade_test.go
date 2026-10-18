package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/update"
	"github.com/rcss-labs/stackstead/internal/version"
)

type exitCoder struct{ code int }

func (e exitCoder) Error() string { return fmt.Sprintf("exit status %d", e.code) }
func (e exitCoder) ExitCode() int { return e.code }

type upgradeRun struct {
	name      string
	args, env []string
	dirExists bool // temp dir existed when the runner was called
	dirMode   os.FileMode
	scriptRaw string // contents of the script argument, for script plans
}

// upgradeRig is a fake release source plus every seam of the upgrade command.
type upgradeRig struct {
	t   *testing.T
	te  *testEnv
	srv *httptest.Server

	latest    string // version in the latest SHA256SUMS ("0.3.0")
	down      bool   // sums answer 500
	corrupt   map[string]bool
	omit      map[string]bool
	assetFail bool
	preexist  string // file name created in the temp dir beforehand (makes the download fail locally)
	ctx       context.Context

	hits, assetHits atomic.Int32
	runs            []upgradeRun
	runnerErr       error
	tmpDirs         []string

	exe, goos string
	exists    map[string]bool
	dnf       bool
	locked    bool
	euid      int
	vars      map[string]string
}

func assetBody(name string) []byte { return []byte("asset:" + name) }

func newUpgradeRig(t *testing.T) *upgradeRig {
	t.Helper()
	r := &upgradeRig{
		t: t, latest: "0.3.0", corrupt: map[string]bool{}, omit: map[string]bool{},
		exe: "/usr/local/bin/stackstead", goos: "linux", exists: map[string]bool{}, euid: 0,
		vars: map[string]string{},
	}
	r.srv = httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(r.srv.Close)

	oldExe, oldExists, oldLook, oldLocked, oldRunner, oldTmp, oldOS, oldArch, oldEuid, oldVer :=
		upgradeExecutable, upgradeExists, upgradeLookPath, upgradeLocked, upgradeRunner, upgradeTempDir,
		upgradeGOOS, upgradeGOARCH, geteuid, version.Version
	t.Cleanup(func() {
		upgradeExecutable, upgradeExists, upgradeLookPath, upgradeLocked, upgradeRunner, upgradeTempDir =
			oldExe, oldExists, oldLook, oldLocked, oldRunner, oldTmp
		upgradeGOOS, upgradeGOARCH, geteuid, version.Version = oldOS, oldArch, oldEuid, oldVer
	})
	version.Version = "v0.2.0"
	upgradeExecutable = func() string { return r.exe }
	upgradeExists = func(p string) bool { return r.exists[p] }
	upgradeLookPath = func(n string) (string, error) {
		if n == "dnf" && r.dnf {
			return "/usr/bin/dnf", nil
		}
		return "", errors.New("not found")
	}
	upgradeLocked = func() (bool, error) { return r.locked, nil }
	upgradeGOOS, upgradeGOARCH = "linux", "amd64"
	geteuid = func() int { return r.euid }
	parent := t.TempDir()
	upgradeTempDir = func() (string, error) {
		d := filepath.Join(parent, fmt.Sprintf("up%d", len(r.tmpDirs)))
		r.tmpDirs = append(r.tmpDirs, d)
		if err := os.Mkdir(d, 0o700); err != nil { // like os.MkdirTemp
			return "", err
		}
		if r.preexist != "" {
			if err := os.WriteFile(filepath.Join(d, r.preexist), []byte("x"), 0o644); err != nil {
				return "", err
			}
		}
		return d, nil
	}
	upgradeRunner = func(_ context.Context, name string, args, env []string) error {
		run := upgradeRun{name: name, args: slices.Clone(args), env: slices.Clone(env)}
		if len(r.tmpDirs) > 0 {
			_, err := os.Stat(r.tmpDirs[len(r.tmpDirs)-1])
			run.dirExists = err == nil
			if fi, err := os.Stat(r.tmpDirs[len(r.tmpDirs)-1]); err == nil {
				run.dirMode = fi.Mode().Perm()
			}
		}
		if name == "/bin/sh" && len(args) > 0 {
			b, _ := os.ReadFile(args[0])
			run.scriptRaw = string(b)
		}
		r.runs = append(r.runs, run)
		return r.runnerErr
	}
	return r
}

func (r *upgradeRig) layout(kind update.Kind) {
	switch kind {
	case update.KindDeb:
		r.exe = "/usr/bin/stackstead"
		r.exists["/var/lib/dpkg/info/stackstead.list"] = true
	case update.KindRPM:
		r.exe = "/usr/bin/stackstead"
		r.exists["/var/lib/rpm"] = true
	case update.KindPkg:
		r.goos = "darwin"
		upgradeGOOS = "darwin"
		r.exists["/var/db/receipts/io.github.rcss-labs.stackstead.bom"] = true
	case update.KindScript:
	}
}

func (r *upgradeRig) darwin() { r.goos = "darwin"; upgradeGOOS = "darwin" }

func (r *upgradeRig) assetNames(ver string) []string {
	return []string{
		"install.sh", "stackstead_" + ver + "_amd64.deb", "stackstead-" + ver + "-1.x86_64.rpm",
		"stackstead_" + ver + "_darwin.pkg", "stackstead_" + ver + "_linux_amd64.tar.gz",
	}
}

func (r *upgradeRig) sums(ver string) string {
	var b strings.Builder
	for _, n := range r.assetNames(ver) {
		if r.omit[n] {
			continue
		}
		h := sha256.Sum256(assetBody(n))
		fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(h[:]), n)
	}
	return b.String()
}

func (r *upgradeRig) serve(w http.ResponseWriter, req *http.Request) {
	r.hits.Add(1)
	p := req.URL.Path
	switch {
	case p == "/releases/latest/download/SHA256SUMS":
		if r.down {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, r.sums(r.latest))
	case strings.HasPrefix(p, "/releases/download/v"):
		rest := strings.TrimPrefix(p, "/releases/download/v")
		ver, name, _ := strings.Cut(rest, "/")
		if name == "SHA256SUMS" {
			if r.down {
				http.Error(w, "down", http.StatusInternalServerError)
				return
			}
			fmt.Fprint(w, r.sums(ver))
			return
		}
		r.assetHits.Add(1)
		if r.assetFail {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		if r.corrupt[name] {
			w.Write([]byte("tampered"))
			return
		}
		w.Write(assetBody(name))
	default:
		http.NotFound(w, req)
	}
}

// run executes `stackstead upgrade <args>`.
func (r *upgradeRig) run(args ...string) (code int, msg string) {
	r.t.Helper()
	if r.te == nil {
		r.te = newTestEnv(r.t, r.vars)
	}
	ctx := r.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	err := Run(ctx, rootCommand(), append([]string{"upgrade"}, args...), r.te.Env)
	if err != nil {
		msg = err.Error()
	}
	return codeFor(err), msg
}

func (r *upgradeRig) terminal(stdin string) {
	r.te = newTestEnv(r.t, r.vars)
	r.te.Env.IsTerminal = func(uintptr) bool { return true }
	r.te.Env.Stdin = strings.NewReader(stdin)
}

func (r *upgradeRig) args(extra ...string) []string {
	return append([]string{"--base-url", r.srv.URL}, extra...)
}

func (r *upgradeRig) wantExit(want int, args ...string) string {
	r.t.Helper()
	code, msg := r.run(args...)
	if code != want {
		r.t.Fatalf("exit = %d (%q), want %d\nstdout: %s\nstderr: %s", code, msg, want, r.te.Out.String(), r.te.Err.String())
	}
	return msg + "\n" + r.te.Err.String()
}

func (r *upgradeRig) noTempDirsLeft() {
	r.t.Helper()
	for _, d := range r.tmpDirs {
		if _, err := os.Stat(d); err == nil {
			r.t.Errorf("temp dir %s was not removed", d)
		}
	}
}

func TestUpgradeCheckText(t *testing.T) {
	r := newUpgradeRig(t)
	r.layout(update.KindDeb)
	r.wantExit(0, r.args("--check")...)
	out := r.te.Out.String()
	for _, want := range []string{
		"installed", "v0.2.0", "latest", "v0.3.0",
		"update: available", "run: sudo /usr/bin/stackstead upgrade",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if n := strings.Count(strings.TrimSpace(out), "\n") + 1; n != 3 {
		t.Errorf("want three lines, got %d:\n%s", n, out)
	}
	if len(r.runs) != 0 || len(r.tmpDirs) != 0 {
		t.Error("--check must run nothing and download nothing")
	}

	r2 := newUpgradeRig(t)
	version.Version = "v0.3.0"
	r2.wantExit(0, r2.args("--check")...)
	if out := r2.te.Out.String(); !strings.Contains(out, "update: none, v0.3.0 is the latest") {
		t.Errorf("up-to-date text wrong:\n%s", out)
	}
}

func TestUpgradeCheckJSON(t *testing.T) {
	r := newUpgradeRig(t)
	r.layout(update.KindDeb)
	r.wantExit(0, r.args("--check", "-o", "json")...)
	var st apitypes.UpdateStatus
	if err := json.Unmarshal([]byte(r.te.Out.String()), &st); err != nil {
		t.Fatalf("not json: %v\n%s", err, r.te.Out.String())
	}
	want := apitypes.UpdateStatus{
		Current: "0.2.0", Installer: "deb", Command: "sudo /usr/bin/stackstead upgrade",
		State: "available", Latest: "0.3.0",
	}
	st.CheckedAt = want.CheckedAt
	if st != want {
		t.Errorf("status = %+v, want %+v", st, want)
	}
}

func TestUpgradeCheckSourceDown(t *testing.T) {
	r := newUpgradeRig(t)
	r.down = true
	r.wantExit(ExitUnavailable, r.args("--check")...)
}

func TestUpgradeCheckUnstableLatest(t *testing.T) {
	r := newUpgradeRig(t)
	r.latest = "0.3.0-rc.1"
	r.wantExit(ExitValidation, r.args("--check")...)
}

func TestUpgradeCheckDevBuild(t *testing.T) {
	r := newUpgradeRig(t)
	version.Version = "dev-dirty"
	r.wantExit(0, r.args("--check", "-o", "json")...)
	var st apitypes.UpdateStatus
	if err := json.Unmarshal([]byte(r.te.Out.String()), &st); err != nil {
		t.Fatal(err)
	}
	if st.State != "unknown" || st.Current != "dev-dirty" || st.Latest != "0.3.0" {
		t.Errorf("dev build status = %+v", st)
	}
	if r.hits.Load() == 0 {
		t.Error("--check must still fetch for a dev build")
	}
}

func TestUpgradeCheckPostTagBuild(t *testing.T) {
	r := newUpgradeRig(t)
	version.Version = "v0.2.0-3-gabcdef"
	r.wantExit(0, r.args("--check", "-o", "json")...)
	var st apitypes.UpdateStatus
	if err := json.Unmarshal([]byte(r.te.Out.String()), &st); err != nil {
		t.Fatal(err)
	}
	if st.State != "unknown" || st.Current != "0.2.0-3-gabcdef" || st.Latest != "0.3.0" {
		t.Errorf("post-tag build status = %+v", st)
	}

	r2 := newUpgradeRig(t)
	version.Version = "v0.2.0-3-gabcdef"
	r2.wantExit(0, r2.args("--check")...)
	out := r2.te.Out.String()
	if !strings.Contains(out, "installed: v0.2.0-3-gabcdef\n") || !strings.Contains(out, "development build (v0.2.0-3-gabcdef)") {
		t.Errorf("version must be shown raw:\n%s", out)
	}
}

func TestUpgradePostTagBuildNeedsForce(t *testing.T) {
	r := newUpgradeRig(t)
	version.Version = "v0.2.0-3-gabcdef"
	got := r.wantExit(ExitConflict, r.args("--yes")...)
	if !strings.Contains(got, "this is a development build (v0.2.0-3-gabcdef); use --force to install v0.3.0 over it") {
		t.Errorf("message = %q", got)
	}
	if len(r.runs) != 0 {
		t.Error("runner called")
	}
	r2 := newUpgradeRig(t)
	version.Version = "v0.2.0-3-gabcdef"
	r2.wantExit(0, r2.args("--yes", "--force")...)
	if len(r2.runs) != 1 {
		t.Error("--force must install over a post-tag build")
	}
	if out := r2.te.Out.String(); !strings.Contains(out, "(was v0.2.0-3-gabcdef)") {
		t.Errorf("output = %q", out)
	}
}

func TestUpgradeTempDirParent(t *testing.T) {
	old := geteuid
	t.Cleanup(func() { geteuid = old })
	t.Setenv("TMPDIR", t.TempDir())
	for _, c := range []struct {
		euid int
		want string
	}{{0, "/tmp"}, {1000, os.Getenv("TMPDIR")}} {
		geteuid = func() int { return c.euid }
		dir, err := defaultUpgradeTempDir()
		if err != nil {
			t.Fatal(err)
		}
		os.Remove(dir)
		if got := filepath.Dir(dir); got != c.want {
			t.Errorf("euid %d: parent = %s, want %s", c.euid, got, c.want)
		}
	}
}

func TestUpgradeCheckRejectsForceAndYes(t *testing.T) {
	for _, flag := range []string{"--force", "--yes"} {
		r := newUpgradeRig(t)
		r.wantExit(ExitUsage, r.args("--check", flag)...)
		if r.hits.Load() != 0 {
			t.Errorf("%s: made a request", flag)
		}
	}
}

func TestUpgradeRejectsBadVersionFlag(t *testing.T) {
	for _, v := range []string{"0.3", "latest", "v0.3.0; rm -rf /", "../x"} {
		r := newUpgradeRig(t)
		r.wantExit(ExitUsage, r.args("--version", v, "--yes")...)
		if r.hits.Load() != 0 || len(r.runs) != 0 {
			t.Errorf("%q: request or run happened", v)
		}
	}
}

func TestUpgradeRejectsBadBaseURL(t *testing.T) {
	for _, u := range []string{"http://example.com", "ftp://127.0.0.1", "https://user:pw@github.com/x", "nonsense"} {
		r := newUpgradeRig(t)
		r.wantExit(ExitUsage, "--base-url", u, "--yes")
		if len(r.runs) != 0 {
			t.Errorf("%q: runner called", u)
		}
	}
}

func TestUpgradeSameVersionIsNoop(t *testing.T) {
	r := newUpgradeRig(t)
	version.Version = "v0.3.0"
	r.wantExit(0, r.args("--yes")...)
	if !strings.Contains(r.te.Out.String(), "stackstead v0.3.0 is already installed") {
		t.Errorf("stdout = %q", r.te.Out.String())
	}
	if len(r.runs) != 0 || len(r.tmpDirs) != 0 {
		t.Error("nothing may run or be downloaded")
	}
}

func TestUpgradeRefusesDowngradeWithoutForce(t *testing.T) {
	r := newUpgradeRig(t)
	version.Version = "v0.4.0"
	got := r.wantExit(ExitConflict, r.args("--yes")...)
	if !strings.Contains(got, "installed v0.4.0 is newer than v0.3.0; use --force to downgrade") {
		t.Errorf("message = %q", got)
	}
	if len(r.runs) != 0 {
		t.Error("runner called")
	}
}

func TestUpgradeRefusesDevBuildWithoutForce(t *testing.T) {
	r := newUpgradeRig(t)
	version.Version = "dev-dirty"
	got := r.wantExit(ExitConflict, r.args("--yes")...)
	if !strings.Contains(got, "this is a development build (dev-dirty); use --force to install v0.3.0 over it") {
		t.Errorf("message = %q", got)
	}
	if len(r.runs) != 0 {
		t.Error("runner called")
	}
	r2 := newUpgradeRig(t)
	version.Version = "dev-dirty"
	r2.wantExit(0, r2.args("--yes", "--force")...)
	if len(r2.runs) != 1 {
		t.Error("--force must install over a dev build")
	}
}

func TestUpgradeNeedsRootOnLinux(t *testing.T) {
	r := newUpgradeRig(t)
	r.euid = 1000
	got := r.wantExit(ExitAuth, r.args("--yes")...)
	if !strings.Contains(got, "needs root: run sudo /usr/local/bin/stackstead upgrade") {
		t.Errorf("message = %q", got)
	}
	if len(r.runs) != 0 || len(r.tmpDirs) != 0 {
		t.Error("nothing may run or be downloaded")
	}
}

func TestUpgradeDarwinPkgNeedsRoot(t *testing.T) {
	r := newUpgradeRig(t)
	r.layout(update.KindPkg)
	r.euid = 501
	got := r.wantExit(ExitAuth, r.args("--yes")...)
	if !strings.Contains(got, "needs root: run sudo ") {
		t.Errorf("message = %q", got)
	}
	if len(r.runs) != 0 {
		t.Error("runner called")
	}
}

func TestUpgradeDarwinScriptAsUserOK(t *testing.T) {
	r := newUpgradeRig(t)
	r.darwin()
	r.euid = 501
	r.wantExit(0, r.args("--yes")...)
	if len(r.runs) != 1 {
		t.Fatalf("runs = %d", len(r.runs))
	}
}

func TestUpgradeDarwinScriptRootNeedsSudoUser(t *testing.T) {
	r := newUpgradeRig(t)
	r.darwin()
	r.euid = 0
	got := r.wantExit(ExitAuth, r.args("--yes")...)
	if !strings.Contains(got, "SUDO_USER") {
		t.Errorf("message = %q", got)
	}
	if len(r.runs) != 0 {
		t.Error("runner called")
	}

	r3 := newUpgradeRig(t)
	r3.darwin()
	r3.vars["SUDO_USER"] = "root"
	r3.wantExit(ExitAuth, r3.args("--yes")...)

	r2 := newUpgradeRig(t)
	r2.darwin()
	r2.vars["SUDO_USER"] = "alice"
	r2.wantExit(0, r2.args("--yes")...)
	if len(r2.runs) != 1 {
		t.Error("sudo with SUDO_USER must be accepted")
	}
}

func TestUpgradeNoTerminalNeedsYes(t *testing.T) {
	r := newUpgradeRig(t)
	r.wantExit(ExitUsage, r.args()...)
	if r.assetHits.Load() != 0 || len(r.runs) != 0 {
		t.Error("asked nothing yet downloaded or ran")
	}
}

func TestUpgradePromptNoAborts(t *testing.T) {
	r := newUpgradeRig(t)
	r.terminal("n\n")
	r.wantExit(0, r.args()...)
	errText := r.te.Err.String()
	if !strings.Contains(errText, "aborted") {
		t.Errorf("stderr = %q", errText)
	}
	if !strings.Contains(errText, "Upgrade v0.2.0 → v0.3.0 with install.sh? The service restarts. [y/N]") {
		t.Errorf("prompt text wrong: %q", errText)
	}
	if len(r.runs) != 0 || r.assetHits.Load() != 0 {
		t.Error("declined prompt must run and download nothing")
	}

	r2 := newUpgradeRig(t)
	r2.terminal("y\n")
	r2.wantExit(0, r2.args()...)
	if len(r2.runs) != 1 {
		t.Error("answering y must proceed")
	}
}

func TestUpgradeScriptPlan(t *testing.T) {
	r := newUpgradeRig(t)
	r.wantExit(0, r.args("--yes")...)
	if len(r.runs) != 1 || len(r.tmpDirs) != 1 {
		t.Fatalf("runs=%d tmpDirs=%d", len(r.runs), len(r.tmpDirs))
	}
	got, dir := r.runs[0], r.tmpDirs[0]
	want := []string{filepath.Join(dir, "install.sh"), "--version", "v0.3.0", "--prefix", "/usr/local", "--base-url", r.srv.URL}
	if got.name != "/bin/sh" || !slices.Equal(got.args, want) {
		t.Errorf("argv = %s %q, want sh %q", got.name, got.args, want)
	}
	if got.scriptRaw != string(assetBody("install.sh")) {
		t.Errorf("script contents = %q", got.scriptRaw)
	}
	if !got.dirExists {
		t.Error("temp dir must exist while the installer runs")
	}
	if got.dirMode != 0o755 {
		t.Errorf("temp dir mode = %v, want 0755 so apt's _apt user can read the asset", got.dirMode)
	}
	r.noTempDirsLeft()
	// the backup tip names the exact binary and a dated destination
	tip := "before upgrading you can back up the database: sudo -u stackstead /usr/local/bin/stackstead db backup /var/backups/stackstead-20261014.db"
	if !strings.Contains(r.te.Err.String(), tip) {
		t.Errorf("backup tip missing in %q", r.te.Err.String())
	}
}

func TestUpgradeDebPlan(t *testing.T) {
	r := newUpgradeRig(t)
	r.layout(update.KindDeb)
	r.wantExit(0, r.args("--yes")...)
	got := r.runs[0]
	want := []string{"install", "-y", "-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold",
		filepath.Join(r.tmpDirs[0], "stackstead_0.3.0_amd64.deb")}
	if got.name != "/usr/bin/apt-get" || !slices.Equal(got.args, want) {
		t.Errorf("argv = %s %q", got.name, got.args)
	}
	if !slices.Contains(got.env, "DEBIAN_FRONTEND=noninteractive") {
		t.Errorf("env = %q", got.env)
	}
	r.noTempDirsLeft()
}

func TestUpgradeRPMPlanWithAndWithoutDNF(t *testing.T) {
	for _, dnf := range []bool{true, false} {
		r := newUpgradeRig(t)
		r.layout(update.KindRPM)
		r.dnf = dnf
		r.wantExit(0, r.args("--yes")...)
		asset := filepath.Join(r.tmpDirs[0], "stackstead-0.3.0-1.x86_64.rpm")
		got := r.runs[0]
		wantName, wantArgs := "/usr/bin/rpm", []string{"-U", asset}
		if dnf {
			wantName, wantArgs = "/usr/bin/dnf", []string{"install", "-y", asset}
		}
		if got.name != wantName || !slices.Equal(got.args, wantArgs) {
			t.Errorf("dnf=%v: argv = %s %q", dnf, got.name, got.args)
		}
	}
}

func TestUpgradeForceDowngradePlans(t *testing.T) {
	// script: install.sh --force does the downgrade
	r := newUpgradeRig(t)
	version.Version = "v0.4.0"
	r.wantExit(0, r.args("--yes", "--force")...)
	if !slices.Contains(r.runs[0].args, "--force") {
		t.Errorf("script argv lacks --force: %q", r.runs[0].args)
	}
	if !strings.Contains(r.te.Err.String(), "warning") {
		t.Error("a forced downgrade must warn about the schema")
	}

	// packages: refused even with --force, manual command printed, nothing run
	for _, c := range []struct {
		kind   update.Kind
		manual string
		asset  string
	}{
		{update.KindDeb, "apt-get install -y --allow-downgrades", "stackstead_0.3.0_amd64.deb"},
		{update.KindRPM, "rpm -U --oldpackage", "stackstead-0.3.0-1.x86_64.rpm"},
	} {
		r := newUpgradeRig(t)
		r.layout(c.kind)
		version.Version = "v0.4.0"
		got := r.wantExit(ExitConflict, r.args("--yes", "--force")...)
		if !strings.Contains(got, c.manual) || !strings.Contains(got, c.asset) {
			t.Errorf("%s: manual command missing in %q", c.kind, got)
		}
		if len(r.runs) != 0 || r.assetHits.Load() != 0 {
			t.Errorf("%s: refusal must run and download nothing", c.kind)
		}
	}

	// same-version --force reinstalls
	for _, c := range []struct {
		kind update.Kind
		name string
		args []string
	}{
		{update.KindDeb, "/usr/bin/apt-get", []string{"install", "-y", "-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold", "--reinstall"}},
		{update.KindRPM, "/usr/bin/rpm", []string{"-U", "--replacepkgs"}},
	} {
		r := newUpgradeRig(t)
		r.layout(c.kind)
		version.Version = "v0.3.0"
		r.wantExit(0, r.args("--yes", "--force")...)
		got := r.runs[0]
		if got.name != c.name || !slices.Equal(got.args[:len(got.args)-1], c.args) {
			t.Errorf("%s: argv = %s %q", c.kind, got.name, got.args)
		}
	}
}

func TestUpgradePkgPlanRunsInstaller(t *testing.T) {
	r := newUpgradeRig(t)
	r.layout(update.KindPkg)
	r.wantExit(0, r.args("--yes")...)
	want := []string{"-pkg", filepath.Join(r.tmpDirs[0], "stackstead_0.3.0_darwin.pkg"), "-target", "/"}
	if got := r.runs[0]; got.name != "/usr/sbin/installer" || !slices.Equal(got.args, want) {
		t.Errorf("argv = %s %q", got.name, got.args)
	}
}

func TestUpgradeChecksumMismatchIsExit6(t *testing.T) {
	r := newUpgradeRig(t)
	r.corrupt["install.sh"] = true
	r.wantExit(ExitValidation, r.args("--yes")...)
	if len(r.runs) != 0 {
		t.Error("runner called after a checksum mismatch")
	}
	r.noTempDirsLeft()
}

func TestUpgradeAssetMissingIsExit6(t *testing.T) {
	r := newUpgradeRig(t)
	r.omit["install.sh"] = true
	r.wantExit(ExitValidation, r.args("--yes")...)
	if len(r.runs) != 0 || r.assetHits.Load() != 0 {
		t.Error("nothing may be downloaded or run")
	}
	r.noTempDirsLeft()
}

func TestUpgradeDownloadFailureIsExit7(t *testing.T) {
	r := newUpgradeRig(t)
	r.assetFail = true
	r.wantExit(ExitUnavailable, r.args("--yes")...)
	if len(r.runs) != 0 {
		t.Error("runner called")
	}
	r.noTempDirsLeft()
}

func TestUpgradeDpkgBusyIsExit5(t *testing.T) {
	r := newUpgradeRig(t)
	r.layout(update.KindDeb)
	r.locked = true
	got := r.wantExit(ExitConflict, r.args("--yes")...)
	if !strings.Contains(got, "dpkg") {
		t.Errorf("message = %q", got)
	}
	if len(r.runs) != 0 {
		t.Error("runner called while dpkg was locked")
	}
	r.noTempDirsLeft()
}

func TestInstallerExitMapping(t *testing.T) {
	for _, c := range []struct {
		kind update.Kind
		err  error
		want int
	}{
		{update.KindScript, exitCoder{6}, 6}, {update.KindScript, exitCoder{5}, 7}, {update.KindScript, exitCoder{8}, 3},
		{update.KindScript, exitCoder{130}, 130}, {update.KindScript, exitCoder{1}, 1}, {update.KindScript, exitCoder{2}, 1},
		{update.KindScript, exitCoder{100}, 1}, {update.KindScript, errors.New("start failed"), 1},
		{update.KindScript, fmt.Errorf("wrapped: %w", exitCoder{6}), 6},
		// only install.sh speaks that code language; apt-get 100 or rpm 5 are just failures
		{update.KindDeb, exitCoder{5}, 1}, {update.KindDeb, exitCoder{6}, 1}, {update.KindDeb, exitCoder{100}, 1},
		{update.KindRPM, exitCoder{8}, 1}, {update.KindPkg, exitCoder{130}, 1}, {update.KindPkg, exitCoder{6}, 1},
		// killed by a signal (ExitCode -1): interrupted, whichever installer
		{update.KindScript, exitCoder{-1}, 130}, {update.KindDeb, exitCoder{-1}, 130}, {update.KindPkg, exitCoder{-1}, 130},
	} {
		if got := installerExit(c.kind, c.err); got != c.want {
			t.Errorf("installerExit(%s, %v) = %d, want %d", c.kind, c.err, got, c.want)
		}
	}
}

func TestUpgradeScriptExitCodeIsMapped(t *testing.T) {
	r := newUpgradeRig(t)
	r.runnerErr = exitCoder{5}
	got := r.wantExit(ExitUnavailable, r.args("--yes")...)
	if !strings.Contains(got, "install.sh exited 5; see its output above") {
		t.Errorf("message = %q", got)
	}
	r.noTempDirsLeft()
}

func TestUpgradePackageManagerFailureIsExit1(t *testing.T) {
	r := newUpgradeRig(t)
	r.layout(update.KindDeb)
	r.runnerErr = exitCoder{100}
	got := r.wantExit(ExitGeneral, r.args("--yes")...)
	if !strings.Contains(got, "apt-get") || !strings.Contains(got, "100") {
		t.Errorf("message = %q", got)
	}
	r.noTempDirsLeft()
}

func TestUpgradeSuccessSummary(t *testing.T) {
	r := newUpgradeRig(t)
	r.wantExit(0, r.args("--yes")...)
	out := r.te.Out.String()
	for _, want := range []string{"upgraded stackstead v0.2.0 → v0.3.0", "the installer restarted the service if it was running"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}

	r2 := newUpgradeRig(t)
	r2.layout(update.KindDeb)
	r2.wantExit(0, r2.args("--yes", "-o", "json")...)
	var got map[string]string
	if err := json.Unmarshal([]byte(r2.te.Out.String()), &got); err != nil {
		t.Fatalf("not json: %v\n%s", err, r2.te.Out.String())
	}
	if got["from"] != "v0.2.0" || got["to"] != "v0.3.0" || got["installer"] != "deb" || len(got) != 3 {
		t.Errorf("json = %v", got)
	}
}

func TestUpgradeTempDirRemovedOnFailure(t *testing.T) {
	r := newUpgradeRig(t)
	r.runnerErr = errors.New("installer blew up")
	r.wantExit(ExitGeneral, r.args("--yes")...)
	if len(r.tmpDirs) != 1 {
		t.Fatalf("tmpDirs = %d", len(r.tmpDirs))
	}
	r.noTempDirsLeft()
}

func TestUpgradeBaseURLWarning(t *testing.T) {
	r := newUpgradeRig(t)
	r.wantExit(0, r.args("--check")...)
	want := "warning: using a non-default release source: " + r.srv.URL
	if !strings.Contains(r.te.Err.String(), want) {
		t.Errorf("stderr = %q, want %q", r.te.Err.String(), want)
	}
}

func TestUpgradeDebDowngradeWithoutForceGoesToManualCommand(t *testing.T) {
	for _, c := range []struct {
		kind   update.Kind
		manual string
	}{
		{update.KindDeb, "sudo apt-get install -y --allow-downgrades ./stackstead_0.3.0_amd64.deb"},
		{update.KindRPM, "sudo rpm -U --oldpackage ./stackstead-0.3.0-1.x86_64.rpm"},
	} {
		r := newUpgradeRig(t)
		r.layout(c.kind)
		version.Version = "v0.4.0"
		got := r.wantExit(ExitConflict, r.args("--yes")...)
		if !strings.Contains(got, c.manual) {
			t.Errorf("%s: manual command missing in %q", c.kind, got)
		}
		if strings.Contains(got, "use --force") {
			t.Errorf("%s: --force cannot help a package downgrade, yet: %q", c.kind, got)
		}
		if r.assetHits.Load() != 0 || len(r.runs) != 0 {
			t.Errorf("%s: downloaded or ran", c.kind)
		}
	}
}

func TestUpgradeManualDowngradeNamesURLAndChecksum(t *testing.T) {
	r := newUpgradeRig(t)
	r.layout(update.KindDeb)
	version.Version = "v0.4.0"
	got := r.wantExit(ExitConflict, r.args("--yes", "--force")...)
	name := "stackstead_0.3.0_amd64.deb"
	sum := sha256.Sum256(assetBody(name))
	for _, want := range []string{
		r.srv.URL + "/releases/download/v0.3.0/" + name,
		hex.EncodeToString(sum[:]) + "  " + name,
		"sha256sum -c",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestUpgradeLocalDownloadErrorIsExit1(t *testing.T) {
	r := newUpgradeRig(t)
	r.preexist = "install.sh" // O_EXCL create fails: a local I/O error, not a network one
	r.wantExit(ExitGeneral, r.args("--yes")...)
	if len(r.runs) != 0 {
		t.Error("runner called")
	}
	r.noTempDirsLeft()
}

// cancelledAfterDownload reports ctx as cancelled once the asset is on disk.
type cancelledAfterDownload struct {
	context.Context
	r *upgradeRig
}

func (c cancelledAfterDownload) Err() error {
	if len(c.r.tmpDirs) > 0 {
		if _, err := os.Stat(filepath.Join(c.r.tmpDirs[0], "install.sh")); err == nil {
			return context.Canceled
		}
	}
	return nil
}

func TestUpgradeCancelledAfterDownloadDoesNotRunInstaller(t *testing.T) {
	r := newUpgradeRig(t)
	r.ctx = cancelledAfterDownload{context.Background(), r}
	r.wantExit(ExitInterrupted, r.args("--yes")...)
	if len(r.runs) != 0 {
		t.Error("installer started after the context was cancelled")
	}
	r.noTempDirsLeft()
}

func TestUpgradeCheckJSONOnFailurePrintsFailedStatus(t *testing.T) {
	r := newUpgradeRig(t)
	r.down = true
	r.wantExit(ExitUnavailable, r.args("--check", "-o", "json")...)
	var st apitypes.UpdateStatus
	if err := json.Unmarshal([]byte(r.te.Out.String()), &st); err != nil {
		t.Fatalf("stdout is not status json: %v\n%q", err, r.te.Out.String())
	}
	if st.State != "failed" || st.Error != "http" || st.Current != "0.2.0" || st.Command == "" {
		t.Errorf("status = %+v", st)
	}

	r2 := newUpgradeRig(t)
	r2.down = true
	r2.wantExit(ExitUnavailable, r2.args("--check")...)
	if r2.te.Out.String() != "" {
		t.Errorf("text mode must print nothing on stdout, got %q", r2.te.Out.String())
	}
}

func TestInstallerEnv(t *testing.T) {
	base := []string{"HOME=/home/u", "PATH=/home/u/bin:/usr/bin", "STACKSTEAD_BASE_URL=http://evil", "STACKSTEAD_X=1", "STACKSTEADY=ok"}
	got := installerEnv(base, 0, []string{"DEBIAN_FRONTEND=noninteractive"})
	want := []string{"HOME=/home/u", "STACKSTEADY=ok", "PATH=/usr/sbin:/usr/bin:/sbin:/bin", "DEBIAN_FRONTEND=noninteractive"}
	if !slices.Equal(got, want) {
		t.Errorf("root env = %q, want %q", got, want)
	}
	got = installerEnv(base, 501, nil)
	if !slices.Contains(got, "PATH=/home/u/bin:/usr/bin") || slices.Contains(got, "STACKSTEAD_BASE_URL=http://evil") {
		t.Errorf("user env = %q: PATH must stay, STACKSTEAD_* must go", got)
	}
}

func TestLookInDirs(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dnf"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "plain"), nil, 0o644)
	if p, err := lookInDirs([]string{"/nonexistent", dir}, "dnf"); err != nil || p != filepath.Join(dir, "dnf") {
		t.Errorf("dnf: %q, %v", p, err)
	}
	for _, n := range []string{"plain", "missing"} {
		if _, err := lookInDirs([]string{dir}, n); err == nil {
			t.Errorf("%s: found", n)
		}
	}
}
