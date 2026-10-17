package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/maintenance"
	"github.com/rcss-labs/stackstead/internal/metrics"
	"github.com/rcss-labs/stackstead/internal/stacks"
	"github.com/rcss-labs/stackstead/internal/store"
	"github.com/rcss-labs/stackstead/internal/update"
)

type probationProviderFunc func(context.Context) (*update.Probation, error)

func (f probationProviderFunc) StartupProbation(ctx context.Context) (*update.Probation, error) {
	return f(ctx)
}

func TestProbationStartupLookupPrecedesMigration(t *testing.T) {
	te, data := serveTestEnv(t)
	sentinel := errors.New("trusted startup lookup unavailable")
	called := false
	p := probationProviderFunc(func(context.Context) (*update.Probation, error) {
		called = true
		if _, err := os.Stat(filepath.Join(data, "stackstead.db")); !os.IsNotExist(err) {
			t.Error("DB opened before startup lookup")
		}
		return nil, sentinel
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := ServeWithOptions(ctx, te.Env, nil, ServeOptions{Startup: p, Maintenance: maintenance.New()})
	if !called || !errors.Is(err, sentinel) {
		t.Fatalf("startup provider called=%v, err=%v; want fail closed", called, err)
	}
	if _, err := os.Stat(filepath.Join(data, "stackstead.db")); !os.IsNotExist(err) {
		t.Fatal("failed startup lookup created DB")
	}
}
func TestProbationDefersBackgroundWrites(t *testing.T) {
	gate := maintenance.New()
	if err := gate.Freeze(); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	synced := make(chan struct{})
	bg := deferredStartup(context.Background(), gate, func(context.Context) { close(synced) }, func(ctx context.Context) { close(started); <-ctx.Done() })
	defer bg.stop(time.Second)
	select {
	case <-started:
		t.Fatal("background writer started before probation release")
	case <-time.After(25 * time.Millisecond):
	}
	gate.Thaw()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("writer did not start after release")
	}
	select {
	case <-synced:
	default:
		t.Fatal("background started before startup sync")
	}
}
func TestProbationFrozenShutdownDoesNotStartWriters(t *testing.T) {
	gate := maintenance.New()
	gate.Freeze()
	started := make(chan struct{}, 2)
	bg := deferredStartup(context.Background(), gate, func(context.Context) { started <- struct{}{} }, func(context.Context) { started <- struct{}{} })
	if !bg.stop(time.Second) {
		t.Fatal("frozen background did not stop")
	}
	select {
	case <-started:
		t.Fatal("shutdown ran deferred writer")
	default:
	}
}

type probationCompose struct{}

func (probationCompose) Version(context.Context) (string, error)                   { return "v2", nil }
func (probationCompose) Config(context.Context, string) error                      { return nil }
func (probationCompose) ProjectName(context.Context, string) (string, error)       { return "demo", nil }
func (probationCompose) Run(context.Context, string, []string, func(string)) error { return nil }

func TestProbationDefersStartupStackSync(t *testing.T) {
	dir := t.TempDir()
	stackDir := filepath.Join(dir, "stacks")
	if err := os.MkdirAll(filepath.Join(stackDir, "demo"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stackDir, "demo", "compose.yaml"), []byte("services: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reg := stacks.NewRegistry(db, time.Now)
	svc := stacks.NewService(stacks.Deps{Registry: reg, StacksDir: stackDir, Compose: probationCompose{}})
	gate := maintenance.New()
	gate.Freeze()
	synced := make(chan error, 1)
	bg := deferredStartup(context.Background(), gate, func(ctx context.Context) { synced <- svc.Sync(ctx) })
	defer bg.stop(time.Second)
	select {
	case <-synced:
		t.Fatal("stack sync ran during probation")
	case <-time.After(25 * time.Millisecond):
	}
	entries, err := reg.List(context.Background())
	if err != nil || len(entries) != 0 {
		t.Fatalf("registry changed during probation: %v %v", entries, err)
	}
	gate.Thaw()
	select {
	case err := <-synced:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("sync did not run after release")
	}
	entries, err = reg.List(context.Background())
	if err != nil || len(entries) != 1 || entries[0].Name != "demo" {
		t.Fatalf("registry not synchronized after release: %v %v", entries, err)
	}
}

func TestProbationDefersLazyMetricsWrites(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "metrics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	g := maintenance.New()
	ms := admittedMetricsStore{Store: metrics.NewStore(db, time.Now), gate: g}
	ctx := context.Background()
	sample := []metrics.Sample{{TS: time.Now(), Container: "c1"}}
	g.Freeze()
	if err := ms.InsertBatch(ctx, sample); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM metric_samples").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("sampler persisted during probation: %d rows", n)
	}
	g.Thaw()
	if err := ms.InsertBatch(ctx, sample); err != nil {
		t.Fatal(err)
	}
	g.Freeze()
	if _, err := ms.Prune(ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM metric_samples").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("sampler pruned during maintenance: %d rows", n)
	}
	g.Thaw()
	if n, err := ms.Prune(ctx, time.Now().Add(time.Hour)); err != nil || n != 1 {
		t.Fatalf("prune after release = %d, %v", n, err)
	}
}

func TestProbationServeFreezesBeforeMigrationAndWaitsForRelease(t *testing.T) {
	te, data := serveTestEnv(t)
	g := maintenance.New()
	p := probationProviderFunc(func(context.Context) (*update.Probation, error) {
		if _, err := os.Stat(filepath.Join(data, "stackstead.db")); !os.IsNotExist(err) {
			t.Error("provider ran after DB open")
		}
		return &update.Probation{JobID: "job", Nonce: "private"}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ServeWithOptions(ctx, te.Env, nil, ServeOptions{Startup: p, Maintenance: g}) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	deadline := time.After(10 * time.Second)
	for !strings.Contains(te.Err.String(), "stackstead listening") {
		select {
		case <-deadline:
			t.Fatal("probation did not reach HTTP readiness with Docker unavailable")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if !g.Frozen() {
		t.Fatal("probation released without trusted control")
	}
	if _, err := os.Stat(filepath.Join(data, "stackstead.db")); err != nil {
		t.Fatalf("migration did not open DB: %v", err)
	}
	stackDir := filepath.Join(filepath.Dir(data), "stacks")
	if _, err := os.Stat(stackDir); !os.IsNotExist(err) {
		t.Fatalf("probation created stacks directory: %v", err)
	}
	g.Thaw()
	deadline = time.After(time.Second)
	for {
		if _, err := os.Stat(stackDir); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("release did not run deferred filesystem work")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestReviewProbationShutdownHonorsOuterGrace(t *testing.T) {
	gate := maintenance.New()
	if err := gate.Freeze(); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	finishCleanup := make(chan struct{})
	exited := make(chan struct{})
	bg := deferredStartup(context.Background(), gate, func(context.Context) {}, func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		<-finishCleanup
		close(exited)
	})
	gate.Thaw()
	<-started
	stopped := bg.stop(2 * time.Second)
	select {
	case <-exited:
	default:
		close(finishCleanup)
		<-exited
		if stopped {
			t.Fatal("background reported stopped while child cleanup remained active, before the 2s outer grace expired")
		}
		return
	}
	close(finishCleanup)
}
