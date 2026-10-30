package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A bind source swapped for a symlink after prepareBind vetted it must not
// make the helper mount (or wipe) a host path outside the vetted root.
func TestRestoreBindSymlinkSwapAfterVetting(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "srv")
	outside := filepath.Join(base, "etc")
	source := filepath.Join(root, "app", "data")
	for _, d := range []string{source, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rs := newRootSet([]string{root}, filepath.Join(base, "state"))
	defer rs.close()
	target, skip, err := rs.prepareBind(source, nil)
	if err != nil || skip != "" {
		t.Fatalf("prepareBind: skip=%q err=%v", skip, err)
	}

	// The attack: swap the vetted directory for a symlink to outside.
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, source); err != nil {
		t.Fatal(err)
	}

	eng := &restoreEngine{}
	err = RestoreBind(context.Background(), eng, "web", target,
		innerTar(t, map[string]string{"data/": "", "data/x": "X"}))
	if err != nil {
		t.Fatal(err)
	}
	spec := eng.specs[0]
	if len(spec.Mounts) != 1 {
		t.Fatalf("mounts = %+v, want exactly one", spec.Mounts)
	}
	m := spec.Mounts[0]
	if m.Source != root || m.ReadOnly {
		t.Errorf("mount source = %q, want the vetted root %q read-write (never the leaf %q)", m.Source, root, source)
	}
	if m.Target == "/data" || strings.HasPrefix(m.Source, outside) {
		t.Errorf("mount = %+v, want the root mounted away from /data and nothing outside the root", m)
	}
	cmd := strings.Join(spec.Cmd, " ")
	if !strings.Contains(cmd, "app/data") || !strings.Contains(cmd, "-L") || !strings.Contains(cmd, "realpath") {
		t.Errorf("helper command = %q, want in-container rel path app/data with symlink and realpath checks", cmd)
	}
	wantPut := "put:helper1:" + m.Target + "/app/data"
	if got := eng.changes(); !containsString(got, wantPut) {
		t.Errorf("calls = %v, want %q", got, wantPut)
	}
	if eng.puts[0]["x"] != "X" {
		t.Errorf("put = %v, want x stripped of the data/ prefix", eng.puts)
	}
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
