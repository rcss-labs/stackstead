package api_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/client"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

func evalTemp(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestClientAgainstRealRouter is the oracle that internal/client and
// internal/apitypes decode what the real router sends and send what it
// accepts (CSRF JSON rule, cookie, error bodies, SSE framing).
func TestClientAgainstRealRouter(t *testing.T) {
	fake := newFakeStacks()
	fake.roots = []string{evalTemp(t)}
	fake.replay = []string{"pulling", "started"}
	fake.live = make(chan string)
	close(fake.live)
	fake.done = make(chan stacks.Operation, 1)
	fake.done <- stacks.Operation{ID: "op1", Stack: "web", Action: "up", State: stacks.OpSucceeded,
		StartedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), ExitCode: 0}

	env, _ := newObserveEnvWith(t, newFakeObserve(), fake)
	srv := httptest.NewServer(env.handler)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	ctx := context.Background()

	anon, err := client.New(client.Config{BaseURL: u, UserAgent: "contract/1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := anon.Health(ctx); err != nil {
		t.Fatalf("Health: %v", err)
	}
	if st, err := anon.SetupStatus(ctx); err != nil || st.NeedsSetup {
		t.Fatalf("SetupStatus = %+v, %v", st, err)
	}
	if _, err := anon.Stacks(ctx); !errors.Is(err, client.ErrNoSession) {
		t.Fatalf("anonymous Stacks err = %v", err)
	}
	if _, _, err := anon.Login(ctx, "admin", "wrong password"); !errors.Is(err, client.ErrUnauthorized) {
		t.Fatalf("bad login err = %v", err)
	}

	session, expires, err := anon.Login(ctx, "admin", adminPassword)
	if err != nil || session == "" || !expires.After(time.Now()) {
		t.Fatalf("Login = %q %v %v", session, expires, err)
	}
	c, err := client.New(client.Config{BaseURL: u, Session: session})
	if err != nil {
		t.Fatal(err)
	}

	if me, err := c.Me(ctx); err != nil || me.Username != "admin" {
		t.Fatalf("Me = %+v %v", me, err)
	}
	if status, err := c.Status(ctx); err != nil || status.Docker != "ok" || status.DockerVersion != "27.1.1" || status.Events != "connected" {
		t.Fatalf("Status = %+v %v", status, err)
	}
	list, err := c.Stacks(ctx)
	if err != nil || len(list) != 1 || list[0].Name != "web" || list[0].ServiceCount != 2 ||
		list[0].Health.Healthy != 1 || list[0].Health.None != 1 || len(list[0].Ports) != 1 || list[0].Ports[0] != "8080" {
		t.Fatalf("Stacks = %+v %v", list, err)
	}
	detail, err := c.Stack(ctx, "web")
	if err != nil || detail.Hash != "h1" || len(detail.Services) != 1 || detail.Services[0].Containers[0].Name != "web-app-1" ||
		detail.Services[0].Containers[0].Health != "none" || detail.Health.Healthy != 1 || len(detail.Ports) != 1 {
		t.Fatalf("Stack = %+v %v", detail, err)
	}
	raw, err := c.Compose(ctx, "web")
	if err != nil || raw.Hash != "h1" || raw.Content == "" {
		t.Fatalf("Compose = %+v %v", raw, err)
	}
	saved, err := c.SaveCompose(ctx, "web", "services: {}\n", "h1")
	if err != nil || saved.Data.Hash != "h2" || saved.ChecksPartial {
		t.Fatalf("SaveCompose = %+v %v", saved, err)
	}
	fake.partial = true
	if imp, err := c.Import(ctx, "/srv/web/compose.yaml"); err != nil || !imp.ChecksPartial || imp.Data.Name != "web" {
		t.Fatalf("Import = %+v %v", imp, err)
	}
	fake.partial = false
	if nets, err := c.Networks(ctx); err != nil || len(nets) != 1 || nets[0].Name != "lan" {
		t.Fatalf("Networks = %+v %v", nets, err)
	}

	op, err := c.Act(ctx, "web", "up")
	if err != nil || op.ID != "op1" || op.Action != "up" || fake.gotAction != "up" {
		t.Fatalf("Act = %+v %v", op, err)
	}
	rm, err := c.Remove(ctx, "web", true)
	if err != nil || rm.ID != "op1" || !fake.gotVolumes {
		t.Fatalf("Remove = %+v %v", rm, err)
	}

	var lines []string
	var final string
	opts := client.StreamOptions{HeartbeatTimeout: 2 * time.Second}
	err = c.StreamOperation(ctx, op.ID, opts, func(e client.Event) error {
		switch e.Name {
		case "line":
			lines = append(lines, e.ID+":"+string(e.Data))
		case "done":
			final = string(e.Data)
			return client.ErrStreamDone
		}
		return nil
	})
	if err != nil {
		t.Fatalf("StreamOperation: %v", err)
	}
	if len(lines) != 2 || lines[0] != `0:"pulling"` || lines[1] != `1:"started"` || final == "" {
		t.Errorf("lines = %v, done = %s", lines, final)
	}

	if ops, err := c.Operations(ctx, "running"); err != nil || len(ops) != 1 {
		t.Fatalf("Operations = %+v %v", ops, err)
	}
	if ops, err := c.StackOperations(ctx, "web"); err != nil || len(ops) != 1 {
		t.Fatalf("StackOperations = %+v %v", ops, err)
	}
	if detail, err := c.Operation(ctx, "op1"); err != nil || len(detail.Lines) != 2 {
		t.Fatalf("Operation = %+v %v", detail, err)
	}
	if got, err := c.Cancel(ctx, "op1"); err != nil || got.ID != "op1" || fake.gotCancel != "op1" {
		t.Fatalf("Cancel = %+v %v", got, err)
	}
	if hist, err := c.MetricsHistory(ctx, "web", client.MetricsQuery{Step: time.Minute}); err != nil {
		t.Fatalf("MetricsHistory = %+v %v", hist, err)
	}

	if roots, err := c.FS(ctx, ""); err != nil || len(roots) != 1 || !roots[0].IsDir || roots[0].Name != fake.roots[0] {
		t.Fatalf("FS roots = %+v %v", roots, err)
	}
	if err := os.WriteFile(filepath.Join(fake.roots[0], "compose.yaml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if entries, err := c.FS(ctx, fake.roots[0]); err != nil || len(entries) != 1 || entries[0].Name != "compose.yaml" || entries[0].IsDir {
		t.Fatalf("FS dir = %+v %v", entries, err)
	}
	if _, err := c.FS(ctx, "/"); !errors.Is(err, client.ErrForbidden) {
		t.Fatalf("FS outside roots err = %v", err)
	}

	if st, err := c.MetricsTokenStatus(ctx); err != nil || st.Configured {
		t.Fatalf("MetricsTokenStatus = %+v %v", st, err)
	}
	tok, err := c.CreateMetricsToken(ctx)
	if err != nil || tok.Token == "" {
		t.Fatalf("CreateMetricsToken err = %v", err)
	}
	if st, err := c.MetricsTokenStatus(ctx); err != nil || !st.Configured {
		t.Fatalf("MetricsTokenStatus after create = %+v %v", st, err)
	}
	if err := c.DeleteMetricsToken(ctx); err != nil {
		t.Fatalf("DeleteMetricsToken: %v", err)
	}
	if st, err := c.MetricsTokenStatus(ctx); err != nil || st.Configured {
		t.Fatalf("MetricsTokenStatus after delete = %+v %v", st, err)
	}

	err = c.ChangePassword(ctx, "not the password", "another long password")
	var apiErr *client.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 403 {
		t.Fatalf("ChangePassword with wrong current: %v", err)
	}

	fake.err = stacks.ErrBusy
	_, err = c.Act(ctx, "web", "up")
	if !errors.Is(err, client.ErrConflict) {
		t.Fatalf("busy Act err = %v", err)
	}
	fake.err = nil

	if err := c.Logout(ctx); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, err := c.Me(ctx); !errors.Is(err, client.ErrUnauthorized) {
		t.Fatalf("Me after logout err = %v", err)
	}
}
