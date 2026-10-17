package update

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func openTestJournal(root string) (*Journal, error) {
	return &Journal{root: root, store: &trustStore{root: root, anchor: root, owner: uint32(os.Getuid())}}, nil
}

func testJournal(t *testing.T) *Journal {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	j, err := openTestJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	return j
}
func TestBootstrapRecoversWithoutPackageHelper(t *testing.T) {
	j := testJournal(t)
	source := filepath.Join(j.root, "package-helper")
	if err := os.WriteFile(source, []byte("trusted worker"), 0700); err != nil {
		t.Fatal(err)
	}
	digest, err := j.pinWorker(source)
	if err != nil {
		t.Fatal(err)
	}
	job := Job{ID: "job", InstallationID: "install", Protocol: 1, Phase: PhaseInstalling, WorkerSHA256: digest}
	if err = j.Save(job); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(source); err != nil {
		t.Fatal(err)
	}
	got, err := j.Load("install")
	if err != nil {
		t.Fatal(err)
	}
	path, err := j.selectWorker(&got)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "trusted worker" {
		t.Fatalf("pinned recovery missing: %q %v", data, err)
	}
	got.Protocol = 99
	if _, err = j.selectWorker(&got); err == nil {
		t.Fatal("unknown journal protocol accepted")
	}
}
func TestJournalRejectsUntrustedExecutablePath(t *testing.T) {
	j := testJournal(t)
	for _, digest := range []string{"../../bin/sh", "", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		if _, err := j.selectWorker(&Job{Protocol: 1, WorkerSHA256: digest}); err == nil {
			t.Fatalf("untrusted digest accepted: %q", digest)
		}
	}
}
func TestTrustStoreRejectsWritableOrSymlinkParent(t *testing.T) {
	j := testJournal(t)
	parent := filepath.Join(j.root, "bad")
	if err := os.Mkdir(parent, 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err := j.store.directory("bad", false); err == nil {
		t.Fatal("writable parent accepted")
	}
	if err := os.Symlink("bad", filepath.Join(j.root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := j.store.directory("link", false); err == nil {
		t.Fatal("symlink parent accepted")
	}
}
func TestJournalCrashKeepsLastDurableState(t *testing.T) {
	j := testJournal(t)
	job := Job{ID: "first", InstallationID: "install", Protocol: 1, Phase: PhaseAccepted}
	if err := j.Save(job); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(j.root, "jobs", "install.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private journal mode: %v %v", info, err)
	}
	j.store.beforeRename = func() error { return errors.New("simulated disk failure") }
	job.ID = "second"
	if err := j.Save(job); err == nil {
		t.Fatal("write acknowledged despite failure")
	}
	got, err := j.Load("install")
	if err != nil || got.ID != "first" {
		t.Fatalf("last durable state lost: %+v %v", got, err)
	}
}

func TestJournalSnapshotWhileMutationLocked(t *testing.T) {
	j := testJournal(t)
	job := Job{ID: "job", InstallationID: "install", Protocol: 1, Phase: PhaseInstalling}
	if err := j.Save(job); err != nil {
		t.Fatal(err)
	}
	lock, err := j.TryLock("install")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err = j.Load("install"); err != nil {
		t.Fatal(err)
	}
	if _, err = j.TryLock("install"); !errors.Is(err, ErrBusy) {
		t.Fatalf("second writer admitted: %v", err)
	}
	job.Phase = PhaseVerifying
	if err = lock.Save(job); err != nil {
		t.Fatal(err)
	}
	got, err := j.Load("install")
	if err != nil || got.Phase != PhaseVerifying {
		t.Fatalf("locked transition not visible: %+v %v", got, err)
	}
}
func TestJournalDetectsCorruptionAndPinnedWorkerTampering(t *testing.T) {
	j := testJournal(t)
	job := Job{ID: "job", InstallationID: "install", Protocol: 1}
	if err := j.Save(job); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(j.root, "jobs", "install.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b = []byte(string(b[:len(b)-1]) + `,"sha256":"bad"}`)
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = j.Load("install"); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("corruption accepted: %v", err)
	}
	source := filepath.Join(j.root, "worker-source")
	if err = os.WriteFile(source, []byte("worker"), 0700); err != nil {
		t.Fatal(err)
	}
	digest, err := j.pinWorker(source)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := j.worker(digest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(worker, []byte("evil"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = j.worker(digest); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("tampered worker accepted: %v", err)
	}
}

func TestBootstrapPinRequiresExecutableSource(t *testing.T) {
	j := testJournal(t)
	source := filepath.Join(j.root, "source")
	if err := os.WriteFile(source, []byte("not executable"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := j.pinWorker(source); err == nil {
		t.Fatal("nonexecutable source pinned")
	}
}
func TestBootstrapPinLockDoesNotCollideWithInstallationID(t *testing.T) {
	j := testJournal(t)
	source := filepath.Join(j.root, "source")
	if err := os.WriteFile(source, []byte("executable"), 0700); err != nil {
		t.Fatal(err)
	}
	lock, err := j.TryLock("workers")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err = j.pinWorker(source); err != nil {
		t.Fatalf("installation ID collided with pin lock: %v", err)
	}
}
