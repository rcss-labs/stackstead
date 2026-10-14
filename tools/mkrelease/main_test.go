package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestNormalizeVersion(t *testing.T) {
	cases := []struct {
		in, ver, pkgver, pre string
		dev                  bool
	}{
		{"v1.2.3", "1.2.3", "1.2.3", "", false},
		{"1.2.3", "1.2.3", "1.2.3", "", false},
		{"v1.2.3-rc.1", "1.2.3-rc.1", "1.2.3~rc.1", "rc.1", false},
		{"v1.2.3-5-gabc1234", "1.2.3-dev.5.abc1234", "1.2.3~dev.5.abc1234", "dev.5.abc1234", true},
		{"v1.2.3-5-gabc1234-dirty", "1.2.3-dev.5.abc1234.dirty", "1.2.3~dev.5.abc1234.dirty", "dev.5.abc1234.dirty", true},
		{"abc1234", "0.0.0-dev.abc1234", "0.0.0~dev.abc1234", "dev.abc1234", true},
		{"abc1234-dirty", "0.0.0-dev.abc1234.dirty", "0.0.0~dev.abc1234.dirty", "dev.abc1234.dirty", true},
	}
	for _, c := range cases {
		got, err := normalizeVersion(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if got.Ver != c.ver || got.PkgVer != c.pkgver || got.Prerelease != c.pre || got.Dev != c.dev {
			t.Errorf("%q: got %+v, want ver=%s pkgver=%s pre=%s dev=%v", c.in, got, c.ver, c.pkgver, c.pre, c.dev)
		}
	}
	for _, bad := range []string{"", "v1.2", "not a version!"} {
		if _, err := normalizeVersion(bad); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}

// fixture creates a bin dir with fake binaries, extra files and an empty dist.
type fixture struct {
	bin, dist string
	extras    []extra
}

func newFixture(t *testing.T, targets ...string) fixture {
	t.Helper()
	dir := t.TempDir()
	f := fixture{bin: filepath.Join(dir, "bin"), dist: filepath.Join(dir, "dist")}
	for _, d := range []string{f.bin, f.dist} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tg := range targets {
		name := "stackstead-" + strings.ReplaceAll(tg, "/", "-")
		write(t, filepath.Join(f.bin, name), "binary "+tg, 0o755)
	}
	src := filepath.Join(dir, "src")
	files := map[string]string{
		"install.sh": "#!/bin/sh\n", "stackstead.service": "[Unit]\n", "launchagent.plist.in": "<plist/>\n",
		"config.example.yaml": "listen: x\n", "README.md": "# readme\n",
	}
	for n, c := range files {
		write(t, filepath.Join(src, n), c, 0o600)
	}
	lic := filepath.Join(repoRoot(t), "LICENSE")
	f.extras = []extra{
		{Name: "install.sh", Path: filepath.Join(src, "install.sh")},
		{Name: "config.example.yaml", Path: filepath.Join(src, "config.example.yaml")},
		{Name: "README.md", Path: filepath.Join(src, "README.md")},
		{Name: "LICENSE", Path: lic},
		{OS: "linux", Name: "stackstead.service", Path: filepath.Join(src, "stackstead.service")},
		{OS: "darwin", Name: "launchagent.plist.in", Path: filepath.Join(src, "launchagent.plist.in")},
	}
	return f
}

func write(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func (f fixture) opts(version string, targets ...string) options {
	return options{Version: version, Dist: f.dist, Bin: f.bin, Targets: targets, Extras: f.extras, Epoch: 1700000000}
}

type entry struct {
	name      string
	mode      int64
	uid, gid  int
	mtimeUnix int64
	content   string
}

func readArchive(t *testing.T, path string) []entry {
	t.Helper()
	fh, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	gz, err := gzip.NewReader(fh)
	if err != nil {
		t.Fatal(err)
	}
	if gz.Name != "" || !gz.ModTime.IsZero() && gz.ModTime.Unix() != 0 {
		t.Errorf("gzip header carries name/time: %q %v", gz.Name, gz.ModTime)
	}
	var out []entry
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		out = append(out, entry{h.Name, h.Mode, h.Uid, h.Gid, h.ModTime.Unix(), string(b)})
	}
}

func TestArchiveIsDeterministic(t *testing.T) {
	f := newFixture(t, "linux/amd64")
	o := f.opts("v1.2.3", "linux/amd64")
	if err := build(o); err != nil {
		t.Fatal(err)
	}
	arc := filepath.Join(f.dist, "stackstead_1.2.3_linux_amd64.tar.gz")
	first, _ := os.ReadFile(arc)
	sums1, _ := os.ReadFile(filepath.Join(f.dist, "SHA256SUMS"))

	// Different wall clock: touch inputs to a new mtime; output must not change.
	later := o
	for _, e := range f.extras {
		_ = os.Chtimes(e.Path, time.Now().Add(time.Hour), time.Now().Add(time.Hour))
	}
	if err := os.Remove(arc); err != nil {
		t.Fatal(err)
	}
	if err := build(later); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(arc)
	sums2, _ := os.ReadFile(filepath.Join(f.dist, "SHA256SUMS"))
	if !bytes.Equal(first, second) || !bytes.Equal(sums1, sums2) {
		t.Fatal("archive or SHA256SUMS differ between builds")
	}

	entries := readArchive(t, arc)
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.name
		if e.uid != 0 || e.gid != 0 {
			t.Errorf("%s: uid/gid %d/%d", e.name, e.uid, e.gid)
		}
		if e.mtimeUnix != 1700000000 {
			t.Errorf("%s: mtime %d", e.name, e.mtimeUnix)
		}
		want := int64(0o644)
		if e.name == "stackstead" || e.name == "install.sh" {
			want = 0o755
		}
		if e.mode != want {
			t.Errorf("%s: mode %o, want %o", e.name, e.mode, want)
		}
	}
	if !sort.StringsAreSorted(names) {
		t.Errorf("entries not sorted: %v", names)
	}
}

func TestArchiveContentsPerOS(t *testing.T) {
	f := newFixture(t, "linux/arm64", "darwin/arm64")
	if err := build(f.opts("v1.2.3", "linux/arm64", "darwin/arm64")); err != nil {
		t.Fatal(err)
	}
	has := func(arch string) map[string]string {
		m := map[string]string{}
		for _, e := range readArchive(t, filepath.Join(f.dist, arch)) {
			m[e.name] = e.content
		}
		return m
	}
	linux := has("stackstead_1.2.3_linux_arm64.tar.gz")
	darwin := has("stackstead_1.2.3_darwin_arm64.tar.gz")
	for _, n := range []string{"stackstead", "install.sh", "config.example.yaml", "README.md", "LICENSE", "stackstead.service"} {
		if _, ok := linux[n]; !ok {
			t.Errorf("linux archive missing %s", n)
		}
	}
	for _, n := range []string{"stackstead", "install.sh", "config.example.yaml", "README.md", "LICENSE", "launchagent.plist.in"} {
		if _, ok := darwin[n]; !ok {
			t.Errorf("darwin archive missing %s", n)
		}
	}
	if _, ok := linux["launchagent.plist.in"]; ok {
		t.Error("linux archive has the plist")
	}
	if _, ok := darwin["stackstead.service"]; ok {
		t.Error("darwin archive has the unit")
	}
	for name, m := range map[string]map[string]string{"linux": linux, "darwin": darwin} {
		if !strings.HasPrefix(m["LICENSE"], "                    GNU AFFERO GENERAL PUBLIC LICENSE") {
			t.Errorf("%s LICENSE is not the AGPL text", name)
		}
	}
}

func TestLicenseFileIsAGPL3(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "LICENSE"))
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if s := strings.TrimSpace(sc.Text()); s != "" {
			lines = append(lines, s)
		}
		if len(lines) == 2 {
			break
		}
	}
	if len(lines) < 2 || lines[0] != "GNU AFFERO GENERAL PUBLIC LICENSE" || lines[1] != "Version 3, 19 November 2007" {
		t.Fatalf("unexpected LICENSE header: %q", lines)
	}
	if !bytes.Contains(b, []byte("END OF TERMS AND CONDITIONS")) || !bytes.Contains(b, []byte("13. Remote Network Interaction")) {
		t.Fatal("LICENSE looks truncated")
	}
}

var sumLine = regexp.MustCompile(`^[0-9a-f]{64}  [^ /]+$`)

func TestChecksumFileFormat(t *testing.T) {
	f := newFixture(t, "linux/amd64")
	write(t, filepath.Join(f.dist, "stackstead_1.2.3_amd64.deb"), "deb", 0o644)
	write(t, filepath.Join(f.dist, "SHA256SUMS.sig"), "sig", 0o644)
	if err := build(f.opts("v1.2.3", "linux/amd64")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(f.dist, "SHA256SUMS"))
	var names []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if !sumLine.MatchString(sc.Text()) {
			t.Fatalf("bad line %q", sc.Text())
		}
		names = append(names, sc.Text()[66:])
	}
	want := []string{"stackstead_1.2.3_amd64.deb", "stackstead_1.2.3_linux_amd64.tar.gz"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("names %v, want %v (sorted, every file, no SHA256SUMS*)", names, want)
	}
	// Idempotent: a second run (SHA256SUMS now present) yields the same file.
	if err := build(f.opts("v1.2.3", "linux/amd64")); err != nil {
		t.Fatal(err)
	}
	b2, _ := os.ReadFile(filepath.Join(f.dist, "SHA256SUMS"))
	if !bytes.Equal(b, b2) {
		t.Fatal("SHA256SUMS not idempotent")
	}
}

func TestMissingExtraFileFails(t *testing.T) {
	f := newFixture(t, "linux/amd64")
	f.extras = append(f.extras, extra{Name: "nope", Path: filepath.Join(f.dist, "does-not-exist")})
	if err := build(f.opts("v1.2.3", "linux/amd64")); err == nil {
		t.Fatal("expected an error for a missing extra file")
	}
	if _, err := os.Stat(filepath.Join(f.dist, "SHA256SUMS")); err == nil {
		t.Fatal("SHA256SUMS written despite failure")
	}
}

func TestMissingBinaryFails(t *testing.T) {
	f := newFixture(t, "linux/amd64")
	if err := build(f.opts("v1.2.3", "linux/amd64", "darwin/arm64")); err == nil {
		t.Fatal("expected an error for a missing binary")
	}
}

func TestDevVersionRefusedWithoutOverride(t *testing.T) {
	f := newFixture(t, "linux/amd64")
	o := f.opts("v1.2.3-5-gabc1234", "linux/amd64")
	if err := build(o); err == nil {
		t.Fatal("dev version accepted without override")
	}
	o.AllowDev = true
	if err := build(o); err != nil {
		t.Fatalf("override rejected: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.dist, "stackstead_1.2.3-dev.5.abc1234_linux_amd64.tar.gz")); err != nil {
		t.Fatal(err)
	}
}

func TestRunPrintVersionAndFlags(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"-version", "v1.2.3-rc.1", "-print-version"}, func(string) string { return "" }, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "1.2.3-rc.1\t1.2.3~rc.1\trc.1\n" {
		t.Fatalf("got %q", out.String())
	}

	f := newFixture(t, "linux/amd64")
	args := []string{"-version", "v1.2.3", "-dist", f.dist, "-bin", f.bin, "-targets", "linux/amd64"}
	for _, e := range f.extras {
		spec := e.Name + "=" + e.Path
		if e.OS != "" {
			spec = e.OS + ":" + spec
		}
		args = append(args, "-extra", spec)
	}
	env := func(k string) string {
		if k == "SOURCE_DATE_EPOCH" {
			return "1700000000"
		}
		return ""
	}
	if err := run(args, env, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, e := range readArchive(t, filepath.Join(f.dist, "stackstead_1.2.3_linux_amd64.tar.gz")) {
		if e.mtimeUnix != 1700000000 {
			t.Errorf("%s mtime %d: SOURCE_DATE_EPOCH ignored", e.name, e.mtimeUnix)
		}
	}
}
