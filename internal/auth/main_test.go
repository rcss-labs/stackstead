package auth

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// Production argon2 cost (64 MiB per hash) makes the suite slow. Same
	// algorithm, cheaper parameters; hashes still verify via their encoded params.
	hashParams = argonParams{memory: 8 * 1024, iterations: 1, threads: 1, keyLen: 32, saltLen: 16}
	os.Exit(m.Run())
}
