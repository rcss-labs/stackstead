package images

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/store"
)

type testClock struct{ t time.Time }

func (c *testClock) now() time.Time { c.t = c.t.Add(time.Second); return c.t }

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	return NewStore(db, clock.now)
}

func mustImage(t *testing.T, s *Store, slug string) Image {
	t.Helper()
	img, err := s.CreateImage(context.Background(), Image{Slug: slug, Name: slug, Source: SourceUpload, DockerfilePath: "Dockerfile"})
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func mustStart(t *testing.T, s *Store, imageID, tag string) Version {
	t.Helper()
	v, err := s.StartVersion(context.Background(), Version{ImageID: imageID, Tag: tag})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestCreateImageSlugUnique(t *testing.T) {
	s := newTestStore(t)
	mustImage(t, s, "app")
	_, err := s.CreateImage(context.Background(), Image{Slug: "app", Name: "Other", Source: SourceUpload})
	if !errors.Is(err, ErrImageExists) {
		t.Fatalf("err = %v, want ErrImageExists", err)
	}
}

func TestImageRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	in := Image{
		Slug: "web", Name: "Web App", Source: SourceGitHub, DockerfilePath: "docker/Dockerfile",
		GitHub:    GitHubSource{Repo: "o/r", Branch: "main", Path: "sub"},
		BuildArgs: map[string]string{"A": "1", "B": "two"},
		Retention: Retention{KeepLast: 5, KeepDays: 30},
	}
	created, err := s.CreateImage(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(created.ID) != 16 || created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatalf("created = %+v, want 16-char ID and timestamps", created)
	}
	for name, get := range map[string]func() (Image, error){
		"Image":     func() (Image, error) { return s.Image(ctx, "web") },
		"ImageByID": func() (Image, error) { return s.ImageByID(ctx, created.ID) },
	} {
		got, err := get()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !got.CreatedAt.Equal(created.CreatedAt) {
			t.Errorf("%s: CreatedAt = %v, want %v", name, got.CreatedAt, created.CreatedAt)
		}
		got.CreatedAt, got.UpdatedAt = in.CreatedAt, in.UpdatedAt
		want := in
		want.ID = created.ID
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %+v, want %+v", name, got, want)
		}
	}

	created.Name = "Renamed"
	created.BuildArgs = map[string]string{}
	created.Retention = Retention{}
	if err := s.UpdateImage(ctx, created); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ImageByID(ctx, created.ID)
	if got.Name != "Renamed" || len(got.BuildArgs) != 0 || got.BuildArgs == nil || got.Retention != (Retention{}) {
		t.Errorf("after update = %+v", got)
	}
	if !got.UpdatedAt.After(got.CreatedAt) {
		t.Errorf("UpdatedAt %v not after CreatedAt %v", got.UpdatedAt, got.CreatedAt)
	}
}

func TestImageNotFoundAndOrder(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if _, err := s.Image(ctx, "nope"); !errors.Is(err, ErrImageNotFound) {
		t.Errorf("Image = %v", err)
	}
	if _, err := s.ImageByID(ctx, "nope"); !errors.Is(err, ErrImageNotFound) {
		t.Errorf("ImageByID = %v", err)
	}
	if err := s.UpdateImage(ctx, Image{ID: "nope", Source: SourceUpload}); !errors.Is(err, ErrImageNotFound) {
		t.Errorf("UpdateImage = %v", err)
	}
	if err := s.DeleteImage(ctx, "nope"); !errors.Is(err, ErrImageNotFound) {
		t.Errorf("DeleteImage = %v", err)
	}
	list, err := s.Images(ctx)
	if err != nil || list == nil || len(list) != 0 {
		t.Errorf("empty Images = %v, %v; want empty non-nil", list, err)
	}
	for _, n := range []string{"zeta", "alpha", "mid"} {
		mustImage(t, s, n)
	}
	list, _ = s.Images(ctx)
	if len(list) != 3 || list[0].Name != "alpha" || list[2].Name != "zeta" {
		t.Errorf("Images order = %+v", list)
	}
}

func TestRevisionsNumberFromOne(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	img := mustImage(t, s, "app")
	if _, ok, err := s.LatestRevision(ctx, img.ID); err != nil || ok {
		t.Fatalf("LatestRevision empty = %v, %v", ok, err)
	}
	for i := 1; i <= 3; i++ {
		r, err := s.AddRevision(ctx, img.ID, "s", `{"n":`+string(rune('0'+i))+`}`)
		if err != nil {
			t.Fatal(err)
		}
		if r.Number != i || r.ImageID != img.ID || r.CreatedAt.IsZero() {
			t.Fatalf("revision %d = %+v", i, r)
		}
	}
	revs, err := s.Revisions(ctx, img.ID)
	if err != nil || len(revs) != 3 || revs[0].Number != 3 || revs[2].Number != 1 {
		t.Fatalf("Revisions = %+v, %v; want newest first", revs, err)
	}
	r, err := s.Revision(ctx, img.ID, 2)
	if err != nil || r.ManifestJSON != `{"n":2}` || r.Summary != "s" {
		t.Errorf("Revision(2) = %+v, %v", r, err)
	}
	if _, err := s.Revision(ctx, img.ID, 9); !errors.Is(err, ErrRevisionNotFound) {
		t.Errorf("Revision(9) = %v", err)
	}
	latest, ok, err := s.LatestRevision(ctx, img.ID)
	if err != nil || !ok || latest.Number != 3 {
		t.Errorf("LatestRevision = %+v, %v, %v", latest, ok, err)
	}
}

func TestStartVersionTagTaken(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	img := mustImage(t, s, "app")
	v := mustStart(t, s, img.ID, "1.0")
	if v.ID == "" || v.Status != VersionBuilding || v.StartedAt.IsZero() {
		t.Fatalf("started = %+v", v)
	}
	if err := s.FinishVersion(ctx, v.ID, VersionResult{Status: VersionSucceeded, Arch: "amd64", ImageDigest: "sha256:abc", Size: 42}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartVersion(ctx, Version{ImageID: img.ID, Tag: "1.0"}); !errors.Is(err, ErrTagTaken) {
		t.Fatalf("err = %v, want ErrTagTaken", err)
	}
	other := mustImage(t, s, "other")
	mustStart(t, s, other.ID, "1.0") // same tag on another image is fine
}

func TestStartVersionBusy(t *testing.T) {
	s := newTestStore(t)
	img := mustImage(t, s, "app")
	mustStart(t, s, img.ID, "1.0")
	if _, err := s.StartVersion(context.Background(), Version{ImageID: img.ID, Tag: "2.0"}); !errors.Is(err, ErrBuildRunning) {
		t.Fatalf("err = %v, want ErrBuildRunning", err)
	}
	// Same tag while building reports the running build.
	if _, err := s.StartVersion(context.Background(), Version{ImageID: img.ID, Tag: "1.0"}); err == nil {
		t.Fatal("same tag while building succeeded")
	}
}

func TestFailedTagReusable(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	img := mustImage(t, s, "app")
	v := mustStart(t, s, img.ID, "1.0")
	if err := s.FinishVersion(ctx, v.ID, VersionResult{Status: VersionFailed, Error: "boom"}); err != nil {
		t.Fatal(err)
	}
	v2 := mustStart(t, s, img.ID, "1.0")
	got, err := s.Version(ctx, img.ID, "1.0")
	if err != nil || got.ID != v2.ID {
		t.Fatalf("Version = %+v, %v; want the non-failed row %s", got, err, v2.ID)
	}
	failed, err := s.VersionByID(ctx, v.ID)
	if err != nil || failed.Status != VersionFailed || failed.Error != "boom" || failed.EndedAt == nil {
		t.Errorf("failed row = %+v, %v", failed, err)
	}
}

func TestVersionsAndLookups(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	img := mustImage(t, s, "app")
	v, err := s.StartVersion(ctx, Version{ImageID: img.ID, Tag: "1", Revision: 2, Commit: "abc", BuildArgs: map[string]string{"A": "b"}, OperationID: "op1"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.VersionByID(ctx, v.ID)
	if err != nil || got.Revision != 2 || got.Commit != "abc" || got.BuildArgs["A"] != "b" || got.OperationID != "op1" || got.EndedAt != nil {
		t.Fatalf("VersionByID = %+v, %v", got, err)
	}
	if _, err := s.VersionByID(ctx, "nope"); !errors.Is(err, ErrVersionNotFound) {
		t.Errorf("VersionByID missing = %v", err)
	}
	if _, err := s.Version(ctx, img.ID, "nope"); !errors.Is(err, ErrVersionNotFound) {
		t.Errorf("Version missing = %v", err)
	}
	if err := s.FinishVersion(ctx, "nope", VersionResult{Status: VersionFailed}); !errors.Is(err, ErrVersionNotFound) {
		t.Errorf("FinishVersion missing = %v", err)
	}
	if err := s.FinishVersion(ctx, v.ID, VersionResult{Status: VersionSucceeded, Arch: "arm64", ImageDigest: "sha256:x", Size: 7}); err != nil {
		t.Fatal(err)
	}
	v2 := mustStart(t, s, img.ID, "2")
	list, err := s.Versions(ctx, img.ID)
	if err != nil || len(list) != 2 || list[0].ID != v2.ID {
		t.Fatalf("Versions = %+v, %v; want newest first", list, err)
	}
	if list[1].Arch != "arm64" || list[1].ImageDigest != "sha256:x" || list[1].Size != 7 || list[1].EndedAt == nil {
		t.Errorf("finished version = %+v", list[1])
	}
	building, err := s.BuildingVersions(ctx)
	if err != nil || len(building) != 1 || building[0].ID != v2.ID {
		t.Errorf("BuildingVersions = %+v, %v", building, err)
	}
	if err := s.DeleteVersion(ctx, v2.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteVersion(ctx, v2.ID); !errors.Is(err, ErrVersionNotFound) {
		t.Errorf("second DeleteVersion = %v", err)
	}
}

func TestLatestSucceededSkipsFailedAndBuilding(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	img := mustImage(t, s, "app")
	if _, ok, err := s.LatestSucceeded(ctx, img.ID); err != nil || ok {
		t.Fatalf("empty LatestSucceeded = %v, %v", ok, err)
	}
	good := mustStart(t, s, img.ID, "1")
	s.FinishVersion(ctx, good.ID, VersionResult{Status: VersionSucceeded})
	bad := mustStart(t, s, img.ID, "2")
	s.FinishVersion(ctx, bad.ID, VersionResult{Status: VersionFailed})
	mustStart(t, s, img.ID, "3") // building
	got, ok, err := s.LatestSucceeded(ctx, img.ID)
	if err != nil || !ok || got.ID != good.ID {
		t.Fatalf("LatestSucceeded = %+v, %v, %v; want %s", got, ok, err, good.ID)
	}
}

func TestDeleteImageCascades(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	img := mustImage(t, s, "app")
	if _, err := s.AddRevision(ctx, img.ID, "", "{}"); err != nil {
		t.Fatal(err)
	}
	v := mustStart(t, s, img.ID, "1")
	if _, err := s.db.Exec(`INSERT INTO registries (id, name, kind, created_at, updated_at) VALUES ('r1','hub','dockerhub',1,1)`); err != nil {
		t.Fatal(err)
	}
	target, err := s.AddPushTarget(ctx, PushTarget{ImageID: img.ID, RegistryID: "r1", Repository: "me/app", AutoPush: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPush(ctx, Push{VersionID: v.ID, TargetID: target.ID, Status: PushPending}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteImage(ctx, img.ID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"image_revisions", "image_versions", "push_targets", "image_pushes"} {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s rows = %d, %v; want 0", table, n, err)
		}
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM registries`).Scan(&n)
	if n != 1 {
		t.Errorf("registries rows = %d, want 1 (untouched)", n)
	}
}

func TestPushTargetsAndPushes(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	img := mustImage(t, s, "app")
	if _, err := s.db.Exec(`INSERT INTO registries (id, name, kind, created_at, updated_at) VALUES ('r1','hub','dockerhub',1,1)`); err != nil {
		t.Fatal(err)
	}
	targets, err := s.PushTargets(ctx, img.ID)
	if err != nil || targets == nil || len(targets) != 0 {
		t.Fatalf("empty PushTargets = %v, %v", targets, err)
	}
	tg, err := s.AddPushTarget(ctx, PushTarget{ImageID: img.ID, RegistryID: "r1", Repository: "me/app"})
	if err != nil || len(tg.ID) != 16 {
		t.Fatalf("AddPushTarget = %+v, %v", tg, err)
	}
	tg.Repository, tg.AutoPush = "me/other", true
	if err := s.UpdatePushTarget(ctx, tg); err != nil {
		t.Fatal(err)
	}
	targets, _ = s.PushTargets(ctx, img.ID)
	if len(targets) != 1 || targets[0] != tg {
		t.Errorf("PushTargets = %+v, want %+v", targets, tg)
	}
	if err := s.UpdatePushTarget(ctx, PushTarget{ID: "nope"}); !errors.Is(err, ErrTargetNotFound) {
		t.Errorf("UpdatePushTarget missing = %v", err)
	}

	v1 := mustStart(t, s, img.ID, "1")
	s.FinishVersion(ctx, v1.ID, VersionResult{Status: VersionSucceeded})
	v2 := mustStart(t, s, img.ID, "2")
	if err := s.SetPush(ctx, Push{VersionID: v1.ID, TargetID: tg.ID, Status: PushPushing}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPush(ctx, Push{VersionID: v1.ID, TargetID: tg.ID, Status: PushPushed, Digest: "sha256:d"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Pushes(ctx, []string{v1.ID, v2.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got[v1.ID]) != 1 || got[v1.ID][0].Status != PushPushed || got[v1.ID][0].Digest != "sha256:d" || got[v1.ID][0].UpdatedAt.IsZero() {
		t.Errorf("Pushes = %+v", got)
	}
	if len(got[v2.ID]) != 0 {
		t.Errorf("v2 pushes = %+v, want none", got[v2.ID])
	}
	if m, err := s.Pushes(ctx, nil); err != nil || m == nil || len(m) != 0 {
		t.Errorf("Pushes(nil) = %v, %v", m, err)
	}
	if err := s.DeletePushTarget(ctx, tg.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePushTarget(ctx, tg.ID); !errors.Is(err, ErrTargetNotFound) {
		t.Errorf("second DeletePushTarget = %v", err)
	}
	got, _ = s.Pushes(ctx, []string{v1.ID})
	if len(got[v1.ID]) != 0 {
		t.Errorf("pushes survived target delete: %+v", got)
	}
}
