package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/api"
	"github.com/rcss-labs/stackstead/internal/composefile"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

// dockerfileStacks adds the optional Dockerfile methods to fakeStacks.
type dockerfileStacks struct {
	*fakeStacks
	view       stacks.DockerfileView
	gotStack   string
	gotService string
	gotBytes   []byte
	gotHash    string
	gotAction  string
	saveErr    error
}

func (d *dockerfileStacks) Dockerfile(_ context.Context, stack, service string) (stacks.DockerfileView, error) {
	d.gotStack, d.gotService = stack, service
	return d.view, d.err
}

func (d *dockerfileStacks) SaveDockerfile(_ context.Context, stack, service string, content []byte, hash string) (string, error) {
	d.gotStack, d.gotService, d.gotBytes, d.gotHash = stack, service, content, hash
	if d.saveErr != nil {
		return "", d.saveErr
	}
	return "newhash", d.err
}

func (d *dockerfileStacks) ActService(_ context.Context, stack, service, action string) (stacks.Operation, error) {
	d.gotStack, d.gotService, d.gotAction = stack, service, action
	op := d.op
	op.Service = service
	return op, d.err
}

func newDockerfileEnv(t *testing.T) (*testEnv, *http.Cookie, *dockerfileStacks) {
	t.Helper()
	fake := &dockerfileStacks{fakeStacks: newFakeStacks(),
		view: stacks.DockerfileView{Content: []byte("FROM x\n"), Hash: "h1", Exists: true, Editable: true, Path: "/srv/web/Dockerfile"}}
	env := newEnvWith(t, "", func(o *api.Options) {
		o.Stacks, o.Operations, o.FSRoots, o.Status = fake, fakeOps{fake.fakeStacks}, nil, fake.status
		o.SSEHeartbeat = 20 * time.Millisecond
	})
	env.createAdmin(t)
	return env, env.login(t), fake
}

const dfPath = "/api/stacks/web/services/app/dockerfile"

func TestGetDockerfile(t *testing.T) {
	env, cookie, fake := newDockerfileEnv(t)
	rec := env.do(t, "GET", dfPath, "", withCookie(cookie))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	keys := func() string {
		var m map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		var ks []string
		for k := range m {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		return strings.Join(ks, ",")
	}
	if got := keys(); got != "content,editable,exists,hash,path" {
		t.Errorf("keys = %s", got)
	}
	if !strings.Contains(rec.Body.String(), `"content":"FROM x\n"`) {
		t.Errorf("body = %s", rec.Body)
	}
	if fake.gotStack != "web" || fake.gotService != "app" {
		t.Errorf("got %q/%q", fake.gotStack, fake.gotService)
	}

	fake.view = stacks.DockerfileView{Editable: false, Reason: "uses variables"}
	rec = env.do(t, "GET", dfPath, "", withCookie(cookie))
	if !strings.Contains(rec.Body.String(), `"reason":"uses variables"`) {
		t.Errorf("body = %s", rec.Body)
	}
}

func TestPutDockerfile(t *testing.T) {
	env, cookie, fake := newDockerfileEnv(t)
	rec := env.do(t, "PUT", dfPath, `{"content":"FROM y\n","hash":"h1"}`, withCookie(cookie))
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"hash":"newhash"}` {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if string(fake.gotBytes) != "FROM y\n" || fake.gotHash != "h1" || fake.gotService != "app" {
		t.Errorf("fake got %q %q %q", fake.gotBytes, fake.gotHash, fake.gotService)
	}
}

func TestPutDockerfileErrors(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		substr string
	}{
		{"stale hash", stacks.ErrDockerfileConflict, 409, "Dockerfile changed on disk since it was loaded"},
		{"outside", fmt.Errorf("w: %w", stacks.ErrOutsideStack), 403, "this Dockerfile is outside the stack's folder and can't be edited here"},
		{"not editable", &stacks.NotEditableError{Reason: "uses variables"}, 422, "uses variables"},
		{"no build", stacks.ErrNoBuild, 404, "service does not build an image"},
		{"too large", &stacks.ValidationError{Fields: []stacks.FieldError{{Field: "content", Message: "too large"}}}, 400, `"field":"content"`},
	}
	env, cookie, fake := newDockerfileEnv(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake.saveErr = tc.err
			rec := env.do(t, "PUT", dfPath, `{"content":"x","hash":"h"}`, withCookie(cookie))
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.substr) {
				t.Errorf("status = %d, body %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestPutDockerfileRejectsOversizeBody(t *testing.T) {
	env, cookie, _ := newDockerfileEnv(t)
	body := `{"content":"` + strings.Repeat("a", 9<<20) + `","hash":"h"}`
	if rec := env.do(t, "PUT", dfPath, body, withCookie(cookie)); rec.Code != 400 || !strings.Contains(rec.Body.String(), "too large") {
		t.Errorf("status = %d, body %s", rec.Code, rec.Body)
	}
}

func TestServiceUpRoute(t *testing.T) {
	env, cookie, fake := newDockerfileEnv(t)
	rec := env.do(t, "POST", "/api/stacks/web/services/app/actions/up", "{}", withCookie(cookie))
	if rec.Code != 202 || !strings.Contains(rec.Body.String(), `"service":"app"`) || !strings.Contains(rec.Body.String(), `"action":"up"`) {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if fake.gotAction != "up" || fake.gotService != "app" {
		t.Errorf("got %q/%q", fake.gotAction, fake.gotService)
	}
	fake.err = stacks.ErrBusy
	if rec := env.do(t, "POST", "/api/stacks/web/services/app/actions/up", "{}", withCookie(cookie)); rec.Code != 409 {
		t.Errorf("busy status = %d", rec.Code)
	}
}

func TestDockerfileRoutesNeedSession(t *testing.T) {
	env, _, _ := newDockerfileEnv(t)
	for _, rt := range []struct{ method, path string }{
		{"GET", dfPath}, {"PUT", dfPath}, {"POST", "/api/stacks/web/services/app/actions/up"},
	} {
		if rec := env.do(t, rt.method, rt.path, "{}"); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401", rt.method, rt.path, rec.Code)
		}
	}
}

func TestDockerfileRoutesAbsentWithoutSupport(t *testing.T) {
	env, cookie := newStacksEnv(t, newFakeStacks())
	for _, rt := range []struct{ method, path string }{
		{"GET", dfPath}, {"PUT", dfPath}, {"POST", "/api/stacks/web/services/app/actions/up"},
	} {
		if rec := env.do(t, rt.method, rt.path, "{}", withCookie(cookie)); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", rt.method, rt.path, rec.Code)
		}
	}
}

func TestServiceUpRouteErrors(t *testing.T) {
	env, cookie, fake := newDockerfileEnv(t)
	fake.err = composefile.ErrServiceNotFound
	if rec := env.do(t, "POST", "/api/stacks/web/services/ghost/actions/up", "{}", withCookie(cookie)); rec.Code != 404 {
		t.Errorf("unknown service status = %d", rec.Code)
	}
	fake.err = &stacks.ValidationError{Fields: []stacks.FieldError{{Field: "action", Message: "unknown service action"}}}
	rec := env.do(t, "POST", "/api/stacks/web/services/app/actions/down", "{}", withCookie(cookie))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"field":"action"`) {
		t.Errorf("down status = %d, body %s", rec.Code, rec.Body)
	}
}
