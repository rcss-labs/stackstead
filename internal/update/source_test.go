package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func hexOf(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func line(name string, content []byte) string { return hexOf(content) + "  " + name + "\n" }

func sumsFor(ver string) string {
	z := strings.Repeat("a", 64)
	return z + "  stackstead_" + ver + "_amd64.deb\n" +
		z + "  stackstead-" + ver + "-1.x86_64.rpm\n" +
		z + "  stackstead_" + ver + "_darwin.pkg\n" +
		z + "  stackstead_" + ver + "_linux_amd64.tar.gz\n" +
		z + "  install.sh\n"
}

func newTestSource(t *testing.T, h http.Handler) (*Source, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	s, err := NewSource(srv.URL, "stackstead/test")
	if err != nil {
		t.Fatal(err)
	}
	return s, srv
}

func serveSums(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
}

func TestNewSourceRejectsBadBaseURL(t *testing.T) {
	for _, u := range []string{"http://example.com/x", "https://user:pw@github.com/x", "ftp://github.com/x", "", "github.com/x"} {
		if _, err := NewSource(u, "ua"); !errors.Is(err, ErrBadBaseURL) {
			t.Errorf("NewSource(%q) = %v, want ErrBadBaseURL", u, err)
		}
	}
	for _, u := range []string{"https://example.com/x", "http://127.0.0.1:1234", "http://localhost:80/x", "http://[::1]:9"} {
		if _, err := NewSource(u, "ua"); err != nil {
			t.Errorf("NewSource(%q) = %v, want ok", u, err)
		}
	}
	s, _ := NewSource(DefaultBaseURL, "ua")
	if !s.IsDefault() {
		t.Error("default source not reported as default")
	}
}

func TestLatestParsesVersionFromTarballNames(t *testing.T) {
	var path atomic.Value
	s, _ := newTestSource(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path.Store(r.URL.Path)
		if r.Header.Get("User-Agent") != "stackstead/test" {
			t.Errorf("user agent %q", r.Header.Get("User-Agent"))
		}
		fmt.Fprint(w, sumsFor("0.3.0"))
	}))
	rel, err := s.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version.String() != "0.3.0" {
		t.Errorf("version %s", rel.Version)
	}
	if path.Load() != "/releases/latest/download/SHA256SUMS" {
		t.Errorf("path %v", path.Load())
	}
	if rel.NotesURL() != "" {
		t.Errorf("notes url for non-default base: %q", rel.NotesURL())
	}
	def := Release{Version: rel.Version, base: DefaultBaseURL}
	if def.NotesURL() != "https://github.com/rcss-labs/stackstead/releases/tag/v0.3.0" {
		t.Errorf("default notes url %q", def.NotesURL())
	}
}

func TestLatestRefusesPrerelease(t *testing.T) {
	s, _ := newTestSource(t, serveSums(sumsFor("0.3.0-rc.1")))
	if _, err := s.Latest(context.Background()); !errors.Is(err, ErrNotStable) {
		t.Fatalf("err = %v, want ErrNotStable", err)
	}
}

func TestLatestMalformed(t *testing.T) {
	z := strings.Repeat("a", 64)
	for name, body := range map[string]string{
		"no tarball":  z + "  install.sh\n",
		"bad line":    "garbage\n",
		"bad version": z + "  stackstead_dev_linux_amd64.tar.gz\n",
		"empty":       "",
	} {
		s, _ := newTestSource(t, serveSums(body))
		_, err := s.Latest(context.Background())
		if !errors.Is(err, ErrMalformed) && !errors.Is(err, ErrNotStable) {
			t.Errorf("%s: err = %v", name, err)
		}
		if err == nil || ErrorCode(err) != "malformed" {
			t.Errorf("%s: code %q", name, ErrorCode(err))
		}
	}
}

func TestLatestHTTPErrors(t *testing.T) {
	s, _ := newTestSource(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", 500) }))
	_, err := s.Latest(context.Background())
	if !errors.Is(err, ErrUnavailable) || ErrorCode(err) != "http" {
		t.Errorf("500: err=%v code=%q", err, ErrorCode(err))
	}

	s, srv := newTestSource(t, serveSums(sumsFor("0.3.0")))
	srv.Close()
	_, err = s.Latest(context.Background())
	if err == nil || ErrorCode(err) != "network" {
		t.Errorf("closed: err=%v code=%q", err, ErrorCode(err))
	}

	release := make(chan struct{})
	s, srv = newTestSource(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	t.Cleanup(func() { close(release) })
	_ = srv
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = s.Latest(ctx)
	if err == nil || ErrorCode(err) != "timeout" {
		t.Errorf("slow: err=%v code=%q", err, ErrorCode(err))
	}
}

func TestDocumentSizeCap(t *testing.T) {
	body := sumsFor("0.3.0")
	s, _ := newTestSource(t, serveSums(body))
	s.docLimit = int64(len(body)) - 1
	if _, err := s.Latest(context.Background()); !errors.Is(err, ErrMalformed) {
		t.Fatalf("err = %v, want ErrMalformed", err)
	}
	s.docLimit = int64(len(body))
	if _, err := s.Latest(context.Background()); err != nil {
		t.Fatalf("at the cap: %v", err)
	}
	// the production default is 1 MiB
	d, _ := NewSource(DefaultBaseURL, "ua")
	if d.docLimit != 1<<20 || d.assetLimit != 512<<20 {
		t.Errorf("limits %d %d", d.docLimit, d.assetLimit)
	}
}

func TestRedirectPolicy(t *testing.T) {
	// A redirect to a host outside the allowlist is refused.
	// The other origin is a live loopback server that would answer validly.
	other := httptest.NewServer(serveSums(sumsFor("9.9.9")))
	t.Cleanup(other.Close)
	s, _ := newTestSource(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/SHA256SUMS", http.StatusFound)
	}))
	if _, err := s.Latest(context.Background()); err == nil {
		t.Fatal("redirect to another origin followed")
	}

	// Same-origin redirect on a loopback test base is allowed, and credentials are stripped.
	var sawAuth atomic.Bool
	s, _ = newTestSource(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/moved" {
			sawAuth.Store(r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "")
			fmt.Fprint(w, sumsFor("0.3.0"))
			return
		}
		http.Redirect(w, r, "/moved", http.StatusFound)
	}))
	if _, err := s.Latest(context.Background()); err != nil {
		t.Fatalf("loopback redirect: %v", err)
	}
	if sawAuth.Load() {
		t.Error("credentials forwarded on redirect")
	}

	// The policy function itself: allowlist, https only, hop limit.
	check := func(raw string, hops int) error {
		req, _ := http.NewRequest("GET", raw, nil)
		req.Header.Set("Authorization", "x")
		return s.client.CheckRedirect(req, make([]*http.Request, hops))
	}
	for _, ok := range []string{"https://github.com/a", "https://objects.githubusercontent.com/a", "https://release-assets.githubusercontent.com/a"} {
		if err := check(ok, 1); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"http://github.com/a", "https://evil.test/a", "https://github.com.evil.test/a", "https://u:p@github.com/a"} {
		if err := check(bad, 1); err == nil {
			t.Errorf("%s allowed", bad)
		}
	}
	if err := check("https://github.com/a", 10); err == nil {
		t.Error("11th hop allowed")
	}
}

func TestReleasePinned(t *testing.T) {
	var path atomic.Value
	s, _ := newTestSource(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path.Store(r.URL.Path)
		fmt.Fprint(w, sumsFor("0.2.5"))
	}))
	rel, err := s.Release(context.Background(), "0.2.5")
	if err != nil {
		t.Fatal(err)
	}
	if path.Load() != "/releases/download/v0.2.5/SHA256SUMS" || rel.Version.String() != "0.2.5" {
		t.Errorf("path %v version %s", path.Load(), rel.Version)
	}
	if _, err := s.Release(context.Background(), "../x"); err == nil {
		t.Error("bad version accepted")
	}
}

func TestAssetNamePrereleasePackages(t *testing.T) {
	rel := Release{Version: mustParse(t, "1.2.3-rc.1")}
	cases := []struct {
		kind         Kind
		goos, goarch string
		want         string
	}{
		{KindScript, "linux", "amd64", "install.sh"},
		{KindDeb, "linux", "amd64", "stackstead_1.2.3~rc.1_amd64.deb"},
		{KindRPM, "linux", "arm64", "stackstead-1.2.3~rc.1-1.aarch64.rpm"},
		{KindPkg, "darwin", "arm64", "stackstead_1.2.3-rc.1_darwin.pkg"},
	}
	for _, c := range cases {
		got, err := rel.AssetName(c.kind, c.goos, c.goarch)
		if err != nil || got != c.want {
			t.Errorf("%v %s/%s = %q, %v; want %q", c.kind, c.goos, c.goarch, got, err, c.want)
		}
	}
}

func TestAssetName(t *testing.T) {
	rel := Release{Version: mustParse(t, "0.3.0")}
	cases := []struct {
		kind         Kind
		goos, goarch string
		want         string
	}{
		{KindScript, "linux", "amd64", "install.sh"},
		{KindDeb, "linux", "amd64", "stackstead_0.3.0_amd64.deb"},
		{KindDeb, "linux", "arm64", "stackstead_0.3.0_arm64.deb"},
		{KindRPM, "linux", "amd64", "stackstead-0.3.0-1.x86_64.rpm"},
		{KindRPM, "linux", "arm64", "stackstead-0.3.0-1.aarch64.rpm"},
		{KindPkg, "darwin", "arm64", "stackstead_0.3.0_darwin.pkg"},
	}
	for _, c := range cases {
		got, err := rel.AssetName(c.kind, c.goos, c.goarch)
		if err != nil || got != c.want {
			t.Errorf("%v %s/%s = %q, %v; want %q", c.kind, c.goos, c.goarch, got, err, c.want)
		}
	}
	if _, err := rel.AssetName(KindDeb, "linux", "riscv64"); err == nil {
		t.Error("unsupported arch accepted")
	}
	if _, err := rel.AssetName(Kind("zip"), "linux", "amd64"); err == nil {
		t.Error("unknown kind accepted")
	}
}

func downloadFixture(t *testing.T, content []byte, listed []byte) (*Source, Release, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	s, _ := newTestSource(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write(content)
	}))
	rel := Release{Version: mustParse(t, "0.3.0"), Sums: []byte(line("stackstead_0.3.0_amd64.deb", listed)), base: s.base}
	return s, rel, &hits
}

const debName = "stackstead_0.3.0_amd64.deb"

func TestDownloadVerifies(t *testing.T) {
	content := []byte("package bytes")
	s, rel, _ := downloadFixture(t, content, content)
	dir := t.TempDir()
	p, err := s.Download(context.Background(), rel, debName, dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(p) != dir || filepath.Base(p) != debName {
		t.Errorf("path %s", p)
	}
	got, _ := os.ReadFile(p)
	if string(got) != string(content) {
		t.Error("content differs")
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o644 {
		t.Errorf("mode %v", st.Mode())
	}
}

func TestDownloadChecksumMismatchRemovesFile(t *testing.T) {
	s, rel, _ := downloadFixture(t, []byte("tampered"), []byte("original"))
	dir := t.TempDir()
	_, err := s.Download(context.Background(), rel, debName, dir)
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("err = %v, want ErrChecksum", err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Errorf("files left behind: %v", ents)
	}
}

func TestDownloadRefusesUnlistedAssetWithoutRequest(t *testing.T) {
	s, rel, hits := downloadFixture(t, []byte("x"), []byte("x"))
	_, err := s.Download(context.Background(), rel, "stackstead_0.3.0_arm64.deb", t.TempDir())
	if !errors.Is(err, ErrAssetMissing) {
		t.Fatalf("err = %v, want ErrAssetMissing", err)
	}
	if hits.Load() != 0 {
		t.Errorf("%d requests made", hits.Load())
	}
}

func TestDownloadSizeCap(t *testing.T) {
	content := []byte("0123456789")
	s, rel, _ := downloadFixture(t, content, content)
	s.assetLimit = 5
	dir := t.TempDir()
	if _, err := s.Download(context.Background(), rel, debName, dir); !errors.Is(err, ErrMalformed) {
		t.Fatalf("err = %v, want ErrMalformed", err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Errorf("files left behind: %v", ents)
	}
}

func TestDownloadRefusesExistingFile(t *testing.T) {
	content := []byte("x")
	s, rel, _ := downloadFixture(t, content, content)
	dir := t.TempDir()
	pre := filepath.Join(dir, debName)
	if err := os.WriteFile(pre, []byte("planted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Download(context.Background(), rel, debName, dir); err == nil {
		t.Fatal("overwrote an existing file")
	}
	if got, _ := os.ReadFile(pre); string(got) != "planted" {
		t.Error("existing file modified")
	}
}

// A slow link must not be cut by the per-request timeout that protects the
// small SHA256SUMS fetch: only ctx bounds a download.
func TestDownloadIsNotBoundByRequestTimeout(t *testing.T) {
	content := []byte(strings.Repeat("x", 3000))
	sums := line(debName, content)
	s, srv := newTestSource(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 3; i++ {
			w.Write(content[i*1000 : (i+1)*1000])
			w.(http.Flusher).Flush()
			time.Sleep(150 * time.Millisecond)
		}
	}))
	s.client.Timeout = 200 * time.Millisecond
	rel := Release{Version: mustVer(t, "0.3.0"), Sums: []byte(sums), base: srv.URL}
	if _, err := s.Download(context.Background(), rel, debName, t.TempDir()); err != nil {
		t.Fatalf("slow download failed: %v", err)
	}
}
