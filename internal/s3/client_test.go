package s3

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/registry/awsauth"
	"github.com/rcss-labs/stackstead/internal/secret"
)

var fixedNow = func() time.Time { return time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC) }

func testCreds() awsauth.Credentials {
	return awsauth.Credentials{AccessKeyID: "AKIDTEST", SecretAccessKey: secret.Secret("sekret")}
}

type recordedReq struct {
	Method, Path, Query string
	Size                int
}

// fakeS3 is a minimal S3 that checks every request is signed with the S3
// payload-hash header and records what it receives.
type fakeS3 struct {
	t        *testing.T
	mu       sync.Mutex
	reqs     []recordedReq
	failPart int // part number answered with HTTP 500 (0 = none)
	pages    map[string]string
	complete string
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	sum := sha256.Sum256(body)
	if got := r.Header.Get("X-Amz-Content-Sha256"); got != hex.EncodeToString(sum[:]) {
		f.t.Errorf("%s %s: x-amz-content-sha256 %q does not match body hash", r.Method, r.URL, got)
	}
	if !strings.Contains(r.Header.Get("Authorization"), "Credential=AKIDTEST/20261026/us-east-1/s3/aws4_request") {
		f.t.Errorf("bad Authorization: %q", r.Header.Get("Authorization"))
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, recordedReq{r.Method, r.URL.Path, r.URL.RawQuery, len(body)})
	f.mu.Unlock()
	q := r.URL.Query()
	switch {
	case r.Method == "POST" && q.Has("uploads"):
		fmt.Fprint(w, `<InitiateMultipartUploadResult><UploadId>UP1</UploadId></InitiateMultipartUploadResult>`)
	case r.Method == "PUT" && q.Has("partNumber"):
		n, _ := strconv.Atoi(q.Get("partNumber"))
		if n == f.failPart {
			w.WriteHeader(500)
			fmt.Fprint(w, `<Error><Code>InternalError</Code><Message>boom</Message></Error>`)
			return
		}
		w.Header().Set("ETag", fmt.Sprintf(`"etag-%d"`, n))
	case r.Method == "POST" && q.Has("uploadId"):
		f.mu.Lock()
		f.complete = string(body)
		f.mu.Unlock()
		fmt.Fprint(w, `<CompleteMultipartUploadResult><ETag>"x"</ETag></CompleteMultipartUploadResult>`)
	case r.Method == "DELETE" && q.Has("uploadId"):
		w.WriteHeader(204)
	case r.Method == "GET" && q.Get("list-type") == "2":
		fmt.Fprint(w, f.pages[q.Get("continuation-token")])
	}
}

func (f *fakeS3) snapshot() ([]recordedReq, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedReq(nil), f.reqs...), f.complete
}

func (f *fakeS3) newClient(srvURL string) *Client {
	return New(Config{Bucket: "bkt", Region: "us-east-1", Endpoint: srvURL, PathStyle: true, Creds: testCreds()}, http.DefaultClient, fixedNow)
}

func TestUploadMultipartParts(t *testing.T) {
	f := &fakeS3{t: t}
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := f.newClient(srv.URL)

	u, err := c.NewUpload(t.Context(), "pre/a.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 1<<20)
	for i := 0; i < 40; i++ {
		if _, err := u.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := u.Close(); err != nil {
		t.Fatal(err)
	}

	var sizes []int
	reqs, _ := f.snapshot()
	for _, r := range reqs {
		if r.Method == "PUT" {
			sizes = append(sizes, r.Size)
		}
	}
	want := []int{16 << 20, 16 << 20, 8 << 20}
	if fmt.Sprint(sizes) != fmt.Sprint(want) {
		t.Fatalf("part sizes %v, want %v", sizes, want)
	}
	first, last := reqs[0], reqs[len(reqs)-1]
	if first.Method != "POST" || first.Query != "uploads=" || first.Path != "/bkt/pre/a.tar.gz" {
		t.Errorf("first request %+v", first)
	}
	if last.Method != "POST" || !strings.Contains(last.Query, "uploadId=UP1") {
		t.Errorf("last request %+v", last)
	}
}

func TestUploadCompleteBodyListsETagsInOrder(t *testing.T) {
	f := &fakeS3{t: t}
	srv := httptest.NewServer(f)
	defer srv.Close()
	u, _ := f.newClient(srv.URL).NewUpload(t.Context(), "k")
	u.Write(make([]byte, 17<<20))
	if err := u.Close(); err != nil {
		t.Fatal(err)
	}
	_, complete := f.snapshot()
	i1 := strings.Index(complete, "etag-1")
	i2 := strings.Index(complete, "etag-2")
	if i1 < 0 || i2 < i1 {
		t.Fatalf("complete body %q", complete)
	}
}

func TestUploadAbortOnError(t *testing.T) {
	f := &fakeS3{t: t, failPart: 2}
	srv := httptest.NewServer(f)
	defer srv.Close()
	u, err := f.newClient(srv.URL).NewUpload(t.Context(), "k")
	if err != nil {
		t.Fatal(err)
	}
	u.Write(make([]byte, 20<<20)) // part 1 flushes; the tail stays buffered
	err = u.Close()               // part 2 fails with 500
	if err == nil || !strings.Contains(err.Error(), "InternalError") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Close err = %v, want S3 code and message", err)
	}
	reqs, _ := f.snapshot()
	last := reqs[len(reqs)-1]
	if last.Method != "DELETE" || !strings.Contains(last.Query, "uploadId=UP1") {
		t.Fatalf("expected AbortMultipartUpload last, got %+v", last)
	}
}

func TestListPaginates(t *testing.T) {
	f := &fakeS3{t: t, pages: map[string]string{
		"": `<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>tok2</NextContinuationToken>
<Contents><Key>p/a</Key><Size>1</Size><LastModified>2026-10-01T10:00:00.000Z</LastModified></Contents></ListBucketResult>`,
		"tok2": `<ListBucketResult><IsTruncated>false</IsTruncated>
<Contents><Key>p/b</Key><Size>22</Size><LastModified>2026-10-02T10:00:00.000Z</LastModified></Contents></ListBucketResult>`,
	}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	objs, err := f.newClient(srv.URL).List(t.Context(), "p/")
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 2 || objs[0].Key != "p/a" || objs[1].Key != "p/b" || objs[1].Size != 22 ||
		!objs[1].Modified.Equal(time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("objects %+v", objs)
	}
}

func TestPutGetDeleteAndErrorSurfacing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "PUT":
			w.WriteHeader(403)
			fmt.Fprint(w, `<Error><Code>AccessDenied</Code><Message>nope</Message></Error>`)
		case "GET":
			w.Write([]byte("data"))
		default:
			w.WriteHeader(204)
		}
	}))
	defer srv.Close()
	c := (&fakeS3{t: t}).newClient(srv.URL)
	if err := c.Put(t.Context(), "k", []byte("x")); err == nil || !strings.Contains(err.Error(), "AccessDenied") || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("Put err = %v", err)
	}
	rc, n, err := c.Get(t.Context(), "k")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "data" || n != 4 {
		t.Fatalf("Get = %q, %d", b, n)
	}
	if err := c.Delete(t.Context(), "k"); err != nil {
		t.Fatal(err)
	}
}

type captureTransport struct{ got *http.Request }

func (c *captureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.got = r
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}, Request: r}, nil
}

func TestPathStyleVsVirtualHost(t *testing.T) {
	cases := []struct {
		name     string
		cfg      Config
		wantHost string
		wantPath string
	}{
		{"aws virtual", Config{Bucket: "bkt", Region: "eu-west-1"}, "bkt.s3.eu-west-1.amazonaws.com", "/dir/a b.txt"},
		{"aws path", Config{Bucket: "bkt", Region: "eu-west-1", PathStyle: true}, "s3.eu-west-1.amazonaws.com", "/bkt/dir/a b.txt"},
		{"custom path", Config{Bucket: "bkt", Region: "us-east-1", Endpoint: "http://minio.local:9000", PathStyle: true}, "minio.local:9000", "/bkt/dir/a b.txt"},
		{"custom virtual", Config{Bucket: "bkt", Region: "us-east-1", Endpoint: "https://s3.wasabisys.com"}, "bkt.s3.wasabisys.com", "/dir/a b.txt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.cfg.Creds = testCreds()
			ct := &captureTransport{}
			c := New(tc.cfg, &http.Client{Transport: ct}, fixedNow)
			if err := c.Put(t.Context(), "dir/a b.txt", []byte("x")); err != nil {
				t.Fatal(err)
			}
			if ct.got.URL.Host != tc.wantHost || ct.got.URL.Path != tc.wantPath {
				t.Errorf("got host=%q path=%q, want host=%q path=%q", ct.got.URL.Host, ct.got.URL.Path, tc.wantHost, tc.wantPath)
			}
			if !strings.Contains(ct.got.URL.EscapedPath(), "a%20b.txt") {
				t.Errorf("path not percent-encoded: %s", ct.got.URL.EscapedPath())
			}
		})
	}
}

func TestListStopsAtObjectCap(t *testing.T) {
	f := &fakeS3{t: t, pages: map[string]string{
		"": `<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>tok2</NextContinuationToken>
<Contents><Key>p/a</Key><Size>1</Size></Contents><Contents><Key>p/b</Key><Size>1</Size></Contents></ListBucketResult>`,
		"tok2": `<ListBucketResult><IsTruncated>false</IsTruncated>
<Contents><Key>p/c</Key><Size>1</Size></Contents></ListBucketResult>`,
	}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := f.newClient(srv.URL)
	c.maxObjects = 2
	objs, err := c.List(t.Context(), "p/")
	if !errors.Is(err, ErrListTooLarge) || objs != nil {
		t.Fatalf("List = %+v, %v; want ErrListTooLarge", objs, err)
	}
}
