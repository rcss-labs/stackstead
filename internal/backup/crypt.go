package backup

import (
	"bufio"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

// Encrypted archive stream (all integers big endian):
//
//	header (63 bytes, plaintext, used as the AAD of every chunk):
//	  magic "SSBACKUP" | version u8 (1) | kdf u8 (1 = argon2id)
//	  | argon time u32 | argon memory KiB u32 | argon threads u8
//	  | keySalt [16] | archiveSalt [16] | keyID [8] | chunkSize u32
//	body: ChaCha20-Poly1305 chunks of at most chunkSize plaintext bytes
//	  nonce = counter u64 | 3 zero bytes | final flag (0 or 1)
//
// The counter orders the chunks and the final flag marks the last one, so
// truncation, reordering, duplication and trailing data are all detected.
const (
	cryptMagic      = "SSBACKUP"
	cryptVersion    = 1
	cryptKDFArgon2  = 1
	cryptChunkSize  = 64 << 10
	cryptHeaderLen  = 63
	cryptKeyIDLen   = 8
	cryptMasterLen  = 32
	argonTime       = 3
	argonMemoryKiB  = 64 << 10
	argonThreads    = 4
	maxArgonTime    = 10
	maxArgonMemory  = 256 << 10 // KiB; a crafted header must not make a reader allocate more
	maxArgonThreads = 16
)

var (
	// ErrArchiveTampered reports an encrypted archive that failed authentication:
	// modified, truncated, reordered, extended or not an Stackstead stream.
	ErrArchiveTampered = errors.New("backup: archive failed authentication (modified, truncated or corrupt)")
	// ErrWrongPassphrase reports a passphrase that does not match the archive's key id.
	ErrWrongPassphrase = errors.New("backup: wrong passphrase for this archive")
	// ErrPassphraseRequired reports an encrypted archive opened without any passphrase.
	ErrPassphraseRequired = errors.New("backup: this archive is encrypted and needs its passphrase")
	// ErrUnencryptedArchive reports a plaintext archive opened without acknowledgement.
	ErrUnencryptedArchive = errors.New("backup: this archive is not encrypted, so it is not authenticated either")
	// ErrNoKeyFile reports encryption that is enabled but has no stored passphrase.
	ErrNoKeyFile = errors.New("backup: no backup passphrase is stored")
)

// KeyParams are the Argon2id parameters and the per-passphrase salt.
type KeyParams struct {
	KeySalt   [16]byte
	Time      uint32
	MemoryKiB uint32
	Threads   uint8
}

// NewKeyParams returns production parameters with a fresh random salt.
func NewKeyParams() KeyParams {
	p := KeyParams{Time: argonTime, MemoryKiB: argonMemoryKiB, Threads: argonThreads}
	mustRandom(p.KeySalt[:])
	return p
}

func (p KeyParams) validate() error {
	if p.Time < 1 || p.Time > maxArgonTime ||
		p.Threads < 1 || p.Threads > maxArgonThreads ||
		p.MemoryKiB < 8*uint32(p.Threads) || p.MemoryKiB > maxArgonMemory {
		return fmt.Errorf("backup: unsupported key derivation parameters (time %d, memory %d KiB, threads %d)", p.Time, p.MemoryKiB, p.Threads)
	}
	return nil
}

func mustRandom(b []byte) {
	if _, err := rand.Read(b); err != nil {
		panic("backup: system random source failed: " + err.Error())
	}
}

// Key is a passphrase stretched with Argon2id plus the subkeys derived from it.
type Key struct {
	params     KeyParams
	master     []byte
	id         [cryptKeyIDLen]byte
	sidecarKey []byte
}

// DeriveKey stretches passphrase with the given parameters.
func DeriveKey(passphrase string, p KeyParams) (*Key, error) {
	if passphrase == "" {
		return nil, ErrPassphraseRequired
	}
	if err := p.validate(); err != nil {
		return nil, err
	}
	master := argon2.IDKey([]byte(passphrase), p.KeySalt[:], p.Time, p.MemoryKiB, p.Threads, cryptMasterLen)
	k := &Key{params: p, master: master}
	copy(k.id[:], k.subkey(nil, "key-id", cryptKeyIDLen))
	k.sidecarKey = k.subkey(nil, "sidecar", 32)
	return k, nil
}

func (k *Key) subkey(salt []byte, purpose string, n int) []byte {
	out := make([]byte, n)
	if _, err := io.ReadFull(hkdf.New(sha256.New, k.master, salt, []byte("stackstead-backup/v1/"+purpose)), out); err != nil {
		panic("backup: hkdf: " + err.Error())
	}
	return out
}

// ID is a short key-check value, stable for one (passphrase, salt). It tells
// which passphrase an archive needs without revealing anything useful.
func (k *Key) ID() string { return hex.EncodeToString(k.id[:]) }

// Params returns the derivation parameters, as recorded in archive headers.
func (k *Key) Params() KeyParams { return k.params }

type cryptHeader [cryptHeaderLen]byte

func (k *Key) header(archiveSalt []byte) cryptHeader {
	var h cryptHeader
	copy(h[:8], cryptMagic)
	h[8], h[9] = cryptVersion, cryptKDFArgon2
	binary.BigEndian.PutUint32(h[10:], k.params.Time)
	binary.BigEndian.PutUint32(h[14:], k.params.MemoryKiB)
	h[18] = k.params.Threads
	copy(h[19:35], k.params.KeySalt[:])
	copy(h[35:51], archiveSalt)
	copy(h[51:59], k.id[:])
	binary.BigEndian.PutUint32(h[59:], cryptChunkSize)
	return h
}

func chunkNonce(counter uint64, final bool) []byte {
	n := make([]byte, chacha20poly1305.NonceSize)
	binary.BigEndian.PutUint64(n, counter)
	if final {
		n[11] = 1
	}
	return n
}

type encryptWriter struct {
	w       io.Writer
	aead    cipher.AEAD
	header  cryptHeader
	started bool
	buf     []byte
	counter uint64
	closed  bool
	err     error
}

// NewEncryptWriter returns a WriteCloser that encrypts everything written to
// it into w. The header goes out with the first chunk. Close seals the final
// chunk and does not close w.
func NewEncryptWriter(w io.Writer, k *Key) (io.WriteCloser, error) {
	archiveSalt := make([]byte, 16)
	mustRandom(archiveSalt)
	aead, err := chacha20poly1305.New(k.subkey(archiveSalt, "stream", chacha20poly1305.KeySize))
	if err != nil {
		return nil, err
	}
	return &encryptWriter{w: w, aead: aead, header: k.header(archiveSalt), buf: make([]byte, 0, cryptChunkSize)}, nil
}

func (e *encryptWriter) seal(final bool) error {
	if !e.started {
		e.started = true
		if _, err := e.w.Write(e.header[:]); err != nil {
			return err
		}
	}
	out := e.aead.Seal(nil, chunkNonce(e.counter, final), e.buf, e.header[:])
	e.counter++
	e.buf = e.buf[:0]
	_, err := e.w.Write(out)
	return err
}

func (e *encryptWriter) Write(p []byte) (int, error) {
	if e.closed {
		return 0, errors.New("backup: write to closed encrypt writer")
	}
	if e.err != nil {
		return 0, e.err
	}
	written := 0
	for len(p) > 0 {
		// A full buffer is only sealed once more data follows, so Close can
		// flag the true last chunk.
		if len(e.buf) == cryptChunkSize {
			if e.err = e.seal(false); e.err != nil {
				return written, e.err
			}
		}
		n := min(len(p), cryptChunkSize-len(e.buf))
		e.buf = append(e.buf, p[:n]...)
		p = p[n:]
		written += n
	}
	return written, nil
}

func (e *encryptWriter) Close() error {
	if e.closed {
		return e.err
	}
	e.closed = true
	if e.err == nil {
		e.err = e.seal(true)
	}
	return e.err
}

// KeyResolver supplies the key for an archive from the header's parameters and
// key id. It is only called after the parameters passed their bounds check.
type KeyResolver func(p KeyParams, keyID string) (*Key, error)

// DecryptReader decrypts a stream written by NewEncryptWriter. A chunk is
// released only after its tag verified.
type DecryptReader struct {
	br      *bufio.Reader
	aead    cipher.AEAD
	header  cryptHeader
	keyID   string
	sealed  []byte
	plain   []byte
	counter uint64
	done    bool
	err     error
}

func tampered(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrArchiveTampered, fmt.Sprintf(format, args...))
}

// NewDecryptReader parses and bounds-checks the header, resolves the key and
// checks it against the header's key id. It returns ErrWrongPassphrase before
// producing any plaintext.
func NewDecryptReader(r io.Reader, resolve KeyResolver) (*DecryptReader, error) {
	br, ok := r.(*bufio.Reader)
	if !ok {
		br = bufio.NewReader(r)
	}
	var h cryptHeader
	if _, err := io.ReadFull(br, h[:]); err != nil {
		return nil, tampered("header: %v", err)
	}
	if string(h[:8]) != cryptMagic {
		return nil, tampered("not an encrypted Stackstead archive")
	}
	if h[8] != cryptVersion || h[9] != cryptKDFArgon2 {
		return nil, tampered("unsupported format version %d/%d", h[8], h[9])
	}
	if binary.BigEndian.Uint32(h[59:]) != cryptChunkSize {
		return nil, tampered("unsupported chunk size")
	}
	p := KeyParams{
		Time:      binary.BigEndian.Uint32(h[10:]),
		MemoryKiB: binary.BigEndian.Uint32(h[14:]),
		Threads:   h[18],
	}
	copy(p.KeySalt[:], h[19:35])
	if err := p.validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrArchiveTampered, err)
	}
	keyID := hex.EncodeToString(h[51:59])
	key, err := resolve(p, keyID)
	if err != nil {
		return nil, err
	}
	if key == nil || !hmac.Equal(key.id[:], h[51:59]) {
		return nil, fmt.Errorf("%w (archive key %s)", ErrWrongPassphrase, keyID)
	}
	aead, err := chacha20poly1305.New(key.subkey(h[35:51], "stream", chacha20poly1305.KeySize))
	if err != nil {
		return nil, err
	}
	return &DecryptReader{br: br, aead: aead, header: h, keyID: keyID, sealed: make([]byte, cryptChunkSize+aead.Overhead())}, nil
}

// KeyID is the key id recorded in the archive header.
func (d *DecryptReader) KeyID() string { return d.keyID }

func (d *DecryptReader) Read(p []byte) (int, error) {
	for len(d.plain) == 0 {
		if d.err != nil {
			return 0, d.err
		}
		if d.done {
			return 0, io.EOF
		}
		d.err = d.nextChunk()
	}
	n := copy(p, d.plain)
	d.plain = d.plain[n:]
	return n, nil
}

func (d *DecryptReader) nextChunk() error {
	n, err := io.ReadFull(d.br, d.sealed)
	final := false
	switch {
	case err == nil:
		// A full chunk is the last one only if nothing follows it.
		if _, perr := d.br.Peek(1); perr == io.EOF {
			final = true
		} else if perr != nil {
			return perr
		}
	case errors.Is(err, io.EOF):
		return tampered("truncated before the final chunk")
	case errors.Is(err, io.ErrUnexpectedEOF):
		final = true
	default:
		return err
	}
	plain, oerr := d.aead.Open(d.sealed[:0], chunkNonce(d.counter, final), d.sealed[:n], d.header[:])
	if oerr != nil {
		return tampered("chunk %d", d.counter)
	}
	d.counter++
	d.plain = plain
	d.done = final
	return nil
}

// sniffEncrypted reports whether the stream starts with the encrypted magic.
// It consumes nothing.
func sniffEncrypted(br *bufio.Reader) (bool, error) {
	head, err := br.Peek(len(cryptMagic))
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	return string(head) == cryptMagic, nil
}

// SidecarMAC authenticates the manifest that is stored next to an archive.
// The MAC field itself is excluded from the input.
func (k *Key) SidecarMAC(m Manifest) (string, error) {
	if m.Encryption != nil {
		enc := *m.Encryption
		enc.SidecarMAC = ""
		m.Encryption = &enc
	}
	data, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, k.sidecarKey)
	mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// VerifySidecar checks the MAC recorded in m. A missing or wrong MAC yields an
// ErrManifestMismatch.
func (k *Key) VerifySidecar(m Manifest) error {
	if m.Encryption == nil || m.Encryption.SidecarMAC == "" {
		return fmt.Errorf("%w: the sidecar carries no authentication code", ErrManifestMismatch)
	}
	want, err := k.SidecarMAC(m)
	if err != nil {
		return err
	}
	got, gerr := hex.DecodeString(m.Encryption.SidecarMAC)
	wantRaw, _ := hex.DecodeString(want)
	if gerr != nil || !hmac.Equal(got, wantRaw) {
		return fmt.Errorf("%w: the sidecar failed authentication", ErrManifestMismatch)
	}
	return nil
}
