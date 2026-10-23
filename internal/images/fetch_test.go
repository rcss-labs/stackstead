package images

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/imageref"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

const tarPayload = "0123456789abcdefghij-tar-bytes"

type fetchRig struct {
	*harness
	peer *fakePeer
	ref  imageref.Ref
}

// newFetchRig is a consumer (arch amd64) with one paired owner "Host B"
// offering shared:2 (arch amd64, image ID sha256:2) over a real HTTP tar route.
func newFetchRig(t *testing.T) *fetchRig {
	t.Helper()
	h := newHarness(t)
	h.eng.arch = "amd64"
	peer := newFakePeer(t, peerBID, "Host B")
	peer.images = []apitypes.LibraryImageDetail{peerImage("shared", "2")}
	h.withPeers(t, time.Second, peer)
	ref := imageref.Ref{Owner: imageref.OwnerID(peerBID), Slug: "shared", Tag: "2"}
	h.eng.loadRef = ref.String()
	h.eng.inspectID = map[string]string{ref.String(): "sha256:2"}
	peer.tar = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-tar")
		w.Header().Set("X-Stackstead-Image-ID", "sha256:2")
		io.WriteString(w, tarPayload)
	}
	return &fetchRig{harness: h, peer: peer, ref: ref}
}

func TestFetchLoadsAndVerifies(t *testing.T) {
	r := newFetchRig(t)
	var gotUser, gotCred string
	r.peer.tar = func(w http.ResponseWriter, req *http.Request) {
		gotUser, gotCred = req.Header.Get("X-Stackstead-Peer-User"), req.Header.Get("X-Stackstead-Peer")
		w.Header().Set("Content-Type", "application/x-tar")
		w.Header().Set("X-Stackstead-Image-ID", "sha256:2")
		io.WriteString(w, tarPayload)
	}
	op, started, err := r.svc.Fetch(context.Background(), "alice", r.ref)
	if err != nil || !started {
		t.Fatalf("Fetch = %v started=%v", err, started)
	}
	if op.Action != "fetch" {
		t.Errorf("action = %q", op.Action)
	}
	if done := r.waitOp(t, op.ID); done.State != stacks.OpSucceeded {
		t.Fatalf("op = %+v", done)
	}
	if string(r.eng.loaded) != tarPayload {
		t.Fatalf("engine received %q", r.eng.loaded)
	}
	if gotUser != "alice" || gotCred == "" {
		t.Errorf("user %q credential-present %v", gotUser, gotCred != "")
	}
	e, ok := r.log.last("image.fetch")
	if !ok || e.Scope != "shared" || e.Detail != "Host B 30" {
		t.Fatalf("log = %+v ok=%v", e, ok)
	}
	if strings.Contains(e.Detail, peerCredential) {
		t.Fatal("credential in log")
	}
	// Present with the recorded ID now: nothing to do and no second request.
	if _, started, err := r.svc.Fetch(context.Background(), "alice", r.ref); err != nil || started {
		t.Fatalf("second Fetch started=%v err=%v", started, err)
	}
	if n := r.peer.tarHits.Load(); n != 1 {
		t.Fatalf("tar requests = %d", n)
	}
}

func TestFetchOwnRefNeedsNothing(t *testing.T) {
	r := newFetchRig(t)
	own := imageref.Ref{Owner: testOwner, Slug: "mine", Tag: "1"}
	if _, started, err := r.svc.Fetch(context.Background(), "alice", own); err != nil || started {
		t.Fatalf("started=%v err=%v", started, err)
	}
}

func TestFetchVerifiesImageID(t *testing.T) {
	r := newFetchRig(t)
	r.eng.inspectID[r.ref.String()] = "sha256:other" // what docker load produced
	op, started, err := r.svc.Fetch(context.Background(), "alice", r.ref)
	if err != nil || !started {
		t.Fatalf("Fetch = %v started=%v", err, started)
	}
	done := r.waitOp(t, op.ID)
	if done.State != stacks.OpFailed || done.Error != "fetched image does not match the owner's record" {
		t.Fatalf("op = %+v", done)
	}
	if len(r.eng.removed) != 1 || r.eng.removed[0] != r.ref.String() {
		t.Fatalf("removed = %v, want the fetched ref untagged", r.eng.removed)
	}
	if e, _ := r.log.last("image.fetch"); !strings.Contains(e.Result, "failed") {
		t.Fatalf("log = %+v", e)
	}
}

func TestFetchIdleWatchdog(t *testing.T) {
	r := newFetchRig(t)
	r.svc.fetchIdle = 50 * time.Millisecond
	r.peer.tar = func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/x-tar")
		w.Header().Set("X-Stackstead-Image-ID", "sha256:2")
		io.WriteString(w, "0123456789")
		w.(http.Flusher).Flush()
		<-req.Context().Done() // the owner stalls
	}
	start := time.Now()
	op, started, err := r.svc.Fetch(context.Background(), "alice", r.ref)
	if err != nil || !started {
		t.Fatalf("Fetch = %v started=%v", err, started)
	}
	done := r.waitOp(t, op.ID)
	if done.State != stacks.OpFailed || done.Error != "transfer from Host B stalled" {
		t.Fatalf("op = %+v", done)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
}

func TestFetchRefusesArchMismatch(t *testing.T) {
	r := newFetchRig(t)
	r.peer.images[0].Versions[0].Arch = "arm64"
	_, started, err := r.svc.Fetch(context.Background(), "alice", r.ref)
	var am *ArchMismatchError
	if !errors.As(err, &am) || am.Built != "arm64" || am.Local != "amd64" || started {
		t.Fatalf("err = %v started=%v", err, started)
	}
	if !errors.Is(err, ErrArchMismatch) || err.Error() != "built for arm64; this server is amd64" {
		t.Fatalf("err = %q", err)
	}
	if n := r.peer.tarHits.Load(); n != 0 {
		t.Fatalf("%d tar requests were sent", n)
	}
}

func TestFetchRefusesUnknownOwnerAndVersion(t *testing.T) {
	r := newFetchRig(t)
	_, _, err := r.svc.Fetch(context.Background(), "alice", imageref.Ref{Owner: "999999999999", Slug: "shared", Tag: "2"})
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Fields[0].Field != "ref" {
		t.Fatalf("unknown owner err = %v", err)
	}
	_, _, err = r.svc.Fetch(context.Background(), "alice", imageref.Ref{Owner: r.ref.Owner, Slug: "shared", Tag: "9"})
	if !errors.Is(err, ErrImageNotBuilt) {
		t.Fatalf("unknown version err = %v", err)
	}
}

func TestEnsureLocalSkipsWhenPresent(t *testing.T) {
	r := newFetchRig(t)
	r.eng.present[r.ref.String()] = true
	if err := r.svc.EnsureLocal(context.Background(), r.ref, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if n := r.peer.tarHits.Load(); n != 0 {
		t.Fatalf("%d tar requests", n)
	}
}

func TestEnsureLocalSelfOwnedMissing(t *testing.T) {
	r := newFetchRig(t)
	own := imageref.Ref{Owner: testOwner, Slug: "mine", Tag: "1"}
	if err := r.svc.EnsureLocal(context.Background(), own, func(string) {}); !errors.Is(err, ErrImageNotBuilt) {
		t.Fatalf("err = %v", err)
	}
}

func TestEnsureLocalFetchesFromOwner(t *testing.T) {
	r := newFetchRig(t)
	r.svc.fetchProgress = 10 * time.Millisecond
	r.peer.tar = func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/x-tar")
		w.Header().Set("X-Stackstead-Image-ID", "sha256:2")
		io.WriteString(w, tarPayload[:10])
		w.(http.Flusher).Flush()
		time.Sleep(80 * time.Millisecond)
		io.WriteString(w, tarPayload[10:])
	}
	// The caller holds the image's operations key, as a stack `up` would hold its own.
	release := make(chan struct{})
	if _, err := r.ops.StartTimeout(OpKey(r.ref.Owner, r.ref.Slug), "fetch", time.Minute, func(ctx context.Context, _ func(string)) error {
		<-release
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	defer close(release)

	var lines []string
	if err := r.svc.EnsureLocal(context.Background(), r.ref, func(l string) { lines = append(lines, l) }); err != nil {
		t.Fatal(err)
	}
	if string(r.eng.loaded) != tarPayload {
		t.Fatalf("engine received %q", r.eng.loaded)
	}
	var progress bool
	for _, l := range lines {
		progress = progress || strings.HasPrefix(l, "fetching "+r.ref.String()+" from Host B (")
	}
	if !progress || lines[len(lines)-1] != "loaded "+r.ref.String() {
		t.Fatalf("lines = %q", lines)
	}
	if e, ok := r.log.last("image.fetch"); !ok || e.Detail != "Host B 30" {
		t.Fatalf("log = %+v", e)
	}
}

func TestEnsureLocalRefusesArchMismatch(t *testing.T) {
	r := newFetchRig(t)
	r.eng.arch = "arm64"
	var am *ArchMismatchError
	if err := r.svc.EnsureLocal(context.Background(), r.ref, func(string) {}); !errors.As(err, &am) || am.Built != "amd64" {
		t.Fatalf("err = %v", err)
	}
	if r.peer.tarHits.Load() != 0 {
		t.Fatal("a request was sent")
	}
}

// A slow stream that keeps making progress is not a stall: every completed
// Read re-arms the watchdog.
func TestFetchIdleWatchdogAllowsSteadyProgress(t *testing.T) {
	r := newFetchRig(t)
	r.svc.fetchIdle = 100 * time.Millisecond
	r.peer.tar = func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/x-tar")
		w.Header().Set("X-Stackstead-Image-ID", "sha256:2")
		for i := 0; i < 6; i++ {
			io.WriteString(w, "chunk")
			w.(http.Flusher).Flush()
			time.Sleep(40 * time.Millisecond)
		}
	}
	op, _, err := r.svc.Fetch(context.Background(), "alice", r.ref)
	if err != nil {
		t.Fatal(err)
	}
	if done := r.waitOp(t, op.ID); done.State != stacks.OpSucceeded {
		t.Fatalf("op = %+v", done)
	}
}
