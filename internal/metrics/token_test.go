package metrics_test

import (
	"regexp"
	"testing"

	"github.com/rcss-labs/stackstead/internal/metrics"
)

func TestNewTokenIsURLSafeAndHashes(t *testing.T) {
	plain, hash, err := metrics.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) != 43 || !regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`).MatchString(plain) {
		t.Errorf("token %q is not 43 unpadded url-safe chars", plain)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(hash) {
		t.Errorf("hash %q is not 64 hex chars", hash)
	}
	if hash != metrics.HashToken(plain) || metrics.HashToken(plain) != metrics.HashToken(plain) {
		t.Error("HashToken is not deterministic or does not match NewToken")
	}
	other, _, _ := metrics.NewToken()
	if other == plain {
		t.Error("two tokens are equal")
	}
}
