package updatehost

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDarwinWriterUsesOnlyInheritedHandles(t *testing.T) {
	e, in := childFixture(t)
	destination := t.TempDir()
	old := filepath.Join(destination, "stackstead")
	if err := os.WriteFile(old, []byte("old-binary"), 0755); err != nil {
		t.Fatal(err)
	}
	fd, err := unix.Open(destination, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	dir := os.NewFile(uintptr(fd), "anchored-prefix")
	defer dir.Close()
	source := childOutput(t)
	payload := []byte("verified-replacement")
	if _, err = source.Write(payload); err != nil {
		t.Fatal(err)
	}
	if _, err = source.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	// Rename the parent after opening; child receives no destination path and must
	// continue to operate on the inherited directory rather than resolving a name.
	moved := destination + "-moved"
	if err = os.Rename(destination, moved); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(moved)
	if err = e.prefixWriter(context.Background(), in.UID, in.GID, source, dir, int64(len(payload))); err != nil {
		t.Fatalf("real inherited-handle writer unavailable: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(moved, "stackstead"))
	if err != nil || string(got) != string(payload) {
		t.Fatalf("writer missed anchored directory: %q %v", got, err)
	}
	st, err := os.Stat(filepath.Join(moved, "stackstead"))
	if err != nil || st.Mode().Perm() != 0755 {
		t.Fatalf("mode=%v err=%v", st, err)
	}
	entries, err := os.ReadDir(moved)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary leak: %v %v", entries, err)
	}
	if err = os.Remove(filepath.Join(moved, "stackstead")); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "victim")
	if err = os.WriteFile(victim, []byte("preserve"), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(victim, filepath.Join(moved, "stackstead")); err != nil {
		t.Fatal(err)
	}
	if _, err = source.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	if err = e.prefixWriter(context.Background(), in.UID, in.GID, source, dir, int64(len(payload))); err == nil {
		t.Fatal("writer followed fixed leaf symlink")
	}
	got, err = os.ReadFile(victim)
	if err != nil || string(got) != "preserve" {
		t.Fatal("writer changed symlink victim")
	}
}
