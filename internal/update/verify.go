package update

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

func checksumFor(sums []byte, name string) (string, error) {
	if int64(len(sums)) > documentLimit {
		return "", incompatible("checksum document exceeds size limit")
	}
	entries := map[string]string{}
	for _, line := range strings.Split(string(sums), "\n") {
		if line == "" {
			continue
		}
		if len(line) < 67 || line[64] != ' ' || (line[65] != ' ' && line[65] != '*') {
			return "", incompatible("malformed SHA256SUMS")
		}
		hash, file := line[:64], line[66:]
		if _, err := hex.DecodeString(hash); err != nil {
			return "", incompatible("invalid SHA256 digest")
		}
		if file == "" || file == "." || file == ".." || strings.ContainsAny(file, "/\\\x00\r\n\t ") {
			return "", incompatible("invalid checksum asset name")
		}
		if _, ok := entries[file]; ok {
			return "", incompatible("duplicate checksum asset")
		}
		entries[file] = strings.ToLower(hash)
	}
	sum, ok := entries[name]
	if !ok {
		return "", incompatible("missing artifact checksum")
	}
	return sum, nil
}
func verifyChecksum(sums []byte, name string, data []byte) error {
	expected, err := checksumFor(sums, name)
	if err != nil {
		return err
	}
	actual := sha256.Sum256(data)
	if hex.EncodeToString(actual[:]) != expected {
		return incompatible("release SHA256 mismatch")
	}
	return nil
}
