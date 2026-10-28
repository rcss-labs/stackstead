package images

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"io"
	"path"
	"strings"
	"sync"
)

// maxArchiveMetadataBytes caps each tag-bearing entry the inspector parses.
const maxArchiveMetadataBytes = 1 << 20

// tarCheckHook is a test seam that runs before each metadata check.
var tarCheckHook func()

var (
	errForeignTags      = errors.New("image archive names other tags")
	errArchiveMalformed = errors.New("image archive is not a valid tar")
	errMetadataTooLarge = errors.New("image archive metadata is too large")
)

// tarInspector is a pass-through reader that parses the tar it carries and
// refuses an archive that names any tag other than want, so docker load can
// never retag an unrelated local image.
//
// Docker applies tags only after it has read the whole archive (a tar reader
// stops at the end-of-archive blocks, not at stream EOF), so the inspector
// never hands a chunk to the consumer before its parser has fully processed
// that chunk. A violation is therefore reported on the Read that would have
// delivered the entry's final bytes, before any trailer reaches docker.
type tarInspector struct {
	src  io.Reader
	want string
	also map[string]bool // other spellings of want that docker save may emit

	pw   *io.PipeWriter
	done chan struct{}

	mu       sync.Mutex
	cond     *sync.Cond
	consumed int64 // bytes the parser has read
	waitAt   int64 // value of consumed when the parser last asked for more
	finished bool
	aborted  bool  // the source failed or the consumer stopped: parse errors are no verdict
	err      error // first violation

	sent int64
}

func newTarInspector(src io.Reader, want string) *tarInspector {
	pr, pw := io.Pipe()
	t := &tarInspector{src: src, want: want, pw: pw, done: make(chan struct{}), waitAt: -1}
	t.cond = sync.NewCond(&t.mu)
	go t.parse(pr)
	return t
}

// parserReader tells the inspector when the parser is waiting for more bytes.
type parserReader struct {
	t  *tarInspector
	pr *io.PipeReader
}

func (p parserReader) Read(b []byte) (int, error) {
	p.t.mu.Lock()
	p.t.waitAt = p.t.consumed
	p.t.cond.Broadcast()
	p.t.mu.Unlock()
	n, err := p.pr.Read(b)
	p.t.mu.Lock()
	p.t.consumed += int64(n)
	p.t.mu.Unlock()
	return n, err
}

func (t *tarInspector) fail(err error) {
	t.mu.Lock()
	if t.err == nil && !t.aborted {
		t.err = err
	}
	t.mu.Unlock()
}

func (t *tarInspector) parse(pr *io.PipeReader) {
	defer close(t.done)
	tr := tar.NewReader(parserReader{t: t, pr: pr})
	for {
		h, err := tr.Next()
		if err != nil {
			if err != io.EOF {
				t.fail(errArchiveMalformed)
			}
			break
		}
		name := path.Clean(strings.TrimLeft(h.Name, "/"))
		if err := checkEntry(name, h); err != nil {
			t.fail(err)
			break
		}
		if !isMetadataName(name) {
			continue
		}
		if h.Size > maxArchiveMetadataBytes {
			t.fail(errMetadataTooLarge)
			break
		}
		data, err := io.ReadAll(io.LimitReader(tr, maxArchiveMetadataBytes+1))
		if err != nil {
			t.fail(errArchiveMalformed)
			break
		}
		if err := t.checkMetadata(name, data); err != nil {
			t.fail(err)
			break
		}
	}
	t.mu.Lock()
	t.finished = true
	t.cond.Broadcast()
	t.mu.Unlock()
	_, _ = io.Copy(io.Discard, pr) // keep the writer unblocked after the parser is done
}

func isMetadataName(n string) bool {
	return n == "manifest.json" || n == "repositories" || n == "index.json"
}

// checkEntry refuses entries that could hide or redirect the tag metadata:
// metadata names that are not plain files at the archive root, and links whose
// target is empty, absolute, the archive root or outside it. It does not
// restrict links to other places inside the archive.
func checkEntry(name string, h *tar.Header) error {
	if isMetadataName(path.Base(name)) {
		if name != path.Base(name) || h.Typeflag != tar.TypeReg {
			return errArchiveMalformed
		}
		return nil
	}
	if h.Typeflag != tar.TypeSymlink && h.Typeflag != tar.TypeLink {
		return nil
	}
	target := h.Linkname
	if target == "" || target == "." || target == ".." || path.IsAbs(target) || strings.HasSuffix(target, "/") {
		return errArchiveMalformed
	}
	resolved := path.Clean(target) // a hard link names a path from the archive root
	if h.Typeflag == tar.TypeSymlink {
		resolved = path.Clean(path.Join(path.Dir(name), target))
	}
	if resolved == "." || resolved == ".." || strings.HasPrefix(resolved, "../") {
		return errArchiveMalformed
	}
	return nil
}

func (t *tarInspector) checkMetadata(name string, data []byte) error {
	if tarCheckHook != nil {
		tarCheckHook()
	}
	var names []string
	switch name {
	case "manifest.json":
		var m []struct{ RepoTags []string }
		if err := json.Unmarshal(data, &m); err != nil {
			return errArchiveMalformed
		}
		for _, e := range m {
			names = append(names, e.RepoTags...)
		}
	case "repositories":
		var r map[string]map[string]string
		if err := json.Unmarshal(data, &r); err != nil {
			return errArchiveMalformed
		}
		for repo, tags := range r {
			for tag := range tags {
				names = append(names, repo+":"+tag)
			}
		}
	case "index.json":
		var idx struct {
			Manifests []struct{ Annotations map[string]string }
		}
		if err := json.Unmarshal(data, &idx); err != nil {
			return errArchiveMalformed
		}
		for _, m := range idx.Manifests {
			cn, hasCN := m.Annotations["io.containerd.image.name"]
			rn, hasRN := m.Annotations["org.opencontainers.image.ref.name"]
			if hasCN {
				names = append(names, cn)
			}
			// A bare tag is only meaningful next to the full name it completes.
			if hasRN && rn != t.want && !(hasCN && cn == t.want && rn == t.wantTag()) {
				names = append(names, rn)
			}
		}
	}
	for _, n := range names {
		if n != t.want && !t.also[n] {
			return errForeignTags
		}
	}
	return nil
}

func (t *tarInspector) wantTag() string {
	if i := strings.LastIndex(t.want, ":"); i >= 0 {
		return t.want[i+1:]
	}
	return t.want
}

// Read passes bytes on only after the parser has processed them.
func (t *tarInspector) Read(p []byte) (int, error) {
	if err := t.violation(); err != nil {
		return 0, err
	}
	n, rerr := t.src.Read(p)
	if n > 0 {
		if _, err := t.pw.Write(p[:n]); err != nil {
			return 0, err
		}
		t.sent += int64(n)
		t.mu.Lock()
		for !t.finished && t.waitAt != t.sent {
			t.cond.Wait()
		}
		t.mu.Unlock()
		if err := t.violation(); err != nil {
			return 0, err
		}
	}
	if rerr != nil {
		if rerr != io.EOF {
			t.abort()
		}
		t.pw.Close()
		<-t.done
		if err := t.violation(); err != nil {
			return 0, err
		}
	}
	return n, rerr
}

func (t *tarInspector) violation() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}

// close stops the parser; call it when the consumer is done reading.
func (t *tarInspector) close() {
	t.abort()
	t.pw.Close()
	<-t.done
}

func (t *tarInspector) abort() {
	t.mu.Lock()
	t.aborted = true
	t.mu.Unlock()
}

// TarGuard exposes the image tar inspector to other packages: it wraps a
// docker-save stream and fails the read of any archive that names a tag other
// than the one ref (or its familiar spellings, such as nginx for nginx:latest
// or library/nginx:1 for docker.io/library/nginx:1).
type TarGuard struct{ insp *tarInspector }

// ErrForeignTags is the violation a TarGuard reports for a foreign tag.
var ErrForeignTags = errForeignTags

// NewTarGuard wraps src; call Close when the consumer is done.
func NewTarGuard(src io.Reader, ref string) *TarGuard {
	insp := newTarInspector(src, ref)
	insp.also = refSpellings(ref)
	return &TarGuard{insp: insp}
}

func (g *TarGuard) Read(p []byte) (int, error) { return g.insp.Read(p) }

// Violation returns the first refusal, or nil.
func (g *TarGuard) Violation() error { return g.insp.violation() }

// Close stops the inspector.
func (g *TarGuard) Close() { g.insp.close() }

// refSpellings lists the names docker save can print for ref: the implicit
// :latest tag and the docker.io/library/ prefixes are dropped or added.
func refSpellings(ref string) map[string]bool {
	out := map[string]bool{}
	bases := []string{ref}
	for _, prefix := range []string{"docker.io/library/", "index.docker.io/library/", "docker.io/", "index.docker.io/"} {
		if rest, ok := strings.CutPrefix(ref, prefix); ok {
			bases = append(bases, rest)
		}
	}
	for _, b := range bases {
		out[b] = true
		last := b[strings.LastIndex(b, "/")+1:]
		if !strings.ContainsAny(last, ":@") {
			out[b+":latest"] = true
		}
	}
	return out
}
