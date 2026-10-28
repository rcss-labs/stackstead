package backup

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"testing"
	"time"
)

// Cheap Argon2 parameters keep the tests fast; production uses NewKeyParams.
func cheapParams() KeyParams {
	return KeyParams{Time: 1, MemoryKiB: 8192, Threads: 1}
}

func testKeyFor(t *testing.T, passphrase string, salt byte) *Key {
	t.Helper()
	p := cheapParams()
	p.KeySalt[0] = salt
	k, err := DeriveKey(passphrase, p)
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	return k
}

func fixedResolver(k *Key) KeyResolver {
	return func(KeyParams, string) (*Key, error) { return k, nil }
}

func encryptBytes(t *testing.T, k *Key, plain []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := NewEncryptWriter(&buf, k)
	if err != nil {
		t.Fatalf("NewEncryptWriter: %v", err)
	}
	if _, err := w.Write(plain); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return buf.Bytes()
}

// decryptAll returns everything decrypted before the first error and that
// error (nil on clean EOF).
func decryptAll(ct []byte, resolve KeyResolver) ([]byte, error) {
	d, err := NewDecryptReader(bytes.NewReader(ct), resolve)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(d)
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

const (
	testHeaderLen = 63
	testChunk     = 65536
	testSealed    = testChunk + 16
)

func TestCryptRoundTrip(t *testing.T) {
	k := testKeyFor(t, "correct horse battery", 1)
	for _, n := range []int{0, 1, 65535, 65536, 65537, 3 * 65536, 1 << 20} {
		plain := randomBytes(t, n)
		ct := encryptBytes(t, k, plain)
		got, err := decryptAll(ct, fixedResolver(k))
		if err != nil {
			t.Fatalf("size %d: %v", n, err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatalf("size %d: plaintext mismatch", n)
		}
		if n >= 16 && bytes.Contains(ct, plain[:min(n, 64)]) {
			t.Fatalf("size %d: ciphertext contains plaintext", n)
		}
	}
	plain := randomBytes(t, 1000)
	a, b := encryptBytes(t, k, plain), encryptBytes(t, k, plain)
	if bytes.Equal(a, b) {
		t.Fatal("two archives from one key are identical; archive salt not random")
	}
}

func TestDecryptRejectsFlippedByte(t *testing.T) {
	k := testKeyFor(t, "correct horse battery", 1)
	plain := randomBytes(t, 3*testChunk+100) // 3 full chunks + short final
	ct := encryptBytes(t, k, plain)
	offsets := map[string]int{
		"keySalt":      20,
		"archiveSalt":  40,
		"first chunk":  testHeaderLen + 10,
		"middle tag":   testHeaderLen + 2*testSealed - 5,
		"last chunk":   len(ct) - 3,
		"final tag":    len(ct) - 1,
		"chunk size":   testHeaderLen - 1,
		"version byte": 8,
	}
	for name, off := range offsets {
		t.Run(name, func(t *testing.T) {
			bad := bytes.Clone(ct)
			bad[off] ^= 0x01
			got, err := decryptAll(bad, fixedResolver(k))
			if !errors.Is(err, ErrArchiveTampered) {
				t.Fatalf("err = %v, want ErrArchiveTampered", err)
			}
			if len(got) > len(plain) || !bytes.Equal(got, plain[:len(got)]) {
				t.Fatal("returned bytes are not a prefix of the plaintext")
			}
			if off >= testHeaderLen && off < testHeaderLen+testSealed && len(got) != 0 {
				t.Fatalf("released %d bytes from a chunk whose tag fails", len(got))
			}
		})
	}
}

func TestDecryptRejectsTruncation(t *testing.T) {
	k := testKeyFor(t, "correct horse battery", 1)
	plain := randomBytes(t, 2*testChunk+500)
	ct := encryptBytes(t, k, plain)
	cuts := []int{0, 5, testHeaderLen - 1, testHeaderLen, testHeaderLen + 1,
		testHeaderLen + testSealed - 1, testHeaderLen + testSealed,
		testHeaderLen + 2*testSealed, len(ct) - 17, len(ct) - 1}
	for _, cut := range cuts {
		got, err := decryptAll(ct[:cut], fixedResolver(k))
		if !errors.Is(err, ErrArchiveTampered) {
			t.Fatalf("cut at %d: err = %v, want ErrArchiveTampered", cut, err)
		}
		if !bytes.Equal(got, plain[:len(got)]) {
			t.Fatalf("cut at %d: returned bytes are not a prefix", cut)
		}
	}
}

func TestDecryptRejectsReorderedAndDuplicatedChunks(t *testing.T) {
	k := testKeyFor(t, "correct horse battery", 1)
	ct := encryptBytes(t, k, randomBytes(t, 3*testChunk+10))
	chunk := func(i int) []byte { return ct[testHeaderLen+i*testSealed : testHeaderLen+(i+1)*testSealed] }
	header, last := ct[:testHeaderLen], ct[testHeaderLen+3*testSealed:]
	join := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	swapped := join(header, chunk(1), chunk(0), chunk(2), last)
	dup := join(header, chunk(0), chunk(0), chunk(1), chunk(2), last)
	for name, bad := range map[string][]byte{"swapped": swapped, "duplicated": dup} {
		if _, err := decryptAll(bad, fixedResolver(k)); !errors.Is(err, ErrArchiveTampered) {
			t.Fatalf("%s: err = %v, want ErrArchiveTampered", name, err)
		}
	}
}

func TestDecryptRejectsTrailingData(t *testing.T) {
	k := testKeyFor(t, "correct horse battery", 1)
	for _, n := range []int{testChunk, 500} { // full final chunk and short final chunk
		ct := encryptBytes(t, k, randomBytes(t, n))
		_, err := decryptAll(append(ct, 0x00), fixedResolver(k))
		if !errors.Is(err, ErrArchiveTampered) {
			t.Fatalf("size %d: err = %v, want ErrArchiveTampered", n, err)
		}
	}
}

func TestDecryptWrongPassphraseBeforePlaintext(t *testing.T) {
	right := testKeyFor(t, "correct horse battery", 1)
	ct := encryptBytes(t, right, randomBytes(t, 1000))
	var seen KeyParams
	var seenID string
	_, err := NewDecryptReader(bytes.NewReader(ct), func(p KeyParams, id string) (*Key, error) {
		seen, seenID = p, id
		return DeriveKey("not the passphrase", p)
	})
	if !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("err = %v, want ErrWrongPassphrase", err)
	}
	if seen != right.Params() || seenID != right.ID() {
		t.Fatalf("resolver saw %+v/%q, want %+v/%q", seen, seenID, right.Params(), right.ID())
	}
}

func TestDecryptRefusesAbsurdKDFParams(t *testing.T) {
	k := testKeyFor(t, "correct horse battery", 1)
	ct := encryptBytes(t, k, []byte("x"))
	cases := map[string]func(b []byte){
		"memory 4 GiB": func(b []byte) { b[14], b[15], b[16], b[17] = 0, 0x40, 0, 0 }, // 0x400000 KiB
		"time 1000":    func(b []byte) { b[10], b[11], b[12], b[13] = 0, 0, 0x03, 0xE8 },
		"zero threads": func(b []byte) { b[18] = 0 },
	}
	for name, mutate := range cases {
		bad := bytes.Clone(ct)
		mutate(bad)
		called := false
		_, err := NewDecryptReader(bytes.NewReader(bad), func(KeyParams, string) (*Key, error) {
			called = true
			return k, nil
		})
		if err == nil {
			t.Fatalf("%s: accepted", name)
		}
		if called {
			t.Fatalf("%s: resolver called before the parameters were bounds-checked", name)
		}
	}
}

func TestKeyIDStableAcrossArchives(t *testing.T) {
	a := testKeyFor(t, "correct horse battery", 1)
	if got := testKeyFor(t, "correct horse battery", 1).ID(); got != a.ID() {
		t.Fatalf("same passphrase and salt: %q != %q", got, a.ID())
	}
	if len(a.ID()) != 16 {
		t.Fatalf("ID length = %d, want 16 hex chars", len(a.ID()))
	}
	if testKeyFor(t, "correct horse battery", 2).ID() == a.ID() {
		t.Fatal("different salt gave the same ID")
	}
	if testKeyFor(t, "another passphrase!!", 1).ID() == a.ID() {
		t.Fatal("different passphrase gave the same ID")
	}
	ct := encryptBytes(t, a, []byte("x"))
	d, err := NewDecryptReader(bytes.NewReader(ct), fixedResolver(a))
	if err != nil {
		t.Fatal(err)
	}
	if d.KeyID() != a.ID() {
		t.Fatalf("DecryptReader.KeyID = %q, want %q", d.KeyID(), a.ID())
	}
}

func TestSniffEncrypted(t *testing.T) {
	k := testKeyFor(t, "correct horse battery", 1)
	ct := encryptBytes(t, k, []byte("x"))
	for name, tc := range map[string]struct {
		in   []byte
		want bool
	}{
		"encrypted": {ct, true},
		"gzip":      {[]byte{0x1f, 0x8b, 8, 0, 0, 0, 0, 0, 0, 0}, false},
		"short":     {[]byte{1}, false},
	} {
		got, err := sniffEncrypted(bufio.NewReader(bytes.NewReader(tc.in)))
		if err != nil || got != tc.want {
			t.Fatalf("%s: got %v, %v want %v", name, got, err, tc.want)
		}
	}
}

func sampleManifest() Manifest {
	return Manifest{
		Format: ManifestFormat, Host: "h", HostSlug: "h", InstanceID: "inst",
		CreatedAt:  time.Date(2026, 10, 28, 1, 2, 3, 0, time.UTC),
		Stacks:     []ManifestStack{{Name: "web", Volumes: []ManifestVolume{{Key: "v0", Kind: "volume", Name: "web_data"}}}},
		Encryption: &ManifestEncryption{KeyID: "abcd", KeySalt: "00"},
	}
}

func TestSidecarMACRoundTripAndTamper(t *testing.T) {
	k := testKeyFor(t, "correct horse battery", 1)
	m := sampleManifest()
	mac, err := k.SidecarMAC(m)
	if err != nil || mac == "" {
		t.Fatalf("SidecarMAC = %q, %v", mac, err)
	}
	m.Encryption.SidecarMAC = mac
	if err := k.VerifySidecar(m); err != nil {
		t.Fatalf("VerifySidecar: %v", err)
	}
	// The MAC field is excluded from its own input.
	if again, _ := k.SidecarMAC(m); again != mac {
		t.Fatal("MAC changed after the MAC field was filled in")
	}

	tamper := map[string]func(*Manifest){
		"volume name": func(m *Manifest) { m.Stacks[0].Volumes[0].Name = "other" },
		"created at":  func(m *Manifest) { m.CreatedAt = m.CreatedAt.Add(time.Second) },
		"key id":      func(m *Manifest) { m.Encryption.KeyID = "ffff" },
	}
	for name, f := range tamper {
		bad := sampleManifest()
		bad.Encryption.SidecarMAC = mac
		f(&bad)
		if err := k.VerifySidecar(bad); !errors.Is(err, ErrManifestMismatch) {
			t.Fatalf("%s: err = %v, want ErrManifestMismatch", name, err)
		}
	}
	plain := sampleManifest()
	plain.Encryption = nil
	if err := k.VerifySidecar(plain); !errors.Is(err, ErrManifestMismatch) {
		t.Fatalf("no encryption block: err = %v, want ErrManifestMismatch", err)
	}
	if err := testKeyFor(t, "another passphrase!!", 1).VerifySidecar(m); !errors.Is(err, ErrManifestMismatch) {
		t.Fatalf("wrong key: err = %v, want ErrManifestMismatch", err)
	}
}
