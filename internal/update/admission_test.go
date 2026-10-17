package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestGlobalReservationSerializesPrefixes(t *testing.T) {
	j := testJournal(t)
	other, _ := openTestJournal(j.root)
	l, err := j.TryLock("first")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if competing, err := other.TryLock("second"); !errors.Is(err, ErrBusy) {
		if competing != nil {
			competing.Close()
		}
		t.Fatalf("different prefix admitted: %v", err)
	}
	if reservation, err := other.TryExternalReservation(); !errors.Is(err, ErrBusy) {
		if reservation != nil {
			reservation.Close()
		}
		t.Fatalf("external operation admitted: %v", err)
	}
	l.Close()
	reservation, err := other.TryExternalReservation()
	if err != nil {
		t.Fatal(err)
	}
	defer reservation.Close()
	if competing, err := j.TryLock("first"); !errors.Is(err, ErrBusy) {
		if competing != nil {
			competing.Close()
		}
		t.Fatalf("update admitted during external operation: %v", err)
	}
}

func TestGlobalPendingJobBlocksAfterCrash(t *testing.T) {
	c, _, job := transactionFixture(t)
	other, _ := openTestJournal(c.journal.root)
	reg := validTestRegistration()
	reg.ID = "other"
	reg.Prefix = "/opt/other"
	reg.Binary = "/opt/other/bin/stackstead"
	reg.UpdateEnabled = true
	// Seed a preexisting registration through the trusted held-lock fixture API.
	l, err := c.journal.TryLock("other")
	if err != nil {
		t.Fatal(err)
	}
	if err = l.SaveRegistration(reg); err != nil {
		t.Fatal(err)
	}
	l.Close()
	second := NewCoordinator(nil, other, c.runtime)
	if _, err = second.Begin(context.Background(), "other", BeginRequest{IdempotencyKey: "different"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("crash-released job did not block Begin: %v", err)
	}
	if err = other.writeRegistration(reg); !errors.Is(err, ErrBusy) {
		t.Fatalf("pending job allowed registration: %v", err)
	}
	if err = second.Recover(context.Background(), "other"); !errors.Is(err, ErrBusy) {
		t.Fatalf("another registration recovery admitted: %v", err)
	}
	status, err := second.Status(context.Background(), "other")
	if err != nil || status.Capability != CapabilityBusy || status.Job != nil {
		t.Fatalf("cross-installation status leaked or idle: %+v %v", status, err)
	}
	if reservation, err := other.TryExternalReservation(); !errors.Is(err, ErrBusy) {
		if reservation != nil {
			reservation.Close()
		}
		t.Fatalf("pending job allowed external operation: %v", err)
	}
	pending, err := other.PendingJob()
	if err != nil || pending == nil || pending.ID != job.ID {
		t.Fatalf("pending job lost: %+v %v", pending, err)
	}
	// A corrupt legacy host with two pending records must never pick one.
	duplicate := job
	duplicate.InstallationID = "other"
	duplicate.ID = "second"
	if err = other.Save(duplicate); err != nil {
		t.Fatal(err)
	}
	if _, err = other.PendingJob(); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("ambiguous pending jobs accepted: %v", err)
	}
	if _, err = other.selectWorker(nil); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("ambiguous bootstrap selected worker: %v", err)
	}
}

func TestBootstrapSelectsOnlyPendingWorker(t *testing.T) {
	c, _, job := transactionFixture(t)
	j := c.journal
	source := filepath.Join(j.root, "new-worker")
	if err := os.WriteFile(source, []byte("new worker"), 0700); err != nil {
		t.Fatal(err)
	}
	digest, err := j.pinWorker(source)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.store.atomic("workers", "active.json", []byte(`"`+digest+`"`), 0600); err != nil {
		t.Fatal(err)
	}
	selected, err := j.selectWorker(nil)
	if err != nil || filepath.Base(filepath.Dir(selected)) != job.WorkerSHA256 {
		t.Fatalf("bootstrap ignored pending worker: %s %v", selected, err)
	}
	wrong := job
	wrong.WorkerSHA256 = digest
	if _, err = j.selectWorker(&wrong); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("unrecorded worker accepted: %v", err)
	}
}

func TestIdlePromotionRejectsPendingJob(t *testing.T) {
	c, _, job := transactionFixture(t)
	if err := c.journal.setActiveWorker(job.WorkerSHA256); !errors.Is(err, ErrBusy) {
		t.Fatalf("promotion during pending job: %v", err)
	}
	job.Phase = PhaseCancelled
	if err := c.journal.Save(job); err != nil {
		t.Fatal(err)
	}
	if err := c.journal.setActiveWorker(job.WorkerSHA256); err != nil {
		t.Fatalf("idle promotion rejected: %v", err)
	}
}

func TestStartupRejectsOriginallyStoppedPeer(t *testing.T) {
	for _, state := range []string{"stopped", "unloaded", "unknown", "duplicate"} {
		t.Run(state, func(t *testing.T) {
			c, _, job := transactionFixture(t)
			job.Phase = PhaseVerifying
			job.Probation = Probation{JobID: job.ID, Nonce: "nonce", Expected: job.Plan.Target.Identity}
			if state != "unknown" {
				job.Plan.States = []ServiceState{{InstanceID: "app", Running: false, Loaded: state != "unloaded"}}
			}
			if state == "duplicate" {
				job.Plan.States = append(job.Plan.States, ServiceState{InstanceID: "app", Running: true})
			}
			if err := c.journal.Save(job); err != nil {
				t.Fatal(err)
			}
			if ticket, err := c.Startup(context.Background(), "install", "app"); ticket != nil || (!errors.Is(err, ErrBusy) && !errors.Is(err, ErrRecoveryRequired)) {
				t.Fatalf("unsafe startup admitted: %+v %v", ticket, err)
			}
		})
	}
}

func TestReadOnlyNegotiationDoesNotWaitForGlobalReservation(t *testing.T) {
	c, _, job := transactionFixture(t)
	j := c.journal
	reg := job.Plan.Registration
	l, err := j.TryLock("install")
	if err != nil {
		t.Fatal(err)
	}
	if err = l.SaveRegistration(reg); err != nil {
		t.Fatal(err)
	}
	l.Close()
	job.Phase = PhaseVerifying
	job.Plan.States = []ServiceState{{InstanceID: "app", Running: true}}
	job.Probation = Probation{JobID: job.ID, Nonce: "nonce", Expected: job.Plan.Target.Identity}
	if err = j.Save(job); err != nil {
		t.Fatal(err)
	}
	reservation, err := j.TryLock("install")
	if err != nil {
		t.Fatal(err)
	}
	defer reservation.Close()
	done := make(chan error, 1)
	go func() {
		p, e := c.Startup(context.Background(), "install", "app")
		if e == nil && p == nil {
			e = errors.New("missing ticket")
		}
		if e == nil {
			_, e = c.Status(context.Background(), "install")
		}
		if e == nil {
			_, e = c.Job(context.Background(), "install", job.ID)
		}
		done <- e
	}()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("read-only negotiation waited for global lock")
	}
}

func TestGlobalDiscoveryRejectsUnexpectedEvidence(t *testing.T) {
	for _, directory := range []string{"jobs", "registrations"} {
		for _, kind := range []string{"unexpected", "malformed", "symlink", "oversized", "ambiguous", "bound"} {
			t.Run(directory+"/"+kind, func(t *testing.T) {
				j := testJournal(t)
				d, err := j.store.directory(directory, true)
				if err != nil {
					t.Fatal(err)
				}
				d.Close()
				path := filepath.Join(j.root, directory, "record.json")
				switch kind {
				case "unexpected":
					path = filepath.Join(j.root, directory, "surprise")
					err = os.WriteFile(path, nil, 0600)
				case "malformed":
					err = os.WriteFile(path, []byte(`{"broken":true}`), 0600)
				case "symlink":
					err = os.Symlink("/dev/null", path)
				case "oversized":
					err = os.WriteFile(path, make([]byte, stateLimit+1), 0600)
				case "bound":
					for n := 0; n < 1025; n++ {
						err = os.WriteFile(filepath.Join(j.root, directory, fmt.Sprintf("record-%d.json", n)), nil, 0600)
						if err != nil {
							break
						}
					}
				case "ambiguous":
					if directory == "jobs" {
						for _, id := range []string{"first", "second"} {
							err = j.Save(Job{ID: id, InstallationID: id, Protocol: JournalProtocol, Phase: PhaseAccepted})
							if err != nil {
								break
							}
						}
					} else {
						reg := validTestRegistration()
						for _, id := range []string{"first", "second"} {
							reg.ID = id
							data, _ := json.Marshal(reg)
							err = j.store.atomic(directory, id+".json", data, 0600)
							if err != nil {
								break
							}
						}
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				if directory == "jobs" {
					if _, err = j.PendingJob(); !errors.Is(err, ErrRecoveryRequired) {
						t.Fatalf("unsafe jobs accepted: %v", err)
					}
				} else {
					if _, err = j.Registrations(); !errors.Is(err, ErrRecoveryRequired) {
						t.Fatalf("unsafe registrations accepted: %v", err)
					}
					if _, err = j.selectWorker(nil); !errors.Is(err, ErrRecoveryRequired) {
						t.Fatalf("bootstrap ignored unsafe registrations: %v", err)
					}
				}
			})
		}
	}
}

func TestGlobalDiscoveryIgnoresOnlyAtomicTemps(t *testing.T) {
	j := testJournal(t)
	for _, directory := range []string{"jobs", "registrations"} {
		if err := j.store.atomic(directory, ".tmp-0123456789abcdef0123456789abcdef", []byte("interrupted"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if pending, err := j.PendingJob(); err != nil || pending != nil {
		t.Fatalf("atomic temp treated as pending: %+v %v", pending, err)
	}
	if regs, err := j.Registrations(); err != nil || len(regs) != 0 {
		t.Fatalf("atomic temp treated as registration: %+v %v", regs, err)
	}
}

func TestGlobalDownloadingOwnsReservation(t *testing.T) {
	c, _ := beginFixture(t)
	reg := validTestRegistration()
	reg.ID = "other"
	reg.Prefix = "/opt/other"
	reg.Binary = "/opt/other/bin/stackstead"
	reg.UpdateEnabled = true
	if err := c.journal.writeRegistration(reg); err != nil {
		t.Fatal(err)
	}
	other, _ := openTestJournal(c.journal.root)
	entered := make(chan struct{})
	c.releases.client.Transport = releaseTransport(func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	v, err := c.Begin(context.Background(), "install", BeginRequest{CandidateID: c.candidates["install"].ID, IdempotencyKey: "download"})
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	second := NewCoordinator(nil, other, c.runtime)
	if _, err = second.Begin(context.Background(), "other", BeginRequest{IdempotencyKey: "compete"}); !errors.Is(err, ErrBusy) {
		t.Errorf("Begin during download: %v", err)
	}
	if err = second.Recover(context.Background(), "other"); !errors.Is(err, ErrBusy) {
		t.Errorf("Recover during download: %v", err)
	}
	if err = other.writeRegistration(reg); !errors.Is(err, ErrBusy) {
		t.Errorf("registration during download: %v", err)
	}
	if reservation, e := other.TryExternalReservation(); !errors.Is(e, ErrBusy) {
		if reservation != nil {
			reservation.Close()
		}
		t.Errorf("external during download: %v", e)
	}
	if _, err = c.Cancel(context.Background(), "install", v.ID); err != nil {
		t.Fatal(err)
	}
}

func TestGlobalReservationAcrossProcesses(t *testing.T) {
	if root := os.Getenv("STACKSTEAD_TEST_GLOBAL_LOCK_ROOT"); root != "" {
		j, err := openTestJournal(root)
		if err != nil {
			t.Fatal(err)
		}
		lock, err := j.TryLock("different-prefix")
		if lock != nil {
			lock.Close()
		}
		if !errors.Is(err, ErrBusy) {
			t.Fatalf("another process entered: %v", err)
		}
		return
	}
	j := testJournal(t)
	lock, err := j.TryLock("first-prefix")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, executable, "-test.run=^TestGlobalReservationAcrossProcesses$", "-test.count=1")
	child.Env = append(os.Environ(), "STACKSTEAD_TEST_GLOBAL_LOCK_ROOT="+j.root)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("cross-process flock: %v\n%s", err, output)
	}
}

func TestGlobalReleasePendingRemainsOwned(t *testing.T) {
	c, _, job := transactionFixture(t)
	job.Phase = PhaseSucceeded
	job.RecoveryDecision = RecoveryComplete
	job.ReleasePending = true
	job.Probation = Probation{JobID: job.ID, Nonce: "nonce", Expected: job.Plan.Target.Identity}
	job.Plan.States = []ServiceState{{InstanceID: "app", Running: false}}
	if err := c.journal.Save(job); err != nil {
		t.Fatal(err)
	}
	if p, err := c.journal.PendingJob(); err != nil || p == nil || p.ID != job.ID {
		t.Fatalf("terminal pending job lost: %+v %v", p, err)
	}
	if err := c.journal.setActiveWorker(job.WorkerSHA256); !errors.Is(err, ErrBusy) {
		t.Fatalf("release-pending promotion admitted: %v", err)
	}
	if p, err := c.Startup(context.Background(), "install", "app"); p != nil || !errors.Is(err, ErrBusy) {
		t.Fatalf("release-pending stopped startup: %+v %v", p, err)
	}
	job.ReleasePending = false
	if err := c.journal.Save(job); err != nil {
		t.Fatal(err)
	}
	if p, err := c.Startup(context.Background(), "install", "app"); p != nil || err != nil {
		t.Fatalf("idle startup refused: %+v %v", p, err)
	}
}
