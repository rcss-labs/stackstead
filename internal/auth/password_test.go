package auth

import (
	"errors"
	"testing"
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
