package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFingerprintBindsExactBytesAndEffectiveEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := []byte("data_dir: /var/lib/stackstead\n")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	empty := func(string) string { return "" }
	cfg, a, err := LoadWithFingerprint(path, empty)
	if err != nil || len(a) != 64 {
		t.Fatalf("fingerprint missing: %q %v", a, err)
	}
	other, b, err := LoadWithFingerprint(path, func(k string) string {
		if k == "STACKSTEAD_DATA_DIR" {
			return "/other/data"
		}
		return ""
	})
	if err != nil || b == a || other.DataDir == cfg.DataDir {
		t.Fatalf("effective environment not bound: %v", err)
	}
	_, normalized, err := LoadWithFingerprint(path, func(k string) string {
		if k == "STACKSTEAD_DATA_DIR" {
			return "/var/lib/./stackstead"
		}
		return ""
	})
	if err != nil || normalized != a {
		t.Fatalf("normalized equivalent config differs: %v", err)
	}
	if err = os.WriteFile(path, append(raw, []byte("# edited\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	_, edited, err := LoadWithFingerprint(path, empty)
	if err != nil || edited == a {
		t.Fatalf("exact source edit not bound: %v", err)
	}
}
func TestLoadFingerprintDoesNotRereadAfterEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := []byte("data_dir: /original\n")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	_, want, err := LoadWithFingerprint(path, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	changed := false
	cfg, got, err := LoadWithFingerprint(path, func(string) string {
		if !changed {
			changed = true
			if err := os.WriteFile(path, []byte("data_dir: /replaced\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		return ""
	})
	if err != nil || cfg.DataDir != "/original" || got != want {
		t.Fatalf("fingerprint reread edited source: cfg=%+v got=%s want=%s err=%v", cfg, got, want, err)
	}
}
