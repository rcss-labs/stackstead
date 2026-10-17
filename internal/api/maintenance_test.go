package api_test

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/api"
	"github.com/rcss-labs/stackstead/internal/auth"
	"github.com/rcss-labs/stackstead/internal/maintenance"
	"github.com/rcss-labs/stackstead/internal/stacks"
	"github.com/rcss-labs/stackstead/internal/store"
)

func TestProbationRejectsAllMutators(t *testing.T) {
	gate := maintenance.New()
	fake := newFakeStacks()
	e := newEnvWith(t, "setup", func(o *api.Options) {
		o.Maintenance = gate
		o.Stacks = fake
		o.Operations = fakeOps{fake}
		o.Observe = newFakeObserve()
		o.NetworkEngine = &fakeNetworkEngine{}
		o.ContainerEngine = &fakeContainerEngine{}
	})
	e.createAdmin(t)
	cookie := e.login(t)
	if err := gate.Freeze(); err != nil {
		t.Fatal(err)
	}
	routes := []string{
		"POST /api/setup", "POST /api/login", "POST /api/logout", "POST /api/password",
		"POST /api/settings/metrics-token", "DELETE /api/settings/metrics-token",
		"POST /api/stacks", "POST /api/stacks/import", "PUT /api/stacks/web/compose",
		"POST /api/stacks/web/services", "POST /api/stacks/web/services/preview", "DELETE /api/stacks/web/services/app",
		"PUT /api/stacks/web/services/app", "POST /api/stacks/web/services/app/preview", "POST /api/stacks/web/actions/up",
		"DELETE /api/stacks/web", "POST /api/operations/op1/cancel", "POST /api/networks", "DELETE /api/networks/n1", "POST /api/containers/c1/start",
	}
	for _, route := range routes {
		t.Run(route, func(t *testing.T) {
			parts := strings.SplitN(route, " ", 2)
			rec := e.do(t, parts[0], parts[1], `{}`, withCookie(cookie))
			if rec.Code != http.StatusServiceUnavailable {
				t.Errorf("frozen %s = %d, want 503; %s", route, rec.Code, rec.Body)
			}
		})
	}
	for _, path := range []string{"/api/health", "/api/me", "/api/setup/status", "/api/stacks", "/api/stacks/web/compose", "/api/settings/metrics-token"} {
		if r := e.do(t, "GET", path, "", withCookie(cookie)); r.Code != 200 {
			t.Errorf("read %s = %d: %s", path, r.Code, r.Body)
		}
	}
	gate.Thaw()
	if r := e.do(t, "POST", "/api/logout", "", withCookie(cookie)); r.Code != 204 {
		t.Errorf("logout after thaw = %d", r.Code)
	}
}

type admittedStacks struct {
	*fakeStacks
	ops  *stacks.Ops
	stop chan struct{}
}

func (f admittedStacks) Act(context.Context, string, string) (stacks.Operation, error) {
	return f.ops.Start("web", "up", func(context.Context, func(string)) error { <-f.stop; return nil })
}
func TestAdmissionHeldAfterHTTP202(t *testing.T) {
	g := maintenance.New()
	ops := stacks.NewOps(time.Second, time.Now)
	stop := make(chan struct{})
	f := admittedStacks{newFakeStacks(), ops, stop}
	e := newEnvWith(t, "", func(o *api.Options) { o.Maintenance = g; o.Stacks = f; o.Operations = ops })
	e.createAdmin(t)
	cookie := e.login(t)
	r := e.do(t, "POST", "/api/stacks/web/actions/up", "{}", withCookie(cookie))
	if r.Code != 202 {
		close(stop)
		t.Fatalf("action = %d: %s", r.Code, r.Body)
	}
	if err := g.Freeze(); !errors.Is(err, maintenance.ErrBusy) {
		t.Errorf("freeze after HTTP 202 = %v; lost operation admission", err)
	}
	close(stop)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ops.Shutdown(ctx)
	if err := g.Freeze(); err != nil {
		t.Fatalf("completed operation admission leaked: %v", err)
	}
}

func TestProbationExpiredSessionGETDoesNotWrite(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "session.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	clock := &fakeClock{now: time.Now()}
	g := maintenance.New()
	svc, err := auth.NewService(db, clock.Now, auth.WithFastHashingForTests())
	if err != nil {
		t.Fatal(err)
	}
	e := &testEnv{auth: svc, clock: clock, handler: api.New(api.Options{Auth: svc, Maintenance: g, Limiter: auth.NewLoginLimiter(5, time.Minute, clock.Now)})}
	e.createAdmin(t)
	cookie := e.login(t)
	clock.Advance(auth.SessionTTL + time.Second)
	g.Freeze()
	r := e.do(t, "GET", "/api/me", "", withCookie(cookie))
	if r.Code != 401 {
		t.Fatalf("expired session GET = %d: %s", r.Code, r.Body)
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expired GET wrote during probation: sessions=%d", n)
	}
	g.Thaw()
	e.do(t, "GET", "/api/me", "", withCookie(cookie))
	if err := db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("ordinary expired GET failed cleanup: sessions=%d", n)
	}
}
