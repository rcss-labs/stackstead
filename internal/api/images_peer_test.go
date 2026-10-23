package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/rcss-labs/stackstead/internal/api"
	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/auth"
	"github.com/rcss-labs/stackstead/internal/images"
	"github.com/rcss-labs/stackstead/internal/peers"
	"github.com/rcss-labs/stackstead/internal/stacks"
	"github.com/rcss-labs/stackstead/internal/store"
)

// imagesPeerNode is a real server with the image library and peer routes,
// paired with a plain peer whose credential the tests use.
type imagesPeerNode struct {
	*peerNode
	svc        *images.Service
	eng        *imgEngine
	ops        *stacks.Ops
	credential string
}

func newImagesPeerNode(t *testing.T) *imagesPeerNode {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "imgpeer.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	authSvc, err := auth.NewService(db, time.Now, auth.WithFastHashingForTests())
	if err != nil {
		t.Fatal(err)
	}
	if err := authSvc.CreateAdmin(context.Background(), "admin", adminPassword); err != nil {
		t.Fatal(err)
	}
	st := peers.NewStore(db, time.Now)
	psvc := peers.NewService(peers.ServiceConfig{
		Store: st, Version: "9.9.9", Logger: slog.New(slog.DiscardHandler), Timeout: 3 * time.Second,
		Hostname: func() (string, error) { return "host-a", nil },
	})
	n := &imagesPeerNode{eng: &imgEngine{present: map[string]bool{}}}
	n.ops = stacks.NewOps(time.Minute, time.Now)
	t.Cleanup(func() { n.ops.Shutdown(context.Background()) })
	n.svc = images.NewService(images.Deps{
		Store: images.NewStore(db, time.Now), Ops: n.ops, Engine: n.eng, Dir: t.TempDir(),
		Limits: images.Limits{MaxFileBytes: 1 << 20, MaxContextBytes: 2 << 20}, OpTimeout: time.Minute,
		Usage: &imgUsage{}, Now: time.Now,
		Self: func(context.Context) (images.Self, error) {
			return images.Self{InstanceID: imgInstanceID, Name: "Host A", Slug: "host-a"}, nil
		},
	})
	handler := api.New(api.Options{
		Auth: authSvc, Limiter: auth.NewLoginLimiter(5, time.Minute, time.Now), Peers: psvc, Images: n.svc,
		Static: fstest.MapFS{"index.html": {Data: []byte("<html></html>")}}, Logger: slog.New(slog.DiscardHandler),
	})
	n.peerNode = &peerNode{t: t, name: "host-a", svc: psvc, store: st}
	n.srv = httptest.NewServer(handler)
	t.Cleanup(n.srv.Close)
	n.login()

	other := newPeerNode(t, "host-b")
	other.login()
	pair(t, other, n.peerNode)
	list, err := other.store.Peers(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("peers = %v err %v", list, err)
	}
	n.credential = list[0].OutCredential.Reveal()
	return n
}

func (n *imagesPeerNode) get(path string, headers ...string) (*http.Response, string) {
	n.t.Helper()
	req, _ := http.NewRequest("GET", n.url()+path, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		n.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func (n *imagesPeerNode) asPeer(path string) (*http.Response, string) {
	return n.get(path, peers.PeerHeader, n.credential)
}

func (n *imagesPeerNode) build(slug, tag string) {
	n.t.Helper()
	_, op, err := n.svc.Build(context.Background(), "admin", slug, images.BuildRequest{Tag: tag, BuildArgs: map[string]string{}})
	if err != nil {
		n.t.Fatal(err)
	}
	waitOp(n.t, n.ops, op.ID)
}

func TestPeerImagesListRequiresCredential(t *testing.T) {
	n := newImagesPeerNode(t)
	for _, path := range []string{"/api/peer/images", "/api/peer/images/usage", "/api/peer/images/app/versions/1/tar"} {
		for _, headers := range [][]string{nil, {peers.PeerHeader, "ssp_wrong"}} {
			resp, body := n.get(path, headers...)
			if resp.StatusCode != 401 || !strings.Contains(body, `"peer_not_paired"`) {
				t.Errorf("%s %v = %d %s", path, headers, resp.StatusCode, body)
			}
		}
	}
	// The operator's session never reaches the peer routes.
	req, _ := http.NewRequest("GET", n.url()+"/api/peer/images", nil)
	req.AddCookie(n.cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("cookie only = %d", resp.StatusCode)
	}
}

func TestPeerImagesListShape(t *testing.T) {
	n := newImagesPeerNode(t)
	resp, body := n.asPeer("/api/peer/images")
	if resp.StatusCode != 200 || strings.TrimSpace(body) != "[]" {
		t.Fatalf("empty list = %d %q", resp.StatusCode, body)
	}
	if resp, body = n.asPeer("/api/peer/images/usage"); resp.StatusCode != 200 || strings.TrimSpace(body) != "[]" {
		t.Fatalf("empty usage = %d %q", resp.StatusCode, body)
	}

	if _, err := n.svc.Create(context.Background(), "admin", images.CreateRequest{Name: "app", Source: images.SourceUpload, BuildArgs: map[string]string{"A": "1"}}); err != nil {
		t.Fatal(err)
	}
	n.build("app", "1")
	n.eng.failBuild = true
	n.build("app", "2")
	n.eng.failBuild = false

	resp, body = n.asPeer("/api/peer/images")
	if resp.StatusCode != 200 {
		t.Fatalf("list = %d %s", resp.StatusCode, body)
	}
	var list []apitypes.LibraryImageDetail
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Slug != "app" || list[0].Owner.ID != imgInstanceID || list[0].Owner.Name != "Host A" {
		t.Fatalf("list = %+v", list)
	}
	if len(list[0].Versions) != 1 || list[0].Versions[0].Tag != "1" || list[0].Versions[0].Status != "succeeded" {
		t.Fatalf("versions = %+v", list[0].Versions)
	}
	if strings.Contains(body, `"log"`) || strings.Contains(body, "build failed") || list[0].PushTargets == nil || len(list[0].PushTargets) != 0 {
		t.Fatalf("body leaks private data: %s", body)
	}
}

func TestPeerTarStreamsAndHeader(t *testing.T) {
	n := newImagesPeerNode(t)
	if _, err := n.svc.Create(context.Background(), "admin", images.CreateRequest{Name: "app", Source: images.SourceUpload}); err != nil {
		t.Fatal(err)
	}
	n.build("app", "1")
	n.eng.failBuild = true
	n.build("app", "2")
	n.eng.failBuild = false
	n.eng.saveData = "FAKE-TAR-BYTES"

	resp, body := n.asPeer("/api/peer/images/app/versions/1/tar")
	if resp.StatusCode != 200 || body != "FAKE-TAR-BYTES" {
		t.Fatalf("tar = %d %q", resp.StatusCode, body)
	}
	if resp.Header.Get("Content-Type") != "application/x-tar" || resp.Header.Get("X-Stackstead-Image-ID") != "sha256:abc" {
		t.Fatalf("headers = %v", resp.Header)
	}
	for _, path := range []string{"/api/peer/images/app/versions/2/tar", "/api/peer/images/app/versions/9/tar", "/api/peer/images/ghost/versions/1/tar"} {
		if resp, body := n.asPeer(path); resp.StatusCode != 404 {
			t.Errorf("%s = %d %s", path, resp.StatusCode, body)
		}
	}
}
