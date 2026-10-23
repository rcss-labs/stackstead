package peers

import (
	"crypto/rand"
	"encoding/base64"
)

const credentialPrefix = "ssp_"

// NewCredential returns a fresh peer credential and the SHA-256 hex the
// issuer stores in its place.
func NewCredential() (Secret, string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", err
	}
	plain := credentialPrefix + base64.RawURLEncoding.EncodeToString(raw[:])
	return Secret(plain), HashSecret(plain), nil
}
