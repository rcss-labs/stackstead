package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

var (
	// ErrEntryOrder is returned when an entry is added after a later section.
	ErrEntryOrder = errors.New("backup: archive entry out of order")
	// ErrUnsafePath is returned for entry names or link targets that could escape the archive.
	ErrUnsafePath = errors.New("backup: unsafe archive path")
)

// Sections in the order they must appear; manifest.json is last.
var sectionRank = map[string]int{"base": 0, "volumes": 1, "images": 2, ManifestEntryName: 3}

func topLevel(name string) string {
	first, _, _ := strings.Cut(name, "/")
	return first
}

// Writer writes a gzip-compressed tar archive in the fixed section order
// base/ -> volumes/ -> images/ -> manifest.json.
type Writer struct {
	gz   *gzip.Writer
	tw   *tar.Writer
	rank int
}

// NewWriter returns a Writer streaming to w. Close does not close w.
func NewWriter(w io.Writer) *Writer {
	gz := gzip.NewWriter(w)
	return &Writer{gz: gz, tw: tar.NewWriter(gz)}
}

func (w *Writer) enter(name string) error {
	top := topLevel(name)
	rank, ok := sectionRank[top]
	if !ok || top == ManifestEntryName {
		return fmt.Errorf("backup: entry %q is not under base/, volumes/ or images/", name)
	}
	if rank < w.rank {
		return fmt.Errorf("%w: %q after a later section", ErrEntryOrder, name)
	}
	w.rank = rank
	return nil
}

// AddFile writes one entry. hdr.Size must be set when hdr is given; with a nil
// hdr, r is read fully into memory and written as a regular 0644 file.
func (w *Writer) AddFile(name string, hdr *tar.Header, r io.Reader) error {
	if err := w.enter(name); err != nil {
		return err
	}
	var h tar.Header
	if hdr != nil {
		h = *hdr
	} else {
		data, err := io.ReadAll(r)
		if err != nil {
			return err
		}
		h = tar.Header{Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(data))}
		r = bytes.NewReader(data)
	}
	h.Name = name
	if err := w.tw.WriteHeader(&h); err != nil {
		return err
	}
	if h.Size > 0 && h.Typeflag == tar.TypeReg {
		if _, err := io.Copy(w.tw, r); err != nil {
			return err
		}
	}
	return nil
}

// AddTarStream re-emits every entry of the inner tar stream r under prefix and
// returns the number of content bytes written.
func (w *Writer) AddTarStream(prefix string, r io.Reader) (int64, error) {
	prefix = strings.TrimSuffix(prefix, "/") + "/"
	if err := w.enter(prefix); err != nil {
		return 0, err
	}
	var written int64
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return written, nil
		}
		if err != nil {
			return written, err
		}
		out := *hdr
		inner := path.Clean(hdr.Name)
		if inner == "." {
			out.Name = prefix
		} else {
			out.Name = prefix + inner
		}
		if hdr.Typeflag == tar.TypeDir && !strings.HasSuffix(out.Name, "/") {
			out.Name += "/"
		}
		if hdr.Typeflag == tar.TypeLink {
			out.Linkname = prefix + path.Clean(hdr.Linkname)
		}
		if err := w.tw.WriteHeader(&out); err != nil {
			return written, err
		}
		if hdr.Typeflag == tar.TypeReg {
			n, err := io.Copy(w.tw, tr)
			written += n
			if err != nil {
				return written, err
			}
		}
	}
}

// Close writes manifest.json as the last entry, then closes tar and gzip.
func (w *Writer) Close(m Manifest) error {
	if m.Format == 0 {
		m.Format = ManifestFormat
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	w.rank = sectionRank[ManifestEntryName]
	if err := w.tw.WriteHeader(&tar.Header{Name: ManifestEntryName, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(data)), ModTime: m.CreatedAt}); err != nil {
		return err
	}
	if _, err := w.tw.Write(data); err != nil {
		return err
	}
	if err := w.tw.Close(); err != nil {
		return err
	}
	return w.gz.Close()
}

// Reader reads an archive written by Writer, validating entry paths.
type Reader struct {
	gz       *gzip.Reader
	tr       *tar.Reader
	lastName string
	pending  *pendingEntry
}

type pendingEntry struct {
	hdr  *tar.Header
	body io.Reader
}

// NewReader opens the gzip stream of an archive.
func NewReader(r io.Reader) (*Reader, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("backup: not a gzip archive: %w", err)
	}
	return &Reader{gz: gz, tr: tar.NewReader(gz)}, nil
}

// Next returns the next entry, or io.EOF at the end of the archive. A damaged
// or truncated archive yields an error naming the last entry read successfully.
// The body is only valid until the next call to Next.
func (r *Reader) Next() (*tar.Header, io.Reader, error) {
	if p := r.pending; p != nil {
		r.pending = nil
		return p.hdr, p.body, nil
	}
	hdr, err := r.tr.Next()
	if errors.Is(err, io.EOF) {
		return nil, nil, io.EOF
	}
	if err != nil {
		return nil, nil, fmt.Errorf("backup: archive damaged or truncated after %q: %w", r.lastName, err)
	}
	if err := validateEntry(hdr); err != nil {
		return nil, nil, err
	}
	r.lastName = hdr.Name
	return hdr, r.tr, nil
}

// Unread hands one entry back so the next Next call returns it again.
// The body must not have been consumed.
func (r *Reader) Unread(hdr *tar.Header, body io.Reader) {
	r.pending = &pendingEntry{hdr: hdr, body: body}
}

func validateEntry(hdr *tar.Header) error {
	name := hdr.Name
	if name == "" || strings.HasPrefix(name, "/") || hasDotDot(name) {
		return fmt.Errorf("%w: entry name %q", ErrUnsafePath, name)
	}
	clean := path.Clean(name)
	top := topLevel(clean)
	switch hdr.Typeflag {
	case tar.TypeLink:
		// Hardlink targets are archive-relative.
		target := path.Clean(hdr.Linkname)
		if strings.HasPrefix(hdr.Linkname, "/") || target == ".." || strings.HasPrefix(target, "../") || topLevel(target) != top {
			return fmt.Errorf("%w: hardlink %q -> %q", ErrUnsafePath, name, hdr.Linkname)
		}
	case tar.TypeSymlink:
		if strings.HasPrefix(hdr.Linkname, "/") {
			// Volume contents are extracted by Docker inside the container, where an
			// absolute target is resolved in the container; elsewhere it would hit the host.
			if top == "volumes" {
				return nil
			}
			return fmt.Errorf("%w: symlink %q -> %q", ErrUnsafePath, name, hdr.Linkname)
		}
		target := path.Join(path.Dir(clean), hdr.Linkname)
		if target == ".." || strings.HasPrefix(target, "../") || topLevel(target) != top {
			return fmt.Errorf("%w: symlink %q -> %q", ErrUnsafePath, name, hdr.Linkname)
		}
	}
	return nil
}

func hasDotDot(name string) bool {
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

// TarStreamUnder rebuilds an inner tar stream (prefix stripped) from the
// consecutive entries under prefix, starting with first/firstBody (an entry
// already returned by r.Next, or nil to start with the next entry). Reading
// stops at the first entry outside prefix, which is handed back with
// r.Unread; only that entry's header has been read. The result must be read to
// EOF before r is used again.
func TarStreamUnder(r *Reader, prefix string, first *tar.Header, firstBody io.Reader) io.Reader {
	prefix = strings.TrimSuffix(prefix, "/") + "/"
	s := &tarUnder{r: r, prefix: prefix, first: first, firstBody: firstBody}
	s.tw = tar.NewWriter(&s.buf)
	return s
}

type tarUnder struct {
	r         *Reader
	prefix    string
	first     *tar.Header
	firstBody io.Reader
	tw        *tar.Writer
	buf       bytes.Buffer
	body      io.Reader
	done      bool
	err       error
	scratch   [32 << 10]byte
}

func (s *tarUnder) Read(p []byte) (int, error) {
	for s.buf.Len() == 0 {
		if s.done {
			return 0, s.err
		}
		s.step()
	}
	return s.buf.Read(p)
}

func (s *tarUnder) finish(err error) {
	s.done = true
	if err == nil {
		err = s.tw.Close()
	}
	if err == nil {
		err = io.EOF
	}
	s.err = err
}

func (s *tarUnder) step() {
	if s.body != nil {
		n, err := s.body.Read(s.scratch[:])
		if n > 0 {
			if _, werr := s.tw.Write(s.scratch[:n]); werr != nil {
				s.finish(werr)
				return
			}
		}
		if errors.Is(err, io.EOF) {
			s.body = nil
		} else if err != nil {
			s.finish(err)
		}
		return
	}
	hdr, body := s.first, s.firstBody
	s.first, s.firstBody = nil, nil
	if hdr == nil {
		var err error
		hdr, body, err = s.r.Next()
		if errors.Is(err, io.EOF) {
			s.finish(nil)
			return
		}
		if err != nil {
			s.finish(err)
			return
		}
	}
	rel, ok := stripPrefix(hdr.Name, s.prefix)
	if !ok {
		s.r.Unread(hdr, body)
		s.finish(nil)
		return
	}
	out := *hdr
	out.Name = rel
	if hdr.Typeflag == tar.TypeLink {
		if linkRel, ok := stripPrefix(hdr.Linkname, s.prefix); ok {
			out.Linkname = linkRel
		}
	}
	if err := s.tw.WriteHeader(&out); err != nil {
		s.finish(err)
		return
	}
	if hdr.Typeflag == tar.TypeReg {
		s.body = body
	}
}

func stripPrefix(name, prefix string) (string, bool) {
	if name == prefix || name+"/" == prefix {
		return "./", true
	}
	if rest, ok := strings.CutPrefix(name, prefix); ok {
		return rest, true
	}
	return "", false
}
