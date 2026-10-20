package api_test

import (
	"reflect"
	"strings"
	"testing"
)

const createExtrasBody = `{"name":"lab","services":[` +
	`{"service":"web","config":{"build":{"context":"./web"}},"extra":{"labels":{"a":"b"},"logging":{"driver":"json-file"}},"dockerfile":{"content":"FROM busybox\n"}},` +
	`{"service":"db","config":{"image":"postgres:17"}}],` +
	`"extra":{"volumes":{"data":{}}}}`

func TestCreateStackPassesExtrasAndDockerfiles(t *testing.T) {
	fake := newFakeStacks()
	env, cookie := newStacksEnv(t, fake)
	rec := env.do(t, "POST", "/api/stacks", createExtrasBody, withCookie(cookie))
	if rec.Code != 201 {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	got := fake.gotCreate
	if !reflect.DeepEqual(got.Extra.Keys(), []string{"volumes"}) {
		t.Errorf("top extra keys = %v", got.Extra.Keys())
	}
	if !reflect.DeepEqual(got.Services[0].Extra.Keys(), []string{"labels", "logging"}) || !got.Services[1].Extra.IsZero() {
		t.Errorf("service extras = %v / %v", got.Services[0].Extra.Keys(), got.Services[1].Extra.Keys())
	}
	if len(got.Dockerfiles) != 1 || string(got.Dockerfiles[0]) != "FROM busybox\n" {
		t.Errorf("dockerfiles = %v", got.Dockerfiles)
	}
}

func TestCreateStackTreatsEmptyExtraAsNone(t *testing.T) {
	for _, extra := range []string{`null`, ` null `} {
		t.Run(extra, func(t *testing.T) {
			fake := newFakeStacks()
			env, cookie := newStacksEnv(t, fake)
			body := `{"name":"lab","services":[{"service":"web","config":{"image":"nginx:1"},"extra":` + extra + `}],"extra":` + extra + `}`
			rec := env.do(t, "POST", "/api/stacks", body, withCookie(cookie))
			if rec.Code != 201 {
				t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
			}
			if !fake.gotCreate.Extra.IsZero() || !fake.gotCreate.Services[0].Extra.IsZero() {
				t.Errorf("extras not empty: %+v", fake.gotCreate)
			}
		})
	}
	// absent
	fake := newFakeStacks()
	env, cookie := newStacksEnv(t, fake)
	rec := env.do(t, "POST", "/api/stacks", `{"name":"lab","services":[{"service":"web","config":{"image":"nginx:1"}}]}`, withCookie(cookie))
	if rec.Code != 201 || !fake.gotCreate.Extra.IsZero() {
		t.Fatalf("absent extra: %d %s", rec.Code, rec.Body)
	}
}

func TestCreateStackRejectsNonObjectExtra(t *testing.T) {
	for _, extra := range []string{`[1]`, `"x"`, `5`, `true`} {
		fake := newFakeStacks()
		env, cookie := newStacksEnv(t, fake)
		body := `{"name":"lab","services":[{"service":"web","config":{"image":"nginx:1"},"extra":` + extra + `}]}`
		rec := env.do(t, "POST", "/api/stacks", body, withCookie(cookie))
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"field":"services[0].extra"`) || !strings.Contains(rec.Body.String(), "extra must be a JSON object") {
			t.Errorf("%s => %d %s", extra, rec.Code, rec.Body)
		}
		body = `{"name":"lab","services":[{"service":"web","config":{"image":"nginx:1"}}],"extra":` + extra + `}`
		rec = env.do(t, "POST", "/api/stacks", body, withCookie(cookie))
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"field":"extra"`) {
			t.Errorf("top %s => %d %s", extra, rec.Code, rec.Body)
		}
	}
}

func TestCreateStackRejectsReservedTopExtra(t *testing.T) {
	for _, key := range []string{"services", "name"} {
		fake := newFakeStacks()
		env, cookie := newStacksEnv(t, fake)
		body := `{"name":"lab","services":[{"service":"web","config":{"image":"nginx:1"}}],"extra":{"` + key + `":{}}}`
		rec := env.do(t, "POST", "/api/stacks", body, withCookie(cookie))
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"field":"extra"`) || !strings.Contains(rec.Body.String(), "extra may not set services or name") {
			t.Errorf("%s => %d %s", key, rec.Code, rec.Body)
		}
	}
}

func TestCreateStackLegacyFormRejectsExtra(t *testing.T) {
	fake := newFakeStacks()
	env, cookie := newStacksEnv(t, fake)
	rec := env.do(t, "POST", "/api/stacks", `{"name":"lab","service":"web","config":{"image":"nginx:1"},"extra":{"volumes":{}}}`, withCookie(cookie))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"field":"extra"`) {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	// a null extra is "no extra" in either form
	rec = env.do(t, "POST", "/api/stacks", `{"name":"lab","service":"web","config":{"image":"nginx:1"},"extra":null}`, withCookie(cookie))
	if rec.Code != 201 {
		t.Fatalf("null extra: status = %d, body %s", rec.Code, rec.Body)
	}
}

func TestCreateStackBodyLimit(t *testing.T) {
	body := func(dockerfileBytes int) string {
		return `{"name":"lab","services":[{"service":"web","config":{"build":{"context":"./web"}},"dockerfile":{"content":"` +
			strings.Repeat("a", dockerfileBytes) + `"}}]}`
	}
	fake := newFakeStacks()
	env, cookie := newStacksEnv(t, fake)
	rec := env.do(t, "POST", "/api/stacks", body(3<<20), withCookie(cookie))
	if rec.Code != 201 || len(fake.gotCreate.Dockerfiles[0]) != 3<<20 {
		t.Fatalf("3 MiB: status = %d", rec.Code)
	}
	rec = env.do(t, "POST", "/api/stacks", body(33<<20), withCookie(cookie))
	if rec.Code != 400 {
		t.Fatalf("33 MiB: status = %d", rec.Code)
	}
}

func TestCreateStackLegacyFormAcceptsEmptyExtraObject(t *testing.T) {
	fake := newFakeStacks()
	env, cookie := newStacksEnv(t, fake)
	rec := env.do(t, "POST", "/api/stacks", `{"name":"lab","service":"web","config":{"image":"nginx:1"},"extra":{}}`, withCookie(cookie))
	if rec.Code != 201 {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
}
