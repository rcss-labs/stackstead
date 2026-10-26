package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
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

// The post-start work must survive a crash or a failed step: ApplyPending
// records it in post-apply.json before it drops the pending marker.
func TestApplyPendingRecordsPostApplyWork(t *testing.T) {
	s := newHostSetup(t)
	stageForApply(t, s.env.DataDir, s.fixture, []string{"web"})
	if _, err := ApplyPending(s.env.DataDir, time.Now()); err != nil {
		t.Fatal(err)
	}
	post, err := ReadPostApply(s.env.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if post == nil || !post.PauseSchedule || !slices.Equal(post.StartStacks, []string{"web"}) {
		t.Fatalf("post-apply = %+v, want pause and web", post)
	}

	// Each finished step is dropped; the marker goes when nothing is left.
	post.PauseSchedule = false
	if err := WritePostApply(s.env.DataDir, *post); err != nil {
		t.Fatal(err)
	}
	if again, _ := ReadPostApply(s.env.DataDir); again == nil || again.PauseSchedule || len(again.StartStacks) != 1 {
		t.Fatalf("after the pause: %+v", again)
	}
	if err := WritePostApply(s.env.DataDir, PostApply{}); err != nil {
		t.Fatal(err)
	}
	if again, err := ReadPostApply(s.env.DataDir); again != nil || err != nil {
		t.Fatalf("after everything: %+v, %v; want no marker", again, err)
	}
}

// A new restore clears its staging folder; it must not drop the previous
// restore's unfinished post-start work or an upload still being written.
func TestHostRestoreKeepsPostApplyAndPartialUpload(t *testing.T) {
	s := newHostSetup(t)
	dir := filepath.Join(s.env.DataDir, "restore")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WritePostApply(s.env.DataDir, PostApply{PauseSchedule: true}); err != nil {
		t.Fatal(err)
	}
	partial := UploadPath(s.env.DataDir) + ".partial"
	if err := os.WriteFile(partial, []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.restore(t, s.fixture.build(t, t.TempDir(), false)); err != nil {
		t.Fatal(err)
	}
	if post, _ := ReadPostApply(s.env.DataDir); post == nil {
		t.Error("post-apply work was deleted")
	}
	if _, err := os.Stat(partial); err != nil {
		t.Error("an upload in progress was deleted")
	}
}
