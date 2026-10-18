package update

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
)

var (
	ErrMalformed    = errors.New("malformed SHA256SUMS")
	ErrAssetMissing = errors.New("asset not listed in SHA256SUMS")
	ErrChecksum     = errors.New("checksum mismatch")
)

// ChecksumFor returns the lower-case digest listed for name. Every line must be
// `<64 hex><space><space|*><name>`; anything else rejects the whole document.
func ChecksumFor(sums []byte, name string) (string, error) {
	entries := map[string]string{}
	for _, line := range strings.Split(string(sums), "\n") {
		if line == "" {
			continue
		}
		if len(line) < 67 || line[64] != ' ' || (line[65] != ' ' && line[65] != '*') {
			return "", ErrMalformed
		}
		hash, file := line[:64], line[66:]
		if _, err := hex.DecodeString(hash); err != nil {
			return "", ErrMalformed
		}
		if file == "." || file == ".." || strings.ContainsAny(file, "/\\\x00\r\n\t ") {
			return "", ErrMalformed
		}
		if _, dup := entries[file]; dup {
			return "", ErrMalformed
		}
		entries[file] = strings.ToLower(hash)
	}
	sum, ok := entries[name]
	if !ok {
		return "", ErrAssetMissing
	}
	return sum, nil
}

// VerifyFile hashes the file at path and compares it with wantHex (any case).
func VerifyFile(path, wantHex string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != strings.ToLower(wantHex) {
		return ErrChecksum
	}
	return nil
}
