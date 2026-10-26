package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// An upload lives in the restore staging folder; clearing that folder for
// the restore must not delete the archive being restored.
func TestHostRestoreFromUploadInStagingDir(t *testing.T) {
	s := newHostSetup(t)
	built := s.fixture.build(t, t.TempDir(), false)
	raw, err := os.ReadFile(built)
	if err != nil {
		t.Fatal(err)
	}
	upload := UploadPath(s.env.DataDir)
	if err := os.MkdirAll(filepath.Dir(upload), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(upload, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	src := HostSource{Kind: SourceUpload, Path: upload}
	restorer := HostRestorer{Env: s.env}
	sum, err := restorer.Inspect(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restorer.Restore(context.Background(), src, true, sum.ConflictsDigest, nil, nil); err != nil {
		t.Fatalf("restore from upload: %v", err)
	}
	readPending(t, s.env.DataDir)
}

func TestHostRestoreReportsAcceptanceOnlyWhenConfirmed(t *testing.T) {
	s := newHostSetup(t)
	path := s.fixture.build(t, t.TempDir(), true)
	restorer := HostRestorer{Env: s.env}
	sum, err := restorer.Inspect(context.Background(), pathSource(path))
	if err != nil {
		t.Fatal(err)
	}

	accepted := 0
	_, err = restorer.Restore(context.Background(), pathSource(path), true, "stale-digest", nil, func() { accepted++ })
	if !errors.Is(err, ErrConflictsChanged) || accepted != 0 {
		t.Fatalf("stale digest: err = %v, accepted %d times; want ErrConflictsChanged and no acceptance", err, accepted)
	}
	var changesAtAccept []string
	_, err = restorer.Restore(context.Background(), pathSource(path), true, sum.ConflictsDigest, nil, func() {
		accepted++
		changesAtAccept = s.eng.changes()
	})
	if err != nil || accepted != 1 {
		t.Fatalf("err = %v, accepted %d times; want nil and once", err, accepted)
	}
	if len(changesAtAccept) != 0 {
		t.Errorf("the host was changed before acceptance: %v", changesAtAccept)
	}
}
