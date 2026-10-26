package stacks_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

func addStack(t *testing.T, h *harness, name string) stacks.Entry {
	t.Helper()
	dir := filepath.Join(h.dir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	e := stacks.Entry{Name: name, ComposePath: filepath.Join(dir, "compose.yaml"), Origin: stacks.OriginManaged, Project: name}
	if err := os.WriteFile(e.ComposePath, []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.reg.Add(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestServiceMountsResolvesRelativeBind(t *testing.T) {
	h := newHarness(t)
	e := addStack(t, h, "web")
	dir := filepath.Dir(e.ComposePath)
	h.compose.configJSON = `{"services":{"app":{"volumes":[
		{"type":"bind","source":"./data","target":"/var/lib/x"},
		{"type":"volume","source":"db","target":"/db"},
		{"type":"tmpfs","target":"/tmp"}]}},
		"volumes":{"db":{"name":"web_db"}}}`
	got, err := h.svc.Mounts(context.Background(), "web")
	if err != nil {
		t.Fatal(err)
	}
	want := []stacks.Mount{
		{Service: "app", Kind: "volume", Source: "web_db", Target: "/db"},
		{Service: "app", Kind: "bind", Source: filepath.Join(dir, "data"), Target: "/var/lib/x"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestServiceImageRefsPrefersRunningContainers(t *testing.T) {
	h := newHarness(t)
	addStack(t, h, "web")
	h.compose.configJSON = `{"services":{"app":{"image":"nginx:1"}}}`
	got, err := h.svc.ImageRefs(context.Background(), "web")
	if err != nil || !reflect.DeepEqual(got, []string{"nginx:1"}) {
		t.Fatalf("fallback: %v %v", got, err)
	}
	h.eng.containers = []engine.Container{
		{ID: "c1", ImageID: "sha256:bbb", Labels: map[string]string{"com.docker.compose.project": "web"}},
		{ID: "c2", ImageID: "sha256:aaa", Labels: map[string]string{"com.docker.compose.project": "web"}},
		{ID: "c3", ImageID: "sha256:ccc", Labels: map[string]string{"com.docker.compose.project": "other"}},
	}
	got, err = h.svc.ImageRefs(context.Background(), "web")
	if err != nil || !reflect.DeepEqual(got, []string{"sha256:aaa", "sha256:bbb"}) {
		t.Fatalf("running: %v %v", got, err)
	}
}

func TestServiceMountsReportsConfigFailures(t *testing.T) {
	h := newHarness(t)
	addStack(t, h, "web")
	h.compose.configJSONErr = errors.New("compose broke")
	if _, err := h.svc.Mounts(context.Background(), "web"); err == nil {
		t.Fatal("want error when compose config fails")
	}
	h.compose.configJSONErr = nil
	h.compose.configJSON = "{not json"
	if _, err := h.svc.Mounts(context.Background(), "web"); err == nil {
		t.Fatal("want error for malformed config JSON")
	}
}
