package peers

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/store"
)

func newTestStore(t *testing.T, now func() time.Time) *Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "stackstead.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewStore(db, now)
}

func TestSlugify(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Mini 02", "mini-02"},
		{"  My_Host!! ", "my-host"},
		{strings.Repeat("a", 60), strings.Repeat("a", 40)},
		{"", "server"},
		{"!!!", "server"},
	}
	for _, c := range cases {
		if got := Slugify(c.in); got != c.want {
			t.Errorf("Slugify(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestIdentityIsCreatedOnceAndStable(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t, time.Now)
	first, err := s.Identity(ctx, "my-box")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(first.InstanceID) {
		t.Fatalf("InstanceID %q is not 32 hex chars", first.InstanceID)
	}
	if first.Name != "my-box" {
		t.Errorf("Name = %q, want fallback my-box", first.Name)
	}
	second, err := s.Identity(ctx, "other")
	if err != nil {
		t.Fatal(err)
	}
	if second.InstanceID != first.InstanceID || second.Name != "my-box" {
		t.Errorf("second Identity = %+v, want same as %+v", second, first)
	}
	updated, err := s.SetIdentity(ctx, "Renamed Host", "https://example.test:8443")
	if err != nil {
		t.Fatal(err)
	}
	if updated.InstanceID != first.InstanceID || updated.Name != "Renamed Host" ||
		updated.AdvertisedURL != "https://example.test:8443" || updated.Slug != "renamed-host" {
		t.Errorf("SetIdentity = %+v", updated)
	}
	again, _ := s.Identity(ctx, "x")
	if again != updated {
		t.Errorf("Identity after SetIdentity = %+v, want %+v", again, updated)
	}
}
