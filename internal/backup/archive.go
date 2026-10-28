package backup

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/rcss-labs/stackstead/internal/secret"
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
	gz    *gzip.Writer
	tw    *tar.Writer
	rank  int
	guard pathGuard
}

// NewWriter returns a Writer streaming to w. Close does not close w.
func NewWriter(w io.Writer) *Writer {
	gz := gzip.NewWriter(w)
	return &Writer{gz: gz, tw: tar.NewWriter(gz), guard: newPathGuard()}
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

// AddFile writes one entry. hdr.Size must be set; name overrides hdr.Name.
func (w *Writer) AddFile(name string, hdr *tar.Header, r io.Reader) error {
	if hdr == nil {
		return errors.New("backup: AddFile needs a header")
	}
	if err := w.enter(name); err != nil {
		return err
	}
	h := *hdr
	h.Name = name
	if err := w.guard.check(&h); err != nil {
		return err
	}
	if err := w.tw.WriteHeader(&h); err != nil {
		return err
	}
	if h.Size > 0 {
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
		inner := path.Clean(strings.TrimLeft(hdr.Name, "/"))
		if inner == "." {
			out.Name = prefix
		} else {
			out.Name = prefix + inner
		}
		if hdr.Typeflag == tar.TypeDir && !strings.HasSuffix(out.Name, "/") {
			out.Name += "/"
		}
		if hdr.Typeflag == tar.TypeLink {
			out.Linkname = prefix + path.Clean(strings.TrimLeft(hdr.Linkname, "/"))
		}
		if err := w.guard.check(&out); err != nil {
			return written, err
		}
		if err := w.tw.WriteHeader(&out); err != nil {
			return written, err
		}
		if hdr.Typeflag == tar.TypeReg {
			n, err := io.Copy(w.tw, tr)
			written += n
			if err != nil {
				// Fill the rest of the entry with zeros so the archive stays a
				// valid tar and later entries can still be written.
				if _, perr := io.CopyN(w.tw, zeroReader{}, hdr.Size-n); perr != nil {
					return written, fmt.Errorf("%s: %w (padding failed: %v)", out.Name, err, perr)
				}
				return written, fmt.Errorf("%s: %w", out.Name, err)
			}
		}
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
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
	raw      io.Reader // the decrypted stream under gz, nil for a plaintext archive
	gz       *gzip.Reader
	tr       *tar.Reader
	lastName string
	pending  *pendingEntry
	guard    pathGuard
}

type pendingEntry struct {
	hdr  *tar.Header
	body io.Reader
}

// newPlainReader opens the gzip stream of a plaintext archive. Everything
// outside this file reads archives through OpenArchive.
func newPlainReader(r io.Reader) (*Reader, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("backup: not a gzip archive: %w", err)
	}
	return &Reader{gz: gz, tr: tar.NewReader(gz), guard: newPathGuard()}, nil
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
		return nil, nil, fmt.Errorf("backup: archive damaged or truncated in or after %q: %w", r.lastName, err)
	}
	if err := r.guard.check(hdr); err != nil {
		return nil, nil, err
	}
	r.lastName = hdr.Name
	return hdr, r.tr, nil
}

// Finish reads the rest of the stream once Next has returned io.EOF. The tar
// reader stops at its end marker, so without this the gzip trailer and, for
// an encrypted archive, the final chunk would never be checked.
func (r *Reader) Finish() error {
	if _, err := io.Copy(io.Discard, r.gz); err != nil {
		return damaged(fmt.Errorf("end of archive: %w", err))
	}
	if r.raw != nil {
		if _, err := io.Copy(io.Discard, r.raw); err != nil {
			return damaged(fmt.Errorf("end of archive: %w", err))
		}
	}
	return nil
}

// Unread hands one entry back so the next Next call returns it again.
// The body must not have been consumed.
func (r *Reader) Unread(hdr *tar.Header, body io.Reader) {
	r.pending = &pendingEntry{hdr: hdr, body: body}
}

// pathGuard validates entry names and link targets. Links must stay inside the
// extraction root of their entry, and nothing may be written below a symlink
// seen earlier in the archive.
type pathGuard struct {
	symlinks map[string]bool
}

func newPathGuard() pathGuard { return pathGuard{symlinks: map[string]bool{}} }

// extractionRoot returns the directory (with trailing "/") that an entry's
// links must stay within, or "" if the entry may not be a link.
func extractionRoot(name string) string {
	parts := strings.Split(name, "/")
	depth := 0
	switch parts[0] {
	case "volumes": // volumes/<stack>/<key>/
		depth = 3
	case "images": // images/<stack>/
		depth = 2
	case "base":
		switch {
		case len(parts) > 1 && parts[1] == "stacks":
			depth = 3 // base/stacks/<stack>/
		case len(parts) > 1 && parts[1] == "images-library":
			depth = 2
		default:
			return ""
		}
	default:
		return ""
	}
	if len(parts) <= depth {
		return ""
	}
	return strings.Join(parts[:depth], "/") + "/"
}

func within(root, target string) bool {
	return root != "" && (strings.HasPrefix(target, root) || target+"/" == root)
}

func (g *pathGuard) check(hdr *tar.Header) error {
	name := hdr.Name
	if name == "" || strings.HasPrefix(name, "/") || hasDotDot(name) {
		return fmt.Errorf("%w: entry name %q", ErrUnsafePath, name)
	}
	clean := path.Clean(name)
	if _, ok := sectionRank[topLevel(clean)]; !ok {
		return fmt.Errorf("%w: unexpected top-level entry %q", ErrUnsafePath, name)
	}
	if g.symlinks[clean] {
		return fmt.Errorf("%w: %q replaces an earlier symlink", ErrUnsafePath, name)
	}
	if dir, ok := g.symlinkAtOrAbove(path.Dir(clean)); ok {
		return fmt.Errorf("%w: %q lies below symlink %q", ErrUnsafePath, name, dir)
	}
	switch hdr.Typeflag {
	case tar.TypeLink:
		// Hardlink targets are archive-relative.
		// link() follows intermediate symlinks, so the target must be a plain
		// path: no "..", and not at or below any earlier symlink.
		target := path.Clean(hdr.Linkname)
		// Under volumes/ a hardlink may point at a symlink itself, not through one.
		linkPath := target
		if topLevel(clean) == "volumes" {
			linkPath = path.Dir(target)
		}
		_, viaSymlink := g.symlinkAtOrAbove(linkPath)
		if strings.HasPrefix(hdr.Linkname, "/") || hasDotDot(hdr.Linkname) || viaSymlink || !within(extractionRoot(clean), target) {
			return fmt.Errorf("%w: hardlink %q -> %q", ErrUnsafePath, name, hdr.Linkname)
		}
	case tar.TypeSymlink:
		g.symlinks[clean] = true
		// Volume contents are only ever restored by Docker inside the helper
		// container, never written to the host by Stackstead, so symlink targets
		// there are unrestricted. Everywhere else they are extracted on the host.
		if topLevel(clean) == "volumes" && extractionRoot(clean) != "" {
			return nil
		}
		if strings.HasPrefix(hdr.Linkname, "/") {
			return fmt.Errorf("%w: symlink %q -> %q", ErrUnsafePath, name, hdr.Linkname)
		}
		// ".." only as leading components keeps the lexical check sound: every
		// earlier symlink already resolves inside the root, so following a
		// chain of them cannot leave it.
		if !leadingDotDotOnly(hdr.Linkname) || !within(extractionRoot(clean), path.Join(path.Dir(clean), hdr.Linkname)) {
			return fmt.Errorf("%w: symlink %q -> %q", ErrUnsafePath, name, hdr.Linkname)
		}
	}
	return nil
}

func (g *pathGuard) symlinkAtOrAbove(p string) (string, bool) {
	for ; p != "." && p != "/" && p != ""; p = path.Dir(p) {
		if g.symlinks[p] {
			return p, true
		}
	}
	return "", false
}

func leadingDotDotOnly(target string) bool {
	parts := strings.Split(target, "/")
	i := 0
	for i < len(parts) && (parts[i] == ".." || parts[i] == ".") {
		i++
	}
	return !hasDotDot(strings.Join(parts[i:], "/"))
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
	peakBuf   int // largest buffered output, for tests
}

func (s *tarUnder) Read(p []byte) (int, error) {
	for s.buf.Len() == 0 {
		if s.done {
			return 0, s.err
		}
		s.step()
	}
	if n := s.buf.Len(); n > s.peakBuf {
		s.peakBuf = n
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
		_ = n
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
		linkRel, ok := stripPrefix(hdr.Linkname, s.prefix)
		if !ok {
			s.finish(fmt.Errorf("%w: hardlink %q -> %q leaves %q", ErrUnsafePath, hdr.Name, hdr.Linkname, s.prefix))
			return
		}
		out.Linkname = linkRel
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

// OpenOptions say how OpenArchive finds the passphrase of an encrypted archive
// and whether a plaintext archive is acceptable.
type OpenOptions struct {
	// Passphrase is used when set; it wins over KeyDir.
	Passphrase secret.Secret
	// KeyDir is tried for <KeyDir>/backup.key when Passphrase is empty.
	KeyDir string
	// AllowUnencrypted accepts a plaintext (unauthenticated) archive.
	AllowUnencrypted bool
}

// ArchiveInfo describes how an opened archive was protected.
type ArchiveInfo struct {
	Encrypted bool
	KeyID     string
	key       *Key
}

// resolver finds the key of an encrypted archive from its header: the explicit
// passphrase first, then the key file. Either is derived with the header's own
// parameters, so an archive keeps opening whatever the defaults become.
func (o OpenOptions) resolver() KeyResolver {
	return func(p KeyParams, keyID string) (*Key, error) {
		if o.Passphrase != "" {
			return DeriveKey(o.Passphrase.Reveal(), p)
		}
		if o.KeyDir != "" {
			kf, ok, err := readKeyFile(o.KeyDir)
			if err != nil {
				return nil, err
			}
			if ok {
				k, err := DeriveKey(kf.Passphrase, p)
				if err != nil {
					return nil, err
				}
				if k.ID() != keyID {
					return nil, fmt.Errorf("%w: the stored passphrase is key %s, the archive was written with key %s", ErrWrongPassphrase, kf.KeyID, keyID)
				}
				return k, nil
			}
		}
		return nil, ErrPassphraseRequired
	}
}

// OpenArchive opens an archive stream, encrypted or not, and is the only way
// archives are read. An encrypted archive is authenticated chunk by chunk as
// it is read; a plaintext one is refused unless opts.AllowUnencrypted says the
// caller has been told it is unauthenticated.
func OpenArchive(r io.Reader, opts OpenOptions) (*Reader, ArchiveInfo, error) {
	br := bufio.NewReaderSize(r, 64<<10)
	encrypted, err := sniffEncrypted(br)
	if err != nil {
		return nil, ArchiveInfo{}, err
	}
	if encrypted {
		var key *Key
		resolve := opts.resolver()
		dr, err := NewDecryptReader(br, func(p KeyParams, id string) (*Key, error) {
			k, err := resolve(p, id)
			key = k
			return k, err
		})
		if err != nil {
			return nil, ArchiveInfo{}, err
		}
		rd, err := newPlainReader(dr)
		if err != nil {
			if errors.Is(err, ErrArchiveTampered) {
				return nil, ArchiveInfo{}, errors.Unwrap(err) // drop the "not a gzip archive" prefix
			}
			return nil, ArchiveInfo{}, damaged(err)
		}
		rd.raw = dr
		return rd, ArchiveInfo{Encrypted: true, KeyID: dr.KeyID(), key: key}, nil
	}
	head, _ := br.Peek(2)
	if len(head) < 2 || head[0] != 0x1f || head[1] != 0x8b {
		return nil, ArchiveInfo{}, damaged(errors.New("not a Stackstead archive"))
	}
	if !opts.AllowUnencrypted {
		return nil, ArchiveInfo{}, ErrUnencryptedArchive
	}
	rd, err := newPlainReader(br)
	if err != nil {
		return nil, ArchiveInfo{}, damaged(err)
	}
	return rd, ArchiveInfo{}, nil
}
