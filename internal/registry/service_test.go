package registry

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/rcss-labs/stackstead/internal/actionlog"
	"github.com/rcss-labs/stackstead/internal/engine"
)

type fakeActions struct{ entries []actionlog.Entry }

func (f *fakeActions) Append(_ context.Context, e actionlog.Entry) error {
	f.entries = append(f.entries, e)
	return nil
}

type fakeProvider struct{ res TestResult }

func (f fakeProvider) Test(context.Context, Registry) (TestResult, error) { return f.res, nil }
func (f fakeProvider) Credentials(context.Context, Registry) (engine.RegistryAuth, error) {
	return engine.RegistryAuth{Username: "u"}, nil
}

func newTestService(t *testing.T) (*Service, *fakeActions) {
	t.Helper()
	actions := &fakeActions{}
	providers := map[Kind]Provider{KindDockerHub: fakeProvider{res: TestResult{OK: true, Host: HubHost, Message: "ok"}}}
	return NewService(newTestStore(t), providers, actions, slog.New(slog.NewTextHandler(io.Discard, nil))), actions
}

func TestUpdateKeepsSecretWhenBlank(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	r, err := svc.Create(ctx, "bob", Request{Name: "hub", Kind: KindDockerHub, Hub: &Hub{Username: "alice", Token: "keepme"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Hub.Token != "" || !r.Hub.TokenSet {
		t.Errorf("service view must hide the token but flag it: %+v", r.Hub)
	}
	if _, err := svc.Update(ctx, "bob", r.ID, Request{Name: "hub2", Kind: KindDockerHub, Hub: &Hub{Username: "alice2"}}); err != nil {
		t.Fatal(err)
	}
	stored, _ := svc.store.Get(ctx, r.ID)
	if stored.Hub.Token.Reveal() != "keepme" || stored.Hub.Username != "alice2" || stored.Name != "hub2" {
		t.Errorf("update lost the secret or ignored fields: %+v", stored)
	}
	if _, err := svc.Update(ctx, "bob", r.ID, Request{Name: "hub2", Kind: KindDockerHub, Hub: &Hub{Username: "alice2", Token: "new"}}); err != nil {
		t.Fatal(err)
	}
	stored, _ = svc.store.Get(ctx, r.ID)
	if stored.Hub.Token.Reveal() != "new" {
		t.Errorf("token not replaced: %+v", stored.Hub)
	}
	list, _ := svc.List(ctx)
	got, _ := svc.Get(ctx, r.ID)
	if list[0].Hub.Token != "" || got.Hub.Token != "" {
		t.Error("List/Get must not return secret values")
	}
}

func TestServiceNameTakenAndTest(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	req := Request{Name: "hub", Kind: KindDockerHub, Hub: &Hub{Username: "a", Token: "t"}}
	r, _ := svc.Create(ctx, "u", req)
	if _, err := svc.Create(ctx, "u", req); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("got %v", err)
	}
	if _, err := svc.Create(ctx, "u", Request{Name: "", Kind: KindDockerHub}); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty name: %v", err)
	}
	res, err := svc.Test(ctx, r.ID)
	if err != nil || !res.OK {
		t.Fatalf("%+v %v", res, err)
	}
	got, _ := svc.Get(ctx, r.ID)
	if !got.LastTestOK || got.LastTestMessage != "ok" {
		t.Errorf("result not recorded: %+v", got)
	}
}

func TestPushRef(t *testing.T) {
	hub := hubRegistry("hub", "t")
	ecr := Registry{Kind: KindECR, Host: "123.dkr.ecr.eu-west-1.amazonaws.com"}
	for _, tc := range []struct {
		r          Registry
		repo, want string
	}{
		{hub, "team/app", "docker.io/team/app:1"},
		{hub, "app", "docker.io/alice/app:1"},
		{ecr, "app", "123.dkr.ecr.eu-west-1.amazonaws.com/app:1"},
	} {
		if got := PushRef(tc.r, tc.repo, "1"); got != tc.want {
			t.Errorf("PushRef(%s) = %q, want %q", tc.repo, got, tc.want)
		}
	}
}

func TestCredentialsUnconfigured(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	r, err := svc.store.Create(ctx, Registry{Name: "empty", Kind: KindDockerHub, Host: HubHost, Hub: Hub{Username: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Credentials(ctx, r.ID); !errors.Is(err, ErrUnconfigured) {
		t.Fatalf("got %v, want ErrUnconfigured", err)
	}
	ok, _ := svc.Create(ctx, "u", Request{Name: "ok", Kind: KindDockerHub, Hub: &Hub{Username: "a", Token: "t"}})
	auth, host, err := svc.Credentials(ctx, ok.ID)
	if err != nil || host != HubHost || auth.Username != "u" {
		t.Errorf("%+v %q %v", auth, host, err)
	}
}

func TestActionLogOnCreate(t *testing.T) {
	svc, actions := newTestService(t)
	if _, err := svc.Create(context.Background(), "bob", Request{Name: "hub", Kind: KindDockerHub, Hub: &Hub{Username: "a", Token: "sekret"}}); err != nil {
		t.Fatal(err)
	}
	if len(actions.entries) != 1 {
		t.Fatalf("entries: %+v", actions.entries)
	}
	e := actions.entries[0]
	if e.Kind != "registry.create" || e.Scope != "host" || e.Username != "bob" || e.Detail != "name=hub kind=dockerhub" {
		t.Errorf("entry: %+v", e)
	}
}
