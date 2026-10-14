package metrics

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// NewToken returns a fresh bearer token for /metrics and the hex SHA-256 that
// is stored in its place. Shared by the server and the local admin command so
// both produce the same format.
func NewToken() (plain, hashHex string, err error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", fmt.Errorf("generate metrics token: %w", err)
	}
	plain = base64.RawURLEncoding.EncodeToString(raw[:])
	return plain, HashToken(plain), nil
}

// HashToken is the hex SHA-256 of a plaintext token.
func HashToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}
