package peers

import (
	"crypto/rand"
	"encoding/base64"
	"log/slog"
)

const credentialPrefix = "ssp_"

const redacted = "[redacted]"

func (Secret) String() string       { return redacted }
func (Secret) GoString() string     { return redacted }
func (Secret) LogValue() slog.Value { return slog.StringValue(redacted) }
func (s Secret) Reveal() string     { return string(s) }

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
