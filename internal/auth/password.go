package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

type argonParams struct {
	memory     uint32 // KiB
	iterations uint32
	threads    uint8
	keyLen     uint32
	saltLen    int
}

// hashParams follow the OWASP argon2id baseline. Verification reads the
// parameters back from each stored hash, so raising them later is safe.
var defaultHashParams = argonParams{memory: 64 * 1024, iterations: 3, threads: 2, keyLen: 32, saltLen: 16}

// hashParams is what NewService reads; only this package's tests reassign it.
var hashParams = defaultHashParams

// fastHashParams is the same algorithm at a fraction of the cost, for tests
// of other packages that create admins and log in many times.
var fastHashParams = argonParams{memory: 1024, iterations: 1, threads: 1, keyLen: 32, saltLen: 16}

// deriveKey is a seam so tests can observe how many derivations run at once.
var deriveKey = argon2.IDKey

// Bounds on parameters read from a stored hash, so a corrupt or tampered row
// cannot panic argon2 or trigger a huge allocation.
const (
	maxHashMemoryKiB = 1 << 20 // 1 GiB
	minHashKeyLen    = 16
)

var errMalformedHash = errors.New("malformed password hash")

// hashPassword returns a PHC-format string: $argon2id$v=19$m=..,t=..,p=..$salt$key
func hashPassword(password string) (string, error) {
	return hashPasswordWith(hashParams, password)
}

func hashPasswordWith(p argonParams, password string) (string, error) {
	salt := make([]byte, p.saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := deriveKey([]byte(password), salt, p.iterations, p.memory, p.threads, p.keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.memory, p.iterations, p.threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

func verifyPassword(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errMalformedHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errMalformedHash
	}
	var memory, iterations uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil {
		return false, errMalformedHash
	}
	if threads == 0 || iterations == 0 || memory == 0 || memory > maxHashMemoryKiB {
		return false, errMalformedHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, errMalformedHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, errMalformedHash
	}
	if len(want) < minHashKeyLen {
		return false, errMalformedHash
	}
	got := deriveKey([]byte(password), salt, iterations, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
