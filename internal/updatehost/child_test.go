package updatehost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/store"
	"github.com/rcss-labs/stackstead/internal/update"
)

// Only the test binary adapts peer credentials; shipped dispatch always requires root.
func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == "internal-child" {
		if err := runInternalChild(context.Background(), uint32(os.Getuid())); err != nil {
			fmt.Fprintln(os.Stderr, "test child:", err)
			os.Exit(71)
		}
		os.Exit(0)
	}
	if len(os.Args) == 2 {
		switch os.Args[1] {
		case "internal-child-production-test":
			if RunInternalChild(context.Background()) != nil {
				os.Exit(71)
			}
			os.Exit(0)
		case "internal-child-untrusted-test":
			if runInternalChild(context.Background(), uint32(os.Getuid()+1)) != nil {
				os.Exit(71)
			}
			os.Exit(0)
		case "internal-child-overflow-test", "internal-child-metadata-overflow-test", "internal-child-timeout-test":
			control := os.NewFile(3, "test-control")
			var request childRequest
			if readChildFrame(control, &request) != nil {
				os.Exit(72)
			}
			if os.Args[1] == "internal-child-overflow-test" {
				output := os.NewFile(5, "test-output")
				chunk := make([]byte, 32768)
				for {
					if _, err := output.Write(chunk); err != nil {
						os.Exit(73)
					}
				}
			}
			if os.Args[1] == "internal-child-metadata-overflow-test" {
				var header [4]byte
				binary.BigEndian.PutUint32(header[:], childMetadataLimit+1)
				control.Write(header[:])
			}
			time.Sleep(30 * time.Second)
			os.Exit(74)
		}
	}
	os.Exit(m.Run())
}
func childFixture(t *testing.T) (*childExecutor, update.Instance) {
	t.Helper()
	base := t.TempDir()
	data := filepath.Join(base, "app")
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(base, "config.yaml")
	if err := os.WriteFile(cfg, []byte("data_dir: "+data+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	pin := filepath.Join(base, "private")
	if err = os.Mkdir(pin, 0700); err != nil {
		t.Fatal(err)
	}
	worker := filepath.Join(pin, "worker")
	if err = os.WriteFile(worker, raw, 0700); err != nil {
		t.Fatal(err)
	}
	sum := fmt.Sprintf("%x", sha256.Sum256(raw))
	runtime := filepath.Join(base, "runtime")
	if err = os.Mkdir(runtime, 0755); err != nil {
		t.Fatal(err)
	}
	e := &childExecutor{workerPath: worker, workerDigest: sum, socketRoot: runtime, owner: uint32(os.Getuid()), testStderr: func(b []byte) {
		if len(b) > 0 {
			t.Logf("child stderr: %s", b)
		}
	}, anchor: base, testCredentialHook: func(cmd *exec.Cmd) error { cmd.SysProcAttr.Credential = nil; return nil }}
	in := update.Instance{ID: "test-instance", UID: uint32(os.Getuid()), GID: uint32(os.Getgid()), ConfigPath: cfg, DataDir: data}
	return e, in
}
func childDatabase(t *testing.T, in update.Instance) map[string][]byte {
	t.Helper()
	p := filepath.Join(in.DataDir, "stackstead.db")
	db, err := store.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("PRAGMA wal_checkpoint(TRUNCATE); PRAGMA wal_autocheckpoint=0; INSERT INTO settings(key,value) VALUES ('child-final','committed')"); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, s := range []string{"", "-wal", "-shm"} {
		files[s], err = os.ReadFile(p + s)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(files["-wal"]) == 0 {
		t.Fatal("fixture lacks committed WAL")
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	for s, b := range files {
		if err = os.WriteFile(p+s, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return files
}
func childOutput(t *testing.T) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "private-backup")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}
func TestWorkerDBMaintenanceUsesRegisteredUID(t *testing.T) {
	e, in := childFixture(t)
	childDatabase(t, in)
	got, err := e.InspectDB(context.Background(), "job-123", in)
	if err != nil {
		t.Fatalf("real registered-UID child inspect unavailable: %v", err)
	}
	if got.UID != in.UID || got.GID != in.GID || got.Schema != store.SupportedSchema() || got.DBBytes <= 0 || got.WALBytes <= 0 || got.FreeBytes == 0 || len(got.ConfigSHA256) != 64 {
		t.Fatalf("actual child evidence: %+v", got)
	}
	if _, err = os.Stat(filepath.Join(e.socketRoot, "children", e.workerDigest, "child")); err != nil {
		t.Fatalf("traversable digest copy missing: %v", err)
	}
	wrong := in
	wrong.UID++
	if _, err = e.InspectDB(context.Background(), "job-123", wrong); err == nil {
		t.Fatal("child accepted wrong registered UID")
	}
}
func TestChildSnapshotPreservesCommittedWAL(t *testing.T) {
	e, in := childFixture(t)
	childDatabase(t, in)
	out := childOutput(t)
	proof, err := e.Snapshot(context.Background(), "job-123", in, 16<<20, out)
	if err != nil {
		t.Fatalf("real child snapshot unavailable: %v", err)
	}
	if proof.Size <= 0 || proof.Schema != store.SupportedSchema() || proof.Budget != 16<<20 {
		t.Fatalf("proof=%+v", proof)
	}
	raw, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(raw)) != proof.Size || fmt.Sprintf("%x", sha256.Sum256(raw)) != proof.SHA256 {
		t.Fatal("parent published unverified bytes")
	}
	db, err := store.OpenExisting(out.Name(), store.OpenOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v string
	if err = db.QueryRow("SELECT value FROM settings WHERE key='child-final'").Scan(&v); err != nil || v != "committed" {
		t.Fatalf("lost final WAL commit: %q %v", v, err)
	}
}
func TestChildRestorePreservesFailedDBAndRejectsAmbiguousRetry(t *testing.T) {
	e, in := childFixture(t)
	childDatabase(t, in)
	out := childOutput(t)
	proof, err := e.Snapshot(context.Background(), "job-123", in, 16<<20, out)
	if err != nil {
		t.Fatalf("snapshot unavailable: %v", err)
	}
	p := filepath.Join(in.DataDir, "stackstead.db")
	failed := []byte("failed-migration-database")
	if err = os.WriteFile(p, failed, 0600); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"-wal", "-shm"} {
		if err = os.WriteFile(p+s, []byte("failed"+s), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = out.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	if err = e.Restore(context.Background(), "job-123", in, proof, out); err != nil {
		t.Fatalf("restore unavailable: %v", err)
	}
	archives, err := filepath.Glob(filepath.Join(in.DataDir, ".updater-restore-*", "failed", "stackstead.db"))
	if err != nil || len(archives) != 1 {
		t.Fatalf("failed archive missing: %v %v", archives, err)
	}
	raw, err := os.ReadFile(archives[0])
	if err != nil || !bytes.Equal(raw, failed) {
		t.Fatal("failed DB bytes changed")
	}
	if _, err = out.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	if err = e.Restore(context.Background(), "job-123", in, proof, out); err != nil {
		t.Fatalf("verified completed retry: %v", err)
	}
	if err = os.WriteFile(p, []byte("changed-after-start"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = out.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	if err = e.Restore(context.Background(), "job-123", in, proof, out); err == nil {
		t.Fatal("replayed restore over changed/current DB")
	}
}
func TestChildStreamOverflowKillsAndReaps(t *testing.T) {
	e, in := childFixture(t)
	childDatabase(t, in)
	out := childOutput(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := e.Snapshot(ctx, "job-123", in, 1, out); err == nil {
		t.Fatal("oversized DB snapshot accepted")
	}
	st, err := out.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() > 1 {
		t.Fatalf("parent exceeded output budget: %d", st.Size())
	}
	if ctx.Err() != nil {
		t.Fatal("overflow hung until caller deadline")
	}
}

func TestChildRejectsUntrustedPeerAndRootIdentity(t *testing.T) {
	e, in := childFixture(t)
	childDatabase(t, in)
	request := childRequest{Protocol: 1, Role: childInspectDB, JobID: "job-123", Instance: in}
	t.Run("untrusted-peer-before-app-io", func(t *testing.T) {
		request.Instance.DataDir = filepath.Join(t.TempDir(), "must-not-be-created")
		if err := rawTestChild(t, e, "internal-child-untrusted-test", request, nil); err == nil {
			t.Fatal("child accepted untrusted peer")
		}
		if _, err := os.Stat(request.Instance.DataDir); !os.IsNotExist(err) {
			t.Fatal("untrusted peer caused app I/O")
		}
	})
	t.Run("root-registration", func(t *testing.T) {
		request.Instance = in
		request.Instance.UID = 0
		if err := rawTestChild(t, e, "internal-child", request, nil); err == nil {
			t.Fatal("child accepted root registration")
		}
	})
	t.Run("root-only-shipped-entry", func(t *testing.T) {
		if err := rawTestChild(t, e, "internal-child-production-test", childRequest{Protocol: 1, Role: childInspectDB, JobID: "job-123", Instance: in}, nil); err == nil {
			t.Fatal("production accepted nonroot parent")
		}
	})
}
func TestChildCannotUsePrivatePaths(t *testing.T) {
	e, in := childFixture(t)
	childDatabase(t, in)
	request := childRequest{Protocol: 1, Role: childInspectDB, JobID: "job-123", Instance: in}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw[:len(raw)-1], []byte(`,"snapshotPath":"/root/private/snapshot.db"}`)...)
	if err = rawTestChild(t, e, "internal-child", request, raw); err == nil {
		t.Fatal("child accepted private backup pathname")
	}
	for _, role := range []childRole{"exec", "read-file", childInspectPayload} {
		request.Role = role
		if err = rawTestChild(t, e, "internal-child", request, nil); err == nil {
			t.Fatalf("unassigned role accepted: %s", role)
		}
	}
	// A private-looking filename is never a substitute for the stream handle.
	if err = os.WriteFile(in.ConfigPath, []byte("data_dir: /wrong/registered-db\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = e.InspectDB(context.Background(), "job-123", in); err == nil {
		t.Fatal("configuration points at an unregistered DB")
	}
}
func rawTestChild(t *testing.T, e *childExecutor, verb string, request childRequest, raw []byte, extras ...*os.File) error {
	t.Helper()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	unix.CloseOnExec(pair[0])
	unix.CloseOnExec(pair[1])
	parent := os.NewFile(uintptr(pair[0]), "raw-parent")
	defer parent.Close()
	child := os.NewFile(uintptr(pair[1]), "raw-child")
	defer child.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, exe, verb)
	cmd.ExtraFiles = append([]*os.File{child}, extras...)
	cmd.Env = []string{"HOME=/", "PATH=/usr/bin:/bin"}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	child.Close()
	if raw == nil {
		raw, err = json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = writeChildFrame(parent, raw); err != nil {
		cmd.Wait()
		return err
	}
	var response childResponse
	readErr := readChildFrame(parent, &response)
	waitErr := cmd.Wait()
	if readErr != nil {
		return readErr
	}
	if waitErr != nil {
		return waitErr
	}
	if response.Error != "" {
		return errors.New(response.Error)
	}
	return nil
}
func TestChildParentOverflowAndDeadlineReapActualProcess(t *testing.T) {
	for _, verb := range []string{"internal-child-overflow-test", "internal-child-metadata-overflow-test", "internal-child-timeout-test"} {
		t.Run(verb, func(t *testing.T) {
			e, in := childFixture(t)
			out := childOutput(t)
			var process *exec.Cmd
			e.testCredentialHook = func(cmd *exec.Cmd) error {
				cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
				cmd.Args[1] = verb
				process = cmd
				return nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			if _, err := e.Snapshot(ctx, "job-123", in, 1024, out); err == nil {
				t.Fatal("unbounded child accepted")
			}
			if process == nil || process.ProcessState == nil {
				t.Fatal("child was not waited/reaped")
			}
			var status unix.WaitStatus
			if _, err := unix.Wait4(process.Process.Pid, &status, unix.WNOHANG, nil); err != unix.ECHILD {
				t.Fatalf("child remains unreaped: %v", err)
			}
			st, err := out.Stat()
			if err != nil || st.Size() > 1024 {
				t.Fatalf("output exceeded parent budget: %v %v", st, err)
			}
			if verb != "internal-child-timeout-test" && ctx.Err() != nil {
				t.Fatal("overflow did not kill immediately")
			}
		})
	}
}

func TestChildRestoreBudgetRejectsBeforeSwap(t *testing.T) {
	e, in := childFixture(t)
	childDatabase(t, in)
	out := childOutput(t)
	proof, err := e.Snapshot(context.Background(), "job-123", in, 1<<20, out)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(in.DataDir, "stackstead.db")
	failed := bytes.Repeat([]byte{0x7f}, (1<<20)+1)
	if err = os.WriteFile(p, failed, 0600); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err = os.Remove(p + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	if _, err = out.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	if err = e.Restore(context.Background(), "job-123", in, proof, out); err == nil {
		t.Fatal("restore exceeded root-reserved failed-archive budget")
	}
	got, err := os.ReadFile(p)
	if err != nil || !bytes.Equal(got, failed) {
		t.Fatal("overbudget failed database was modified")
	}
	entries, err := filepath.Glob(filepath.Join(in.DataDir, ".updater-restore-*"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("overbudget restore began archive/swap: %v %v", entries, err)
	}
}

func TestChildRejectsInappropriateInheritedHandles(t *testing.T) {
	e, in := childFixture(t)
	childDatabase(t, in)
	request := childRequest{Protocol: 1, Role: childInspectDB, JobID: "job-123", Instance: in}
	extra := childOutput(t)
	for _, slot := range []int{4, 5, 6, 7} {
		files := make([]*os.File, slot-3)
		files[len(files)-1] = extra
		if err := rawTestChild(t, e, "internal-child", request, nil, files...); err == nil {
			t.Fatalf("inspect accepted inherited role FD%d", slot)
		}
	}
}
func TestChildRestoreRejectsPreparedMarkerAndBadStream(t *testing.T) {
	e, in := childFixture(t)
	childDatabase(t, in)
	out := childOutput(t)
	proof, err := e.Snapshot(context.Background(), "job-123", in, 16<<20, out)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(in.DataDir, "stackstead.db"))
	if err != nil {
		t.Fatal(err)
	}
	bad := proof
	bad.SHA256 = strings.Repeat("a", 64)
	out.Seek(0, 0)
	if err = e.Restore(context.Background(), "job-123", in, bad, out); err == nil {
		t.Fatal("restore accepted wrong stream digest")
	}
	after, err := os.ReadFile(filepath.Join(in.DataDir, "stackstead.db"))
	if err != nil || !bytes.Equal(after, before) {
		t.Fatal("bad stream changed database")
	}
	sum := sha256.Sum256([]byte("job-123\x00" + in.ID))
	stateDir := filepath.Join(in.DataDir, ".updater-restore-"+fmt.Sprintf("%x", sum[:16]))
	if err = os.Mkdir(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	marker := childRestoreMarker{Protocol: 1, JobID: "job-123", InstanceID: in.ID, Expected: proof, Phase: "prepared"}
	if err = writeChildRestoreMarker(filepath.Join(stateDir, "marker.json"), marker, false); err != nil {
		t.Fatal(err)
	}
	out.Seek(0, 0)
	if err = e.Restore(context.Background(), "job-123", in, proof, out); err == nil {
		t.Fatal("ambiguous prepared marker authorized replay")
	}
	after, err = os.ReadFile(filepath.Join(in.DataDir, "stackstead.db"))
	if err != nil || !bytes.Equal(after, before) {
		t.Fatal("ambiguous retry changed database")
	}
}
func TestChildPublicationRefusesTamper(t *testing.T) {
	e, in := childFixture(t)
	childDatabase(t, in)
	if _, err := e.InspectDB(context.Background(), "job-123", in); err != nil {
		t.Fatal(err)
	}
	copy := filepath.Join(e.socketRoot, "children", e.workerDigest, "child")
	if err := os.WriteFile(copy, []byte("tampered-worker"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := e.InspectDB(context.Background(), "job-123", in); err == nil {
		t.Fatal("reused tampered digest executable")
	}
	if err := os.Remove(copy); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(e.workerPath, copy); err != nil {
		t.Fatal(err)
	}
	if _, err := e.InspectDB(context.Background(), "job-123", in); err == nil {
		t.Fatal("followed digest executable symlink")
	}
}
