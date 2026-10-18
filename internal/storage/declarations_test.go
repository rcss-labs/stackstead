package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/rcss-labs/stackstead/internal/composecli"
	"github.com/rcss-labs/stackstead/internal/stacks"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type configRunnerFunc func(context.Context, string) ([]byte, error)

func (f configRunnerFunc) ConfigJSON(ctx context.Context, path string) ([]byte, error) {
	return f(ctx, path)
}
func declarationEntry(t *testing.T, name string) stacks.Entry {
	t.Helper()
	p := filepath.Join(t.TempDir(), "compose.yaml")
	if err := os.WriteFile(p, []byte("services: {}"), 0o600); err != nil {
		t.Fatal(err)
	}
	return stacks.Entry{Name: name, Project: "registered", ComposePath: p}
}
func fixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/compose-config.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func TestDeclarationsSanitizeResolveCacheAndInvalidate(t *testing.T) {
	data := fixture(t)
	calls := 0
	c := NewDeclarationCache(configRunnerFunc(func(context.Context, string) ([]byte, error) { calls++; return data, nil }), nil)
	e := declarationEntry(t, "media-stack")
	got := c.Get(context.Background(), e)
	want := StackDeclarations{Name: e.Name, Project: "media", Known: true, Images: []DeclaredImage{{Service: "api", Reference: "registry/api:v2", Built: true}, {Service: "db", Reference: "postgres:17"}, {Service: "web", Reference: "media-web", Built: true}}, Volumes: []DeclaredVolume{{Key: "db", Name: "media_db"}, {Key: "named", Name: "explicit"}, {Key: "shared", Name: "shared_data", External: true}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), "hunter2") {
		t.Fatal("environment leaked")
	}
	got.Images[0].Reference = "changed"
	got.Volumes[0].Name = "changed"
	got.State = "running"
	if next := c.Get(context.Background(), e); !reflect.DeepEqual(next, want) || calls != 1 {
		t.Fatalf("cache corrupted %#v calls %d", next, calls)
	}
	for _, change := range []struct{ path, body string }{{filepath.Join(filepath.Dir(e.ComposePath), ".env"), "PASSWORD=hunter2"}, {e.ComposePath, "services: {web: {}}"}} {
		if err := os.WriteFile(change.path, []byte(change.body), 0o600); err != nil {
			t.Fatal(err)
		}
		c.Get(context.Background(), e)
	}
	if calls != 3 {
		t.Fatalf("calls %d want 3", calls)
	}
}
func TestDeclarationsFailuresUnknownSafeAndRecover(t *testing.T) {
	data := fixture(t)
	for _, kind := range []string{"exit", "invalid", "missing", "binary"} {
		t.Run(kind, func(t *testing.T) {
			e := declarationEntry(t, "safe-stack")
			var logs bytes.Buffer
			calls := 0
			runner := configRunnerFunc(func(context.Context, string) ([]byte, error) {
				calls++
				if calls > 1 {
					return data, nil
				}
				switch kind {
				case "exit":
					return nil, &composecli.ExitError{Code: 17, Output: "hunter2"}
				case "invalid":
					return []byte(`{"name":"hunter2`), nil
				case "binary":
					return nil, composecli.ErrComposeUnavailable
				}
				return data, nil
			})
			c := NewDeclarationCache(runner, slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
			if kind == "missing" {
				if err := os.Remove(e.ComposePath); err != nil {
					t.Fatal(err)
				}
			}
			got := c.Get(context.Background(), e)
			if got.Known || len(got.Images) != 0 || len(got.Volumes) != 0 || got.Project != e.Project {
				t.Fatalf("failure %#v", got)
			}
			if strings.Contains(logs.String(), "hunter2") || !strings.Contains(logs.String(), "safe-stack") || !strings.Contains(logs.String(), "DEBUG") || !strings.Contains(logs.String(), "exit_code=") {
				t.Fatalf("unsafe/incomplete logs %s", logs.String())
			}
			if kind == "missing" {
				if calls != 0 {
					t.Fatal("ran compose for missing file")
				}
				return
			}
			if !c.Get(context.Background(), e).Known || calls != 2 {
				t.Fatalf("did not recover calls %d", calls)
			}
		})
	}
}
func TestDeclarationsConcurrencyAndDeadline(t *testing.T) {
	data := fixture(t)
	entered := make(chan string, 20)
	release := make(chan struct{})
	var mu sync.Mutex
	active, maxActive, calls := 0, 0, 0
	c := NewDeclarationCache(configRunnerFunc(func(ctx context.Context, path string) ([]byte, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 10*time.Second {
			t.Error("missing 10s deadline")
		}
		mu.Lock()
		active++
		calls++
		if active > maxActive {
			maxActive = active
		}
		mu.Unlock()
		entered <- path
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		mu.Lock()
		active--
		mu.Unlock()
		return data, nil
	}), nil)
	entries := []stacks.Entry{declarationEntry(t, "one"), declarationEntry(t, "two"), declarationEntry(t, "three")}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if !c.Get(context.Background(), entries[i%3]).Known {
				t.Error("unknown")
			}
		}(i)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("concurrent runs did not start")
		}
	}
	select {
	case <-entered:
		t.Error("more than two subprocesses")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if maxActive != 2 || calls != 3 {
		t.Fatalf("max %d calls %d", maxActive, calls)
	}
}
func TestDeclarationsCanceledWaitDoesNotRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := NewDeclarationCache(configRunnerFunc(func(context.Context, string) ([]byte, error) {
		t.Error("runner called")
		return nil, errors.New("unexpected")
	}), nil)
	if c.Get(ctx, declarationEntry(t, "canceled")).Known {
		t.Fatal("known")
	}
}
