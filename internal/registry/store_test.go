package registry

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
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

func hubRegistry(name, token string) Registry {
	return Registry{Name: name, Kind: KindDockerHub, Host: HubHost, Hub: Hub{Username: "alice", Token: secretOf(token)}}
}

func TestStoreSecretsNeverInConfigColumn(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	r := hubRegistry("hub", "dckr_pat_SECRET")
	r.ECR = ECR{Region: "eu-west-1", Mode: ECRStatic, AccessKeyID: "AKIAXX", SecretAccessKey: secretOf("ecr-secret-key"), SessionToken: secretOf("ecr-session")}
	created, err := s.Create(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	var config, secrets string
	if err := s.db.QueryRow(`SELECT config, secrets FROM registries WHERE id = ?`, created.ID).Scan(&config, &secrets); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"dckr_pat_SECRET", "ecr-secret-key", "ecr-session"} {
		if strings.Contains(config, v) {
			t.Errorf("config column holds secret %q: %s", v, config)
		}
		if !strings.Contains(secrets, v) {
			t.Errorf("secrets column lacks %q", v)
		}
	}
	got, err := s.Get(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Hub.Token.Reveal() != "dckr_pat_SECRET" || got.ECR.SecretAccessKey.Reveal() != "ecr-secret-key" ||
		got.ECR.SessionToken.Reveal() != "ecr-session" || got.Hub.Username != "alice" || got.ECR.Region != "eu-west-1" {
		t.Errorf("round trip lost data: %#v", got)
	}
	if !got.Hub.TokenSet || !got.ECR.SecretKeySet || !got.ECR.SessionTokenSet {
		t.Errorf("configured flags not set: %+v %+v", got.Hub, got.ECR)
	}
}

func TestStoreNameTaken(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.Create(ctx, hubRegistry("hub", "a")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, hubRegistry("hub", "b")); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("got %v, want ErrNameTaken", err)
	}
}

func TestStoreRecordTestAndNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	r, _ := s.Create(ctx, hubRegistry("hub", "a"))
	if err := s.RecordTest(ctx, r.ID, true, "Signed in as alice"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, r.ID)
	if !got.LastTestOK || got.LastTestMessage != "Signed in as alice" || got.LastTestAt == nil {
		t.Errorf("test result not stored: %+v", got)
	}
	if _, err := s.Get(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get: %v", err)
	}
	if err := s.Delete(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete: %v", err)
	}
}

func TestDeleteCascadesPushTargets(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	r, _ := s.Create(ctx, hubRegistry("hub", "a"))
	now := time.Now().Unix()
	if _, err := s.db.Exec(`INSERT INTO images (id, slug, name, source, created_at, updated_at) VALUES ('i1','img','img','upload',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO push_targets (id, image_id, registry_id, repository) VALUES ('t1','i1',?,'alice/img')`, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM push_targets`).Scan(&n); err != nil || n != 0 {
		t.Errorf("push targets left: %d, %v", n, err)
	}
}
