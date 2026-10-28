package backup

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rcss-labs/stackstead/internal/stacks"
)

// An image tar that names a tag other than its manifest ref must never finish
// loading, or docker would retag an unrelated local image (SEC-13).
func TestHostRestoreRefusesImageWithForeignTag(t *testing.T) {
	for name, tc := range map[string]struct{ file, body string }{
		"foreign only":   {"manifest.json", `[{"RepoTags":["postgres:16"]}]`},
		"foreign + ref":  {"manifest.json", `[{"RepoTags":["nginx:1.27","postgres:16"]}]`},
		"legacy listing": {"repositories", `{"postgres":{"16":"abc"}}`},
	} {
		t.Run(name, func(t *testing.T) {
			s := newHostSetup(t)
			s.fixture.images["web"]["nginx:1.27"] = map[string]string{tc.file: tc.body, "blobs/sha256/abc": "layer"}
			path := s.fixture.build(t, t.TempDir(), true)

			report, err := s.restore(t, path)
			if err != nil {
				t.Fatal(err)
			}
			if len(s.eng.loads) != 0 {
				t.Errorf("a foreign-tagged image finished loading: %v", s.eng.loads)
			}
			if !slices.ContainsFunc(report.Items, func(it ItemResult) bool {
				return it.Kind == "image" && it.Name == "nginx:1.27" && it.Status == "failed" && strings.Contains(it.Note, "other tags")
			}) {
				t.Errorf("no failed image item naming the foreign tags: %+v", report.Items)
			}
		})
	}
}

func TestHostRestoreLoadsImageWithOnlyItsManifestTag(t *testing.T) {
	for _, ref := range []string{"nginx:1.27", "nginx"} {
		t.Run(ref, func(t *testing.T) {
			s := newHostSetup(t)
			delete(s.fixture.images["web"], "nginx:1.27")
			tag := ref
			if !strings.Contains(tag, ":") {
				tag += ":latest" // docker save names an untagged ref with :latest
			}
			s.fixture.images["web"][ref] = map[string]string{"manifest.json": `[{"RepoTags":["` + tag + `"]}]`, "blobs/sha256/abc": "layer"}
			path := s.fixture.build(t, t.TempDir(), true)

			report, err := s.restore(t, path)
			if err != nil {
				t.Fatal(err)
			}
			if len(s.eng.loads) != 1 {
				t.Errorf("loads = %v, want the correctly tagged image loaded; items %+v", s.eng.loads, report.Items)
			}
		})
	}
}

// Editing only the manifest or sidecar must not redirect archived bind data to
// another directory: the archive key has to be the key of the volume's name.
func TestHostRestoreRefusesVolumeWhoseKeyDoesNotMatchItsName(t *testing.T) {
	for _, kind := range []string{"bind", "volume"} {
		t.Run(kind, func(t *testing.T) {
			s := newHostSetup(t)
			redirected := filepath.Join(s.rootA, "web", "elsewhere")
			key, name := VolumeKey(stacks.Mount{Kind: "bind", Source: filepath.Join(s.rootA, "web", "uploads")}), redirected
			if kind == "volume" {
				key, name = "v-web_data", "web_other"
			}
			s.fixture.volumes["web"] = []ManifestVolume{{Key: key, Kind: kind, Name: name, Target: "/x"}}
			s.fixture.volData = map[string]map[string]string{"web/" + key: {"data/": "", "data/f": "data"}}
			path := s.fixture.build(t, t.TempDir(), true)

			report, err := s.restore(t, path)
			if err != nil {
				t.Fatal(err)
			}
			if len(s.eng.puts) != 0 {
				t.Errorf("volume data was written despite the key mismatch: %v", s.eng.puts)
			}
			if _, err := os.Stat(redirected); err == nil {
				t.Error("the redirected bind directory was created")
			}
			if !slices.ContainsFunc(report.Items, func(it ItemResult) bool {
				return it.Kind == conflictVolume && it.Status == "failed" && strings.Contains(it.Note, "does not match")
			}) {
				t.Errorf("no failed volume item for the key mismatch: %+v", report.Items)
			}
		})
	}
}

func TestRestoreStackRefusesImageWithForeignTag(t *testing.T) {
	e := newRestoreEnv(t)
	compose := e.existingStack(t, "web", "compose.yaml", "services: {}\n", false)
	f := webFixture(compose, false)
	f.images["web"]["nginx:1.27"] = map[string]string{"manifest.json": `[{"RepoTags":["postgres:16"]}]`}
	ref := e.addArchive(t, f)

	e.run(t, StackRestoreRequest{Archive: ref, Stack: "web", Images: true})
	if len(e.eng.loads) != 0 {
		t.Errorf("a foreign-tagged image finished loading: %v", e.eng.loads)
	}
	if acts := e.restoreActions(t); len(acts) != 1 || acts[0].Result != "failed" || !strings.Contains(acts[0].Detail, "other tags") {
		t.Errorf("action log = %+v, want the refused image reported", acts)
	}
}

func TestRestoreStackLoadsImageWithOnlyItsManifestTag(t *testing.T) {
	e := newRestoreEnv(t)
	compose := e.existingStack(t, "web", "compose.yaml", "services: {}\n", false)
	f := webFixture(compose, false)
	f.images["web"]["nginx:1.27"] = map[string]string{"manifest.json": `[{"RepoTags":["nginx:1.27"]}]`}
	ref := e.addArchive(t, f)

	e.run(t, StackRestoreRequest{Archive: ref, Stack: "web", Images: true})
	if len(e.eng.loads) != 1 {
		t.Errorf("loads = %v, want the image loaded", e.eng.loads)
	}
}

func TestRestoreStackRefusesBindWhoseKeyDoesNotMatchItsName(t *testing.T) {
	e := newRestoreEnv(t)
	compose := e.existingStack(t, "web", "compose.yaml", "services: {}\n", false)
	archived := filepath.Join(e.root, "data", "web")
	redirected := filepath.Join(e.root, "data", "elsewhere")
	f := webFixture(compose, false)
	key := VolumeKey(stacks.Mount{Kind: "bind", Source: archived})
	f.volumes = map[string][]ManifestVolume{"web": {{Key: key, Kind: "bind", Name: redirected, Target: "/data"}}}
	f.volData = map[string]map[string]string{"web/" + key: {"x": "1"}}
	ref := e.addArchive(t, f)

	e.run(t, StackRestoreRequest{Archive: ref, Stack: "web", Volumes: true})
	if calls := e.eng.changes(); indexOf(calls, "create:") >= 0 || indexOf(calls, "put:") >= 0 {
		t.Errorf("calls = %v, want nothing mounted or written", calls)
	}
	if _, err := os.Stat(redirected); err == nil {
		t.Error("the redirected bind directory was created")
	}
	if acts := e.restoreActions(t); len(acts) != 1 || acts[0].Result != "failed" || !strings.Contains(acts[0].Detail, "does not match") {
		t.Errorf("action log = %+v, want the key mismatch reported", acts)
	}
}
