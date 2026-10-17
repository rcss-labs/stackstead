package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractVerifiedPayloadRechecksDigestAndTrust(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	s := &trustStore{root: root, anchor: root, owner: uint32(os.Getuid())}
	dir := filepath.Join(root, StagingDirectory)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "stackstead", Mode: 0755, Size: 3}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write([]byte("app"))
	_ = tw.Close()
	_ = gz.Close()
	path := filepath.Join(dir, "payload.tgz")
	if err := os.WriteFile(path, buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buf.Bytes())
	a := VerifiedArtifact{Path: path, Size: int64(buf.Len()), SHA256: hex.EncodeToString(sum[:])}
	dest := filepath.Join(dir, "extract")
	if err := os.Mkdir(dest, 0700); err != nil {
		t.Fatal(err)
	}
	if err := extractVerifiedPayload(s, a, dest); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(dest, "stackstead")); err != nil || string(got) != "app" {
		t.Fatalf("payload: %q %v", got, err)
	}
	if err := os.Remove(filepath.Join(dest, "stackstead")); err != nil {
		t.Fatal(err)
	}
	a.SHA256 = string(bytes.Repeat([]byte("0"), 64))
	if err := extractVerifiedPayload(s, a, dest); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("digest mutation: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "stackstead")); !os.IsNotExist(err) {
		t.Fatal("wrote unverified data")
	}
	if err := os.Chmod(dest, 0777); err != nil {
		t.Fatal(err)
	}
	if err := extractVerifiedPayload(s, a, dest); !errors.Is(err, ErrSetupRequired) {
		t.Fatalf("untrusted destination: %v", err)
	}
}

func TestVerifyNativeArtifactRechecksBytesWithoutTarExtraction(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	s := &trustStore{root: root, anchor: root, owner: uint32(os.Getuid())}
	dir := filepath.Join(root, StagingDirectory)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "native.deb")
	data := []byte("!<arch>\nnative package")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	a := VerifiedArtifact{Path: path, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
	if err := verifyArtifact(s, a); err != nil {
		t.Fatalf("valid native bytes refused: %v", err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), len(data)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyArtifact(s, a); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("mutated native artifact: %v", err)
	}
}
