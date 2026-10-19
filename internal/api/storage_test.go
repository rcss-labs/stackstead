package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/rcss-labs/stackstead/internal/actionlog"
	"github.com/rcss-labs/stackstead/internal/store"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/api"
	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/client"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/stacks"
	"github.com/rcss-labs/stackstead/internal/storage"
)

const storageImageID = "sha256:aaaaaaaaaaaa1111111111111111111111111111111111111111111111111111"

type storageEngine struct {
	now          func() time.Time
	registry     storage.StackRegistry
	declarations storage.DeclarationSource
	mu           sync.Mutex
	images       []engine.Image
	volumes      []engine.Volume
	containers   []engine.Container
	lists, dfs   int
	down, dfFail bool
	dfGate       <-chan struct{}
	removeGate   <-chan struct{}
}

func (e *storageEngine) Images(context.Context) ([]engine.Image, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lists++
	if e.down {
		return nil, engine.ErrUnavailable
	}
	return e.images, nil
}
func (e *storageEngine) Volumes(context.Context) ([]engine.Volume, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.volumes, nil
}
func (e *storageEngine) Containers(context.Context) ([]engine.Container, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.containers, nil
}
func (e *storageEngine) DiskUsage(ctx context.Context, _ engine.DFKind) (engine.DFSnapshot, error) {
	e.mu.Lock()
	e.dfs++
	gate, fail := e.dfGate, e.dfFail
	images, volumes := e.images, e.volumes
	e.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return engine.DFSnapshot{}, ctx.Err()
		}
	}
	if fail {
		return engine.DFSnapshot{}, errors.New("df failed")
	}
	snap := engine.DFSnapshot{Kinds: []engine.DFKind{engine.DFImages, engine.DFContainers, engine.DFVolumes, engine.DFBuildCache}}
	for _, i := range images {
		size, shared := i.Size, int64(2)
		snap.Images = append(snap.Images, engine.DFImage{ID: i.ID, Size: &size, SharedSize: &shared})
	}
	for _, v := range volumes {
		snap.Volumes = append(snap.Volumes, engine.DFVolume{Name: v.Name, UsageData: &engine.DFVolumeUsage{Size: 10}})
	}
	return snap, nil
}
func (e *storageEngine) InspectImage(_ context.Context, id string) (engine.ImageInspect, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, i := range e.images {
		if i.ID == id {
			return engine.ImageInspect{Image: i, User: "fixture-image-user-secret", OS: "linux", Architecture: "amd64", LayerCount: 3}, nil
		}
	}
	return engine.ImageInspect{}, engine.ErrImageNotFound
}
func (e *storageEngine) InspectVolume(_ context.Context, name string) (engine.VolumeInspect, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, v := range e.volumes {
		if v.Name == name {
			return v, nil
		}
	}
	return engine.VolumeInspect{}, engine.ErrVolumeNotFound
}
func (e *storageEngine) RemoveImage(ctx context.Context, _ string) (engine.ImageDeleteResult, error) {
	if e.removeGate != nil {
		select {
		case <-e.removeGate:
		case <-ctx.Done():
			return engine.ImageDeleteResult{}, ctx.Err()
		}
	}
	return engine.ImageDeleteResult{}, nil
}
func (e *storageEngine) RemoveVolume(_ context.Context, name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	kept := make([]engine.Volume, 0, len(e.volumes))
	for _, v := range e.volumes {
		if v.Name != name {
			kept = append(kept, v)
		}
	}
	e.volumes = kept
	return nil
}
func (e *storageEngine) BuildPruneByID(context.Context) (bool, error)             { return false, nil }
func (e *storageEngine) PruneBuildCache(context.Context, []string) (int64, error) { return 0, nil }

func newStorageEnv(t *testing.T, e *storageEngine, tweak func(*api.Options, *storage.Service)) (*testEnv, *http.Cookie, *storage.Service) {
	t.Helper()
	now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	clock := e.now
	if clock == nil {
		clock = func() time.Time { return now }
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "storage-actions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	actions := actionlog.NewStore(db, clock)
	ops := stacks.NewOps(time.Minute, time.Now)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		ops.Shutdown(ctx)
	})
	svc := storage.NewService(storage.ServiceConfig{Engine: e, Now: clock, Ops: ops, Actions: actions, Registry: e.registry, Declarations: e.declarations})
	env := newEnvWith(t, "", func(o *api.Options) {
		o.Storage = svc
		o.Actions = actions
		o.Operations = ops
		o.Now = clock
		if tweak != nil {
			tweak(o, svc)
		}
	})
	env.createAdmin(t)
	return env, env.login(t), svc
}
func storageFixture() *storageEngine {
	return &storageEngine{images: []engine.Image{{ID: storageImageID, Size: 20}}, volumes: []engine.Volume{{Name: "old_data", Driver: "local", Scope: "local", Mountpoint: "/var/lib/docker/volumes/old_data", CreatedAt: time.Now(), OptionType: "nfs"}}}
}
func storageRequest(t *testing.T, e *testEnv, c *http.Cookie, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return e.do(t, method, path, body, withCookie(c))
}

func TestStorageRoutesRequireSessionAndJSON(t *testing.T) {
	env, c, _ := newStorageEnv(t, storageFixture(), nil)
	for _, route := range []struct{ m, p string }{{"GET", "/api/storage"}, {"GET", "/api/storage/images"}, {"GET", "/api/storage/images/aaaaaaaaaaaa"}, {"GET", "/api/storage/volumes"}, {"GET", "/api/storage/volumes/old_data"}, {"GET", "/api/storage/build-cache"}, {"POST", "/api/storage/refresh"}, {"POST", "/api/storage/plans"}, {"GET", "/api/storage/plans/p_missing"}, {"POST", "/api/storage/plans/p_missing/confirm"}, {"GET", "/api/actions"}} {
		if r := env.do(t, route.m, route.p, "{}"); r.Code != 401 {
			t.Errorf("anonymous %s %s=%d", route.m, route.p, r.Code)
		}
	}
	for _, path := range []string{"/api/storage/refresh", "/api/storage/plans", "/api/storage/plans/p_missing/confirm"} {
		if r := storageRequest(t, env, c, "POST", path, ""); r.Code != 415 {
			t.Errorf("no JSON %s=%d %s", path, r.Code, r.Body)
		}
		if r := env.do(t, "POST", path, "", withCookie(c), withHeader("Content-Type", "application/json")); r.Code != 400 {
			t.Errorf("no body %s=%d %s", path, r.Code, r.Body)
		}
	}
}
func TestStorageOverviewPendingAndSingleFlight(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	eng := storageFixture()
	eng.dfGate = gate
	eng.images[0].Size = -1
	env, c, _ := newStorageEnv(t, eng, nil)
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := storageRequest(t, env, c, "GET", "/api/storage/images", "")
			if r.Code != 200 {
				t.Errorf("list=%d %s", r.Code, r.Body)
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("list requests waited for disk usage")
	}
	eng.mu.Lock()
	n := eng.lists
	eng.mu.Unlock()
	if n != 1 {
		t.Fatalf("image lists=%d want1", n)
	}
	r := storageRequest(t, env, c, "GET", "/api/storage", "")
	var overview apitypes.StorageOverview
	if err := json.Unmarshal(r.Body.Bytes(), &overview); err != nil || r.Code != 200 {
		t.Fatalf("overview=%d %s %v", r.Code, r.Body, err)
	}
	if overview.Images.Size != nil || overview.Images.SizesAt != nil || !overview.Images.Refreshing || overview.TopContainers == nil || overview.DeclarationsUnknown == nil {
		t.Fatalf("pending overview=%+v", overview)
	}
	var images apitypes.StorageImages
	r = storageRequest(t, env, c, "GET", "/api/storage/images", "")
	_ = json.Unmarshal(r.Body.Bytes(), &images)
	if len(images.Images) != 1 || images.Images[0].Size != nil || images.Images[0].Tags == nil || images.Images[0].Digests == nil || images.Images[0].Containers == nil || images.Images[0].DeclaredBy == nil {
		t.Fatalf("pending images=%s", r.Body)
	}
	for _, path := range []string{"/api/storage/volumes", "/api/storage/build-cache"} {
		r = storageRequest(t, env, c, "GET", path, "")
		if r.Code != 200 || strings.Contains(r.Body.String(), "records\":null") || strings.Contains(r.Body.String(), "containers\":null") {
			t.Fatalf("list=%d %s", r.Code, r.Body)
		}
	}
}
func TestStorageRefreshStartsWorkAndRateLimits(t *testing.T) {
	eng := storageFixture()
	env, c, _ := newStorageEnv(t, eng, nil)
	r := storageRequest(t, env, c, "POST", "/api/storage/refresh", "{}")
	if r.Code != 202 || !strings.Contains(r.Body.String(), "startedAt") {
		t.Fatalf("refresh=%d %s", r.Code, r.Body)
	}
	deadline := time.Now().Add(time.Second)
	for {
		eng.mu.Lock()
		n := eng.dfs
		eng.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("accepted refresh did not start df")
		}
		time.Sleep(time.Millisecond)
	}
	r = storageRequest(t, env, c, "POST", "/api/storage/refresh", "{}")
	if r.Code != 429 || r.Header().Get("Retry-After") != "60" || !strings.Contains(r.Body.String(), `"retryAfter":60`) {
		t.Fatalf("throttled=%d %s %s", r.Code, r.Body, r.Header())
	}
}
func TestStorageDetailsSafeAndPathValidation(t *testing.T) {
	env, c, _ := newStorageEnv(t, storageFixture(), nil)
	for _, tc := range []struct {
		path   string
		status int
	}{{"/api/storage/images/aaaaaaaaaaaa", 200}, {"/api/storage/images/not-an-image", 400}, {"/api/storage/images/bbbbbbbbbbbb", 404}, {"/api/storage/volumes/old_data", 200}, {"/api/storage/volumes/.bad", 400}, {"/api/storage/volumes/missing", 404}} {
		r := storageRequest(t, env, c, "GET", tc.path, "")
		if r.Code != tc.status {
			t.Errorf("%s=%d %s", tc.path, r.Code, r.Body)
		}
		for _, key := range []string{`"env"`, `"cmd"`, `"command"`, `"entrypoint"`, `"user"`, `"options"`, "fixture-image-user-secret"} {
			if strings.Contains(strings.ToLower(r.Body.String()), key) {
				t.Errorf("unsafe body %s", r.Body)
			}
		}
	}
	eng := storageFixture()
	eng.images = append(eng.images, engine.Image{ID: "sha256:aaaaaaaaaaaa2222222222222222222222222222222222222222222222222222"})
	env, c, _ = newStorageEnv(t, eng, nil)
	if r := storageRequest(t, env, c, "GET", "/api/storage/images/aaaaaaaaaaaa", ""); r.Code != 409 {
		t.Fatalf("ambiguous=%d %s", r.Code, r.Body)
	}
}
func TestStoragePlanAndConfirmStatuses(t *testing.T) {
	eng := storageFixture()
	env, c, _ := newStorageEnv(t, eng, nil)
	for _, tc := range []struct {
		body   string
		status int
		field  string
	}{{`{"kind":"bad"}`, 400, "kind"}, {`{"kind":"image","ref":"bad?"}`, 400, "ref"}, {`{"kind":"volume","name":"missing"}`, 404, ""}, {`{"kind":"volumes","selection":"unused"}`, 201, ""}, {`{"kind":"build-cache","selection":"unused"}`, 201, ""}} {
		r := storageRequest(t, env, c, "POST", "/api/storage/plans", tc.body)
		if r.Code != tc.status {
			t.Fatalf("plan %s=%d %s", tc.body, r.Code, r.Body)
		}
		if tc.field != "" && !strings.Contains(r.Body.String(), fmt.Sprintf(`"field":%q`, tc.field)) {
			t.Errorf("missing field=%s", r.Body)
		}
		if r.Code == 201 {
			var p apitypes.StoragePlan
			_ = json.Unmarshal(r.Body.Bytes(), &p)
			if p.Items == nil || p.Protected == nil || p.Warnings == nil {
				t.Fatalf("null arrays=%s", r.Body)
			}
			if p.Kind != "build-cache" && strings.Contains(r.Body.String(), `"mode"`) {
				t.Fatalf("ordinary mode=%s", r.Body)
			}
			if p.Kind == "build-cache" && p.Mode != "all-unused" {
				t.Fatalf("fallback=%s", r.Body)
			}
			got := storageRequest(t, env, c, "GET", "/api/storage/plans/"+p.ID, "")
			if got.Code != 200 {
				t.Fatalf("getplan=%d %s", got.Code, got.Body)
			}
			if len(p.Items) == 0 {
				got = storageRequest(t, env, c, "POST", "/api/storage/plans/"+p.ID+"/confirm", `{"confirm":"`+p.ConfirmPhrase+`"}`)
				if got.Code != 409 {
					t.Fatalf("empty=%d %s", got.Code, got.Body)
				}
			}
		}
	}
	r := storageRequest(t, env, c, "POST", "/api/storage/plans", `{"kind":"image","ref":"aaaaaaaaaaaa"}`)
	var p apitypes.StoragePlan
	_ = json.Unmarshal(r.Body.Bytes(), &p)
	if r.Code != 201 {
		t.Fatalf("single plan=%d %s", r.Code, r.Body)
	}
	r = storageRequest(t, env, c, "POST", "/api/storage/plans/"+p.ID+"/confirm", `{"confirm":"wrong"}`)
	if r.Code != 400 || !strings.Contains(r.Body.String(), `"field":"confirm"`) {
		t.Fatalf("wrong phrase=%d %s", r.Code, r.Body)
	}
	r = storageRequest(t, env, c, "POST", "/api/storage/plans/"+p.ID+"/confirm", `{"confirm":"`+p.ConfirmPhrase+`"}`)
	if r.Code != 202 || !strings.Contains(r.Body.String(), `"action":"storage-cleanup"`) {
		t.Fatalf("confirm=%d %s", r.Code, r.Body)
	}
	r = storageRequest(t, env, c, "POST", "/api/storage/plans/p_missing/confirm", `{"confirm":"x"}`)
	if r.Code != 404 {
		t.Fatalf("unknown confirm=%d", r.Code)
	}
	r = storageRequest(t, env, c, "GET", "/api/storage/plans/"+p.ID, "")
	if r.Code != 404 {
		t.Fatalf("consumed get=%d", r.Code)
	}
}
func TestStorageUnavailable(t *testing.T) {
	for _, dfOnly := range []bool{false, true} {
		eng := storageFixture()
		eng.down = !dfOnly
		eng.dfFail = dfOnly
		env, c, _ := newStorageEnv(t, eng, nil)
		paths := []struct{ m, p, b string }{{"POST", "/api/storage/plans", `{"kind":"volume","name":"old_data"}`}}
		if !dfOnly {
			paths = append(paths, struct{ m, p, b string }{"GET", "/api/storage", ""}, struct{ m, p, b string }{"GET", "/api/storage/images", ""}, struct{ m, p, b string }{"GET", "/api/storage/volumes", ""}, struct{ m, p, b string }{"GET", "/api/storage/build-cache", ""})
		}
		for _, p := range paths {
			r := storageRequest(t, env, c, p.m, p.p, p.b)
			if r.Code != 503 {
				t.Errorf("down %s=%d %s", p.p, r.Code, r.Body)
			}
		}
	}
}
func TestStorageClientRealRouter(t *testing.T) {
	env, cookie, _ := newStorageEnv(t, storageFixture(), nil)
	srv := httptest.NewServer(env.handler)
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	c, err := client.New(client.Config{BaseURL: u, Session: cookie.Value})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := c.StorageOverview(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := c.CreateStoragePlan(ctx, apitypes.StoragePlanRequest{Kind: "volume", Name: "old_data"})
	if err != nil || p.ConfirmPhrase != "old_data" {
		t.Fatalf("create=%+v %v", p, err)
	}
	if _, err = c.ConfirmStoragePlan(ctx, p.ID, "wrong"); err == nil {
		t.Fatal("wrong phrase accepted")
	}
	if o, err := c.ConfirmStoragePlan(ctx, p.ID, p.ConfirmPhrase); err != nil || o.Action != "storage-cleanup" {
		t.Fatalf("confirm=%+v %v", o, err)
	}
	if _, err = c.RefreshStorage(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = c.RefreshStorage(ctx); err == nil {
		t.Fatal("refresh limit missing")
	}
}

type storageRegistry []stacks.Entry

func (r storageRegistry) List(context.Context) ([]stacks.Entry, error) { return r, nil }

type storageDeclarations struct{}

func (storageDeclarations) Get(context.Context, stacks.Entry) storage.StackDeclarations {
	return storage.StackDeclarations{Name: "media", Project: "media", Known: true, Images: []storage.DeclaredImage{{Service: "db", Reference: "db:1"}}, Volumes: []storage.DeclaredVolume{{Key: "db", Name: "old_data"}}}
}
func TestStorageRefusalsAndExpiry(t *testing.T) {
	eng := storageFixture()
	eng.registry = storageRegistry{{Name: "media", Project: "media"}}
	eng.declarations = storageDeclarations{}
	env, c, _ := newStorageEnv(t, eng, nil)
	r := storageRequest(t, env, c, "POST", "/api/storage/plans", `{"kind":"volume","name":"old_data"}`)
	if r.Code != 409 || !strings.Contains(r.Body.String(), "declared by stack media; remove it with the stack") {
		t.Errorf("protected=%d %s", r.Code, r.Body)
	}
	r = storageRequest(t, env, c, "POST", "/api/storage/plans", `{"kind":"volumes","selection":"unused"}`)
	var p apitypes.StoragePlan
	_ = json.Unmarshal(r.Body.Bytes(), &p)
	if r.Code != 201 || len(p.Items) != 0 || len(p.Protected) != 1 || p.Protected[0].Stack != "media" || p.Protected[0].StackState != "not-deployed" {
		t.Fatalf("protected bulk=%d %s", r.Code, r.Body)
	}
	eng = storageFixture()
	eng.images = append(eng.images, engine.Image{ID: "sha256:aaaaaaaaaaaa2222222222222222222222222222222222222222222222222222", Size: 30})
	env, c, _ = newStorageEnv(t, eng, nil)
	r = storageRequest(t, env, c, "POST", "/api/storage/plans", `{"kind":"image","ref":"aaaaaaaaaaaa"}`)
	if r.Code != 409 {
		t.Fatalf("ambiguous plan=%d %s", r.Code, r.Body)
	}
	var elapsed atomic.Int64
	eng = storageFixture()
	eng.now = func() time.Time {
		return time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC).Add(time.Duration(elapsed.Load()))
	}
	env, c, _ = newStorageEnv(t, eng, nil)
	r = storageRequest(t, env, c, "POST", "/api/storage/plans", `{"kind":"volume","name":"old_data"}`)
	_ = json.Unmarshal(r.Body.Bytes(), &p)
	if r.Code != 201 {
		t.Fatal(r.Body)
	}
	elapsed.Store(int64(5 * time.Minute))
	for _, route := range []struct{ method, path, body string }{{"GET", "/api/storage/plans/" + p.ID, ""}, {"POST", "/api/storage/plans/" + p.ID + "/confirm", `{"confirm":"old_data"}`}} {
		r = storageRequest(t, env, c, route.method, route.path, route.body)
		if r.Code != 404 {
			t.Fatalf("expired=%d %s", r.Code, r.Body)
		}
	}
}
func TestStorageConfirmBusyDownAndActor(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	eng := storageFixture()
	eng.removeGate = gate
	env, c, _ := newStorageEnv(t, eng, nil)
	create := func() apitypes.StoragePlan {
		r := storageRequest(t, env, c, "POST", "/api/storage/plans", `{"kind":"image","ref":"aaaaaaaaaaaa"}`)
		var p apitypes.StoragePlan
		_ = json.Unmarshal(r.Body.Bytes(), &p)
		if r.Code != 201 {
			t.Fatalf("create=%d %s", r.Code, r.Body)
		}
		return p
	}
	p, p2 := create(), create()
	r := storageRequest(t, env, c, "POST", "/api/storage/plans/"+p.ID+"/confirm", `{"confirm":"`+p.ConfirmPhrase+`"}`)
	if r.Code != 202 {
		t.Fatalf("confirm=%d %s", r.Code, r.Body)
	}
	r = storageRequest(t, env, c, "POST", "/api/storage/plans/"+p2.ID+"/confirm", `{"confirm":"`+p2.ConfirmPhrase+`"}`)
	if r.Code != 409 {
		t.Fatalf("busy=%d %s", r.Code, r.Body)
	}
	eng.mu.Lock()
	eng.down = true
	eng.mu.Unlock()
	r = storageRequest(t, env, c, "POST", "/api/storage/plans/"+p2.ID+"/confirm", `{"confirm":"`+p2.ConfirmPhrase+`"}`)
	if r.Code != 503 {
		t.Fatalf("down=%d %s", r.Code, r.Body)
	}
	// Let the first operation finish and verify the actor comes from the session.
	eng.mu.Lock()
	eng.down = false
	eng.mu.Unlock()
	// The operation is blocked in RemoveImage, after preflight.
	// Gate closing is deferred; a separate image operation below records promptly.
	eng2 := storageFixture()
	env2, c2, _ := newStorageEnv(t, eng2, nil)
	rr := storageRequest(t, env2, c2, "POST", "/api/storage/plans", `{"kind":"volume","name":"old_data"}`)
	var vp apitypes.StoragePlan
	_ = json.Unmarshal(rr.Body.Bytes(), &vp)
	rr = storageRequest(t, env2, c2, "POST", "/api/storage/plans/"+vp.ID+"/confirm", `{"confirm":"old_data"}`)
	if rr.Code != 202 {
		t.Fatalf("actor confirm=%d %s", rr.Code, rr.Body)
	}
	deadline := time.Now().Add(time.Second)
	for {
		rr = storageRequest(t, env2, c2, "GET", "/api/actions?kind=storage.", "")
		var rows []apitypes.ActionEntry
		_ = json.Unmarshal(rr.Body.Bytes(), &rows)
		if len(rows) > 0 {
			if rows[0].Username != "admin" || rows[0].OperationID == "" {
				t.Fatalf("actor=%s", rr.Body)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("missing actor row=%s", rr.Body)
		}
		time.Sleep(time.Millisecond)
	}
}
func TestStorageLoadedMetadataSortingAndRefreshCeiling(t *testing.T) {
	var elapsed atomic.Int64
	eng := storageFixture()
	eng.now = func() time.Time {
		return time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC).Add(time.Duration(elapsed.Load()))
	}
	eng.images = append(eng.images, engine.Image{ID: "sha256:bbbbbbbbbbbb2222222222222222222222222222222222222222222222222222", Size: 100}, engine.Image{ID: "sha256:cccccccccccc3333333333333333333333333333333333333333333333333333", Size: -1})
	env, c, svc := newStorageEnv(t, eng, nil)
	if _, err := svc.FreshDF(context.Background(), engine.DFImages); err != nil {
		t.Fatal(err)
	}
	r := storageRequest(t, env, c, "GET", "/api/storage/images", "")
	var out apitypes.StorageImages
	_ = json.Unmarshal(r.Body.Bytes(), &out)
	if r.Code != 200 || len(out.Images) != 3 || out.Images[0].ID != eng.images[1].ID || out.Images[2].Size != nil || out.Totals.SizesAt == nil || out.Totals.Refreshing {
		t.Fatalf("loaded sorted=%d %s", r.Code, r.Body)
	}
	for _, path := range []string{"/api/storage/volumes", "/api/storage/build-cache"} {
		r = storageRequest(t, env, c, "GET", path, "")
		var body struct {
			Totals apitypes.StorageKindTotals `json:"totals"`
		}
		_ = json.Unmarshal(r.Body.Bytes(), &body)
		if r.Code != 200 || body.Totals.SizesAt == nil {
			t.Fatalf("metadata=%d %s", r.Code, r.Body)
		}
	}
	r = storageRequest(t, env, c, "POST", "/api/storage/refresh", "{}")
	if r.Code != 202 {
		t.Fatal(r.Body)
	}
	elapsed.Store(int64(1500 * time.Millisecond))
	r = storageRequest(t, env, c, "POST", "/api/storage/refresh", "{}")
	if r.Code != 429 || r.Header().Get("Retry-After") != "59" {
		t.Fatalf("ceil=%d %s %s", r.Code, r.Body, r.Header())
	}
}

// Exercise the real engine decoder and router together, so forbidden Docker
// inspect fields cannot reach HTTP even when present in the daemon response.
func TestStorageInspectFixtureSecretsNeverReachHTTP(t *testing.T) {
	imageRaw, err := os.ReadFile("../engine/testdata/image-inspect.json")
	if err != nil {
		t.Fatal(err)
	}
	volumeRaw, err := os.ReadFile("../engine/testdata/volume-inspect.json")
	if err != nil {
		t.Fatal(err)
	}
	imageBody := strings.ReplaceAll(string(imageRaw), "sha256:img", storageImageID)
	dir, err := os.MkdirTemp("/tmp", "storage-api-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	daemon := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/_ping":
			w.Header().Set("Api-Version", "1.47")
			fmt.Fprint(w, "OK")
		case "/version":
			fmt.Fprint(w, `{"ApiVersion":"1.47","MinAPIVersion":"1.41"}`)
		case "/v1.47/images/json":
			fmt.Fprintf(w, `[{"Id":%q,"Size":1234}]`, storageImageID)
		case "/v1.47/volumes":
			fmt.Fprint(w, `{"Volumes":[{"Name":"demo-data","Driver":"local"}]}`)
		case "/v1.47/containers/json":
			fmt.Fprint(w, `[]`)
		case "/v1.47/images/" + storageImageID + "/json":
			fmt.Fprint(w, imageBody)
		case "/v1.47/volumes/demo-data":
			_, _ = w.Write(volumeRaw)
		case "/v1.47/system/df":
			fmt.Fprint(w, `{}`)
		default:
			http.NotFound(w, r)
		}
	})}
	go daemon.Serve(listener)
	defer daemon.Close()
	svc := storage.NewService(storage.ServiceConfig{Engine: engine.New(socket, time.Second)})
	env := newEnvWith(t, "", func(o *api.Options) { o.Storage = svc })
	env.createAdmin(t)
	c := env.login(t)
	for _, path := range []string{"/api/storage/images/aaaaaaaaaaaa", "/api/storage/volumes/demo-data"} {
		r := storageRequest(t, env, c, "GET", path, "")
		if r.Code != 200 {
			t.Fatalf("inspect=%d %s", r.Code, r.Body)
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"env", "cmd", "command", "entrypoint", "user", "options", "User", "Env", "Cmd", "Entrypoint", "Options"} {
			if _, ok := body[key]; ok {
				t.Errorf("forbidden key %s", key)
			}
		}
		if strings.Contains(r.Body.String(), "fixture-") {
			t.Fatal("inspect fixture secret reached HTTP")
		}
		if strings.Contains(path, "images") {
			var d apitypes.StorageImageDetail
			_ = json.Unmarshal(r.Body.Bytes(), &d)
			if d.Architecture != "arm64" || d.OS != "linux" || d.Layers != 2 || d.Labels["owner"] != "demo" || d.ID != storageImageID {
				t.Fatalf("image detail=%+v", d)
			}
		} else {
			var d apitypes.StorageVolumeDetail
			_ = json.Unmarshal(r.Body.Bytes(), &d)
			if d.Name != "demo-data" || d.Mountpoint != "/var/lib/docker/volumes/demo-data/_data" || d.Labels["owner"] != "demo" {
				t.Fatalf("volume detail=%+v", d)
			}
		}
	}
}

func TestStorageConfirmInvalidatesCachedList(t *testing.T) {
	var ops *stacks.Ops
	env, c, _ := newStorageEnv(t, storageFixture(), func(o *api.Options, _ *storage.Service) { ops = o.Operations.(*stacks.Ops) })
	r := storageRequest(t, env, c, "GET", "/api/storage/volumes", "")
	var before apitypes.StorageVolumes
	_ = json.Unmarshal(r.Body.Bytes(), &before)
	if r.Code != 200 || len(before.Volumes) != 1 {
		t.Fatalf("initial=%d %s", r.Code, r.Body)
	}
	r = storageRequest(t, env, c, "POST", "/api/storage/plans", `{"kind":"volume","name":"old_data"}`)
	var p apitypes.StoragePlan
	_ = json.Unmarshal(r.Body.Bytes(), &p)
	if r.Code != 201 {
		t.Fatal(r.Body)
	}
	r = storageRequest(t, env, c, "POST", "/api/storage/plans/"+p.ID+"/confirm", `{"confirm":"old_data"}`)
	var op apitypes.Operation
	_ = json.Unmarshal(r.Body.Bytes(), &op)
	if r.Code != 202 {
		t.Fatal(r.Body)
	}
	deadline := time.Now().Add(time.Second)
	for {
		current, err := ops.Get(op.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.State != stacks.OpRunning {
			if current.State != stacks.OpSucceeded {
				t.Fatalf("operation=%+v", current)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("operation did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	r = storageRequest(t, env, c, "GET", "/api/storage/volumes", "")
	var after apitypes.StorageVolumes
	_ = json.Unmarshal(r.Body.Bytes(), &after)
	if r.Code != 200 || after.Volumes == nil || len(after.Volumes) != 0 {
		t.Fatalf("deleted volume remained in cached list=%d %s", r.Code, r.Body)
	}
}
