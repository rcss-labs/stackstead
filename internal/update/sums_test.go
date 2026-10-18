package update

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChecksumFor(t *testing.T) {
	h1 := strings.Repeat("ab", 32)
	h2 := strings.Repeat("CD", 32)
	good := h1 + "  stackstead_0.3.0_linux_amd64.tar.gz\n" + h2 + " *install.sh\n\n"

	got, err := ChecksumFor([]byte(good), "stackstead_0.3.0_linux_amd64.tar.gz")
	if err != nil || got != h1 {
		t.Fatalf("two-space line: %q, %v", got, err)
	}
	got, err = ChecksumFor([]byte(good), "install.sh")
	if err != nil || got != strings.ToLower(h2) {
		t.Fatalf("star line must return lower-case digest: %q, %v", got, err)
	}
	if _, err := ChecksumFor([]byte(good), "nope"); !errors.Is(err, ErrAssetMissing) {
		t.Errorf("absent name err = %v, want ErrAssetMissing", err)
	}

	for name, doc := range map[string]string{
		"short line":     "abc  file\n",
		"non-hex":        strings.Repeat("zz", 32) + "  file\n",
		"slash in name":  h1 + "  dir/file\n",
		"dotdot":         h1 + "  ..\n",
		"duplicate":      h1 + "  file\n" + h1 + "  file\n",
		"bad separator":  h1 + "   file\n",
		"single space":   h1 + " file\n",
		"tab in name":    h1 + "  fi\tle\n",
		"backslash name": h1 + "  fi\\le\n",
	} {
		if _, err := ChecksumFor([]byte(doc), "file"); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: err = %v, want ErrMalformed", name, err)
		}
	}
}

func TestVerifyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "asset")
	if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("payload"))
	want := hex.EncodeToString(sum[:])

	if err := VerifyFile(path, want); err != nil {
		t.Errorf("matching digest: %v", err)
	}
	if err := VerifyFile(path, strings.ToUpper(want)); err != nil {
		t.Errorf("upper-case digest must match: %v", err)
	}
	if err := VerifyFile(path, strings.Repeat("0", 64)); !errors.Is(err, ErrChecksum) {
		t.Errorf("mismatch err = %v, want ErrChecksum", err)
	}
	err := VerifyFile(filepath.Join(t.TempDir(), "missing"), want)
	if err == nil || errors.Is(err, ErrChecksum) {
		t.Errorf("missing file err = %v, want a non-checksum error", err)
	}
}
