package api_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/api"
	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/imageref"
	"github.com/rcss-labs/stackstead/internal/images"
)

// TestImageSharedAcrossPeers runs two paired servers: A owns and builds an
// image, B lists it, fetches it, uses it in a stack and so blocks A's delete.
func TestImageSharedAcrossPeers(t *testing.T) {
	ctx := context.Background()
	libStack := &libStacks{fakeStacks: newFakeStacks()}
	a := newImagesNode(t, true)
	b := newImagesNodeWith(t, true, func(o *api.Options) {
		o.Stacks, o.Operations, o.FSRoots, o.Status = libStack, fakeOps{libStack.fakeStacks}, libStack.roots, libStack.status
	})
	pair(t, b.peerNode, a.peerNode)
	aInfo, err := a.svc.LocalServer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bInfo, err := b.svc.LocalServer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owner := imageref.OwnerID(aInfo.ID)
	ref := func(tag string) imageref.Ref { return imageref.Ref{Owner: owner, Slug: "web", Tag: tag} }

	if _, err := a.svc.Create(ctx, "admin", images.CreateRequest{Name: "web", Source: images.SourceUpload}); err != nil {
		t.Fatal(err)
	}
	a.build("web", "1")
	manifest, _ := json.Marshal([]map[string]any{{"RepoTags": []string{ref("1").String()}}})
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o644, Size: int64(len(manifest)), Typeflag: tar.TypeReg})
	tw.Write(manifest)
	tw.Close()
	a.eng.saveData = archive.String()

	// B lists A's image, owned by A.
	status, body := b.do("GET", "/api/images", "")
	var lib apitypes.Library
	if err := json.Unmarshal([]byte(body), &lib); err != nil || status != 200 {
		t.Fatalf("library = %d %s", status, body)
	}
	if len(lib.Images) != 1 || lib.Images[0].Slug != "web" || lib.Images[0].Owner.ID != aInfo.ID || lib.Images[0].Owner.Local {
		t.Fatalf("B's library = %s", body)
	}

	// A fetch for another architecture is refused before any transfer.
	b.eng.arch = "arm64"
	if status, body := b.do("POST", "/api/images/fetch", fetchBody(ref("1").String())); status != 409 || !strings.Contains(body, `"arch_mismatch"`) {
		t.Fatalf("arch = %d %s", status, body)
	}
	b.eng.arch = "amd64"

	// Fetching loads it into B's engine with the owner's recorded ID.
	b.eng.loadRef = ref("1").String()
	status, body = b.do("POST", "/api/images/fetch", fetchBody(ref("1").String()))
	if status != 202 {
		t.Fatalf("fetch = %d %s", status, body)
	}
	var op apitypes.Operation
	if err := json.Unmarshal([]byte(body), &op); err != nil || op.Action != "fetch" {
		t.Fatalf("operation = %s err %v", body, err)
	}
	waitOp(t, b.ops, op.ID)
	if done, err := b.ops.Get(op.ID); err != nil || done.State != "succeeded" {
		t.Fatalf("fetch op = %+v err %v", done, err)
	}
	in, err := b.eng.InspectImage(ctx, ref("1").String())
	if err != nil || in.ID != "sha256:abc" {
		t.Fatalf("B's engine has %+v err %v, want the owner's recorded ID", in, err)
	}
	if status, body := b.do("POST", "/api/images/fetch", fetchBody(ref("1").String())); status != 204 || body != "" {
		t.Fatalf("second fetch = %d %q, want 204", status, body)
	}

	// A stack on B uses web:1, so A cannot delete it.
	libStack.images = []string{ref("1").String()}
	b.usage.usages = []imageref.Usage{{Stack: "shop", Service: "app", Ref: ref("1")}}
	status, body = a.do("GET", "/api/images/web", "")
	var detail apitypes.LibraryImageDetail
	if err := json.Unmarshal([]byte(body), &detail); err != nil || status != 200 || len(detail.Versions) != 1 {
		t.Fatalf("A's image = %d %s", status, body)
	}
	v1 := "/api/images/web/versions/" + detail.Versions[0].ID
	status, body = a.do("DELETE", v1, "")
	var refused struct {
		Code        string                   `json:"code"`
		UsedBy      []apitypes.LibraryUse    `json:"usedBy"`
		Unreachable []apitypes.LibraryServer `json:"unreachable"`
	}
	if err := json.Unmarshal([]byte(body), &refused); err != nil || status != 409 || refused.Code != "image_in_use" {
		t.Fatalf("delete in use = %d %s", status, body)
	}
	if len(refused.UsedBy) != 1 || refused.UsedBy[0].Stack != "shop" || refused.UsedBy[0].Server != bInfo.ID || len(refused.Unreachable) != 0 {
		t.Fatalf("in-use body = %s", body)
	}

	// B's stack list flags the update once A builds web:2. B's peer library
	// is cached for a few seconds, so poll.
	a.build("web", "2")
	deadline := time.Now().Add(20 * time.Second)
	for {
		_, body := b.do("GET", "/api/stacks", "")
		var list []apitypes.StackSummary
		if err := json.Unmarshal([]byte(body), &list); err != nil {
			t.Fatalf("stacks = %s", body)
		}
		if len(list) == 1 && list[0].LibraryUpdates == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("B never flagged the library update: %s", body)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// A delete is refused when B cannot be asked, and the body names B.
	status, body = a.do("GET", "/api/images/web", "")
	if err := json.Unmarshal([]byte(body), &detail); err != nil || len(detail.Versions) != 2 {
		t.Fatalf("A's image = %d %s", status, body)
	}
	v2 := "/api/images/web/versions/" + detail.Versions[0].ID // newest first
	usage := func(answer func(http.ResponseWriter)) {
		h := func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Path != "/api/peer/images/usage" {
				return false
			}
			answer(w)
			return true
		}
		b.override.Store(&h)
	}
	usage(func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte("<html>bad gateway</html>"))
	})
	status, body = a.do("DELETE", v2, "")
	refused.Unreachable, refused.UsedBy = nil, nil
	if err := json.Unmarshal([]byte(body), &refused); err != nil || status != 409 || refused.Code != "image_in_use" {
		t.Fatalf("delete with B unreachable = %d %s", status, body)
	}
	if len(refused.Unreachable) != 1 || refused.Unreachable[0].ID != bInfo.ID || refused.Unreachable[0].Name != bInfo.Name {
		t.Fatalf("unreachable = %s", body)
	}
	status, body = a.do("GET", v2, "")
	var shown apitypes.LibraryVersionDetail
	if err := json.Unmarshal([]byte(body), &shown); err != nil || status != 200 {
		t.Fatalf("version vanished after a refused delete: %d %s", status, body)
	}
	if len(shown.Unreachable) != 1 || shown.Unreachable[0].ID != bInfo.ID || shown.Unreachable[0].Name != bInfo.Name {
		t.Fatalf("version detail unreachable = %s", body)
	}

	// A Stackstead 404 means B is an old server without image sharing: it
	// uses nothing and does not block the delete.
	usage(func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"not found"}`))
	})
	if status, body := a.do("DELETE", v2, ""); status != 204 {
		t.Fatalf("delete with an old peer = %d %s", status, body)
	}
}
