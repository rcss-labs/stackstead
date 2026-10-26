package backup

import (
	"archive/tar"
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"
)

type innerEntry struct {
	name, body string
	mode       int64
	dir        bool
}

func buildInnerTar(t *testing.T, entries []innerEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: e.mode, Typeflag: tar.TypeReg, Size: int64(len(e.body))}
		if e.dir {
			h.Typeflag, h.Size = tar.TypeDir, 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func readInner(t *testing.T, r io.Reader) []innerEntry {
	t.Helper()
	var out []innerEntry
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		out = append(out, innerEntry{name: h.Name, body: string(b), mode: h.Mode, dir: h.Typeflag == tar.TypeDir})
	}
}

func regHeader(body string) *tar.Header {
	return &tar.Header{Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(body))}
}

func TestArchiveRoundTrip(t *testing.T) {
	inner := []innerEntry{
		{name: "./", dir: true, mode: 0o755},
		{name: "a.txt", body: "alpha", mode: 0o640},
		{name: "sub/", dir: true, mode: 0o750},
		{name: "sub/b.bin", body: "beta", mode: 0o600},
	}
	want := Manifest{
		StacksteadVersion: "1.2.3", Host: "My Host", HostSlug: "my-host", InstanceID: "abc",
		CreatedAt: time.Date(2026, 10, 26, 3, 0, 0, 0, time.UTC), Status: "success", Networks: 2,
		Stacks: []ManifestStack{{Name: "web", ComposePath: "/srv/web/compose.yaml", Origin: "managed", Project: "web",
			WasRunning: true, LiveCopy: true, Images: []string{"nginx:1"},
			Volumes: []ManifestVolume{{Key: "data", Kind: "volume", Name: "web_data", Target: "/data", Bytes: 9}}}},
		Items: []ItemResult{{Kind: "volume", Stack: "web", Name: "data", Status: "ok", Bytes: 9}},
	}

	var archive bytes.Buffer
	w := NewWriter(&archive)
	if err := w.AddFile("base/stackstead.db", regHeader("db"), bytes.NewReader([]byte("db"))); err != nil {
		t.Fatal(err)
	}
	n, err := w.AddTarStream("volumes/web/data/", bytes.NewReader(buildInnerTar(t, inner)))
	if err != nil {
		t.Fatal(err)
	}
	if n != 9 {
		t.Fatalf("AddTarStream bytes = %d, want 9", n)
	}
	if err := w.Close(want); err != nil {
		t.Fatal(err)
	}

	r, err := NewReader(&archive)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	var got Manifest
	for {
		h, body, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
		if h.Name == "manifest.json" {
			if err := decodeManifest(body, &got); err != nil {
				t.Fatal(err)
			}
		}
	}
	wantNames := []string{"base/stackstead.db", "volumes/web/data/", "volumes/web/data/a.txt", "volumes/web/data/sub/", "volumes/web/data/sub/b.bin", "manifest.json"}
	if !reflect.DeepEqual(names, wantNames) {
		t.Fatalf("names = %v, want %v", names, wantNames)
	}
	want.Format = 1
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("manifest = %+v, want %+v", got, want)
	}
}

func TestWriterRejectsOutOfOrder(t *testing.T) {
	w := NewWriter(io.Discard)
	if err := w.AddFile("volumes/a/b/x", regHeader("x"), bytes.NewReader([]byte("x"))); err != nil {
		t.Fatal(err)
	}
	err := w.AddFile("base/x", regHeader("x"), bytes.NewReader([]byte("x")))
	if !errors.Is(err, ErrEntryOrder) {
		t.Fatalf("err = %v, want ErrEntryOrder", err)
	}
	if _, err := w.AddTarStream("base/stacks/web", bytes.NewReader(nil)); !errors.Is(err, ErrEntryOrder) {
		t.Fatalf("AddTarStream err = %v, want ErrEntryOrder", err)
	}
}

func archiveWithEntry(t *testing.T, h *tar.Header) *Reader {
	t.Helper()
	var buf bytes.Buffer
	// Write raw through a Writer's internals-free path: build gzip+tar directly.
	w := NewWriter(&buf)
	if err := w.tw.WriteHeader(h); err != nil {
		t.Fatal(err)
	}
	if err := w.tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.gz.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := NewReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestReaderRejectsPathEscape(t *testing.T) {
	cases := map[string]*tar.Header{
		"dotdot":                   {Name: "../evil", Typeflag: tar.TypeDir},
		"nested dotdot":            {Name: "base/../../evil", Typeflag: tar.TypeDir},
		"absolute":                 {Name: "/etc/passwd", Typeflag: tar.TypeDir},
		"escaping symlink":         {Name: "volumes/a/x", Typeflag: tar.TypeSymlink, Linkname: "../../../etc"},
		"cross-section symlink":    {Name: "volumes/a/x", Typeflag: tar.TypeSymlink, Linkname: "../../base/stackstead.db"},
		"absolute symlink in base": {Name: "base/x", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
		"escaping hardlink":        {Name: "volumes/a/x", Typeflag: tar.TypeLink, Linkname: "base/stackstead.db"},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			r := archiveWithEntry(t, h)
			if _, _, err := r.Next(); !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("err = %v, want ErrUnsafePath", err)
			}
		})
	}
	t.Run("safe links pass", func(t *testing.T) {
		for _, h := range []*tar.Header{
			{Name: "volumes/a/x", Typeflag: tar.TypeSymlink, Linkname: "y"},
			{Name: "volumes/a/x", Typeflag: tar.TypeSymlink, Linkname: "../b/y"},
			{Name: "volumes/a/x", Typeflag: tar.TypeSymlink, Linkname: "/abs/in/container"},
			{Name: "volumes/a/x", Typeflag: tar.TypeLink, Linkname: "volumes/a/y"},
		} {
			if _, _, err := archiveWithEntry(t, h).Next(); err != nil {
				t.Fatalf("%+v: %v", h, err)
			}
		}
	})
}

func TestReaderTruncatedArchive(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	// Incompressible data so cutting the gzip in half lands inside the second file.
	blob := make([]byte, 256<<10)
	if _, err := rand.Read(blob); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"base/first.bin", "base/second.bin"} {
		if err := w.AddFile(name, regHeader(string(blob)), bytes.NewReader(blob)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(Manifest{}); err != nil {
		t.Fatal(err)
	}

	r, err := NewReader(bytes.NewReader(buf.Bytes()[:buf.Len()/2]))
	if err != nil {
		t.Fatal(err)
	}
	var last error
	lastGood := ""
	for {
		h, body, err := r.Next()
		if err == nil {
			lastGood = h.Name
		}
		if err != nil {
			last = err
			break
		}
		if _, err := io.Copy(io.Discard, body); err != nil {
			// body read failure is surfaced by the following Next
			continue
		}
	}
	if last == nil || errors.Is(last, io.EOF) {
		t.Fatalf("err = %v, want non-EOF error", last)
	}
	if lastGood == "" {
		t.Fatal("no entry was read before the cut")
	}
	if want := `"` + lastGood + `"`; !bytes.Contains([]byte(last.Error()), []byte(want)) {
		t.Fatalf("err %q does not name last good entry %s", last, want)
	}
}

func TestTarStreamUnderRebuilds(t *testing.T) {
	inner := []innerEntry{
		{name: "./", dir: true, mode: 0o755},
		{name: "a.txt", body: "alpha", mode: 0o640},
		{name: "sub/", dir: true, mode: 0o750},
		{name: "sub/b.bin", body: "beta", mode: 0o600},
	}
	var buf bytes.Buffer
	w := NewWriter(&buf)
	if _, err := w.AddTarStream("volumes/web/data/", bytes.NewReader(buildInnerTar(t, inner))); err != nil {
		t.Fatal(err)
	}
	if _, err := w.AddTarStream("volumes/web/other/", bytes.NewReader(buildInnerTar(t, []innerEntry{{name: "z", body: "zed", mode: 0o600}}))); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(Manifest{}); err != nil {
		t.Fatal(err)
	}

	r, err := NewReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	h, body, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	got := readInner(t, TarStreamUnder(r, "volumes/web/data/", h, body))
	if !reflect.DeepEqual(got, inner) {
		t.Fatalf("rebuilt = %+v, want %+v", got, inner)
	}
	// The lookahead entry must come back intact, body unread.
	h, body, err = r.Next()
	if err != nil || h.Name != "volumes/web/other/z" {
		t.Fatalf("next after stream = %v, %v", h, err)
	}
	got = readInner(t, TarStreamUnder(r, "volumes/web/other/", h, body))
	if len(got) != 1 || got[0].name != "z" || got[0].body != "zed" {
		t.Fatalf("second stream = %+v", got)
	}
	if h, _, err = r.Next(); err != nil || h.Name != "manifest.json" {
		t.Fatalf("expected manifest after streams, got %v, %v", h, err)
	}
}
