package api_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/api"
	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/images/github"
	"github.com/rcss-labs/stackstead/internal/peers"
	"github.com/rcss-labs/stackstead/internal/secret"
)

// fakeGitHub stands in for the GitHub API: no network is used.
type fakeGitHub struct {
	login      string
	head       string
	userErr    error
	headErr    error
	dockerfile string
	gotTokens  []secret.Secret
	headBranch string
}

func (f *fakeGitHub) User(_ context.Context, t secret.Secret) (string, error) {
	f.gotTokens = append(f.gotTokens, t)
	return f.login, f.userErr
}

func (f *fakeGitHub) Head(_ context.Context, t secret.Secret, _, branch string) (string, error) {
	f.headBranch = branch
	f.gotTokens = append(f.gotTokens, t)
	return f.head, f.headErr
}

func (f *fakeGitHub) File(_ context.Context, _ secret.Secret, _, _, _ string) ([]byte, error) {
	return []byte(f.dockerfile), nil
}

func (f *fakeGitHub) Tarball(context.Context, secret.Secret, string, string) (io.ReadCloser, error) {
	return nil, io.EOF
}

const ghImageBody = `{"name":"app","source":"github","dockerfilePath":"Dockerfile","buildArgs":{},"github":{"repo":"acme/app","branch":"main","path":"svc"}}`

func (e *imagesEnv) setGitHubToken(t *testing.T, token string) {
	t.Helper()
	if rec := e.req(t, "PUT", "/api/settings/github", `{"token":"`+token+`"}`); rec.Code != 200 {
		t.Fatalf("set token = %d %s", rec.Code, rec.Body)
	}
}

func TestGitHubSettingsNeverReturnToken(t *testing.T) {
	env := newImagesEnv(t)
	const tok = "ghp_SECRETTOKENVALUE"
	var st apitypes.GitHubSettings
	decodeBody(t, env.req(t, "GET", "/api/settings/github", ""), &st)
	if st.Configured {
		t.Fatalf("fresh status = %+v", st)
	}
	rec := env.req(t, "PUT", "/api/settings/github", `{"token":"`+tok+`"}`)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), tok) {
		t.Fatalf("put = %d %s", rec.Code, rec.Body)
	}
	decodeBody(t, rec, &st)
	if !st.Configured {
		t.Errorf("after put = %+v", st)
	}
	rec = env.req(t, "GET", "/api/settings/github", "")
	if strings.Contains(rec.Body.String(), tok) || strings.Contains(rec.Body.String(), "token") {
		t.Errorf("get leaks token: %s", rec.Body)
	}
	// test records the login
	var res apitypes.GitHubTest
	decodeBody(t, env.req(t, "POST", "/api/settings/github/test", ""), &res)
	if !res.OK || res.Login != "octocat" {
		t.Errorf("test = %+v", res)
	}
	decodeBody(t, env.req(t, "GET", "/api/settings/github", ""), &st)
	if st.Login != "octocat" {
		t.Errorf("status login = %+v", st)
	}
	// clearing
	rec = env.req(t, "PUT", "/api/settings/github", `{"token":""}`)
	decodeBody(t, rec, &st)
	if rec.Code != 200 || st.Configured || st.Login != "" {
		t.Errorf("cleared = %d %+v", rec.Code, st)
	}
}

func TestGitHubTestFailureAnswers200(t *testing.T) {
	env := newImagesEnv(t)
	rec := env.req(t, "POST", "/api/settings/github/test", "")
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "github_unconfigured") {
		t.Fatalf("no token = %d %s", rec.Code, rec.Body)
	}
	env.setGitHubToken(t, "ghp_SECRETTOKENVALUE")
	env.gh.userErr = errors.New("connection refused")
	rec = env.req(t, "POST", "/api/settings/github/test", "")
	var res apitypes.GitHubTest
	decodeBody(t, rec, &res)
	if rec.Code != 200 || res.OK || res.Message == "" {
		t.Errorf("failed test = %d %+v", rec.Code, res)
	}
}

func TestGitHubSettingsDeniedToPeer(t *testing.T) {
	clock := &peerTestClock{t: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)}
	st, psvc := newPeerStore(t, clock)
	if err := st.InsertPeer(context.Background(), peers.Peer{InstanceID: "inst-a", Name: "mini-01", URL: "http://127.0.0.1:1", State: peers.StateActive}, peers.HashSecret("ssp_good")); err != nil {
		t.Fatal(err)
	}
	env := newImagesEnv(t, func(o *api.Options) { o.Peers = psvc })
	for _, rt := range []struct{ method, path string }{
		{"GET", "/api/settings/github"}, {"PUT", "/api/settings/github"}, {"POST", "/api/settings/github/test"},
	} {
		rec := env.do(t, rt.method, rt.path, `{}`, peerReq("ssp_good", "")...)
		if rec.Code != 403 || !strings.Contains(rec.Body.String(), "peer_forbidden") {
			t.Errorf("%s %s = %d %s, want 403 peer_forbidden", rt.method, rt.path, rec.Code, rec.Body)
		}
	}
}

func TestGitHubHeadRoute(t *testing.T) {
	env := newImagesEnv(t)
	rec := env.req(t, "POST", "/api/images", ghImageBody)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "github_unconfigured") {
		t.Fatalf("create without token = %d %s", rec.Code, rec.Body)
	}
	env.setGitHubToken(t, "ghp_SECRETTOKENVALUE")
	if rec = env.req(t, "POST", "/api/images", ghImageBody); rec.Code != 201 {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	env.gh.dockerfile = "FROM alpine\n"
	rec = env.req(t, "GET", "/api/images/app/github/head", "")
	var head apitypes.GitHubHead
	decodeBody(t, rec, &head)
	if rec.Code != 200 || head.Commit != strings.Repeat("a", 40) || head.Dockerfile != "FROM alpine\n" || head.Path != "svc/Dockerfile" {
		t.Errorf("head = %d %+v", rec.Code, head)
	}
	// an upload image has no GitHub head
	env.createImage(t, "plain")
	if rec = env.req(t, "GET", "/api/images/plain/github/head", ""); rec.Code != 400 {
		t.Errorf("upload head = %d %s", rec.Code, rec.Body)
	}
	if rec = env.req(t, "GET", "/api/images/nope/github/head", ""); rec.Code != 404 {
		t.Errorf("missing head = %d %s", rec.Code, rec.Body)
	}
}

func TestGitHubHeadForwardableToPeer(t *testing.T) {
	clock := &peerTestClock{t: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)}
	st, psvc := newPeerStore(t, clock)
	if err := st.InsertPeer(context.Background(), peers.Peer{InstanceID: "inst-a", Name: "mini-01", URL: "http://127.0.0.1:1", State: peers.StateActive}, peers.HashSecret("ssp_good")); err != nil {
		t.Fatal(err)
	}
	env := newImagesEnv(t, func(o *api.Options) { o.Peers = psvc })
	rec := env.do(t, "GET", "/api/images/nope/github/head", "", peerReq("ssp_good", "")...)
	if rec.Code != 404 {
		t.Errorf("peer head = %d %s, want 404 (not 403)", rec.Code, rec.Body)
	}
}

func TestCreateGitHubImageValidation(t *testing.T) {
	env := newImagesEnv(t)
	env.setGitHubToken(t, "ghp_SECRETTOKENVALUE")
	rec := env.req(t, "POST", "/api/images", strings.Replace(ghImageBody, "acme/app", "not a repo", 1))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"github.repo"`) {
		t.Errorf("bad repo = %d %s", rec.Code, rec.Body)
	}
	rec = env.req(t, "POST", "/api/images", strings.Replace(ghImageBody, `"svc"`, `"../x"`, 1))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"github.path"`) {
		t.Errorf("bad path = %d %s", rec.Code, rec.Body)
	}
}

func TestGitHubImageIsReadOnlyOverAPI(t *testing.T) {
	env := newImagesEnv(t)
	env.setGitHubToken(t, "ghp_SECRETTOKENVALUE")
	if rec := env.req(t, "POST", "/api/images", ghImageBody); rec.Code != 201 {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	rec := env.req(t, "PUT", "/api/images/app/dockerfile", `{"content":"FROM x\n","revision":0}`)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "read-only") {
		t.Errorf("save dockerfile = %d %s", rec.Code, rec.Body)
	}
}

func TestBuildRefRefusedForUploadImage(t *testing.T) {
	env := newImagesEnv(t)
	env.createImage(t, "plain")
	rec := env.req(t, "POST", "/api/images/plain/builds", `{"tag":"1","ref":"main"}`)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"ref"`) {
		t.Errorf("ref on upload image = %d %s", rec.Code, rec.Body)
	}
}

func TestGitHubBuildPassesRefToService(t *testing.T) {
	env := newImagesEnv(t)
	env.setGitHubToken(t, "ghp_SECRETTOKENVALUE")
	if rec := env.req(t, "POST", "/api/images", ghImageBody); rec.Code != 201 {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	env.gh.headErr = nil
	// An unknown tag name is resolved through Head, which the fake answers
	// with a failure; the failure proves the ref reached the service.
	env.gh.headErr = github.ErrRateLimited
	rec := env.req(t, "POST", "/api/images/app/builds", `{"tag":"1","ref":"v1"}`)
	if rec.Code != 429 || env.gh.headBranch != "v1" {
		t.Errorf("build with ref = %d %s (head asked for %q)", rec.Code, rec.Body, env.gh.headBranch)
	}
}

func TestGitHubFailuresMapToCodes(t *testing.T) {
	env := newImagesEnv(t)
	env.setGitHubToken(t, "ghp_SECRETTOKENVALUE")
	if rec := env.req(t, "POST", "/api/images", ghImageBody); rec.Code != 201 {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	for _, c := range []struct {
		err    error
		status int
		code   string
	}{
		{github.ErrUnauthorized, 409, "github_unauthorized"},
		{github.ErrRateLimited, 429, "github_rate_limited"},
		{github.ErrStalled, 502, "github_unreachable"},
		{errors.New("github: dial tcp refused"), 502, "github_unreachable"},
	} {
		env.gh.headErr = c.err
		for _, path := range []string{"/api/images/app/github/head"} {
			rec := env.req(t, "GET", path, "")
			if rec.Code != c.status || !strings.Contains(rec.Body.String(), `"`+c.code+`"`) || strings.Contains(rec.Body.String(), "ghp_") {
				t.Errorf("%v: %s = %d %s, want %d %s", c.err, path, rec.Code, rec.Body, c.status, c.code)
			}
		}
		rec := env.req(t, "POST", "/api/images/app/builds", `{"tag":"1"}`)
		if rec.Code != c.status || !strings.Contains(rec.Body.String(), `"`+c.code+`"`) {
			t.Errorf("%v: build = %d %s, want %d %s", c.err, rec.Code, rec.Body, c.status, c.code)
		}
	}
	// A failed token test still answers 200 ok:false.
	env.gh.userErr = github.ErrRateLimited
	rec := env.req(t, "POST", "/api/settings/github/test", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"ok":false`) {
		t.Errorf("test = %d %s", rec.Code, rec.Body)
	}
	// Creating and updating still work with a healthy client.
	env.gh.headErr = nil
	if rec := env.req(t, "PUT", "/api/images/app", ghUpdateBody); rec.Code != 200 {
		t.Errorf("update = %d %s", rec.Code, rec.Body)
	}
}

const ghUpdateBody = `{"name":"app","dockerfilePath":"Dockerfile","buildArgs":{},"github":{"repo":"acme/app","branch":"dev","path":"svc"},"retention":{}}`
