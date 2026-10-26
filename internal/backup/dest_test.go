package backup

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPruneLocalKeepsNewestOwnSlug(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	touch := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 9; i++ {
		name := ArchiveName("a", base.AddDate(0, 0, i))
		touch(name)
		touch(SidecarName(name))
	}
	for i := 0; i < 2; i++ {
		touch(ArchiveName("b", base.AddDate(0, 0, i)))
	}
	partial := ArchiveName("a", base.AddDate(0, 0, 20)) + ".partial"
	touch(partial)

	deleted, err := PruneLocal(LocalDest{Path: dir, KeepLast: 7}, "a")
	if err != nil {
		t.Fatal(err)
	}
	oldest := ArchiveName("a", base)
	second := ArchiveName("a", base.AddDate(0, 0, 1))
	want := []string{
		filepath.Join(dir, oldest), filepath.Join(dir, SidecarName(oldest)),
		filepath.Join(dir, second), filepath.Join(dir, SidecarName(second)),
	}
	sort.Strings(deleted)
	sort.Strings(want)
	if strings.Join(deleted, "|") != strings.Join(want, "|") {
		t.Fatalf("deleted = %v, want %v", deleted, want)
	}
	entries, _ := os.ReadDir(dir)
	// 7 archives + 7 sidecars of a, 2 of b, 1 partial.
	if len(entries) != 14+2+1 {
		t.Fatalf("remaining files = %d, want 17", len(entries))
	}
	if _, err := os.Stat(filepath.Join(dir, partial)); err != nil {
		t.Fatalf("partial file was touched: %v", err)
	}
}

func TestLocalCreateIsAtomic(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "backups")
	d := NewLocal(LocalDest{Enabled: true, Path: dir, KeepLast: 7})
	name := ArchiveName("h", time.Now())
	w, err := d.Create(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("data")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
		t.Fatalf("final file exists before Close: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, name+".partial")); err != nil {
		t.Fatalf("partial missing: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, name+".partial")); !os.IsNotExist(err) {
		t.Fatalf("partial remains after Close: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, name))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("final file: %v mode %v", err, info)
	}
	if di, _ := os.Stat(dir); di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %v", di.Mode().Perm())
	}
}

func TestLocalAbortRemovesPartialAndRoundTrip(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	d := NewLocal(LocalDest{Enabled: true, Path: dir, KeepLast: 7})
	name := ArchiveName("h", time.Date(2026, 5, 1, 1, 2, 3, 0, time.UTC))
	w, _ := d.Create(ctx, name)
	_, _ = w.Write([]byte("zzz"))
	if err := d.Abort(ctx, name); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("files left after abort: %v", entries)
	}

	w, _ = d.Create(ctx, name)
	_, _ = w.Write([]byte("payload"))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := d.PutSidecar(ctx, name, []byte(`{"hostSlug":"h"}`)); err != nil {
		t.Fatal(err)
	}
	// A foreign file and a partial must not be listed.
	_ = os.WriteFile(filepath.Join(dir, "notes.txt"), nil, 0o600)
	refs, err := d.List(ctx)
	if err != nil || len(refs) != 1 {
		t.Fatalf("List = %v, %v", refs, err)
	}
	if refs[0].Dest != "local" || refs[0].Name != name || refs[0].HostSlug != "h" || refs[0].Size != 7 {
		t.Fatalf("ref = %+v", refs[0])
	}
	m, err := d.ReadSidecar(ctx, name)
	if err != nil || m.HostSlug != "h" {
		t.Fatalf("sidecar = %+v, %v", m, err)
	}
	rc, err := d.Open(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != "payload" {
		t.Fatalf("Open = %q", got)
	}
}

// fakeS3 is a minimal in-memory S3 recording multipart activity.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	parts   map[string][]byte // uploadId -> data so far
	keyOf   map[string]string
	aborted []string
	puts    []string
}

func newFakeS3() (*fakeS3, *httptest.Server) {
	f := &fakeS3{objects: map[string][]byte{}, parts: map[string][]byte{}, keyOf: map[string]string{}}
	return f, httptest.NewServer(f)
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Path-style: /bucket/key...
	key := strings.TrimPrefix(r.URL.Path, "/bkt")
	key = strings.TrimPrefix(key, "/")
	q := r.URL.Query()
	body, _ := io.ReadAll(r.Body)
	switch {
	case r.Method == http.MethodGet && q.Get("list-type") == "2":
		prefix := q.Get("prefix")
		var b strings.Builder
		b.WriteString(`<ListBucketResult><IsTruncated>false</IsTruncated>`)
		keys := make([]string, 0, len(f.objects))
		for k := range f.objects {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if strings.HasPrefix(k, prefix) {
				b.WriteString(`<Contents><Key>` + k + `</Key><Size>` + itoa(len(f.objects[k])) + `</Size><LastModified>2026-01-01T00:00:00Z</LastModified></Contents>`)
			}
		}
		b.WriteString(`</ListBucketResult>`)
		_, _ = w.Write([]byte(b.String()))
	case r.Method == http.MethodPost && q.Has("uploads"):
		id := "u" + itoa(len(f.keyOf)+1)
		f.keyOf[id] = key
		f.parts[id] = nil
		_, _ = w.Write([]byte(`<InitiateMultipartUploadResult><UploadId>` + id + `</UploadId></InitiateMultipartUploadResult>`))
	case r.Method == http.MethodPut && q.Get("uploadId") != "":
		id := q.Get("uploadId")
		f.parts[id] = append(f.parts[id], body...)
		w.Header().Set("ETag", `"e"`)
	case r.Method == http.MethodPost && q.Get("uploadId") != "":
		id := q.Get("uploadId")
		f.objects[f.keyOf[id]] = f.parts[id]
		delete(f.parts, id)
		_, _ = w.Write([]byte(`<CompleteMultipartUploadResult></CompleteMultipartUploadResult>`))
	case r.Method == http.MethodDelete && q.Get("uploadId") != "":
		f.aborted = append(f.aborted, f.keyOf[q.Get("uploadId")])
		delete(f.parts, q.Get("uploadId"))
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPut:
		f.objects[key] = body
		f.puts = append(f.puts, key)
	case r.Method == http.MethodGet:
		data, ok := f.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`<Error><Code>NoSuchKey</Code><Message>nope</Message></Error>`))
			return
		}
		_, _ = w.Write(data)
	case r.Method == http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func testS3Dest(url, prefix string) S3Dest {
	return S3Dest{Enabled: true, Bucket: "bkt", Prefix: prefix, Region: "us-east-1", Endpoint: url, PathStyle: true, AccessKeyID: "AK", SecretKey: "SK"}
}

func TestS3DestinationUploadAndAbort(t *testing.T) {
	ctx := context.Background()
	f, srv := newFakeS3()
	defer srv.Close()
	d := NewS3(testS3Dest(srv.URL, "/stackstead//"), srv.Client())
	if d.Name() != "s3" {
		t.Fatalf("Name = %q", d.Name())
	}
	name := ArchiveName("host", time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC))

	w, err := d.Create(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("hello"))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if got := string(f.objects["stackstead/"+name]); got != "hello" {
		t.Fatalf("object = %q, keys %v", got, f.objects)
	}

	other := ArchiveName("host", time.Date(2026, 3, 5, 5, 6, 7, 0, time.UTC))
	w, _ = d.Create(ctx, other)
	_, _ = w.Write([]byte("x"))
	if err := d.Abort(ctx, other); err != nil {
		t.Fatal(err)
	}
	if len(f.aborted) != 1 || f.aborted[0] != "stackstead/"+other {
		t.Fatalf("aborted = %v", f.aborted)
	}
}

func TestS3DestinationListAndSidecar(t *testing.T) {
	ctx := context.Background()
	f, srv := newFakeS3()
	defer srv.Close()
	d := NewS3(testS3Dest(srv.URL, ""), srv.Client())
	name := ArchiveName("host", time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC))
	f.objects[name] = []byte("12345")
	f.objects["README.txt"] = []byte("x")
	f.objects["stackstead-bogus.tar.gz"] = []byte("x")

	if err := d.PutSidecar(ctx, name, []byte(`{"hostSlug":"host"}`)); err != nil {
		t.Fatal(err)
	}
	refs, err := d.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Fatalf("refs = %+v", refs)
	}
	r := refs[0]
	if r.Dest != "s3" || r.Name != name || r.HostSlug != "host" || r.Size != 5 || !r.CreatedAt.Equal(time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)) {
		t.Fatalf("ref = %+v", r)
	}
	m, err := d.ReadSidecar(ctx, name)
	if err != nil || m.HostSlug != "host" {
		t.Fatalf("sidecar = %+v, %v", m, err)
	}
	rc, err := d.Open(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != "12345" {
		t.Fatalf("Open = %q", got)
	}
}

func TestTestS3PutsThenDeletes(t *testing.T) {
	f, srv := newFakeS3()
	defer srv.Close()
	if err := TestS3(context.Background(), testS3Dest(srv.URL, "pre"), srv.Client()); err != nil {
		t.Fatal(err)
	}
	if len(f.puts) != 1 || !strings.HasPrefix(f.puts[0], "pre/.stackstead-test-") {
		t.Fatalf("puts = %v", f.puts)
	}
	if len(f.objects) != 0 {
		t.Fatalf("test object not deleted: %v", f.objects)
	}
}
