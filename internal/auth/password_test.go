package auth

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/store"
)

func TestVerifyPasswordRejectsUnsafeParameters(t *testing.T) {
	const salt = "c2FsdHNhbHRzYWx0c2FsdA"                       // 16 bytes
	const key32 = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" // 32 bytes
	const key8 = "AAAAAAAAAAA"                                  // 8 bytes
	tests := map[string]string{
		"zero threads":    "$argon2id$v=19$m=8192,t=1,p=0$" + salt + "$" + key32,
		"zero iterations": "$argon2id$v=19$m=8192,t=0,p=1$" + salt + "$" + key32,
		"zero memory":     "$argon2id$v=19$m=0,t=1,p=1$" + salt + "$" + key32,
		"huge memory":     "$argon2id$v=19$m=1048577,t=1,p=1$" + salt + "$" + key32,
		"short key":       "$argon2id$v=19$m=8192,t=1,p=1$" + salt + "$" + key8,
	}
	for name, encoded := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := verifyPassword(encoded, "whatever"); !errors.Is(err, errMalformedHash) {
				t.Errorf("err = %v, want errMalformedHash", err)
			}
		})
	}
}

func TestVerifyPasswordAcceptsValidHash(t *testing.T) {
	hash, err := hashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := verifyPassword(hash, "correct horse battery"); err != nil || !ok {
		t.Errorf("verify = %v, %v; want true, nil", ok, err)
	}
	if ok, _ := verifyPassword(hash, "wrong"); ok {
		t.Error("wrong password verified")
	}
}

func TestProductionHashParamsAreUnchanged(t *testing.T) {
	want := argonParams{memory: 64 * 1024, iterations: 3, threads: 2, keyLen: 32, saltLen: 16}
	if defaultHashParams != want {
		t.Fatalf("defaultHashParams = %+v, want %+v", defaultHashParams, want)
	}
	hash, err := hashPasswordWith(defaultHashParams, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(hash, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Errorf("hash %q does not carry production parameters", hash)
	}
}

func TestNewServiceUsesDefaultParamsUnlessFastHashingRequested(t *testing.T) {
	orig := hashParams
	t.Cleanup(func() { hashParams = orig })
	hashParams = defaultHashParams
	db, err := store.Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	plain, err := NewService(db, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if plain.params != defaultHashParams {
		t.Errorf("default service params = %+v, want production params", plain.params)
	}
	fast, err := NewService(db, time.Now, WithFastHashingForTests())
	if err != nil {
		t.Fatal(err)
	}
	if fast.params == defaultHashParams || fast.params.memory >= defaultHashParams.memory {
		t.Errorf("fast service params = %+v, want cheaper than production", fast.params)
	}
}
