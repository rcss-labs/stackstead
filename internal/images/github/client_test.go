package github

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/secret"
)

const sha40 = "0123456789abcdef0123456789abcdef01234567"

const testToken = secret.Secret("ghp_supersecret")

func newClient(srv *httptest.Server) *Client { return &Client{Base: srv.URL, HTTP: srv.Client()} }

func TestUserAndErrors(t *testing.T) {
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		if r.Header.Get("Authorization") != "Bearer ghp_supersecret" {
			w.WriteHeader(401)
			return
		}
		io.WriteString(w, `{"login":"octo"}`)
	}))
	defer srv.Close()
	c := newClient(srv)
	login, err := c.User(context.Background(), testToken)
	if err != nil || login != "octo" {
		t.Fatalf("User = %q, %v", login, err)
	}
	for k, want := range map[string]string{
		"Accept": "application/vnd.github+json", "X-Github-Api-Version": "2022-11-28", "User-Agent": "stackstead",
	} {
		if gotHeaders.Get(k) != want {
			t.Errorf("header %s = %q, want %q", k, gotHeaders.Get(k), want)
		}
	}
	if _, err := c.User(context.Background(), "wrong"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized, got %v", err)
	}
}

func TestStatusMapping(t *testing.T) {
	for code, want := range map[int]error{401: ErrUnauthorized, 403: ErrUnauthorized, 404: ErrNotFound} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) }))
		_, err := newClient(srv).Head(context.Background(), testToken, "o/r", "main")
		srv.Close()
		if !errors.Is(err, want) {
			t.Errorf("status %d: got %v, want %v", code, err, want)
		}
	}
}

func TestHeadSHA(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/commits/feature/x" {
			t.Errorf("path = %s", r.URL.Path)
		}
		io.WriteString(w, `{"sha":"`+sha40+`"}`)
	}))
	defer srv.Close()
	sha, err := newClient(srv).Head(context.Background(), testToken, "o/r", "feature/x")
	if err != nil || sha != sha40 {
		t.Fatalf("Head = %q, %v", sha, err)
	}
}

func TestFileDecodes(t *testing.T) {
	enc := base64.StdEncoding.EncodeToString([]byte("FROM alpine\n"))
	enc = enc[:6] + "\n" + enc[6:] // GitHub wraps base64 in newlines
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/contents/svc/Docker file" || r.URL.Query().Get("ref") != "main" {
			t.Errorf("url = %s", r.URL)
		}
		io.WriteString(w, `{"type":"file","encoding":"base64","size":12,"content":"`+strings.ReplaceAll(enc, "\n", `\n`)+`"}`)
	}))
	defer srv.Close()
	b, err := newClient(srv).File(context.Background(), testToken, "o/r", "main", "svc/Docker file")
	if err != nil || string(b) != "FROM alpine\n" {
		t.Fatalf("File = %q, %v", b, err)
	}
}

func TestFileTooLarge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"type":"file","encoding":"none","size":2000000,"content":""}`)
	}))
	defer srv.Close()
	_, err := newClient(srv).File(context.Background(), testToken, "o/r", "main", "Dockerfile")
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
}

func TestFileMalformedBodyIsUnexpectedResponse(t *testing.T) {
	for name, body := range map[string]string{
		"html":            `<html><body>captive portal</body></html>`,
		"truncated":       `{"type":"file","encoding":"base64","con`,
		"truncated array": `[{"type":"fi`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
			defer srv.Close()
			_, err := newClient(srv).File(context.Background(), testToken, "o/r", "main", "Dockerfile")
			if err == nil || errors.Is(err, ErrNotFound) {
				t.Fatalf("want an unexpected-response error, got %v", err)
			}
		})
	}
}

func TestFileDirectoryIsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `[{"type":"file"}]`) }))
	defer srv.Close()
	if _, err := newClient(srv).File(context.Background(), testToken, "o/r", "main", "svc"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestTarballRedirectDropsToken(t *testing.T) {
	var mu sync.Mutex
	var targetAuth []string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		targetAuth = append(targetAuth, r.Header.Get("Authorization"))
		mu.Unlock()
		io.WriteString(w, "TARBYTES")
	}))
	defer target.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ghp_supersecret" {
			t.Error("api call lacks the token")
		}
		http.Redirect(w, r, target.URL+"/dl", http.StatusFound)
	}))
	defer api.Close()
	rc, err := (&Client{Base: api.URL, HTTP: &http.Client{}}).Tarball(context.Background(), testToken, "o/r", "main")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	b, _ := io.ReadAll(rc)
	if string(b) != "TARBYTES" {
		t.Fatalf("body = %q", b)
	}
	if len(targetAuth) != 1 || targetAuth[0] != "" {
		t.Fatalf("redirect target saw Authorization %q", targetAuth)
	}
}

func TestTarballAtMostOneRedirect(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+r.URL.Path+"x", http.StatusFound)
	}))
	defer srv.Close()
	if _, err := newClient(srv).Tarball(context.Background(), testToken, "o/r", "main"); err == nil {
		t.Fatal("want error after a second redirect")
	}
}

func TestAPIRefusesCrossHostRedirect(t *testing.T) {
	hit := false
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		io.WriteString(w, `{"login":"evil"}`)
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer srv.Close()
	if _, err := newClient(srv).User(context.Background(), testToken); err == nil {
		t.Fatal("want error")
	}
	if hit {
		t.Fatal("followed a cross-host redirect on an API call")
	}
}

func TestGitHubErrorRedacts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		io.WriteString(w, `{"message":"bad credentials `+r.Header.Get("Authorization")+` ghp_supersecret"}`)
	}))
	defer srv.Close()
	_, err := newClient(srv).User(context.Background(), testToken)
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), "ghp_supersecret") || strings.Contains(err.Error(), "bad credentials") {
		t.Fatalf("error leaks response body: %v", err)
	}
}

func TestValidateRepoAndBranch(t *testing.T) {
	for _, ok := range []string{"o/r", "my-org/my.repo_1"} {
		if ValidateRepo(ok) != nil {
			t.Errorf("repo %q rejected", ok)
		}
	}
	for _, bad := range []string{"", "o", "./r", "o/..", "../r", "o/.", "o/r/x", "/r", "o/", "o/r?x", "o/../r x", "o /r"} {
		if ValidateRepo(bad) == nil {
			t.Errorf("repo %q accepted", bad)
		}
	}
	for _, ok := range []string{"main", "feature/x", "v1.2.3"} {
		if ValidateBranch(ok) != nil {
			t.Errorf("branch %q rejected", ok)
		}
	}
	for _, bad := range []string{"", "-x", "a..b", strings.Repeat("a", 256), "a\x00b"} {
		if ValidateBranch(bad) == nil {
			t.Errorf("branch %q accepted", bad)
		}
	}
}

func TestHeadRejectsNonSHA(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"sha":"abc123"}`) }))
	defer srv.Close()
	if _, err := newClient(srv).Head(context.Background(), testToken, "o/r", "main"); err == nil {
		t.Fatal("want error for a malformed sha")
	}
}

func TestRateLimited(t *testing.T) {
	cases := []struct {
		name   string
		status int
		remain string
		want   error
	}{
		{"429", 429, "", ErrRateLimited},
		{"403 exhausted", 403, "0", ErrRateLimited},
		{"403 plain", 403, "", ErrUnauthorized},
		{"403 with quota", 403, "42", ErrUnauthorized},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if tc.remain != "" {
				w.Header().Set("X-RateLimit-Remaining", tc.remain)
			}
			w.WriteHeader(tc.status)
		}))
		_, err := newClient(srv).User(context.Background(), testToken)
		srv.Close()
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestFileRejectsHostilePath(t *testing.T) {
	c := &Client{Base: "http://127.0.0.1:1"}
	for _, p := range []string{"../x", "/etc/passwd", `a\b`} {
		if _, err := c.File(context.Background(), testToken, "o/r", "main", p); !errors.Is(err, ErrBadPath) {
			t.Errorf("%q: want ErrBadPath, got %v", p, err)
		}
	}
}

func TestTarballIdleWatchdog(t *testing.T) {
	old := idleTimeout
	idleTimeout = 100 * time.Millisecond
	defer func() { idleTimeout = old }()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "start")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	rc, err := (&Client{Base: srv.URL, HTTP: &http.Client{Timeout: time.Hour}}).Tarball(context.Background(), testToken, "o/r", "main")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	done := make(chan error, 1)
	go func() { _, err := io.ReadAll(rc); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrStalled) {
			t.Fatalf("want ErrStalled, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watchdog did not fire")
	}
}

func TestTarballIdleResetsOnProgress(t *testing.T) {
	old := idleTimeout
	idleTimeout = 300 * time.Millisecond
	defer func() { idleTimeout = old }()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 4; i++ {
			io.WriteString(w, "x")
			w.(http.Flusher).Flush()
			time.Sleep(150 * time.Millisecond)
		}
	}))
	defer srv.Close()
	rc, err := newClient(srv).Tarball(context.Background(), testToken, "o/r", "main")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil || string(b) != "xxxx" {
		t.Fatalf("got %q, %v", b, err)
	}
}

func TestRedirectKeepsTokenOnlyForSameSchemeAndHost(t *testing.T) {
	mk := func(u string) *http.Request {
		r, _ := http.NewRequest("GET", u, nil)
		r.Header.Set("Authorization", "Bearer x")
		return r
	}
	orig := mk("https://api.example.com/a")
	cases := []struct {
		to       string
		tarball  bool
		wantKeep bool
		wantErr  bool
	}{
		{"https://api.example.com/b", false, true, false},
		{"http://api.example.com/b", true, false, false}, // scheme downgrade
		{"http://api.example.com/b", false, false, true}, // API refuses
		{"https://codeload.example.com/b", true, false, false},
		{"https://codeload.example.com/b", false, false, true},
	}
	for _, tc := range cases {
		req := mk(tc.to)
		err := redirectPolicy(tc.tarball)(req, []*http.Request{orig})
		if (err != nil) != tc.wantErr {
			t.Errorf("%s tarball=%v: err=%v", tc.to, tc.tarball, err)
		}
		if err == nil && (req.Header.Get("Authorization") != "") != tc.wantKeep {
			t.Errorf("%s tarball=%v: Authorization kept=%v", tc.to, tc.tarball, !tc.wantKeep)
		}
	}
}
