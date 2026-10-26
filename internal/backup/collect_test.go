package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"syscall"
	"testing"

	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/stacks"
	"github.com/rcss-labs/stackstead/internal/store"
)

// archiveNames closes w and lists the entry names (without the manifest).
func archiveNames(t *testing.T, w *Writer, buf *bytes.Buffer) []string {
	t.Helper()
	if err := w.Close(Manifest{}); err != nil {
		t.Fatal(err)
	}
	r, err := NewReader(buf)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for {
		h, _, err := r.Next()
		if err != nil {
			break
		}
		if h.Name != ManifestEntryName {
			names = append(names, h.Name)
		}
	}
	sort.Strings(names)
	return names
}

func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCollectStackFilesSkipsBindSources(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"compose.yaml": "services: {}", ".env": "A=1", "data/big": "xxxx"})
	var buf bytes.Buffer
	w := NewWriter(&buf)
	items, err := CollectStackFiles(w, stacks.Entry{Name: "web", ComposePath: filepath.Join(dir, "compose.yaml")}, []string{filepath.Join(dir, "data")})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("unexpected items %+v", items)
	}
	want := []string{"base/stacks/web/", "base/stacks/web/.env", "base/stacks/web/compose.yaml"}
	if got := archiveNames(t, w, &buf); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestCollectStackFilesSkipsSpecialFiles(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"compose.yaml": "x"})
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o600); err != nil {
		t.Skip("mkfifo unavailable:", err)
	}
	var buf bytes.Buffer
	w := NewWriter(&buf)
	items, err := CollectStackFiles(w, stacks.Entry{Name: "web", ComposePath: filepath.Join(dir, "compose.yaml")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Status != "skipped" || items[0].Note == "" || items[0].Stack != "web" {
		t.Fatalf("items = %+v", items)
	}
	for _, n := range archiveNames(t, w, &buf) {
		if n == "base/stacks/web/pipe" {
			t.Fatal("fifo was archived")
		}
	}
}

func TestCollectStackFilesSymlinks(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"compose.yaml": "x", "conf/a": "1"})
	for link, target := range map[string]string{"inside": "conf/a", "outside": "../elsewhere", "abs": "/etc/passwd"} {
		if err := os.Symlink(target, filepath.Join(dir, link)); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	w := NewWriter(&buf)
	items, err := CollectStackFiles(w, stacks.Entry{Name: "web", ComposePath: filepath.Join(dir, "compose.yaml")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 skipped symlinks, got %+v", items)
	}
	for _, it := range items {
		if it.Status != "skipped" || it.Note != "symlink points outside the archived directory" {
			t.Fatalf("item %+v", it)
		}
	}
	got := archiveNames(t, w, &buf)
	var hasInside bool
	for _, n := range got {
		if n == "base/stacks/web/inside" {
			hasInside = true
		}
		if n == "base/stacks/web/outside" || n == "base/stacks/web/abs" {
			t.Fatalf("escaping symlink archived: %v", got)
		}
	}
	if !hasInside {
		t.Fatalf("inside symlink missing: %v", got)
	}
}

func TestCollectDirMissingIsNotAnError(t *testing.T) {
	var buf bytes.Buffer
	items, err := CollectDir(NewWriter(&buf), filepath.Join(t.TempDir(), "nope"), "base/images-library")
	if err != nil || len(items) != 0 {
		t.Fatalf("items=%v err=%v", items, err)
	}
}

type fakeEngine struct {
	networks    []engine.Network
	archiveErr  error
	archive     []byte
	removed     []string
	removeForce []bool
	spec        engine.HelperSpec
	archivePath string
}

func (f *fakeEngine) CreateContainer(_ context.Context, spec engine.HelperSpec) (string, error) {
	f.spec = spec
	return "helper1", nil
}
func (f *fakeEngine) WaitContainer(context.Context, string) (int, error) { return 0, nil }
func (f *fakeEngine) RemoveContainer(_ context.Context, id string, force bool) error {
	f.removed = append(f.removed, id)
	f.removeForce = append(f.removeForce, force)
	return nil
}
func (f *fakeEngine) GetArchive(_ context.Context, _ string, path string) (io.ReadCloser, error) {
	f.archivePath = path
	if f.archiveErr != nil {
		return nil, f.archiveErr
	}
	return io.NopCloser(bytes.NewReader(f.archive)), nil
}
func (f *fakeEngine) PutArchive(context.Context, string, string, io.Reader) error { return nil }
func (f *fakeEngine) PullImage(context.Context, string, func(string)) error       { return nil }
func (f *fakeEngine) InspectImage(context.Context, string) (engine.ImageInspect, error) {
	return engine.ImageInspect{}, errors.New("not found")
}
func (f *fakeEngine) SaveImage(context.Context, string) (io.ReadCloser, error) { return nil, nil }
func (f *fakeEngine) LoadImage(context.Context, io.Reader) error               { return nil }
func (f *fakeEngine) Networks(context.Context) ([]engine.Network, error)       { return f.networks, nil }
func (f *fakeEngine) CreateNetwork(context.Context, engine.NetworkSpec) (string, error) {
	return "", nil
}
func (f *fakeEngine) InspectVolume(context.Context, string) (engine.VolumeInspect, error) {
	return engine.VolumeInspect{}, nil
}
func (f *fakeEngine) CreateVolume(context.Context, string, map[string]string) error { return nil }
func (f *fakeEngine) InspectContainer(context.Context, string) (engine.ContainerInspect, error) {
	return engine.ContainerInspect{}, nil
}
func (f *fakeEngine) Containers(context.Context) ([]engine.Container, error) { return nil, nil }

func TestCollectNetworksOnlyStacksteadCreated(t *testing.T) {
	eng := &fakeEngine{networks: []engine.Network{
		{Name: "mine", Driver: "bridge", Internal: true, Labels: map[string]string{"dev.stackstead.created": "true"}},
		{Name: "compose_default", Driver: "bridge", Labels: map[string]string{"com.docker.compose.project": "x"}},
		{Name: "bridge", Driver: "bridge"},
	}}
	data, n, err := CollectNetworks(context.Background(), eng)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	var specs []engine.NetworkSpec
	if err := json.Unmarshal(data, &specs); err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 || specs[0].Name != "mine" || specs[0].Driver != "bridge" || !specs[0].Internal {
		t.Fatalf("specs = %+v", specs)
	}
}

func TestCollectDBSnapshotAndCleanup(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	path, cleanup, err := CollectDB(context.Background(), db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(path); err != nil || st.Size() == 0 {
		t.Fatalf("snapshot missing: %v", err)
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("cleanup left the snapshot")
	}
}

func TestCollectStackFilesKeepsComposeAndEnvDespiteBinds(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"compose.yaml": "x", "compose.override.yml": "y", "docker-compose.yaml": "z", ".env": "A=1", "data/big": "xxxx", "other": "o"})
	entry := stacks.Entry{Name: "web", ComposePath: filepath.Join(dir, "compose.yaml")}
	skip := []string{dir, filepath.Join(dir, ".env"), filepath.Join(dir, "compose.yaml"), filepath.Join(dir, "data")}
	var buf bytes.Buffer
	w := NewWriter(&buf)
	items, err := CollectStackFiles(w, entry, skip)
	if err != nil || len(items) != 0 {
		t.Fatalf("items=%v err=%v", items, err)
	}
	got := archiveNames(t, w, &buf)
	for _, must := range []string{"base/stacks/web/compose.yaml", "base/stacks/web/compose.override.yml", "base/stacks/web/docker-compose.yaml", "base/stacks/web/.env"} {
		if !slices.Contains(got, must) {
			t.Fatalf("%s missing: %v", must, got)
		}
	}
	for _, n := range got {
		if strings.HasPrefix(n, "base/stacks/web/data") {
			t.Fatalf("bind source archived: %v", got)
		}
	}
}

func TestCollectStackFilesUnreadableDirIsRecorded(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"compose.yaml": "x", "locked/f": "1"})
	if err := os.Chmod(filepath.Join(dir, "locked"), 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(filepath.Join(dir, "locked"), 0o755)
	var buf bytes.Buffer
	w := NewWriter(&buf)
	items, err := CollectStackFiles(w, stacks.Entry{Name: "web", ComposePath: filepath.Join(dir, "compose.yaml")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Status != "failed" || items[0].Note == "" {
		t.Fatalf("items = %+v", items)
	}
	if !slices.Contains(archiveNames(t, w, &buf), "base/stacks/web/compose.yaml") {
		t.Fatal("compose.yaml lost")
	}
}

func TestCollectDBFailureLeavesNoTempDir(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	tmp := t.TempDir()
	if _, _, err := CollectDB(context.Background(), db, tmp); err == nil {
		t.Fatal("want error from a closed db")
	}
	if entries, _ := os.ReadDir(tmp); len(entries) != 0 {
		t.Fatalf("temp dir left behind: %v", entries)
	}
}
