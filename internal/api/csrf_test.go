package api_test

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/rcss-labs/stackstead/internal/apitypes"
)

var unsafeRoutePattern = regexp.MustCompile(`"(POST|PUT|PATCH|DELETE) (/api/[^"\s]*)"`)
var pathWildcard = regexp.MustCompile(`\{[^}]*\}`)

// unsafeRoutes reads every unsafe route pattern registered in the package's
// non-test sources, so a route added later is covered without editing the test.
func unsafeRoutes(t *testing.T) []struct{ method, path string } {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	files, err := filepath.Glob(filepath.Join(filepath.Dir(thisFile), "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	var out []struct{ method, path string }
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range unsafeRoutePattern.FindAllStringSubmatch(string(src), -1) {
			key := m[1] + " " + m[2]
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, struct{ method, path string }{m[1], pathWildcard.ReplaceAllString(m[2], "x")})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].method+out[i].path < out[j].method+out[j].path })
	if len(out) < 20 {
		t.Fatalf("found only %d unsafe routes; the route scan is broken", len(out))
	}
	return out
}

func foreignOrigin(r *http.Request) { r.Header.Set("Origin", "http://evil.example:8081") }
func secFetch(v string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", v) }
}

func TestUnsafeRoutesRejectCrossSiteRequests(t *testing.T) {
	env := newImagesEnv(t)
	cases := map[string]func(*http.Request){
		"foreign Origin":             foreignOrigin,
		"Sec-Fetch-Site cross-site":  secFetch("cross-site"),
		"Sec-Fetch-Site same-site":   secFetch("same-site"),
		"same-site with same Origin": func(r *http.Request) { secFetch("same-site")(r); r.Header.Set("Origin", "http://"+r.Host) },
	}
	for _, rt := range unsafeRoutes(t) {
		for name, mark := range cases {
			// A valid session means only the middleware can produce a 403 here.
			rec := env.req(t, rt.method, rt.path, "", mark)
			var body apitypes.ErrorBody
			decodeBody(t, rec, &body)
			if rec.Code != http.StatusForbidden || body.Code != "bad_origin" {
				t.Errorf("%s %s with %s = %d %s, want 403 bad_origin", rt.method, rt.path, name, rec.Code, rec.Body)
			}
		}
	}
}

func TestCrossOriginUploadCreatesNoRevision(t *testing.T) {
	env := newImagesEnv(t)
	env.createImage(t, "web")
	count := func() int {
		var revs []apitypes.ImageRevision
		decodeBody(t, env.req(t, "GET", "/api/images/web/revisions", ""), &revs)
		return len(revs)
	}
	before := count()
	body, ctype := multipartBody(t, []multipartPart{{"f/Dockerfile", "RUN echo pwned\n"}})
	rec := env.req(t, "POST", "/api/images/web/files", body, foreignOrigin, func(r *http.Request) {
		r.Header.Set("Content-Type", ctype)
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin upload = %d %s, want 403", rec.Code, rec.Body)
	}
	if got := count(); got != before {
		t.Fatalf("revisions = %d, want %d", got, before)
	}
}

func TestUnsafeRequestsFromSameOriginAndCLIStillWork(t *testing.T) {
	env := newImagesEnv(t)
	env.createImage(t, "web")
	for name, mark := range map[string]func(*http.Request){
		"no browser headers (CLI)":   func(*http.Request) {},
		"Sec-Fetch-Site same-origin": secFetch("same-origin"),
		"Origin matches Host":        func(r *http.Request) { r.Header.Set("Origin", "http://"+r.Host) },
	} {
		rec := env.req(t, "POST", "/api/images/web/files", "", mark, func(r *http.Request) {
			body, ctype := multipartBody(t, []multipartPart{{"f/Dockerfile", "FROM alpine\n"}})
			r.Body = http.NoBody
			r.Body = nopCloser(body)
			r.ContentLength = int64(len(body))
			r.Header.Set("Content-Type", ctype)
		})
		if rec.Code != http.StatusCreated {
			t.Errorf("%s: upload = %d %s, want 201", name, rec.Code, rec.Body)
		}
	}
	if rec := env.req(t, "POST", "/api/networks/prune", "", secFetch("same-origin")); rec.Code == http.StatusForbidden {
		t.Errorf("same-origin prune was blocked: %s", rec.Body)
	}
}

func nopCloser(s string) io.ReadCloser { return io.NopCloser(strings.NewReader(s)) }
