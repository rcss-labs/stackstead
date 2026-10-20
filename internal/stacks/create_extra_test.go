package stacks_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rcss-labs/stackstead/internal/composefile"
	"github.com/rcss-labs/stackstead/internal/engine"
)

func mustExtra(t *testing.T, raw string) composefile.Extra {
	t.Helper()
	extra, err := composefile.ParseExtra([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return extra
}

func managedWeb() composefile.NamedService {
	return composefile.NamedService{Name: "web", Config: composefile.ServiceConfig{Build: &composefile.Build{Context: "./web"}}}
}

func TestCreateWritesManagedDockerfile(t *testing.T) {
	h := newHarness(t)
	req := multiRequest(managedWeb())
	req.Dockerfiles = map[int][]byte{0: []byte("FROM busybox\n")}
	if _, err := h.svc.Create(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(h.dir, "lab", "web", "Dockerfile")
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "FROM busybox\n" {
		t.Fatalf("Dockerfile = %q, %v", data, err)
	}
	if info, _ := os.Stat(file); info.Mode().Perm() != 0o644 {
		t.Errorf("file mode = %v", info.Mode().Perm())
	}
	if info, _ := os.Stat(filepath.Join(h.dir, "lab", "web")); info.Mode().Perm() != 0o755 {
		t.Errorf("folder mode = %v", info.Mode().Perm())
	}
	compose, _ := os.ReadFile(h.path("lab"))
	for _, want := range []string{"build: ./web", "image: stackstead/lab-web:latest"} {
		if !strings.Contains(string(compose), want) {
			t.Errorf("compose lacks %q:\n%s", want, compose)
		}
	}
}

func TestCreateDockerfileRollbackOnComposeConfigFailure(t *testing.T) {
	h := newHarness(t)
	h.compose.configErr = errors.New("compose config failed")
	req := multiRequest(managedWeb())
	req.Dockerfiles = map[int][]byte{0: []byte("FROM busybox\n")}
	if _, err := h.svc.Create(context.Background(), req); err == nil {
		t.Fatal("want error")
	}
	for _, p := range []string{filepath.Join(h.dir, "lab", "web"), filepath.Join(h.dir, "lab")} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left behind: %v", p, err)
		}
	}
}

func TestCreateDockerfileLimits(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content []byte
		msg     string
	}{
		{"too large", bytes.Repeat([]byte("a"), 1<<20+1), "Dockerfile is larger than 1 MiB"},
		{"NUL", []byte("FROM x\x00"), "Dockerfile must not contain NUL bytes"},
		{"empty", nil, "Dockerfile is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			req := multiRequest(managedWeb())
			req.Dockerfiles = map[int][]byte{0: tc.content}
			_, err := h.svc.Create(context.Background(), req)
			fs := fieldsOf(t, err)
			if len(fs) != 1 || fs[0].Field != "services[0].dockerfile" || fs[0].Message != tc.msg {
				t.Fatalf("fields = %+v", fs)
			}
			if _, err := os.Stat(filepath.Join(h.dir, "lab")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("stack dir exists: %v", err)
			}
		})
	}
}

func TestCreateDockerfileRequiresBuildInsideStack(t *testing.T) {
	const msg = "a Dockerfile can only be written inside the stack's folder"
	for _, tc := range []struct {
		name string
		cfg  func(h *harness) composefile.ServiceConfig
	}{
		{"image only", func(*harness) composefile.ServiceConfig { return composefile.ServiceConfig{Image: "nginx:1"} }},
		// both contexts are allowed (inside the allowed root) yet outside the stack folder
		{"parent context", func(h *harness) composefile.ServiceConfig {
			if err := os.MkdirAll(filepath.Join(h.dir, "x"), 0o755); err != nil {
				t.Fatal(err)
			}
			return composefile.ServiceConfig{Build: &composefile.Build{Context: "../x"}}
		}},
		{"absolute outside", func(h *harness) composefile.ServiceConfig {
			return composefile.ServiceConfig{Build: &composefile.Build{Context: filepath.Dir(h.dir)}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			cfg := tc.cfg(h)
			req := multiRequest(composefile.NamedService{Name: "ok", Config: webCfg()}, composefile.NamedService{Name: "web", Config: cfg})
			req.Dockerfiles = map[int][]byte{1: []byte("FROM x\n")}
			_, err := h.svc.Create(context.Background(), req)
			fs := fieldsOf(t, err)
			found := false
			for _, f := range fs {
				if f.Field == "services[1].dockerfile" && f.Message == msg {
					found = true
				}
			}
			if !found {
				t.Fatalf("fields = %+v", fs)
			}
			if _, err := os.Stat(filepath.Join(h.dir, "lab")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("stack dir exists: %v", err)
			}
		})
	}
}

func TestCreateMergesExtras(t *testing.T) {
	h := newHarness(t)
	req := multiRequest(composefile.NamedService{
		Name: "web", Config: webCfg(),
		Extra: mustExtra(t, `{"labels":{"a":"b"},"logging":{"driver":"json-file"}}`),
	})
	req.Extra = mustExtra(t, `{"volumes":{"data":{}},"secrets":{"s":{"file":"./s.txt"}},"x-common":{"k":1}}`)
	if _, err := h.svc.Create(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	compose, _ := os.ReadFile(h.path("lab"))
	for _, want := range []string{"labels:", "logging:", "driver: json-file", "volumes:", "secrets:", "x-common:"} {
		if !strings.Contains(string(compose), want) {
			t.Errorf("compose lacks %q:\n%s", want, compose)
		}
	}
	if len(h.compose.configContents) != 1 || h.compose.configContents[0] != string(compose) {
		t.Errorf("compose config saw %q", h.compose.configContents)
	}
}

func TestCreateExtraBuildConfined(t *testing.T) {
	h := newHarness(t)
	req := multiRequest(composefile.NamedService{
		Name: "web", Extra: mustExtra(t, `{"build":{"context":"/etc","args":{"A":"1"}}}`),
	})
	_, err := h.svc.Create(context.Background(), req)
	fs := fieldsOf(t, err)
	if len(fs) != 1 || fs[0].Field != "services[0].build.context" || fs[0].Message != "build context must be inside an allowed directory" {
		t.Fatalf("fields = %+v", fs)
	}
}

func TestCreateExtraAdditionalContextsConfined(t *testing.T) {
	h := newHarness(t)
	req := multiRequest(composefile.NamedService{
		Name: "web", Extra: mustExtra(t, `{"build":{"context":".","additional_contexts":{"x":"/etc","y":"docker-image://alpine:3","z":"https://example.com/r.git"}}}`),
	})
	_, err := h.svc.Create(context.Background(), req)
	fs := fieldsOf(t, err)
	if len(fs) != 1 || fs[0].Field != "services[0].build.context" || fs[0].Message != "build context must be inside an allowed directory" {
		t.Fatalf("fields = %+v", fs)
	}
}

func TestCreateExtraLongPortsChecked(t *testing.T) {
	h := newHarness(t)
	h.create(t, "other", webCfg("8080:80"))
	req := multiRequest(composefile.NamedService{
		Name: "web", Config: webCfg(),
		Extra: mustExtra(t, `{"ports":[{"target":80,"published":8080}]}`),
	})
	_, err := h.svc.Create(context.Background(), req)
	fs := fieldsOf(t, err)
	if len(fs) != 1 || fs[0].Field != "services[0].ports[0]" || !strings.Contains(fs[0].Message, "other") {
		t.Fatalf("fields = %+v", fs)
	}
}

func TestCreateExtraStaticIPChecked(t *testing.T) {
	h := newHarness(t)
	h.eng.networks = []engine.Network{{Name: "lan", Subnets: []engine.Subnet{{Prefix: mustPrefix("192.168.1.0/24")}}}}
	h.eng.containers = []engine.Container{{
		ID: "c1", Name: "old", State: "running",
		Networks: map[string]string{"lan": "192.168.1.5"},
	}}
	req := multiRequest(composefile.NamedService{
		Name: "web", Config: webCfg(),
		Extra: mustExtra(t, `{"networks":{"lan":{"ipv4_address":"192.168.1.5","aliases":["a"]}}}`),
	})
	_, err := h.svc.Create(context.Background(), req)
	fs := fieldsOf(t, err)
	if len(fs) != 1 || fs[0].Field != "services[0].networks.lan.ipv4_address" {
		t.Fatalf("fields = %+v", fs)
	}
}

func TestCreateRejectsReservedAndClashingExtras(t *testing.T) {
	h := newHarness(t)
	req := multiRequest(composefile.NamedService{Name: "web", Config: webCfg()})
	req.Extra = mustExtra(t, `{"name":"x"}`)
	fs := fieldsOf(t, func() error { _, err := h.svc.Create(context.Background(), req); return err }())
	if len(fs) != 1 || fs[0].Field != "extra" || fs[0].Message != "extra may not set services or name" {
		t.Fatalf("reserved: %+v", fs)
	}

	// image is a scalar in the form; a mapping in the extra cannot merge.
	req = multiRequest(composefile.NamedService{Name: "web", Config: webCfg(), Extra: mustExtra(t, `{"image":{"a":"b"}}`)})
	fs = fieldsOf(t, func() error { _, err := h.svc.Create(context.Background(), req); return err }())
	if len(fs) != 1 || fs[0].Field != "services[0].extra" || !strings.Contains(fs[0].Message, "image") {
		t.Fatalf("clash: %+v", fs)
	}
}

func TestCreateWithoutExtrasWritesSameBytes(t *testing.T) {
	h := newHarness(t)
	services := []composefile.NamedService{
		{Name: "web", Config: composefile.ServiceConfig{Image: "nginx:1", Ports: []string{"8080:80"}, Networks: []composefile.NetworkAttachment{{Name: "lan"}}}},
		{Name: "db", Config: webCfg()},
	}
	if _, err := h.svc.Create(context.Background(), multiRequest(services...)); err != nil {
		t.Fatal(err)
	}
	want, err := composefile.New(services)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(h.path("lab"))
	if !bytes.Equal(got, want) {
		t.Fatalf("bytes differ:\n%s\nwant:\n%s", got, want)
	}
}
