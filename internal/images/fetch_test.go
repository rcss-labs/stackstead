package images

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/imageref"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

// tarPayload is a valid docker-save style archive naming exactly the rig's ref.
var tarPayload = string(archiveFor(rigRef, rigRef))

const rigRef = "stackstead.invalid/bbbbbbbbbbbb/shared:2"

type archiveFile struct {
	name, content string
	typ           byte   // 0 = regular file
	link          string // target of a symlink or hard link
}

// archiveFor builds a tar with a layer, then manifest.json naming tags last,
// as docker save orders it.
func archiveFor(_ string, tags ...string) []byte {
	return buildArchive(archiveFile{name: "layer.tar", content: strings.Repeat("L", 4096)}, archiveFile{name: "manifest.json", content: manifestJSON(tags...)})
}

func manifestJSON(tags ...string) string {
	b, _ := json.Marshal([]map[string]any{{"Config": "c.json", "RepoTags": tags, "Layers": []string{"layer.tar"}}})
	return string(b)
}

func buildArchive(files ...archiveFile) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range files {
		h := &tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.content)), Typeflag: tar.TypeReg}
		if f.typ != 0 {
			h.Typeflag, h.Linkname, h.Size = f.typ, f.link, 0
		}
		tw.WriteHeader(h)
		tw.Write([]byte(f.content))
	}
	tw.Close()
	return buf.Bytes()
}

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
		w.Write([]byte(tarPayload))
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
		w.Write([]byte(tarPayload))
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
	if !ok || e.Scope != "shared" || e.Detail != fmt.Sprintf("Host B %d", len(tarPayload)) {
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
		w.Write([]byte(tarPayload[:10]))
		w.(http.Flusher).Flush()
		time.Sleep(80 * time.Millisecond)
		w.Write([]byte(tarPayload[10:]))
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
	if e, ok := r.log.last("image.fetch"); !ok || e.Detail != fmt.Sprintf("Host B %d", len(tarPayload)) {
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
		step := len(tarPayload)/6 + 1
		for off := 0; off < len(tarPayload); off += step {
			w.Write([]byte(tarPayload[off:min(off+step, len(tarPayload))]))
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

// dockerLikeLoad reads like docker does: tar entries until the end-of-archive
// blocks, never the stream's own EOF, then tags the image.
func (r *fetchRig) dockerLikeLoad(result *error) {
	r.eng.loadFn = func(ctx context.Context, in io.Reader) error {
		tr := tar.NewReader(bufio.NewReaderSize(in, 64<<10)) // real docker reads in large chunks
		for {
			_, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				*result = err
				return err
			}
			if _, err := io.Copy(io.Discard, tr); err != nil {
				*result = err
				return err
			}
		}
		r.eng.mu.Lock()
		r.eng.present[r.ref.String()] = true // tagging happens here
		r.eng.mu.Unlock()
		return nil
	}
}

func (r *fetchRig) serve(archive []byte) {
	r.peer.tar = func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/x-tar")
		w.Header().Set("X-Stackstead-Image-ID", "sha256:2")
		w.Write(archive)
		w.(http.Flusher).Flush()
		time.Sleep(80 * time.Millisecond) // the stream's EOF comes well after the archive's last block
	}
}

func TestFetchRejectsForeignTags(t *testing.T) {
	own := rigRef
	index := func(ann map[string]string) string {
		b, _ := json.Marshal(map[string]any{"manifests": []map[string]any{{"annotations": ann}}})
		return string(b)
	}
	layer := archiveFile{name: "layer.tar", content: strings.Repeat("L", 4096)}
	cases := map[string][]byte{
		"manifest extra tag":    buildArchive(layer, archiveFile{name: "manifest.json", content: manifestJSON(own, "nginx:latest")}),
		"manifest only foreign": buildArchive(layer, archiveFile{name: "manifest.json", content: manifestJSON("nginx:latest")}),
		"repositories": buildArchive(layer, archiveFile{name: "manifest.json", content: manifestJSON(own)},
			archiveFile{name: "repositories", content: `{"nginx":{"latest":"abc"}}`}),
		"index containerd name": buildArchive(layer, archiveFile{name: "index.json", content: index(map[string]string{"io.containerd.image.name": "docker.io/library/nginx:latest"})}),
		"index ref name":        buildArchive(layer, archiveFile{name: "index.json", content: index(map[string]string{"org.opencontainers.image.ref.name": "docker.io/library/nginx:latest"})}),
		"metadata too large":    buildArchive(layer, archiveFile{name: "manifest.json", content: manifestJSON(own) + strings.Repeat(" ", 2<<20)}),
		"not a tar":             []byte(strings.Repeat("\x1f\x8b not a tar ", 200)),
	}
	// A slow check proves the chunk carrying the entry's end is withheld until judged.
	tarCheckHook = func() { time.Sleep(30 * time.Millisecond) }
	t.Cleanup(func() { tarCheckHook = nil })
	for name, archive := range cases {
		t.Run(name, func(t *testing.T) {
			r := newFetchRig(t)
			var loadErr error
			r.dockerLikeLoad(&loadErr)
			r.serve(archive)
			op, _, err := r.svc.Fetch(context.Background(), "alice", r.ref)
			if err != nil {
				t.Fatal(err)
			}
			done := r.waitOp(t, op.ID)
			if done.State != stacks.OpFailed {
				t.Fatalf("op = %+v", done)
			}
			if name != "not a tar" && name != "metadata too large" && done.Error != "image archive names other tags" {
				t.Errorf("error = %q", done.Error)
			}
			if loadErr == nil {
				t.Error("load saw a clean archive and may have tagged it")
			}
			if r.eng.present[r.ref.String()] {
				t.Error("image was tagged")
			}
		})
	}
}

func TestFetchAcceptsOwnTagInEveryMetadataFile(t *testing.T) {
	r := newFetchRig(t)
	idx, _ := json.Marshal(map[string]any{"manifests": []map[string]any{{"annotations": map[string]string{
		"io.containerd.image.name": rigRef, "org.opencontainers.image.ref.name": "2"}}}})
	r.serve(buildArchive(archiveFile{name: "layer.tar", content: "L"}, archiveFile{name: "index.json", content: string(idx)},
		archiveFile{name: "manifest.json", content: manifestJSON(rigRef)}, archiveFile{name: "repositories", content: `{"stackstead.invalid/bbbbbbbbbbbb/shared":{"2":"abc"}}`}))
	var loadErr error
	r.dockerLikeLoad(&loadErr)
	op, _, err := r.svc.Fetch(context.Background(), "alice", r.ref)
	if err != nil {
		t.Fatal(err)
	}
	if done := r.waitOp(t, op.ID); done.State != stacks.OpSucceeded {
		t.Fatalf("op = %+v", done)
	}
}

func TestFetchWatchdogStopsAfterEOF(t *testing.T) {
	r := newFetchRig(t)
	r.svc.fetchIdle = 50 * time.Millisecond
	r.eng.loadFn = func(ctx context.Context, in io.Reader) error {
		if _, err := io.Copy(io.Discard, in); err != nil {
			return err
		}
		select { // docker unpacking after the body ended; the request dies with ctx
		case <-time.After(200 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
		r.eng.mu.Lock()
		r.eng.present[r.ref.String()] = true
		r.eng.mu.Unlock()
		return nil
	}
	op, _, err := r.svc.Fetch(context.Background(), "alice", r.ref)
	if err != nil {
		t.Fatal(err)
	}
	if done := r.waitOp(t, op.ID); done.State != stacks.OpSucceeded {
		t.Fatalf("op = %+v", done)
	}
}

func TestFetchSharesOneTransferPerRef(t *testing.T) {
	r := newFetchRig(t)
	r.peer.tar = func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/x-tar")
		w.Header().Set("X-Stackstead-Image-ID", "sha256:2")
		w.Write([]byte(tarPayload[:10]))
		w.(http.Flusher).Flush()
		time.Sleep(150 * time.Millisecond)
		w.Write([]byte(tarPayload[10:]))
	}
	errs := make(chan error, 3)
	for i := 0; i < 3; i++ {
		go func() { errs <- r.svc.EnsureLocal(context.Background(), r.ref, func(string) {}) }()
	}
	for i := 0; i < 3; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if n := r.peer.tarHits.Load(); n != 1 {
		t.Fatalf("tar requests = %d, want 1 shared transfer", n)
	}
}

func TestFetchRefusesBadTransferHeaders(t *testing.T) {
	type tc struct {
		name   string
		serve  func(w http.ResponseWriter)
		errMsg string
	}
	cases := []tc{
		{"wrong content type", func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/html")
			w.Header().Set("X-Stackstead-Image-ID", "sha256:2")
			w.Write([]byte(tarPayload))
		}, "did not send an image"},
		{"missing image id", func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/x-tar")
			w.Write([]byte(tarPayload))
		}, "fetched image does not match the owner's record"},
		{"id differs from the library record", func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/x-tar")
			w.Header().Set("X-Stackstead-Image-ID", "sha256:else")
			w.Write([]byte(tarPayload))
		}, "fetched image does not match the owner's record"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newFetchRig(t)
			r.peer.tar = func(w http.ResponseWriter, _ *http.Request) { c.serve(w) }
			op, _, err := r.svc.Fetch(context.Background(), "alice", r.ref)
			if err != nil {
				t.Fatal(err)
			}
			done := r.waitOp(t, op.ID)
			if done.State != stacks.OpFailed || !strings.Contains(done.Error, c.errMsg) {
				t.Fatalf("op = %+v", done)
			}
			if len(r.eng.loaded) != 0 || r.eng.present[r.ref.String()] {
				t.Fatal("image was loaded")
			}
		})
	}
}

func TestFetchRefusesVersionWithoutArch(t *testing.T) {
	r := newFetchRig(t)
	r.peer.images[0].Versions[0].Arch = ""
	_, _, err := r.svc.Fetch(context.Background(), "alice", r.ref)
	if err == nil || !strings.Contains(err.Error(), "did not record an architecture") || r.peer.tarHits.Load() != 0 {
		t.Fatalf("err = %v", err)
	}
}

// The chunk that completes a metadata entry is judged before docker sees it:
// docker stops at the archive's end blocks and never needs the stream's EOF.
func TestTarInspectorWithholdsChunkWithForeignTag(t *testing.T) {
	tarCheckHook = func() { time.Sleep(30 * time.Millisecond) }
	defer func() { tarCheckHook = nil }()
	// The manifest is exactly one block, so a chunk can end where its content ends.
	manifest := manifestJSON(rigRef, "nginx:latest")
	manifest += strings.Repeat(" ", 512-len(manifest))
	archive := buildArchive(archiveFile{name: "layer.tar", content: "L"}, archiveFile{name: "manifest.json", content: manifest})
	cut := len(archive) - 1024 // the two end-of-archive blocks arrive later
	insp := newTarInspector(io.MultiReader(bytes.NewReader(archive[:cut]), bytes.NewReader(archive[cut:])), rigRef)
	defer insp.close()
	n, err := insp.Read(make([]byte, 64<<10))
	if n != 0 || !errors.Is(err, errForeignTags) {
		t.Fatalf("first read = %d, %v", n, err)
	}

	own := newTarInspector(bytes.NewReader(buildArchive(archiveFile{name: "manifest.json", content: manifestJSON(rigRef)})), rigRef)
	defer own.close()
	got, err := io.ReadAll(own)
	if err != nil || len(got) == 0 {
		t.Fatalf("own archive: %d bytes, %v", len(got), err)
	}
}

func inspectAll(archive []byte) error {
	insp := newTarInspector(bytes.NewReader(archive), rigRef)
	defer insp.close()
	_, err := io.ReadAll(insp)
	return err
}

// Each archive tries to hide a foreign tag from, or smuggle one past, the inspector.
func TestTarInspectorRejectsMetadataBypasses(t *testing.T) {
	foreign := manifestJSON(rigRef, "nginx:latest")
	layer := archiveFile{name: "layer.tar", content: "L"}
	idx := func(ann map[string]string) string {
		b, _ := json.Marshal(map[string]any{"manifests": []map[string]any{{"annotations": ann}}})
		return string(b)
	}
	cases := map[string][]archiveFile{
		"absolute manifest name":       {layer, {name: "/manifest.json", content: foreign}},
		"double slash manifest name":   {layer, {name: "//manifest.json", content: foreign}},
		"dotdot-cleaned manifest name": {layer, {name: "a/../manifest.json", content: foreign}},
		"absolute repositories":        {layer, {name: "/repositories", content: `{"nginx":{"latest":"a"}}`}},
		"manifest symlink":             {layer, {name: "manifest.json", typ: tar.TypeSymlink, link: "layer.tar"}},
		"manifest hard link":           {layer, {name: "manifest.json", typ: tar.TypeLink, link: "layer.tar"}},
		"manifest directory":           {layer, {name: "manifest.json/", typ: tar.TypeDir}},
		"index symlink":                {layer, {name: "index.json", typ: tar.TypeSymlink, link: "layer.tar"}},
		"nested manifest":              {layer, {name: "sub/manifest.json", content: foreign}},
		"nested repositories":          {layer, {name: "blobs/repositories", content: "{}"}},
		"nested index":                 {layer, {name: "x/y/index.json", content: "{}"}},
		"symlink to dot":               {layer, {name: "d", typ: tar.TypeSymlink, link: "."}},
		"symlink to dotdot":            {layer, {name: "d", typ: tar.TypeSymlink, link: ".."}},
		"symlink absolute":             {layer, {name: "d", typ: tar.TypeSymlink, link: "/"}},
		"symlink absolute path":        {layer, {name: "d", typ: tar.TypeSymlink, link: "/etc"}},
		"symlink trailing slash":       {layer, {name: "d", typ: tar.TypeSymlink, link: "layer/"}},
		"symlink resolves to root":     {layer, {name: "a/b", typ: tar.TypeSymlink, link: ".."}},
		"symlink resolves to root 2":   {layer, {name: "x", typ: tar.TypeSymlink, link: "d/.."}},
		"symlink escapes root":         {layer, {name: "a/l", typ: tar.TypeSymlink, link: "../../x"}},
		"hard link to root":            {layer, {name: "h", typ: tar.TypeLink, link: "."}},
		"hard link escapes":            {layer, {name: "h", typ: tar.TypeLink, link: "../x"}},
		"index bare ref name only":     {layer, {name: "index.json", content: idx(map[string]string{"org.opencontainers.image.ref.name": "2"})}},
		"index bare ref name, foreign": {layer, {name: "index.json", content: idx(map[string]string{"io.containerd.image.name": "nginx:latest", "org.opencontainers.image.ref.name": "2"})}},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			if err := inspectAll(buildArchive(files...)); err == nil {
				t.Fatal("archive passed the inspector")
			}
		})
	}
}

// docker save shares layers between images with relative links; those stay legal.
func TestTarInspectorAcceptsDockerSaveLayout(t *testing.T) {
	idx, _ := json.Marshal(map[string]any{"manifests": []map[string]any{{"annotations": map[string]string{
		"io.containerd.image.name": rigRef, "org.opencontainers.image.ref.name": "2"}}}})
	archive := buildArchive(
		archiveFile{name: "abc/", typ: tar.TypeDir},
		archiveFile{name: "abc/VERSION", content: "1.0"},
		archiveFile{name: "abc/json", content: "{}"},
		archiveFile{name: "abc/layer.tar", content: "LAYER"},
		archiveFile{name: "def/", typ: tar.TypeDir},
		archiveFile{name: "def/layer.tar", typ: tar.TypeSymlink, link: "../abc/layer.tar"},
		archiveFile{name: "ghi/layer.tar", typ: tar.TypeLink, link: "abc/layer.tar"},
		archiveFile{name: "blobs/sha256/aaa", content: "blob"},
		archiveFile{name: "index.json", content: string(idx)},
		archiveFile{name: "manifest.json", content: manifestJSON(rigRef)},
		archiveFile{name: "./repositories", content: `{"stackstead.invalid/bbbbbbbbbbbb/shared":{"2":"abc"}}`},
	)
	if err := inspectAll(archive); err != nil {
		t.Fatalf("docker save layout rejected: %v", err)
	}
}

func TestFetchSharedTransferSurvivesLeaderCancel(t *testing.T) {
	r := newFetchRig(t)
	release := make(chan struct{})
	r.peer.tar = func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/x-tar")
		w.Header().Set("X-Stackstead-Image-ID", "sha256:2")
		<-release
		w.Write([]byte(tarPayload))
	}
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leader := make(chan error, 1)
	go func() { leader <- r.svc.EnsureLocal(leaderCtx, r.ref, func(string) {}) }()
	for r.peer.tarHits.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	waiter := make(chan error, 1)
	go func() { waiter <- r.svc.EnsureLocal(context.Background(), r.ref, func(string) {}) }()
	time.Sleep(50 * time.Millisecond)
	cancelLeader()
	if err := <-leader; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader err = %v, want its own cancellation", err)
	}
	close(release)
	if err := <-waiter; err != nil {
		t.Fatalf("waiter err = %v", err)
	}
	if n := r.peer.tarHits.Load(); n != 1 {
		t.Fatalf("tar requests = %d", n)
	}
	if !r.eng.present[r.ref.String()] {
		t.Fatal("image not loaded")
	}
}

func TestFetchSharedFailureReachesEveryWaiter(t *testing.T) {
	r := newFetchRig(t)
	r.peer.tar = func(w http.ResponseWriter, req *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.Header().Set("Content-Type", "application/x-tar")
		w.Header().Set("X-Stackstead-Image-ID", "sha256:else")
	}
	errs := make(chan error, 3)
	for i := 0; i < 3; i++ {
		go func() { errs <- r.svc.EnsureLocal(context.Background(), r.ref, func(string) {}) }()
	}
	for i := 0; i < 3; i++ {
		if err := <-errs; err == nil || !strings.Contains(err.Error(), "does not match the owner's record") {
			t.Fatalf("err = %v", err)
		}
	}
	if n := r.peer.tarHits.Load(); n != 1 {
		t.Fatalf("tar requests = %d", n)
	}
}

func TestFetchSharedPanicBecomesError(t *testing.T) {
	r := newFetchRig(t)
	r.eng.loadFn = func(context.Context, io.Reader) error { panic("boom") }
	r.peer.tar = func(w http.ResponseWriter, req *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.Header().Set("Content-Type", "application/x-tar")
		w.Header().Set("X-Stackstead-Image-ID", "sha256:2")
		w.Write([]byte(tarPayload))
	}
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { errs <- r.svc.EnsureLocal(context.Background(), r.ref, func(string) {}) }()
	}
	for i := 0; i < 2; i++ {
		if err := <-errs; err == nil || !strings.Contains(err.Error(), "panicked") {
			t.Fatalf("err = %v", err)
		}
	}
}

func TestFetchPanicStopsProgressAndLogsFailure(t *testing.T) {
	r := newFetchRig(t)
	r.svc.fetchProgress = 5 * time.Millisecond
	r.eng.loadFn = func(context.Context, io.Reader) error {
		time.Sleep(40 * time.Millisecond) // long enough for progress lines
		panic("boom")
	}
	var mu sync.Mutex
	var lines []string
	count := func() int { mu.Lock(); defer mu.Unlock(); return len(lines) }
	err := r.svc.EnsureLocal(context.Background(), r.ref, func(l string) { mu.Lock(); lines = append(lines, l); mu.Unlock() })
	if err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("err = %v", err)
	}
	if e, ok := r.log.last("image.fetch"); !ok || e.Result != "failed" {
		t.Fatalf("image.fetch log = %+v ok=%v", e, ok)
	}
	before := count()
	time.Sleep(60 * time.Millisecond)
	if after := count(); after != before {
		t.Fatalf("progress goroutine still running after the panic: %d -> %d lines", before, after)
	}
}

// lineCollector gathers progress lines and lets a test wait for one.
type lineCollector struct {
	mu    sync.Mutex
	lines []string
	seen  chan string
}

func newLineCollector() *lineCollector { return &lineCollector{seen: make(chan string, 64)} }

func (c *lineCollector) add(l string) {
	c.mu.Lock()
	c.lines = append(c.lines, l)
	c.mu.Unlock()
	select {
	case c.seen <- l:
	default:
	}
}

func (c *lineCollector) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.lines...)
}

func (c *lineCollector) has(line string) bool {
	for _, l := range c.all() {
		if l == line {
			return true
		}
	}
	return false
}

// gatedTransfer makes the peer's tar route announce its start and then wait
// for release before it sends the archive.
func (r *fetchRig) gatedTransfer() (started <-chan struct{}, release func()) {
	in, gate := make(chan struct{}), make(chan struct{})
	var once sync.Once
	r.peer.tar = func(w http.ResponseWriter, req *http.Request) {
		once.Do(func() { close(in) })
		w.Header().Set("Content-Type", "application/x-tar")
		w.Header().Set("X-Stackstead-Image-ID", "sha256:2")
		<-gate
		w.Write([]byte(tarPayload))
	}
	return in, func() { close(gate) }
}

// joinFlight starts a second EnsureLocal on the rig's ref and returns once it
// has joined the running transfer.
func (r *fetchRig) joinFlight(out *lineCollector) <-chan error {
	errc := make(chan error, 1)
	go func() { errc <- r.svc.EnsureLocal(context.Background(), r.ref, out.add) }()
	waiting := "waiting for the transfer of " + r.ref.String() + " already in progress"
	for l := range out.seen {
		if l == waiting {
			break
		}
	}
	return errc
}

func TestFetchJoinersReceiveTheTransfersProgressLines(t *testing.T) {
	r := newFetchRig(t)
	started, release := r.gatedTransfer()
	starter, joiner := newLineCollector(), newLineCollector()
	starterErr := make(chan error, 1)
	go func() { starterErr <- r.svc.EnsureLocal(context.Background(), r.ref, starter.add) }()
	<-started
	joinerErr := r.joinFlight(joiner)
	release()
	if err := <-starterErr; err != nil {
		t.Fatal(err)
	}
	if err := <-joinerErr; err != nil {
		t.Fatal(err)
	}
	loaded := "loaded " + r.ref.String()
	if !starter.has(loaded) || !joiner.has(loaded) {
		t.Fatalf("starter lines %q, joiner lines %q; both want %q", starter.all(), joiner.all(), loaded)
	}
}

func TestFetchWaiterThatLeftStopsReceivingLines(t *testing.T) {
	r := newFetchRig(t)
	started, release := r.gatedTransfer()
	starter, joiner := newLineCollector(), newLineCollector()
	starterCtx, cancelStarter := context.WithCancel(context.Background())
	starterErr := make(chan error, 1)
	go func() { starterErr <- r.svc.EnsureLocal(starterCtx, r.ref, starter.add) }()
	<-started
	joinerErr := r.joinFlight(joiner)
	cancelStarter()
	if err := <-starterErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("starter err = %v", err)
	}
	release()
	if err := <-joinerErr; err != nil {
		t.Fatalf("joiner err = %v; the transfer must survive the starter leaving", err)
	}
	loaded := "loaded " + r.ref.String()
	if !joiner.has(loaded) {
		t.Fatalf("joiner lines %q; want %q", joiner.all(), loaded)
	}
	if starter.has(loaded) {
		t.Fatalf("starter lines %q; a waiter that left must not receive later lines", starter.all())
	}
}
