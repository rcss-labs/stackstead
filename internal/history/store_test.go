package history_test

import (
	"context"
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
