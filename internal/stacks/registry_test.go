package stacks_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/stacks"
	"github.com/rcss-labs/stackstead/internal/store"
)

var fixedNow = time.Unix(1_700_000_000, 0).UTC()

func newRegistry(t *testing.T) *stacks.Registry {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "stackstead.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return stacks.NewRegistry(db, func() time.Time { return fixedNow })
}

func entry(name string) stacks.Entry {
	return stacks.Entry{
		Name:        name,
		ComposePath: "/opt/stacks/" + name + "/compose.yaml",
		Origin:      stacks.OriginManaged,
		Project:     name,
	}
}

func TestAddThenGetRoundTripsEveryField(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()
	in := stacks.Entry{Name: "web", ComposePath: "/srv/web/docker-compose.yml", Origin: stacks.OriginExternal, Project: "web-proj"}
	if err := r.Add(ctx, in); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got, err := r.Get(ctx, "web")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	want := in
	want.CreatedAt = fixedNow
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

func TestAddDuplicateNameOrPathIsErrStackExists(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()
	if err := r.Add(ctx, entry("web")); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(ctx, entry("web")); !errors.Is(err, stacks.ErrStackExists) {
		t.Errorf("duplicate name: err = %v, want ErrStackExists", err)
	}
	samePath := entry("other")
	samePath.ComposePath = entry("web").ComposePath
	if err := r.Add(ctx, samePath); !errors.Is(err, stacks.ErrStackExists) {
		t.Errorf("duplicate path: err = %v, want ErrStackExists", err)
	}
}

func TestGetAndRemoveMissingAreErrStackNotFound(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()
	if _, err := r.Get(ctx, "nope"); !errors.Is(err, stacks.ErrStackNotFound) {
		t.Errorf("Get: err = %v, want ErrStackNotFound", err)
	}
	if err := r.Remove(ctx, "nope"); !errors.Is(err, stacks.ErrStackNotFound) {
		t.Errorf("Remove: err = %v, want ErrStackNotFound", err)
	}
	if err := r.SetProject(ctx, "nope", "p"); !errors.Is(err, stacks.ErrStackNotFound) {
		t.Errorf("SetProject: err = %v, want ErrStackNotFound", err)
	}
}

func TestListIsSortedByName(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()
	for _, n := range []string{"zeta", "alpha", "mid"} {
		if err := r.Add(ctx, entry(n)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := r.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var names []string
	for _, e := range got {
		names = append(names, e.Name)
	}
	if want := []string{"alpha", "mid", "zeta"}; !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
}

func TestListEmptyIsNotAnError(t *testing.T) {
	got, err := newRegistry(t).List(context.Background())
	if err != nil || len(got) != 0 {
		t.Errorf("List = %v, %v; want empty, nil", got, err)
	}
}

func TestRemoveDeletesAndFreesNameAndPath(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()
	if err := r.Add(ctx, entry("web")); err != nil {
		t.Fatal(err)
	}
	if err := r.Remove(ctx, "web"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := r.Get(ctx, "web"); !errors.Is(err, stacks.ErrStackNotFound) {
		t.Errorf("Get after Remove: err = %v, want ErrStackNotFound", err)
	}
	if err := r.Add(ctx, entry("web")); err != nil {
		t.Errorf("re-Add after Remove: %v", err)
	}
}

func TestSetProjectUpdatesOnlyProject(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()
	if err := r.Add(ctx, entry("web")); err != nil {
		t.Fatal(err)
	}
	if err := r.SetProject(ctx, "web", "renamed"); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	got, err := r.Get(ctx, "web")
	if err != nil {
		t.Fatal(err)
	}
	if got.Project != "renamed" || got.ComposePath != entry("web").ComposePath {
		t.Errorf("got %+v, want project renamed and path unchanged", got)
	}
}

func TestDuplicateProjectIsErrProjectTaken(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()
	a := entry("a")
	a.Project = "shared"
	if err := r.Add(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := entry("b")
	b.Project = "shared"
	if err := r.Add(ctx, b); !errors.Is(err, stacks.ErrProjectTaken) {
		t.Errorf("Add err = %v", err)
	}
	if err := r.Add(ctx, entry("c")); err != nil {
		t.Fatal(err)
	}
	if err := r.SetProject(ctx, "c", "shared"); !errors.Is(err, stacks.ErrProjectTaken) {
		t.Errorf("SetProject err = %v", err)
	}
	if err := r.Add(ctx, a); !errors.Is(err, stacks.ErrStackExists) {
		t.Errorf("name collision err = %v, want ErrStackExists", err)
	}
}
