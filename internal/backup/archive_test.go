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

func archiveWithEntries(t *testing.T, hdrs ...*tar.Header) *Reader {
	t.Helper()
	var buf bytes.Buffer
	w := NewWriter(&buf)
	for _, h := range hdrs {
		if err := w.tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
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

func nextAll(r *Reader) error {
	for {
		if _, _, err := r.Next(); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func sym(name, target string) *tar.Header {
	return &tar.Header{Name: name, Typeflag: tar.TypeSymlink, Linkname: target}
}
func hard(name, target string) *tar.Header {
	return &tar.Header{Name: name, Typeflag: tar.TypeLink, Linkname: target}
}
func file(name string) *tar.Header { return &tar.Header{Name: name, Typeflag: tar.TypeReg} }

func TestReaderRejectsPathEscape(t *testing.T) {
	cases := map[string][]*tar.Header{
		"dotdot":                   {{Name: "../evil", Typeflag: tar.TypeDir}},
		"nested dotdot":            {{Name: "base/../../evil", Typeflag: tar.TypeDir}},
		"absolute":                 {{Name: "/etc/passwd", Typeflag: tar.TypeDir}},
		"unknown top level":        {file("etc/passwd")},
		"absolute symlink in base": {sym("base/stacks/web/x", "/etc/passwd")},
		"escaping hardlink":        {hard("volumes/a/k/x", "base/stackstead.db")},
		"hardlink to other volume": {hard("volumes/a/k/x", "volumes/a/k2/y")},
		"base non-leading dotdot":  {sym("base/stacks/web/x", "a/../y")},
		"base dot-prefixed dotdot": {sym("base/stacks/web/x", "./a/../../../y")},
		"base symlink to /etc":     {sym("base/stacks/web/d", "../../etc")},
		"cross-stack symlink":      {sym("base/stacks/a/x", "../b/y")},
		"cross-stack hardlink":     {hard("base/stacks/a/x", "base/stacks/b/y")},
		"top-level base file link": {sym("base/stackstead.db", "config.yaml")},
		"image symlink to sibling": {sym("images/web/x", "../other/y")},
		"write through symlink":    {sym("base/stacks/web/d", "real"), file("base/stacks/web/d/cron.d/evil")},
		"dir under symlink":        {sym("volumes/a/k/d", "/etc"), file("volumes/a/k/d/evil")},
		"symlink via earlier symlink": {
			{Name: "base/stacks/web/a/b/c/", Typeflag: tar.TypeDir},
			sym("base/stacks/web/a/b/c/up", "../../.."),
			sym("base/stacks/web/x", "a/b/c/up/../../../etc"),
		},
		"hardlink via earlier symlink": {
			sym("base/stacks/web/a/b/c/up", "../../.."),
			hard("base/stacks/web/h", "base/stacks/web/a/b/c/up/../../../etc/passwd"),
		},
		"hardlink with dotdot":       {hard("base/stacks/web/h", "base/stacks/web/a/../b")},
		"hardlink below symlink":     {sym("base/stacks/web/d", "."), hard("base/stacks/web/h", "base/stacks/web/d/y")},
		"cross-stack by indirection": {sym("base/stacks/web/d", "."), sym("base/stacks/web/x", "d/../b/y")},
		"file replaces symlink":      {sym("base/stacks/web/x", "y"), file("base/stacks/web/x")},
		"dir replaces symlink":       {sym("base/stacks/web/x", "y"), {Name: "base/stacks/web/x/", Typeflag: tar.TypeDir}},
	}
	for name, hdrs := range cases {
		t.Run(name, func(t *testing.T) {
			if err := nextAll(archiveWithEntries(t, hdrs...)); !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("err = %v, want ErrUnsafePath", err)
			}
		})
	}
	t.Run("safe entries pass", func(t *testing.T) {
		safe := [][]*tar.Header{
			{sym("volumes/a/k/x", "y")},
			{sym("volumes/a/k/x", "../lib/../bin/y")},
			{sym("volumes/a/k/x", "./../y")},
			{sym("volumes/a/k/x", "../../../../usr/share/zoneinfo/UTC")},
			{sym("volumes/a/k/x", "../../../etc")},
			{sym("volumes/a/k/x", "../../../base/stackstead.db")},
			{sym("volumes/a/k/s", "target"), hard("volumes/a/k/h", "volumes/a/k/s")},
			{sym("volumes/a/k/sub/x", "../y")},
			{sym("volumes/a/k/x", "/abs/in/container")},
			{hard("volumes/a/k/x", "volumes/a/k/y")},
			{sym("base/stacks/web/sub/x", "../compose.yaml")},
			{sym("base/images-library/a/x", "../b")},
			{sym("images/web/x", "y")},
			{file("base/stacks/web/d"), file("base/stacks/web/d2/x")},
		}
		for _, hdrs := range safe {
			if err := nextAll(archiveWithEntries(t, hdrs...)); err != nil {
				t.Fatalf("%+v: %v", hdrs[0], err)
			}
		}
	})
}

func TestAddTarStreamRejectsUnsafeInner(t *testing.T) {
	bad := buildLinkTar(t, []*tar.Header{{Name: "x", Typeflag: tar.TypeLink, Linkname: "../../etc"}})
	w := NewWriter(io.Discard)
	if _, err := w.AddTarStream("volumes/web/data/", bytes.NewReader(bad)); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("err = %v, want ErrUnsafePath", err)
	}
}

func TestAddTarStreamCanonicalisesAbsoluteInnerNames(t *testing.T) {
	inner := buildLinkTar(t, []*tar.Header{{Name: "/etc/x", Typeflag: tar.TypeReg, Mode: 0o600, Size: 1}})
	var buf bytes.Buffer
	w := NewWriter(&buf)
	if _, err := w.AddTarStream("volumes/web/data/", bytes.NewReader(inner)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(Manifest{}); err != nil {
		t.Fatal(err)
	}
	r, _ := NewReader(&buf)
	h, _, err := r.Next()
	if err != nil || h.Name != "volumes/web/data/etc/x" {
		t.Fatalf("name = %v, err = %v", h, err)
	}
}

func buildLinkTar(t *testing.T, hdrs []*tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, h := range hdrs {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			if _, err := tw.Write(bytes.Repeat([]byte("x"), int(h.Size))); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type linkInfo struct {
	name, link, body string
	typ              byte
}

func readLinks(t *testing.T, r io.Reader) []linkInfo {
	t.Helper()
	var out []linkInfo
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
		out = append(out, linkInfo{h.Name, h.Linkname, string(b), h.Typeflag})
	}
}

func TestLinksRoundTripThroughArchive(t *testing.T) {
	inner := buildLinkTar(t, []*tar.Header{
		{Name: "file.txt", Typeflag: tar.TypeReg, Mode: 0o600, Size: 5},
		{Name: "hard.txt", Typeflag: tar.TypeLink, Linkname: "file.txt"},
		{Name: "soft", Typeflag: tar.TypeSymlink, Linkname: "file.txt"},
	})
	want := readLinks(t, bytes.NewReader(inner))

	var buf bytes.Buffer
	w := NewWriter(&buf)
	if _, err := w.AddTarStream("volumes/web/data/", bytes.NewReader(inner)); err != nil {
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
	if h.Name != "volumes/web/data/file.txt" {
		t.Fatalf("first entry %q", h.Name)
	}
	got := readLinks(t, TarStreamUnder(r, "volumes/web/data/", h, body))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestArchiveStoresHardlinkTargetWithPrefix(t *testing.T) {
	inner := buildLinkTar(t, []*tar.Header{
		{Name: "file.txt", Typeflag: tar.TypeReg, Mode: 0o600, Size: 5},
		{Name: "hard.txt", Typeflag: tar.TypeLink, Linkname: "file.txt"},
	})
	var buf bytes.Buffer
	w := NewWriter(&buf)
	if _, err := w.AddTarStream("volumes/web/data/", bytes.NewReader(inner)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(Manifest{}); err != nil {
		t.Fatal(err)
	}
	r, _ := NewReader(&buf)
	for {
		h, _, err := r.Next()
		if err != nil {
			t.Fatal("hardlink entry not found")
		}
		if h.Typeflag == tar.TypeLink {
			if h.Linkname != "volumes/web/data/file.txt" {
				t.Fatalf("linkname = %q", h.Linkname)
			}
			return
		}
	}
}

func TestTarStreamUnderRejectsHardlinkOutsidePrefix(t *testing.T) {
	r := archiveWithEntries(t,
		file("volumes/web/data/a"),
		hard("volumes/web/data/b", "volumes/web/other/c"),
	)
	h, body, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(io.Discard, TarStreamUnder(r, "volumes/web/data/", h, body))
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("err = %v, want ErrUnsafePath", err)
	}
}

func TestTarStreamUnderBoundsMemory(t *testing.T) {
	const size = 8 << 20
	var buf bytes.Buffer
	w := NewWriter(&buf)
	hdr := &tar.Header{Typeflag: tar.TypeReg, Mode: 0o600, Size: size}
	if err := w.AddFile("volumes/web/data/big", hdr, bytes.NewReader(make([]byte, size))); err != nil {
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
	stream := TarStreamUnder(r, "volumes/web/data/", h, body)
	n, err := io.Copy(io.Discard, stream)
	if err != nil {
		t.Fatal(err)
	}
	if n < size {
		t.Fatalf("streamed %d bytes, want >= %d", n, size)
	}
	if peak := stream.(*tarUnder).peakBuf; peak > 33<<10 {
		t.Fatalf("internal buffer grew to %d bytes", peak)
	}
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

	r, err := NewReader(bytes.NewReader(buf.Bytes()[:buf.Len()*3/4]))
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
	if lastGood != "base/second.bin" {
		t.Fatalf("lastGood = %q, want base/second.bin", lastGood)
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
