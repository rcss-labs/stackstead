package images

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/imageref"
	"github.com/rcss-labs/stackstead/internal/peers"
)

const peerCredential = "ssp_test_credential"

// fakePeer is a Stackstead peer answering the three image routes.
type fakePeer struct {
	srv     *httptest.Server
	id      string
	name    string
	images  []apitypes.LibraryImageDetail
	usage   []apitypes.LibraryUsage
	status  int         // non-zero: answer this status with a plain-text body
	hanging atomic.Bool // true: block until released or the request ends
	release chan struct{}
	calls   atomic.Int32
	usageHi atomic.Int32
}

func newFakePeer(t *testing.T, id, name string) *fakePeer {
	t.Helper()
	p := &fakePeer{id: id, name: name, release: make(chan struct{})}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(peers.PeerHeader) != peerCredential {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/api/peer/images/usage" {
			p.usageHi.Add(1)
		} else {
			p.calls.Add(1)
		}
		if p.hanging.Load() {
			select {
			case <-p.release:
			case <-r.Context().Done():
			}
			return
		}
		if p.status != 0 {
			http.Error(w, "nope", p.status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/peer/images":
			json.NewEncoder(w).Encode(p.images)
		case "/api/peer/images/usage":
			json.NewEncoder(w).Encode(p.usage)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(func() {
		close(p.release)
		p.srv.Close()
	})
	return p
}

type fakePeerSource struct{ list []*fakePeer }

func (f *fakePeerSource) Views(context.Context) ([]apitypes.PeerView, error) {
	out := []apitypes.PeerView{}
	for _, p := range f.list {
		out = append(out, apitypes.PeerView{ID: p.id, Name: p.name, Slug: strings.ToLower(p.name), Status: "reachable", Compatible: true})
	}
	return out, nil
}

func (f *fakePeerSource) Usable(id string) (peers.Peer, error) {
	for _, p := range f.list {
		if p.id == id {
			return peers.Peer{InstanceID: p.id, Name: p.name, URL: p.srv.URL, TLS: peers.TLSOptions{Plaintext: true},
				OutCredential: peers.Secret(peerCredential), State: peers.StateActive}, nil
		}
	}
	return peers.Peer{}, errors.New("unknown")
}

const (
	peerBID = "bbbbbbbbbbbb111122223333"
	peerCID = "cccccccccccc111122223333"
	peerDID = "dddddddddddd111122223333"
)

func peerImage(slug string, tags ...string) apitypes.LibraryImageDetail {
	d := apitypes.LibraryImageDetail{}
	d.Slug, d.Name = slug, slug
	for _, tag := range tags { // newest first
		d.Versions = append(d.Versions, apitypes.LibraryVersion{ID: "v-" + tag, Tag: tag, Status: "succeeded", Arch: "amd64", ImageID: "sha256:" + tag})
	}
	return d
}

func (h *harness) withPeers(t *testing.T, timeout time.Duration, list ...*fakePeer) {
	t.Helper()
	h.svc.peers = &fakePeerSource{list: list}
	h.svc.peerTimeout = timeout
}

func TestLibraryMergesPeers(t *testing.T) {
	h := newHarness(t)
	h.create(t, "mine")
	good := newFakePeer(t, peerBID, "Host B")
	good.images = []apitypes.LibraryImageDetail{peerImage("shared", "2", "1")}
	old := newFakePeer(t, peerCID, "Host C")
	old.status = http.StatusNotFound
	hung := newFakePeer(t, peerDID, "Host D")
	hung.hanging.Store(true)
	h.withPeers(t, 300*time.Millisecond, good, old, hung)

	start := time.Now()
	lib, err := h.svc.Library(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("library took %v with a hanging peer", took)
	}
	if len(lib.Images) != 2 {
		t.Fatalf("images = %d, want local + peer", len(lib.Images))
	}
	peerEntry := lib.Images[1]
	if peerEntry.Slug != "shared" || peerEntry.Owner.Local || peerEntry.Owner.ID != peerBID || peerEntry.Owner.Name != "Host B" || len(peerEntry.Versions) != 2 {
		t.Fatalf("peer entry = %+v", peerEntry)
	}
	if peerEntry.PushTargets == nil || peerEntry.Pushes == nil {
		t.Fatal("nil list or map on peer entry")
	}
	want := map[string]string{testInstanceID: "reachable", peerBID: "reachable", peerCID: "old", peerDID: "unreachable"}
	if len(lib.Servers) != 4 {
		t.Fatalf("servers = %+v", lib.Servers)
	}
	for _, s := range lib.Servers {
		if s.Status != want[s.ID] {
			t.Errorf("server %s status = %q, want %q", s.Name, s.Status, want[s.ID])
		}
		if s.ID == peerDID {
			if s.Error == "" || strings.Contains(s.Error, hung.srv.URL) || strings.Contains(s.Error, peerCredential) {
				t.Errorf("unreachable error = %q", s.Error)
			}
		}
	}
}

func TestLibraryCachesPeerResults(t *testing.T) {
	h := newHarness(t)
	good := newFakePeer(t, peerBID, "Host B")
	good.images = []apitypes.LibraryImageDetail{peerImage("shared", "1")}
	h.withPeers(t, time.Second, good)
	for i := 0; i < 3; i++ {
		if _, err := h.svc.Library(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if n := good.calls.Load(); n != 1 {
		t.Fatalf("peer asked %d times, want 1 (cached)", n)
	}
	h.svc.libraryTTL = time.Nanosecond
	time.Sleep(2 * time.Millisecond)
	if _, err := h.svc.Library(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := good.calls.Load(); n != 2 {
		t.Fatalf("peer asked %d times after expiry, want 2", n)
	}
}

func TestResolvePeerOwnedRef(t *testing.T) {
	h := newHarness(t)
	good := newFakePeer(t, peerBID, "Host B")
	good.images = []apitypes.LibraryImageDetail{peerImage("shared", "2", "1")}
	h.withPeers(t, time.Second, good)
	ctx := context.Background()
	if _, err := h.svc.Library(ctx); err != nil { // warms the cache
		t.Fatal(err)
	}
	ref := imageref.Format(imageref.OwnerID(peerBID), "shared", "1")
	got, err := h.svc.Resolve(ctx, []string{ref})
	if err != nil {
		t.Fatal(err)
	}
	r := got[ref]
	if !r.Known || !r.Newer || r.LatestTag != "2" || r.Arch != "amd64" || r.OwnerName != "Host B" {
		t.Fatalf("resolved = %+v", r)
	}
}

func TestResolveNeverWaitsOnPeers(t *testing.T) {
	h := newHarness(t)
	hung := newFakePeer(t, peerDID, "Host D")
	hung.hanging.Store(true)
	h.withPeers(t, 5*time.Second, hung)
	ref := imageref.Format(imageref.OwnerID(peerDID), "shared", "1")
	start := time.Now()
	got, err := h.svc.Resolve(context.Background(), []string{ref})
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("resolve took %v", took)
	}
	if got[ref].Known {
		t.Fatal("unknown peer image reported known")
	}
}

func TestUsedByIncludesPeerUsage(t *testing.T) {
	h := newHarness(t)
	h.create(t, "app")
	h.build(t, "app", "1", nil)
	good := newFakePeer(t, peerBID, "Host B")
	ref := imageref.Format(testOwner, "app", "1")
	good.usage = []apitypes.LibraryUsage{{Ref: ref, Stack: "web", Service: "api"}, {Ref: imageref.Format(testOwner, "app", "9"), Stack: "x", Service: "y"}}
	h.withPeers(t, time.Second, good)
	h.usage.usages = []imageref.Usage{{Stack: "local", Service: "svc", Ref: imageref.Ref{Owner: testOwner, Slug: "app", Tag: "1"}}}

	uses, unreachable, err := h.svc.usedBy(context.Background(), imageref.Ref{Owner: testOwner, Slug: "app", Tag: "1"})
	if err != nil || len(unreachable) != 0 {
		t.Fatalf("err %v unreachable %+v", err, unreachable)
	}
	if len(uses) != 2 || uses[0].Server != testInstanceID || uses[1].Server != peerBID || uses[1].ServerName != "Host B" || uses[1].Stack != "web" {
		t.Fatalf("uses = %+v", uses)
	}
}

func TestDeleteVersionRefusedWhenPeerUnreachable(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.create(t, "app")
	v := h.build(t, "app", "1", nil)
	hung := newFakePeer(t, peerDID, "Host D")
	hung.hanging.Store(true)
	h.withPeers(t, 200*time.Millisecond, hung)

	err := h.svc.DeleteVersion(ctx, "alice", "app", v.ID)
	var inUse *InUseError
	if !errors.As(err, &inUse) || len(inUse.Unreachable) != 1 || inUse.Unreachable[0].ID != peerDID {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "Host D") {
		t.Fatalf("error does not name the server: %v", err)
	}
	if _, err := h.svc.VersionByTag(ctx, "app", "1"); err != nil {
		t.Fatalf("version gone: %v", err)
	}
	for _, c := range h.eng.engineRemoved() {
		t.Fatalf("docker tag removed: %s", c)
	}
	if err := h.svc.Delete(ctx, "alice", "app"); !errors.As(err, &inUse) || len(inUse.Unreachable) != 1 {
		t.Fatalf("delete image err = %v", err)
	}
	// Once the peer answers and reports no use, the delete goes through.
	hung.hanging.Store(false)
	h.svc.peerTimeout = time.Second
	if err := h.svc.DeleteVersion(ctx, "alice", "app", v.ID); err != nil {
		t.Fatalf("delete after peer recovered: %v", err)
	}
}

func TestDeleteVersionRefusedWhenPeerIsOld(t *testing.T) {
	h := newHarness(t)
	h.create(t, "app")
	v := h.build(t, "app", "1", nil)
	old := newFakePeer(t, peerCID, "Host C")
	old.status = http.StatusNotFound
	h.withPeers(t, time.Second, old)
	err := h.svc.DeleteVersion(context.Background(), "alice", "app", v.ID)
	var inUse *InUseError
	if !errors.As(err, &inUse) || len(inUse.Unreachable) != 1 || inUse.Unreachable[0].Status != "old" {
		t.Fatalf("err = %v", err)
	}
}

func (f *fakeEngine) engineRemoved() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.removed...)
}

func TestOwnImagesOmitsFailedAndLogs(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.create(t, "app")
	h.create(t, "empty")
	h.build(t, "app", "1", nil)
	h.eng.buildErr = errors.New("boom")
	h.build(t, "app", "2", nil) // failed
	h.eng.buildErr = nil

	got, err := h.svc.OwnImages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Slug != "app" || len(got[0].Versions) != 1 || got[0].Versions[0].Tag != "1" {
		t.Fatalf("own images = %+v", got)
	}
	if got[0].Owner.ID != testInstanceID || len(got[0].PushTargets) != 0 || len(got[0].Pushes) != 0 {
		t.Fatalf("entry = %+v", got[0])
	}
	empty, err := h.svc.OwnUsage(ctx)
	if err != nil || empty == nil {
		t.Fatalf("usage = %v err %v (want empty non-nil)", empty, err)
	}
}

func TestOpenVersionTarStreamsSucceededOnly(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.create(t, "app")
	v := h.build(t, "app", "1", nil)
	h.eng.buildErr = errors.New("boom")
	h.build(t, "app", "2", nil)
	h.eng.buildErr = nil
	h.eng.saveData = "TARBYTES"

	rc, imageID, err := h.svc.OpenVersionTar(ctx, "app", "1")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "TARBYTES" || imageID != v.ImageDigest || imageID == "" {
		t.Fatalf("tar %q id %q (want %q)", b, imageID, v.ImageDigest)
	}
	if len(h.eng.saved) != 1 || h.eng.saved[0] != imageref.Format(testOwner, "app", "1") {
		t.Fatalf("saved = %v", h.eng.saved)
	}
	for _, tag := range []string{"2", "nope"} {
		if _, _, err := h.svc.OpenVersionTar(ctx, "app", tag); !errors.Is(err, ErrVersionNotFound) {
			t.Errorf("tag %s err = %v", tag, err)
		}
	}
	if _, _, err := h.svc.OpenVersionTar(ctx, "ghost", "1"); !errors.Is(err, ErrImageNotFound) {
		t.Errorf("unknown image err = %v", err)
	}
}

func TestFindOwner(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.withPeers(t, time.Second, newFakePeer(t, peerBID, "Host B"))
	self, ok, err := h.svc.FindOwner(ctx, testOwner)
	if err != nil || !ok || !self.Local || self.ID != testInstanceID {
		t.Fatalf("self = %+v ok %v err %v", self, ok, err)
	}
	p, ok, err := h.svc.FindOwner(ctx, imageref.OwnerID(peerBID))
	if err != nil || !ok || p.ID != peerBID || p.Local || p.Name != "Host B" {
		t.Fatalf("peer = %+v ok %v err %v", p, ok, err)
	}
	if _, ok, _ := h.svc.FindOwner(ctx, "ffffffffffff"); ok {
		t.Fatal("unknown owner found")
	}
}
