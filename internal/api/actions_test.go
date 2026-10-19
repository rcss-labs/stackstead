package api_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/actionlog"
	"github.com/rcss-labs/stackstead/internal/api"
	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/client"
	"github.com/rcss-labs/stackstead/internal/store"
)

func TestActionsIndependentFilteringAndValidation(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "actions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	actions := actionlog.NewStore(db, time.Now)
	ctx := context.Background()
	for _, e := range []actionlog.Entry{{Username: "admin", Kind: "storage.delete", Scope: "host", Target: "volume/old", Result: "deleted"}, {Username: "bob", Kind: "storage.prune", Scope: "demo", Target: "build-cache/x", Result: "deleted"}, {Username: "bob", Kind: "mount.fix", Scope: "host", Target: "volume/x", Result: "fixed"}} {
		if err := actions.Append(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	env := newEnvWith(t, "", func(o *api.Options) { o.Actions = actions })
	env.createAdmin(t)
	c := env.login(t)
	for _, q := range []string{"limit=0", "limit=501", "limit=-1", "limit=no", "limit=", "kind=storage_%", "kind=Storage.", "kind=storage..", "scope=.bad", "scope=" + url.QueryEscape("has space")} {
		r := storageRequest(t, env, c, "GET", "/api/actions?"+q, "")
		if r.Code != 400 {
			t.Errorf("%s=%d %s", q, r.Code, r.Body)
		}
	}
	for _, tc := range []struct {
		q    string
		want int
	}{{"", 3}, {"kind=storage.&scope=host", 1}, {"kind=storage", 2}, {"kind=storage.delete", 1}, {"scope=demo&limit=1", 1}, {"kind=missing.", 0}, {"limit=500", 3}} {
		r := storageRequest(t, env, c, "GET", "/api/actions?"+tc.q, "")
		var got []apitypes.ActionEntry
		err := json.Unmarshal(r.Body.Bytes(), &got)
		if r.Code != 200 || err != nil || got == nil || len(got) != tc.want {
			t.Errorf("%s=%d %s %v", tc.q, r.Code, r.Body, err)
		}
	}
	srv := httptest.NewServer(env.handler)
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	cl, err := client.New(client.Config{BaseURL: u, Session: c.Value})
	if err != nil {
		t.Fatal(err)
	}
	got, err := cl.RecentActions(ctx, client.ActionsQuery{Kind: "storage.", Scope: "host", Limit: 50})
	if err != nil || len(got) != 1 || got[0].Username != "admin" || got[0].Bytes != nil {
		t.Fatalf("client actions=%+v %v", got, err)
	}
}
