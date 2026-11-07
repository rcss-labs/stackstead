package stacks_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/rcss-labs/stackstead/internal/composefile"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

func TestRestoreComposeBackup(t *testing.T) {
	h := newHarness(t)
	good := setupWeb(t, h)
	ctx := context.Background()
	goodBytes, _, _ := h.svc.Raw(ctx, "web")
	path := filepath.Join(h.dir, "web", "compose.yaml")

	if h.svc.HasBackup(ctx, "web") {
		t.Fatal("HasBackup before any edit")
	}
	if _, err := h.svc.RestoreBackup(ctx, "web", good); !errors.Is(err, composefile.ErrNoBackup) {
		t.Fatalf("no backup: err = %v", err)
	}

	// An edit leaves the previous version as .bak; then the file is broken by hand.
	if _, err := h.svc.SaveRaw(ctx, "web", []byte("services:\n  web:\n    image: a\n"), good); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("services: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	_, brokenHash, _ := h.svc.Raw(ctx, "web")
	if !h.svc.HasBackup(ctx, "web") {
		t.Fatal("HasBackup = false after an edit")
	}
	if _, err := h.svc.RestoreBackup(ctx, "web", "stale"); !errors.Is(err, composefile.ErrConflict) {
		t.Fatalf("stale: err = %v", err)
	}
	hash, err := h.svc.RestoreBackup(ctx, "web", brokenHash)
	if err != nil {
		t.Fatal(err)
	}
	now, nowHash, _ := h.svc.Raw(ctx, "web")
	if string(now) != string(goodBytes) || nowHash != hash {
		t.Errorf("restored %q (hash %s, returned %s)", now, nowHash, hash)
	}
	if got, _ := os.ReadFile(path + ".broken"); string(got) != "services: [" {
		t.Errorf(".broken = %q", got)
	}
}

func TestRestoreComposeBackupOutsideRoots(t *testing.T) {
	h := newHarness(t)
	hash := setupWeb(t, h)
	ctx := context.Background()
	outside := filepath.Join(t.TempDir(), "elsewhere.yaml")
	if err := os.WriteFile(outside, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A link to a real file outside every allowed root.
	if err := os.Symlink(outside, filepath.Join(h.dir, "web", "compose.yaml.bak")); err != nil {
		t.Skip("symlinks unavailable")
	}
	if h.svc.HasBackup(ctx, "web") {
		t.Error("HasBackup followed a link out of the roots")
	}
	if _, err := h.svc.RestoreBackup(ctx, "web", hash); !errors.Is(err, composefile.ErrNoBackup) {
		t.Fatalf("err = %v, want ErrNoBackup", err)
	}
	if got, _, _ := h.svc.Raw(ctx, "web"); string(got) == "services: {}\n" {
		t.Error("content from outside the roots was restored")
	}
}

func TestRestoreComposeBackupUnknownStack(t *testing.T) {
	h := newHarness(t)
	if _, err := h.svc.RestoreBackup(context.Background(), "nope", "x"); !errors.Is(err, stacks.ErrStackNotFound) {
		t.Fatalf("err = %v", err)
	}
}
