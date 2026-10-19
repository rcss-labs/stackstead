package history_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/history"
	"github.com/rcss-labs/stackstead/internal/store"
)

func newStore(t *testing.T) *history.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return history.NewStore(db)
}

func at(sec int64) time.Time { return time.Unix(sec, 0) }

func row(sec int64, project, container, state string) history.Row {
	return history.Row{TS: at(sec), Project: project, Service: "web", Container: container, State: state}
}

func TestStore_InsertAndQueryWindow(t *testing.T) {
	s := newStore(t)
	one := 1
	exited := row(300, "p", "a", history.StateExited)
	exited.ExitCode = &one
	rows := []history.Row{
		row(100, "p", "a", history.StateRunning),
		row(200, "p", "a", history.StateUnhealthy),
		row(250, "p", "a", history.StateRestart),
		exited,
		row(150, "p", "b", history.StateHealthy),
		row(200, "q", "c", history.StateRunning),
	}
	if err := s.Insert(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	got, err := s.Query(context.Background(), "p", at(220), at(320))
	if err != nil {
		t.Fatal(err)
	}
	type key struct {
		container, state string
		ts               int64
	}
	var keys []key
	for _, r := range got {
		keys = append(keys, key{r.Container, r.State, r.TS.Unix()})
	}
	want := []key{{"a", "unhealthy", 200}, {"a", "restart", 250}, {"a", "exited", 300}, {"b", "healthy", 150}}
	if len(keys) != len(want) {
		t.Fatalf("got %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("got %v, want %v", keys, want)
		}
	}
	if got[2].ExitCode == nil || *got[2].ExitCode != 1 {
		t.Errorf("exit code = %v, want 1", got[2].ExitCode)
	}
	if got[0].ExitCode != nil {
		t.Errorf("exit code = %v, want nil", got[0].ExitCode)
	}
	none, err := s.Query(context.Background(), "p", at(50), at(90))
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Errorf("got %v, want nothing", none)
	}
}

func TestStore_QueryAtWindowEdges(t *testing.T) {
	s := newStore(t)
	if err := s.Insert(context.Background(), []history.Row{
		row(100, "p", "a", history.StateRunning),
		row(200, "p", "a", history.StateUnhealthy),
		row(201, "p", "a", history.StateHealthy),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Query(context.Background(), "p", at(100), at(200))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].TS.Unix() != 100 || got[1].TS.Unix() != 200 {
		t.Fatalf("got %v, want rows at 100 and 200 only", got)
	}
}

func TestStore_InsertEmptyIsNoop(t *testing.T) {
	s := newStore(t)
	if err := s.Insert(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	got, err := s.Query(context.Background(), "p", at(0), at(1000))
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestStore_InsertRejectsUnknownState(t *testing.T) {
	s := newStore(t)
	err := s.Insert(context.Background(), []history.Row{row(1, "p", "a", "bogus")})
	if err == nil || !strings.Contains(err.Error(), "CHECK") {
		t.Fatalf("err = %v, want CHECK failure", err)
	}
}

func TestStore_PruneRemovesOlderRows(t *testing.T) {
	s := newStore(t)
	if err := s.Insert(context.Background(), []history.Row{
		row(100, "p", "a", history.StateRunning),
		row(200, "p", "a", history.StateExited),
	}); err != nil {
		t.Fatal(err)
	}
	n, err := s.Prune(context.Background(), at(150))
	if err != nil || n != 1 {
		t.Fatalf("Prune = %d, %v; want 1", n, err)
	}
	got, err := s.Query(context.Background(), "p", at(0), at(1000))
	if err != nil || len(got) != 1 || got[0].TS.Unix() != 200 {
		t.Fatalf("got %v, %v", got, err)
	}
}

// svc is a row whose service is named after its container, so timelines keep
// containers apart.
func svc(sec int64, container, state string) history.Row {
	return history.Row{TS: at(sec), Project: "p", Service: container, Container: container, State: state}
}

// A container that never changes has only its baseline row; pruning it would
// make a stable service read as nodata. Prune keeps each container's state in
// force unless that state is removed.
func TestStore_PruneKeepsStateInForce(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if err := s.Insert(ctx, []history.Row{
		svc(100, "a", history.StateHealthy),
		svc(90, "a", history.StateRestart),
		svc(100, "b", history.StateExited),
		svc(120, "b", history.StateRunning),
		svc(50, "c", history.StateRunning),
		svc(60, "c", history.StateRemoved),
	}); err != nil {
		t.Fatal(err)
	}
	n, err := s.Prune(ctx, at(200))
	if err != nil || n != 4 {
		t.Fatalf("Prune = %d, %v; want 4", n, err)
	}
	got, err := s.Query(ctx, "p", at(0), at(1000))
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, r := range got {
		keys = append(keys, fmt.Sprintf("%s@%d=%s", r.Container, r.TS.Unix(), r.State))
	}
	if want := "a@100=healthy b@120=running"; strings.Join(keys, " ") != want {
		t.Fatalf("rows after prune = %v, want %s", keys, want)
	}

	tl, err := s.Timeline(ctx, "p", at(500), at(600))
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Services) != 2 || tl.Services[0].Service != "a" ||
		len(tl.Services[0].Runs) != 1 || tl.Services[0].Runs[0].State != history.StateHealthy {
		t.Fatalf("timeline after prune = %+v, want a healthy across the window", tl.Services)
	}
}
