//go:build installer

// Package installer_test drives install.sh (and, in later tasks, the
// packages) against fake releases served over HTTP. Host tests never touch
// the real system: the script runs with --root and a PATH whose first entry
// holds stub executables that log their arguments.
package installer_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// run is one test's identity: a unique id and a scratch directory.
type run struct {
	ID  string
	Dir string
}

// newRun creates a unique run. Every resource a test creates (containers,
// networks, images in later tasks) must be named stackstead-itest-<ID>-... and
// labelled stackstead.test=<ID>; cleanup is registered here, before anything
// else exists.
func newRun(t *testing.T) *run {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return &run{ID: hex.EncodeToString(b), Dir: t.TempDir()}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

func installScript(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "install.sh")
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// buildBinary returns the path of a fake stackstead for goos/goarch that
// follows the "Contract for Plan 7" (version --short, version, version -o
// json, config validate, serve, health). Plan 6 is not merged yet, and a
// shell script also runs on whatever host the tests run on; the real binary
// is exercised once Plan 7 is rebased onto Plan 6 and by the container tests.
// Every invocation is appended to $FAKE_STACKSTEAD_LOG when that is set.
func buildBinary(t *testing.T, goos, goarch, version string) string {
	t.Helper()
	v := "v" + strings.TrimPrefix(version, "v")
	script := fmt.Sprintf(`#!/bin/sh
[ -z "${FAKE_STACKSTEAD_LOG:-}" ] || echo "$*" >> "$FAKE_STACKSTEAD_LOG"
case "${1:-}" in
version)
	case "${2:-}" in
	--short) echo %[1]s ;;
	-o) printf '{"version":"%[1]s","commit":"fake","date":"x","go":"go","os":"%[2]s","arch":"%[3]s","license":"AGPL-3.0"}\n' ;;
	*) echo "stackstead %[1]s (fake x) go %[2]s/%[3]s"; echo "License: AGPL-3.0" ;;
	esac ;;
config) [ "${2:-}" = validate ] && exit 0; exit 2 ;;
health) echo "fake health"; exit 7 ;;
serve) sleep 3600 ;;
*) echo "usage: stackstead <command>" >&2; exit 2 ;;
esac
`, v, goos, goarch)
	p := filepath.Join(t.TempDir(), "stackstead")
	writeFile(t, p, []byte(script), 0o755)
	return p
}

type releaseOpts struct {
	Sign         bool // add SHA256SUMS.sig made with the release key (the key in allowed_signers)
	SignOtherKey bool // add SHA256SUMS.sig made with a key that is NOT in allowed_signers
	OmitFromSums bool // leave the linux tarballs out of SHA256SUMS
}

func (r *run) signersFile() string { return filepath.Join(r.Dir, "allowed_signers") }

func hexSum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func tarball(t *testing.T, files map[string][]byte, modes map[string]int64) []byte {
	t.Helper()
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, n := range names {
		mode := int64(0o644)
		if m, ok := modes[n]; ok {
			mode = m
		}
		hdr := &tar.Header{Name: n, Mode: mode, Size: int64(len(files[n])), ModTime: time.Unix(1700000000, 0)}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(files[n]); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sshKeygen(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("ssh-keygen", args...).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen %v: %v\n%s", args, err, out)
	}
}

func haveSSHSign() bool {
	p, err := exec.LookPath("ssh-keygen")
	if err != nil {
		return false
	}
	out, _ := exec.Command(p, "-Y").CombinedOutput()
	return strings.Contains(string(out), "verify")
}

// fakeRelease builds <r.Dir>/dist/v<version>/ holding one tarball per
// "os/arch" key of bins (default: fake linux/amd64 and linux/arm64) and
// SHA256SUMS, optionally SHA256SUMS.sig. It returns the parent dist directory,
// which serveRelease serves; call it once per version. The tarball mirrors
// what mkrelease produces: stackstead, install.sh, stackstead.service,
// config.example.yaml, README.md, LICENSE, flat, named
// stackstead_<version>_<os>_<arch>.tar.gz.
func fakeRelease(t *testing.T, r *run, version string, bins map[string]string, opts releaseOpts) string {
	t.Helper()
	version = strings.TrimPrefix(version, "v")
	root := repoRoot(t)
	if bins == nil {
		bins = map[string]string{
			"linux/amd64": buildBinary(t, "linux", "amd64", version),
			"linux/arm64": buildBinary(t, "linux", "arm64", version),
		}
	}
	unit := filepath.Join(root, "deploy", "systemd", "stackstead.service")
	if _, err := os.Stat(unit); err != nil {
		unit = filepath.Join(root, "deploy", "stackstead.service")
	}
	common := map[string][]byte{
		"install.sh":          readFile(t, installScript(t)),
		"stackstead.service":  readFile(t, unit),
		"config.example.yaml": readFile(t, filepath.Join(root, "deploy", "config.example.yaml")),
		"README.md":           []byte("# stackstead\n"),
		"LICENSE":             []byte("GNU AFFERO GENERAL PUBLIC LICENSE\n"),
	}
	dist := filepath.Join(r.Dir, "dist")
	vdir := filepath.Join(dist, "v"+version)
	if err := os.MkdirAll(vdir, 0o755); err != nil {
		t.Fatal(err)
	}
	var lines []string
	for target, bin := range bins {
		goos, goarch, _ := strings.Cut(target, "/")
		files := map[string][]byte{"stackstead": readFile(t, bin)}
		for k, v := range common {
			files[k] = v
		}
		name := fmt.Sprintf("stackstead_%s_%s_%s.tar.gz", version, goos, goarch)
		data := tarball(t, files, map[string]int64{"stackstead": 0o755, "install.sh": 0o755})
		writeFile(t, filepath.Join(vdir, name), data, 0o644)
		if !(opts.OmitFromSums && goos == "linux") {
			lines = append(lines, hexSum(data)+"  "+name)
		}
	}
	installSh := common["install.sh"]
	writeFile(t, filepath.Join(vdir, "install.sh"), installSh, 0o755)
	lines = append(lines, hexSum(installSh)+"  install.sh")
	sort.Strings(lines)
	sums := filepath.Join(vdir, "SHA256SUMS")
	writeFile(t, sums, []byte(strings.Join(lines, "\n")+"\n"), 0o644)

	if opts.Sign || opts.SignOtherKey {
		if !haveSSHSign() {
			t.Skip("ssh-keygen with -Y is not available")
		}
		keyDir := filepath.Join(r.Dir, "keys")
		if err := os.MkdirAll(keyDir, 0o700); err != nil {
			t.Fatal(err)
		}
		release, other := filepath.Join(keyDir, "release"), filepath.Join(keyDir, "other")
		for _, k := range []string{release, other} {
			if _, err := os.Stat(k); err != nil {
				sshKeygen(t, "-q", "-t", "ed25519", "-N", "", "-C", "test", "-f", k)
			}
		}
		pub := strings.TrimSpace(string(readFile(t, release+".pub")))
		fields := strings.Fields(pub)
		writeFile(t, r.signersFile(), []byte("release@stackstead "+fields[0]+" "+fields[1]+"\n"), 0o644)
		key := release
		if opts.SignOtherKey {
			key = other
		}
		sshKeygen(t, "-Y", "sign", "-q", "-f", key, "-n", "stackstead-release", sums)
	}
	return dist
}

var (
	reqMu  sync.Mutex
	reqLog = map[string][]string{}
)

// serveRelease serves dist (see fakeRelease) with GitHub's URL layout:
// /releases/download/v<ver>/<asset>, and /releases/latest/download/<asset>
// redirecting to latest ("v1.2.3"). The returned base URL is what
// --base-url takes.
func serveRelease(t *testing.T, dist string, latest string) string {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		reqMu.Lock()
		reqLog[srv.URL] = append(reqLog[srv.URL], req.URL.Path)
		reqMu.Unlock()
		p := req.URL.Path
		if rest, ok := strings.CutPrefix(p, "/releases/latest/download/"); ok {
			http.Redirect(w, req, "/releases/download/"+latest+"/"+rest, http.StatusFound)
			return
		}
		if rest, ok := strings.CutPrefix(p, "/releases/download/"); ok && !strings.Contains(rest, "..") {
			http.ServeFile(w, req, filepath.Join(dist, filepath.FromSlash(rest)))
			return
		}
		http.NotFound(w, req)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// serveStatus serves every request with the given HTTP status.
func serveStatus(t *testing.T, code int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", code)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func requestsTo(base string) []string {
	reqMu.Lock()
	defer reqMu.Unlock()
	return append([]string(nil), reqLog[base]...)
}

// stubPath writes executables into a fresh directory; each logs its argv to
// <dir>/<name>.log and then runs the given shell body (for example "exit 2").
// It returns the directory.
func stubPath(t *testing.T, stubs map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range stubs {
		script := "#!/bin/sh\necho \"$*\" >> \"" + filepath.Join(dir, name+".log") + "\"\n" + body + "\n"
		writeFile(t, filepath.Join(dir, name), []byte(script), 0o755)
	}
	return dir
}

func stubLog(dir, name string) string {
	b, _ := os.ReadFile(filepath.Join(dir, name+".log"))
	return string(b)
}

// minimalPath returns a directory of symlinks to the host tools install.sh
// may use, minus the excluded ones, for tests of "tool X is missing".
func minimalPath(t *testing.T, exclude ...string) string {
	t.Helper()
	skip := map[string]bool{}
	for _, e := range exclude {
		skip[e] = true
	}
	dir := t.TempDir()
	for _, name := range strings.Fields(`awk basename cat chmod chown cp cut date dirname env find grep head id ln ls mkdir
		mktemp mv openssl readlink rm rmdir sed sha256sum shasum sleep sort ssh-keygen tail tar tee touch tr wc curl wget`) {
		if skip[name] {
			continue
		}
		if p, err := exec.LookPath(name); err == nil {
			_ = os.Symlink(p, filepath.Join(dir, name))
		}
	}
	return dir
}

// hostArch is the uname -m value the default uname stub reports.
func hostArch() string {
	if runtime.GOARCH == "arm64" {
		return "aarch64"
	}
	return "x86_64"
}

// defaultStubs are the commands every host test stubs: uname (always Linux so
// the Linux flow runs on a macOS host), getent (nothing exists yet) and the
// service/account commands that must never actually run under --root.
func defaultStubs() map[string]string {
	return map[string]string{
		"uname":     `case "$1" in -m) echo ` + hostArch() + ` ;; *) echo Linux ;; esac`,
		"getent":    `exit 2`,
		"systemctl": `exit 0`,
		"useradd":   `exit 0`,
		"groupadd":  `exit 0`,
		"userdel":   `exit 0`,
	}
}

// runScript runs install.sh with sh. PATH is the stub directory followed by
// env["PATH"] (default: the host PATH). Pass env["STUBS_DIR"] to reuse a
// directory from stubPath (to read its logs afterwards); otherwise stubs are
// written to a fresh one. HOME and TMPDIR point into temp dirs unless given.
func runScript(t *testing.T, args []string, env map[string]string, stubs map[string]string) (stdout, stderr string, code int) {
	t.Helper()
	sd := env["STUBS_DIR"]
	if sd == "" {
		sd = stubPath(t, stubs)
	}
	path := env["PATH"]
	if path == "" {
		path = os.Getenv("PATH")
	}
	cmdEnv := []string{"PATH=" + sd + ":" + path, "LANG=C", "HOME=" + t.TempDir()}
	tmp := env["TMPDIR"]
	if tmp == "" {
		tmp = t.TempDir()
	}
	cmdEnv = append(cmdEnv, "TMPDIR="+tmp)
	for k, v := range env {
		switch k {
		case "STUBS_DIR", "PATH", "TMPDIR":
		default:
			cmdEnv = append(cmdEnv, k+"="+v)
		}
	}
	shell := os.Getenv("STACKSTEAD_TEST_SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	cmd := exec.Command(shell, append([]string{installScript(t)}, args...)...)
	cmd.Env = cmdEnv
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	code = 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running install.sh: %v", err)
		}
		code = ee.ExitCode()
	}
	return so.String(), se.String(), code
}
