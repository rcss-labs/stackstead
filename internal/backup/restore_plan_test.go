package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/stacks"
	"github.com/rcss-labs/stackstead/internal/store"
	"github.com/rcss-labs/stackstead/internal/version"
)

type planFixture struct {
	root      string // the only allowed root
	stacksDir string // inside root
	outside   string // a directory outside the allowed roots
	reg       *stacks.Registry
	env       PlanEnv
}

func newPlanFixture(t *testing.T) *planFixture {
	t.Helper()
	base := t.TempDir()
	f := &planFixture{
		root:    filepath.Join(base, "srv"),
		outside: filepath.Join(base, "elsewhere"),
	}
	f.stacksDir = filepath.Join(f.root, "stacks")
	for _, d := range []string{f.stacksDir, f.outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	db, err := store.Open(filepath.Join(base, "stackstead.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	f.reg = stacks.NewRegistry(db, time.Now)
	f.env = PlanEnv{
		Registry:     f.reg,
		AllowedRoots: []string{f.root},
		StacksDir:    f.stacksDir,
		SelfSlug:     "self",
		Networks:     func(context.Context) ([]engine.Network, error) { return nil, nil },
		ReadCurrent:  os.ReadFile,
	}
	setInstalledVersion(t, "v1.4.0")
	return f
}

func setInstalledVersion(t *testing.T, v string) {
	t.Helper()
	old := version.Version
	version.Version = v
	t.Cleanup(func() { version.Version = old })
}

func writeCompose(t *testing.T, dir, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func planManifest(hostSlug string, st ManifestStack) Manifest {
	return Manifest{Format: ManifestFormat, StacksteadVersion: "v1.3.2", Host: hostSlug, HostSlug: hostSlug, Stacks: []ManifestStack{st}}
}

func entryFor(st ManifestStack) stacks.Entry {
	return stacks.Entry{Name: st.Name, ComposePath: st.ComposePath, Origin: stacks.Origin(st.Origin), Project: st.Project}
}

func TestPlanSameHostSamePath(t *testing.T) {
	f := newPlanFixture(t)
	dir := filepath.Join(f.stacksDir, "web")
	compose := writeCompose(t, dir, "services: {old: {}}\n")
	if err := f.reg.Add(context.Background(), stacks.Entry{Name: "web", ComposePath: compose, Origin: stacks.OriginManaged, Project: "web"}); err != nil {
		t.Fatal(err)
	}
	st := ManifestStack{Name: "web", ComposePath: compose, Origin: "managed", Project: "web",
		Volumes: []ManifestVolume{{Key: "data", Kind: "volume", Name: "web_data", Target: "/data", Bytes: 42}},
		Images:  []string{"nginx:1.27"}}
	f.env.ReadArchived = func(rel string) ([]byte, error) {
		if rel != "compose.yaml" {
			t.Fatalf("ReadArchived(%q), want compose.yaml", rel)
		}
		return []byte("services: {new: {}}\n"), nil
	}
	req := StackRestoreRequest{Stack: "web", Files: true, Volumes: true, Images: true}

	plan, err := PlanStackRestore(context.Background(), planManifest("self", st), entryFor(st), req, f.env)
	if err != nil {
		t.Fatal(err)
	}
	if plan.TargetName != "web" || plan.TargetDir != dir || !plan.Overwrites || plan.CrossHost {
		t.Fatalf("plan = %+v, want same-host overwrite of web in %s", plan, dir)
	}
	if plan.Origin != stacks.OriginManaged {
		t.Errorf("Origin = %q", plan.Origin)
	}
	wantDiff := []FileDiff{{Path: compose, Old: "services: {old: {}}\n", New: "services: {new: {}}\n"}}
	if !reflect.DeepEqual(plan.FileDiffs, wantDiff) {
		t.Errorf("FileDiffs = %+v, want %+v", plan.FileDiffs, wantDiff)
	}
	wantVols := []PlannedVolume{{Key: "data", Kind: "volume", Name: "web_data", Target: "/data", Bytes: 42}}
	if !reflect.DeepEqual(plan.Volumes, wantVols) {
		t.Errorf("Volumes = %+v, want %+v", plan.Volumes, wantVols)
	}
	if !reflect.DeepEqual(plan.Images, []string{"nginx:1.27"}) {
		t.Errorf("Images = %v", plan.Images)
	}
	if !contains(plan.Warnings, "volume contents are replaced and not kept") {
		t.Errorf("Warnings = %v, want the volume-replacement warning", plan.Warnings)
	}
}

func TestPlanCrossHostRelocatesWhenOutsideRoots(t *testing.T) {
	f := newPlanFixture(t)
	origDir := filepath.Join(f.outside, "app")
	st := ManifestStack{Name: "app", ComposePath: filepath.Join(origDir, "compose.yaml"), Origin: "external", Project: "app"}

	plan, err := PlanStackRestore(context.Background(), planManifest("other-host", st), entryFor(st),
		StackRestoreRequest{Stack: "app", Files: true}, f.env)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(f.stacksDir, "app"); plan.TargetDir != want {
		t.Errorf("TargetDir = %q, want %q", plan.TargetDir, want)
	}
	if !plan.CrossHost || plan.Overwrites {
		t.Errorf("CrossHost = %v, Overwrites = %v, want true/false", plan.CrossHost, plan.Overwrites)
	}
	if plan.Origin != stacks.OriginManaged {
		t.Errorf("Origin = %q, want managed after relocation into the stacks dir", plan.Origin)
	}
}

func TestPlanCrossHostKeepsAllowedFreePath(t *testing.T) {
	f := newPlanFixture(t)
	origDir := filepath.Join(f.root, "projects", "app") // inside the root, does not exist
	st := ManifestStack{Name: "app", ComposePath: filepath.Join(origDir, "compose.yaml"), Origin: "external", Project: "app"}

	plan, err := PlanStackRestore(context.Background(), planManifest("other-host", st), entryFor(st),
		StackRestoreRequest{Stack: "app", Files: true}, f.env)
	if err != nil {
		t.Fatal(err)
	}
	if plan.TargetDir != origDir {
		t.Errorf("TargetDir = %q, want original %q", plan.TargetDir, origDir)
	}
	if plan.Origin != stacks.OriginExternal {
		t.Errorf("Origin = %q, want external kept at the original path", plan.Origin)
	}

	// The same path, now occupied by something else, is no longer free.
	writeCompose(t, origDir, "services: {}\n")
	plan, err = PlanStackRestore(context.Background(), planManifest("other-host", st), entryFor(st),
		StackRestoreRequest{Stack: "app", Files: true}, f.env)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(f.stacksDir, "app"); plan.TargetDir != want {
		t.Errorf("occupied: TargetDir = %q, want %q", plan.TargetDir, want)
	}
}

func TestPlanNameClashOverwrites(t *testing.T) {
	f := newPlanFixture(t)
	existing := writeCompose(t, filepath.Join(f.stacksDir, "db"), "services: {}\n")
	if err := f.reg.Add(context.Background(), stacks.Entry{Name: "db", ComposePath: existing, Origin: stacks.OriginManaged, Project: "db"}); err != nil {
		t.Fatal(err)
	}
	st := ManifestStack{Name: "db", ComposePath: filepath.Join(f.outside, "db", "compose.yaml"), Origin: "managed", Project: "db"}

	plan, err := PlanStackRestore(context.Background(), planManifest("other-host", st), entryFor(st),
		StackRestoreRequest{Stack: "db", Files: true}, f.env)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Overwrites || plan.TargetName != "db" {
		t.Errorf("Overwrites = %v, TargetName = %q, want overwrite of db", plan.Overwrites, plan.TargetName)
	}

	// A fresh name avoids the overwrite and relocates away from the occupied dir.
	plan, err = PlanStackRestore(context.Background(), planManifest("other-host", st), entryFor(st),
		StackRestoreRequest{Stack: "db", NewName: "db-copy", Files: true}, f.env)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Overwrites || plan.TargetName != "db-copy" || plan.TargetDir != filepath.Join(f.stacksDir, "db-copy") {
		t.Errorf("plan = %+v, want new stack db-copy in its own dir", plan)
	}
}

func TestPlanNewNameClashErrors(t *testing.T) {
	f := newPlanFixture(t)
	existing := writeCompose(t, filepath.Join(f.stacksDir, "taken"), "services: {}\n")
	if err := f.reg.Add(context.Background(), stacks.Entry{Name: "taken", ComposePath: existing, Origin: stacks.OriginManaged, Project: "taken"}); err != nil {
		t.Fatal(err)
	}
	st := ManifestStack{Name: "app", ComposePath: filepath.Join(f.stacksDir, "app", "compose.yaml"), Origin: "managed", Project: "app"}

	_, err := PlanStackRestore(context.Background(), planManifest("self", st), entryFor(st),
		StackRestoreRequest{Stack: "app", NewName: "taken", Files: true}, f.env)
	if !errors.Is(err, stacks.ErrStackExists) {
		t.Fatalf("err = %v, want ErrStackExists", err)
	}
}

func TestPlanSkipsAbsoluteBindOutsideRoots(t *testing.T) {
	f := newPlanFixture(t)
	st := ManifestStack{Name: "app", ComposePath: filepath.Join(f.stacksDir, "app", "compose.yaml"), Origin: "managed", Project: "app",
		Volumes: []ManifestVolume{
			{Key: "etc", Kind: "bind", Name: filepath.Join(f.outside, "etc"), Target: "/etc/app"},
			{Key: "media", Kind: "bind", Name: filepath.Join(f.root, "media"), Target: "/media"},
			{Key: "sibling", Kind: "bind", Name: f.root + "-old/media", Target: "/old"}, // shares the root's string prefix only
			{Key: "rel", Kind: "bind", Name: "./data", Target: "/data"},
			{Key: "vol", Kind: "volume", Name: "app_cache", Target: "/cache"},
		}}

	plan, err := PlanStackRestore(context.Background(), planManifest("other-host", st), entryFor(st),
		StackRestoreRequest{Stack: "app", Volumes: true, VolumeKeys: []string{"etc", "media", "sibling", "rel"}}, f.env)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]PlannedVolume{}
	for _, v := range plan.Volumes {
		got[v.Key] = v
	}
	if len(got) != 4 {
		t.Fatalf("Volumes = %+v, want only the 4 requested keys", plan.Volumes)
	}
	if v := got["etc"]; !v.Skip || v.Reason != "outside allowed roots" {
		t.Errorf("etc = %+v, want skipped outside allowed roots", v)
	}
	if v := got["sibling"]; !v.Skip {
		t.Errorf("sibling = %+v, want skipped (prefix of a root is not inside it)", v)
	}
	if v := got["media"]; v.Skip {
		t.Errorf("media = %+v, want kept (inside root)", v)
	}
	if v := got["rel"]; v.Skip {
		t.Errorf("rel = %+v, want kept (relative binds follow the compose dir)", v)
	}
	if !contains(plan.Warnings, "volume contents are replaced and not kept") {
		t.Errorf("Warnings = %v", plan.Warnings)
	}

	_, err = PlanStackRestore(context.Background(), planManifest("other-host", st), entryFor(st),
		StackRestoreRequest{Stack: "app", Volumes: true, VolumeKeys: []string{"zz", "nope", "aa"}}, f.env)
	if err == nil || !strings.Contains(err.Error(), `"aa", "nope", "zz"`) {
		t.Errorf("unknown volume keys: err = %v, want all three listed in sorted order", err)
	}
}

func TestPlanMissingNetworksListed(t *testing.T) {
	f := newPlanFixture(t)
	f.env.Networks = func(context.Context) ([]engine.Network, error) {
		return []engine.Network{{Name: "frontend"}, {Name: "bridge"}}, nil
	}
	f.env.ArchivedNetworks = []engine.NetworkSpec{
		{Name: "frontend", Driver: "bridge"},
		{Name: "lan", Driver: "macvlan", Parent: "eth0", Subnet: "192.168.1.0/24"},
		{Name: "unused", Driver: "bridge"},
	}
	st := ManifestStack{Name: "app", ComposePath: filepath.Join(f.stacksDir, "app", "compose.yaml"), Origin: "managed", Project: "app",
		Networks: []string{"frontend", "lan"}}

	plan, err := PlanStackRestore(context.Background(), planManifest("other-host", st), entryFor(st),
		StackRestoreRequest{Stack: "app", Files: true}, f.env)
	if err != nil {
		t.Fatal(err)
	}
	want := []engine.NetworkSpec{{Name: "lan", Driver: "macvlan", Parent: "eth0", Subnet: "192.168.1.0/24"}}
	if !reflect.DeepEqual(plan.MissingNetworks, want) {
		t.Errorf("MissingNetworks = %+v, want %+v", plan.MissingNetworks, want)
	}

	// A missing network with no archived spec, or no way to list networks, is warned about.
	st.Networks = []string{"frontend", "ghost"}
	plan, err = PlanStackRestore(context.Background(), planManifest("other-host", st), entryFor(st),
		StackRestoreRequest{Stack: "app", Files: true}, f.env)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSubstring(plan.Warnings, "ghost") {
		t.Errorf("Warnings = %v, want one naming the unknown network ghost", plan.Warnings)
	}
	f.env.Networks = nil
	plan, err = PlanStackRestore(context.Background(), planManifest("other-host", st), entryFor(st),
		StackRestoreRequest{Stack: "app", Files: true}, f.env)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Warnings) == 0 {
		t.Error("Networks == nil: want a warning that networks were not checked")
	}
}

func TestPlanRefusesNewerArchive(t *testing.T) {
	f := newPlanFixture(t)
	st := ManifestStack{Name: "app", ComposePath: filepath.Join(f.stacksDir, "app", "compose.yaml"), Origin: "managed", Project: "app"}
	req := StackRestoreRequest{Stack: "app", Files: true}

	for _, tc := range []struct {
		installed, archive string
		refused            bool
	}{
		{"v1.4.0", "v1.5.0", true},
		{"v1.4.0", "1.4.1-rc.1", true},
		{"v1.4.0", "v1.4.0", false},
		{"v1.4.0", "v1.3.9", false},
		{"v1.10.0", "v1.9.0", false},
		{"dev", "v9.0.0", false},
		{"v1.4.0", "dev", false},
		{"v1.4.0-rc.1", "v1.4.0", true},
		{"v1.4.0", "v1.4.0-rc.1", false},
	} {
		setInstalledVersion(t, tc.installed)
		m := planManifest("self", st)
		m.StacksteadVersion = tc.archive
		_, err := PlanStackRestore(context.Background(), m, entryFor(st), req, f.env)
		if got := errors.Is(err, ErrArchiveNewer); got != tc.refused {
			t.Errorf("installed %s, archive %s: err = %v, want refused=%v", tc.installed, tc.archive, err, tc.refused)
		}
	}
}

func TestPlanDeletedStackRecreatedFromArchivedRow(t *testing.T) {
	f := newPlanFixture(t)
	dir := filepath.Join(f.root, "ext", "legacy") // the stack and its dir are gone
	archived := stacks.Entry{Name: "legacy", ComposePath: filepath.Join(dir, "docker-compose.yml"), Origin: stacks.OriginExternal, Project: "legacy-proj"}
	st := ManifestStack{Name: "legacy", ComposePath: archived.ComposePath, Origin: "external", Project: "legacy-proj"}
	f.env.ReadArchived = func(rel string) ([]byte, error) { return []byte("services: {}\n"), nil }

	plan, err := PlanStackRestore(context.Background(), planManifest("self", st), archived,
		StackRestoreRequest{Stack: "legacy", Files: true}, f.env)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Overwrites || plan.TargetName != "legacy" || plan.TargetDir != dir || plan.Origin != stacks.OriginExternal {
		t.Errorf("plan = %+v, want recreation of external stack legacy in %s", plan, dir)
	}
	want := []FileDiff{{Path: archived.ComposePath, Old: "", New: "services: {}\n"}}
	if !reflect.DeepEqual(plan.FileDiffs, want) {
		t.Errorf("FileDiffs = %+v, want %+v", plan.FileDiffs, want)
	}

	_, err = PlanStackRestore(context.Background(), planManifest("self", st), archived,
		StackRestoreRequest{Stack: "missing", Files: true}, f.env)
	if !errors.Is(err, ErrStackNotInArchive) {
		t.Errorf("err = %v, want ErrStackNotInArchive", err)
	}
}

func TestArchivedEntryReadsStagedDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stackstead.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	want := stacks.Entry{Name: "web", ComposePath: "/srv/stacks/web/compose.yaml", Origin: stacks.OriginManaged, Project: "web"}
	if err := stacks.NewRegistry(db, time.Now).Add(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	db.Close()

	got, err := ArchivedEntry(context.Background(), path, "web")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != want.Name || got.ComposePath != want.ComposePath || got.Origin != want.Origin || got.Project != want.Project {
		t.Errorf("ArchivedEntry = %+v, want %+v", got, want)
	}
	if _, err := ArchivedEntry(context.Background(), path, "gone"); !errors.Is(err, stacks.ErrStackNotFound) {
		t.Errorf("missing stack: err = %v, want ErrStackNotFound", err)
	}
}

func TestPlanRejectsUnsafeStackName(t *testing.T) {
	f := newPlanFixture(t)
	st := ManifestStack{Name: "../../etc", ComposePath: filepath.Join(f.outside, "x", "compose.yaml"), Origin: "managed", Project: "x"}
	plan, err := PlanStackRestore(context.Background(), planManifest("other-host", st), entryFor(st),
		StackRestoreRequest{Stack: "../../etc", Files: true}, f.env)
	if err == nil {
		t.Fatalf("plan = %+v, want an error for an unsafe archived stack name", plan)
	}

	// A stacks dir outside the allowed roots must never be written to.
	f.env.StacksDir = filepath.Join(f.outside, "stacks")
	st = ManifestStack{Name: "app", ComposePath: filepath.Join(f.outside, "app", "compose.yaml"), Origin: "managed", Project: "app"}
	plan, err = PlanStackRestore(context.Background(), planManifest("other-host", st), entryFor(st),
		StackRestoreRequest{Stack: "app", Files: true}, f.env)
	if err == nil {
		t.Fatalf("plan = %+v, want an error for a target outside the allowed roots", plan)
	}
}

func TestPlanRebasesBindsUnderComposeDir(t *testing.T) {
	f := newPlanFixture(t)
	origDir := filepath.Join(f.outside, "app")
	st := ManifestStack{Name: "app", ComposePath: filepath.Join(origDir, "compose.yaml"), Origin: "managed", Project: "app",
		Volumes: []ManifestVolume{{Key: "data", Kind: "bind", Name: filepath.Join(origDir, "data", "db"), Target: "/var/lib/db"}}}

	plan, err := PlanStackRestore(context.Background(), planManifest("other-host", st), entryFor(st),
		StackRestoreRequest{Stack: "app", Volumes: true}, f.env)
	if err != nil {
		t.Fatal(err)
	}
	want := []PlannedVolume{{Key: "data", Kind: "bind", Name: filepath.Join(f.stacksDir, "app", "data", "db"), Target: "/var/lib/db"}}
	if !reflect.DeepEqual(plan.Volumes, want) {
		t.Errorf("Volumes = %+v, want %+v (rebased onto the target dir, not skipped)", plan.Volumes, want)
	}
}

func TestPlanOverwriteTargetsExistingStackDir(t *testing.T) {
	f := newPlanFixture(t)
	liveDir := filepath.Join(f.outside, "live", "db") // registered here, outside the roots
	existing := writeCompose(t, liveDir, "services: {}\n")
	if err := f.reg.Add(context.Background(), stacks.Entry{Name: "db", ComposePath: existing, Origin: stacks.OriginExternal, Project: "db"}); err != nil {
		t.Fatal(err)
	}
	st := ManifestStack{Name: "db", ComposePath: filepath.Join(f.root, "archived", "db", "compose.yaml"), Origin: "managed", Project: "db"}

	plan, err := PlanStackRestore(context.Background(), planManifest("other-host", st), entryFor(st),
		StackRestoreRequest{Stack: "db", Files: true}, f.env)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Overwrites || plan.TargetDir != liveDir {
		t.Errorf("Overwrites = %v, TargetDir = %q, want overwrite in the existing dir %q", plan.Overwrites, plan.TargetDir, liveDir)
	}
	if plan.Origin != stacks.OriginExternal {
		t.Errorf("Origin = %q, want the existing stack's origin", plan.Origin)
	}
}

func TestPlanRefusesOccupiedFallbackDir(t *testing.T) {
	f := newPlanFixture(t)
	writeCompose(t, filepath.Join(f.stacksDir, "app"), "unrelated\n")
	st := ManifestStack{Name: "app", ComposePath: filepath.Join(f.outside, "app", "compose.yaml"), Origin: "managed", Project: "app"}

	_, err := PlanStackRestore(context.Background(), planManifest("other-host", st), entryFor(st),
		StackRestoreRequest{Stack: "app", Files: true}, f.env)
	if !errors.Is(err, ErrTargetOccupied) {
		t.Errorf("err = %v, want ErrTargetOccupied", err)
	}
}

func containsSubstring(list []string, sub string) bool {
	for _, v := range list {
		if strings.Contains(v, sub) {
			return true
		}
	}
	return false
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// R17: overwriting a stack whose compose file has another name diffs and
// writes that existing file, not a second file under the archived name.
func TestPlanOverwriteDiffsExistingComposeName(t *testing.T) {
	f := newPlanFixture(t)
	dir := filepath.Join(f.stacksDir, "web")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(existing, []byte("services: {old: {}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.reg.Add(context.Background(), stacks.Entry{Name: "web", ComposePath: existing, Origin: stacks.OriginManaged, Project: "web"}); err != nil {
		t.Fatal(err)
	}
	st := ManifestStack{Name: "web", ComposePath: filepath.Join(dir, "compose.yaml"), Origin: "managed", Project: "web"}
	f.env.ReadArchived = func(rel string) ([]byte, error) {
		if rel != "compose.yaml" {
			t.Fatalf("ReadArchived(%q), want the archived name compose.yaml", rel)
		}
		return []byte("services: {new: {}}\n"), nil
	}

	plan, err := PlanStackRestore(context.Background(), planManifest("self", st), entryFor(st),
		StackRestoreRequest{Stack: "web", Files: true}, f.env)
	if err != nil {
		t.Fatal(err)
	}
	want := []FileDiff{{Path: existing, Old: "services: {old: {}}\n", New: "services: {new: {}}\n"}}
	if !reflect.DeepEqual(plan.FileDiffs, want) {
		t.Errorf("FileDiffs = %+v, want %+v", plan.FileDiffs, want)
	}
	if plan.ComposePath != existing || plan.Project != "web" {
		t.Errorf("ComposePath = %q, Project = %q, want %q and web", plan.ComposePath, plan.Project, existing)
	}
}

// A new name is also the compose project, so the project's named volumes
// follow it instead of overwriting the original stack's volumes.
func TestPlanNewNameRenamesProjectVolumes(t *testing.T) {
	f := newPlanFixture(t)
	st := ManifestStack{Name: "app", ComposePath: filepath.Join(f.outside, "app", "compose.yaml"), Origin: "managed", Project: "app",
		Volumes: []ManifestVolume{
			{Key: "v-app_data", Kind: "volume", Name: "app_data", Target: "/data"},
			{Key: "v-shared", Kind: "volume", Name: "shared", Target: "/shared"},
		}}

	plan, err := PlanStackRestore(context.Background(), planManifest("other-host", st), entryFor(st),
		StackRestoreRequest{Stack: "app", NewName: "app2", Files: true, Volumes: true}, f.env)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Project != "app2" || plan.ComposePath != filepath.Join(f.stacksDir, "app2", "compose.yaml") {
		t.Errorf("Project = %q, ComposePath = %q", plan.Project, plan.ComposePath)
	}
	if len(plan.Volumes) != 2 || plan.Volumes[0].Name != "app2_data" || plan.Volumes[1].Name != "shared" {
		t.Errorf("Volumes = %+v, want app_data renamed to app2_data and shared kept", plan.Volumes)
	}
}

func TestPlanRefusesProjectOwnedByAnotherStack(t *testing.T) {
	f := newPlanFixture(t)
	if err := f.reg.Add(context.Background(), stacks.Entry{Name: "owner", ComposePath: filepath.Join(f.stacksDir, "owner", "compose.yaml"), Origin: stacks.OriginManaged, Project: "app"}); err != nil {
		t.Fatal(err)
	}
	st := ManifestStack{Name: "app", ComposePath: filepath.Join(f.outside, "app", "compose.yaml"), Origin: "managed", Project: "app"}
	_, err := PlanStackRestore(context.Background(), planManifest("other-host", st), entryFor(st), StackRestoreRequest{Stack: "app", Files: true}, f.env)
	if !errors.Is(err, stacks.ErrProjectTaken) {
		t.Errorf("err = %v, want ErrProjectTaken", err)
	}
}

func TestCheckArchiveVersionTreatsUnparsableInstalledAsUnknown(t *testing.T) {
	for _, tc := range []struct {
		installed, archive string
		wantErr            bool
	}{
		{"e2e-test", "v9.0.0", false}, // an unstamped or custom build cannot be ordered, like dev
		{"custom-build", "v1.0.0", false},
		{"e2e-test", "e2e-test", false}, // the same build wrote the archive, so it cannot be newer
		{"v1.4.0", "garbage", true},     // an archive from a non-dev build must carry a version
		{"v1.4.0", "1.4", true},
		{"v1.4.0", "dev", false},
		{"v1.4.0", "v2.0.0", true},
	} {
		err := checkArchiveVersion(tc.archive, tc.installed)
		if (err != nil) != tc.wantErr {
			t.Errorf("installed %q, archive %q: err = %v, wantErr %v", tc.installed, tc.archive, err, tc.wantErr)
		}
	}
}

// A bind recorded under the resolved path of a symlinked stack directory must
// be rebased onto the new target, never left pointing at the original stack.
func TestPlanVolumesRebasesBindThroughSymlinkedDir(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "stacks-link")
	symlinkOrFatal(t, real, link)
	if err := os.MkdirAll(filepath.Join(real, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	vols := []ManifestVolume{{Key: "b-1", Kind: "bind", Name: filepath.Join(real, "app", "data"), Target: "/d"}}
	target := filepath.Join(link, "app2")

	got, err := planVolumes(vols, nil, []string{link}, filepath.Join(link, "app"), target)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(target, "data"); len(got) != 1 || got[0].Name != want || got[0].Skip {
		t.Errorf("planned volumes = %+v, want bind rebased to %s", got, want)
	}
}
