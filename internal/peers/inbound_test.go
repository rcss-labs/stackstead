package peers

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
)

func newTestService(t *testing.T) (*Service, *Store) {
	t.Helper()
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	st := newTestStore(t, clock.now)
	return NewService(ServiceConfig{
		Store: st, Version: "1.0.0", Now: clock.now, Logger: slog.New(slog.DiscardHandler),
		Hostname: func() (string, error) { return "b-host", nil },
	}), st
}

func TestAuthenticateStates(t *testing.T) {
	ctx := context.Background()
	svc, st := newTestService(t)
	for id, state := range map[string]PeerState{"p-active": StateActive, "p-pending": StatePending, "p-removed": StateRemoved} {
		if err := st.InsertPeer(ctx, Peer{InstanceID: id, Name: id, State: state}, HashSecret("ssp_"+id)); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		cred      string
		helloOnly bool
		wantErr   bool
	}{
		{"ssp_p-active", false, false},
		{"ssp_p-active", true, false},
		{"ssp_p-pending", false, true},
		{"ssp_p-pending", true, false},
		{"ssp_p-removed", true, true},
		{"ssp_unknown", true, true},
		{"", true, true},
	}
	for _, c := range cases {
		_, err := svc.Authenticate(ctx, c.cred, c.helloOnly)
		if c.wantErr && !errors.Is(err, ErrNotPaired) {
			t.Errorf("Authenticate(%q, %v) = %v, want ErrNotPaired", c.cred, c.helloOnly, err)
		}
		if !c.wantErr && err != nil {
			t.Errorf("Authenticate(%q, %v) = %v", c.cred, c.helloOnly, err)
		}
	}
}

func TestApplyUpdateRecomputesSlugUniquely(t *testing.T) {
	ctx := context.Background()
	svc, st := newTestService(t)
	for _, p := range []Peer{{InstanceID: "a", Name: "Alpha"}, {InstanceID: "b", Name: "Beta"}} {
		p.State = StateActive
		if err := st.InsertPeer(ctx, p, HashSecret(p.InstanceID)); err != nil {
			t.Fatal(err)
		}
	}
	from, _ := st.Peer(ctx, "b")
	if err := svc.ApplyUpdate(ctx, from, apitypes.PeerUpdate{Name: "Alpha"}); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Peer(ctx, "b")
	if got.Name != "Alpha" || got.Slug != "alpha-2" {
		t.Errorf("after rename: name %q slug %q", got.Name, got.Slug)
	}
	// Renaming to a name whose slug is its own keeps the slug.
	if err := svc.ApplyUpdate(ctx, got, apitypes.PeerUpdate{Name: "ALPHA 2"}); err != nil {
		t.Fatal(err)
	}
	again, _ := st.Peer(ctx, "b")
	if again.Slug != "alpha-2" {
		t.Errorf("slug = %q, want alpha-2", again.Slug)
	}
}

func TestPeerNamesLoseControlAndFormatCharacters(t *testing.T) {
	if got := cleanName("mini\n\u202e-01\x00", ""); got != "mini-01" {
		t.Errorf("cleanName = %q", got)
	}
	if got := cleanName("\n\u202e", "fallback"); got != "fallback" {
		t.Errorf("cleanName fallback = %q", got)
	}
	ctx := context.Background()
	svc, st := newTestService(t)
	if err := st.InsertPeer(ctx, Peer{InstanceID: "a", Name: "Alpha", State: StateActive}, HashSecret("a")); err != nil {
		t.Fatal(err)
	}
	from, _ := st.Peer(ctx, "a")
	if err := svc.ApplyUpdate(ctx, from, apitypes.PeerUpdate{Name: "Be\nta\u202e"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Peer(ctx, "a"); got.Name != "Beta" {
		t.Errorf("name = %q", got.Name)
	}
	if err := svc.ApplyUpdate(ctx, from, apitypes.PeerUpdate{Name: "\n\u202e"}); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("blank-after-strip = %v", err)
	}
}
