package update

import (
	"errors"
	"strings"
	"testing"
)

func TestVerifyRejectsWrongAssetChecksum(t *testing.T) {
	if err := verifyChecksum([]byte(strings.Repeat("0", 64)+"  file\n"), "file", []byte("payload")); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("wrong checksum: %v", err)
	}
}
