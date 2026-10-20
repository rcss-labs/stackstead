package peers

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func TestConnectionTokenRules(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	s := newTestStore(t, clock.now)

	plain, expires, err := s.CreateToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plain, "ssc_") {
		t.Errorf("token %q lacks ssc_ prefix", plain)
	}
	if !expires.Equal(clock.t.Add(15 * time.Minute)) {
		t.Errorf("expiresAt = %v, want now+15m", expires)
	}
	var stored string
	if err := s.db.QueryRow(`SELECT hash FROM connection_tokens`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != HashSecret(plain) || strings.Contains(stored, plain) {
		t.Errorf("stored %q, want only sha256 of token", stored)
	}

	// Expired.
	clock.advance(15*time.Minute + time.Second)
	if err := s.ConsumeToken(ctx, plain, func() error { return nil }); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("expired consume = %v, want ErrTokenInvalid", err)
	}
	clock.advance(-(15*time.Minute + time.Second))

	// A failing callback leaves the token usable; success consumes it.
	boom := errors.New("pair failed")
	if err := s.ConsumeToken(ctx, plain, func() error { return boom }); !errors.Is(err, boom) {
		t.Errorf("failing consume = %v, want callback error", err)
	}
	if err := s.ConsumeToken(ctx, plain, func() error { return nil }); err != nil {
		t.Errorf("retry after failed callback = %v, want nil", err)
	}
	if err := s.ConsumeToken(ctx, plain, func() error { return nil }); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("second use = %v, want ErrTokenInvalid", err)
	}
	if err := s.ConsumeToken(ctx, "ssc_unknown", func() error { return nil }); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("unknown token = %v, want ErrTokenInvalid", err)
	}

	// Sixth token evicts the oldest.
	var tokens []string
	for i := 0; i < 6; i++ {
		clock.advance(time.Second)
		p, _, err := s.CreateToken(ctx)
		if err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, p)
	}
	if err := s.ConsumeToken(ctx, tokens[0], func() error { return nil }); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("oldest token = %v, want ErrTokenInvalid", err)
	}
	for _, p := range tokens[1:] {
		if err := s.ConsumeToken(ctx, p, func() error { return nil }); err != nil {
			t.Errorf("token %s = %v, want valid", p, err)
		}
	}
}

func TestInsertPeerAssignsUniqueSlug(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	s := newTestStore(t, clock.now)
	wantSlugs := []string{"mini", "mini-2", "mini-3"}
	ids := []string{"id-a", "id-b", "id-c"}
	for _, id := range ids {
		clock.advance(time.Second)
		p := Peer{InstanceID: id, Name: "mini", URL: "https://x", State: StatePending}
		if err := s.InsertPeer(ctx, p, ""); err != nil {
			t.Fatalf("InsertPeer %s: %v", id, err)
		}
	}
	got, err := s.Peers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("Peers = %d, want 3", len(got))
	}
	for i, p := range got {
		if p.InstanceID != ids[i] || p.Slug != wantSlugs[i] {
			t.Errorf("peer %d = %s/%s, want %s/%s", i, p.InstanceID, p.Slug, ids[i], wantSlugs[i])
		}
	}
	if err := s.InsertPeer(ctx, Peer{InstanceID: "id-a", Name: "dup", URL: "u", State: StateActive}, ""); !errors.Is(err, ErrAlreadyPaired) {
		t.Errorf("duplicate instance = %v, want ErrAlreadyPaired", err)
	}
}

func TestPeerLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t, time.Now)
	if _, err := s.Peer(ctx, "nope"); !errors.Is(err, ErrPeerNotFound) {
		t.Errorf("Peer missing = %v, want ErrPeerNotFound", err)
	}
	p := Peer{InstanceID: "p1", Name: "One", URL: "https://one", State: StatePending,
		TLS: TLSOptions{CAPEM: "ca", PinSHA256: "pin"}, OutCredential: "secret"}
	if err := s.InsertPeer(ctx, p, "hash1"); err != nil {
		t.Fatal(err)
	}
	got, err := s.PeerByInboundHash(ctx, "hash1")
	if err != nil || got.InstanceID != "p1" || got.TLS.PinSHA256 != "pin" || got.OutCredential != "secret" {
		t.Fatalf("PeerByInboundHash = %+v, %v", got, err)
	}
	if _, err := s.PeerByInboundHash(ctx, "other"); !errors.Is(err, ErrPeerNotFound) {
		t.Errorf("unknown hash = %v, want ErrPeerNotFound", err)
	}
	seen := time.Unix(1_700_000_500, 0)
	if err := s.UpdatePeer(ctx, "p1", func(p *Peer) error {
		p.State, p.StackCount, p.LastSeenAt = StateActive, 4, &seen
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Peer(ctx, "p1")
	if got.State != StateActive || got.StackCount != 4 || got.LastSeenAt == nil || !got.LastSeenAt.Equal(seen) {
		t.Errorf("after update = %+v", got)
	}
	if err := s.SetInboundHash(ctx, "p1", "hash2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PeerByInboundHash(ctx, "hash1"); !errors.Is(err, ErrPeerNotFound) {
		t.Errorf("old hash still resolves: %v", err)
	}
	if n, _ := s.CountPeers(ctx); n != 1 {
		t.Errorf("CountPeers = %d, want 1", n)
	}
	if err := s.DeletePeer(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePeer(ctx, "p1"); !errors.Is(err, ErrPeerNotFound) {
		t.Errorf("second delete = %v, want ErrPeerNotFound", err)
	}
}

func TestConsumeTokenSucceededCallbackIsFinalEvenIfContextCancelled(t *testing.T) {
	s := newTestStore(t, time.Now)
	plain, _, err := s.CreateToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	err = s.ConsumeToken(ctx, plain, func() error {
		cancel()
		return nil
	})
	if err != nil {
		t.Fatalf("ConsumeToken = %v, want nil", err)
	}
	if err := s.ConsumeToken(context.Background(), plain, func() error { return nil }); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("reuse after cancelled-ctx success = %v, want ErrTokenInvalid", err)
	}
}

func TestConsumeTokenConcurrentUseRunsCallbackOnce(t *testing.T) {
	s := newTestStore(t, time.Now)
	plain, _, err := s.CreateToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = s.ConsumeToken(context.Background(), plain, func() error {
				calls.Add(1)
				time.Sleep(50 * time.Millisecond)
				return nil
			})
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Errorf("callback ran %d times, want 1", calls.Load())
	}
	ok, invalid := 0, 0
	for _, e := range errs {
		switch {
		case e == nil:
			ok++
		case errors.Is(e, ErrTokenInvalid):
			invalid++
		}
	}
	if ok != 1 || invalid != 1 {
		t.Errorf("results = %v, want one nil and one ErrTokenInvalid", errs)
	}
}

func TestConsumeTokenBogusTokenIsNotBlockedBySlowCallback(t *testing.T) {
	s := newTestStore(t, time.Now)
	plain, _, _ := s.CreateToken(context.Background())
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.ConsumeToken(context.Background(), plain, func() error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	bogus := make(chan error, 1)
	go func() {
		bogus <- s.ConsumeToken(context.Background(), "ssc_bogus", func() error { return nil })
	}()
	select {
	case err := <-bogus:
		if !errors.Is(err, ErrTokenInvalid) {
			t.Errorf("bogus = %v, want ErrTokenInvalid", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("bogus token blocked behind a slow callback")
	}
	close(release)
	if err := <-done; err != nil {
		t.Errorf("slow consume = %v", err)
	}
}
