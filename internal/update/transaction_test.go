package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type transactionRuntime struct {
	journal     *Journal
	events      []string
	fail        string
	releaseFail bool
	tickets     []Probation
}

func (r *transactionRuntime) event(name string, phases ...Phase) error {
	j, e := r.journal.Load("install")
	if e != nil {
		return e
	}
	allowed := false
	for _, p := range phases {
		allowed = allowed || j.Phase == p
	}
	if !allowed {
		return errors.New("mutation before durable phase: " + name + " " + string(j.Phase))
	}
	r.events = append(r.events, name)
	if r.fail == name {
		return errors.New("injected " + name)
	}
	return nil
}
func (r *transactionRuntime) Preflight(context.Context, Plan) ([]ServiceState, error) {
	return []ServiceState{{InstanceID: "app", Running: true}}, r.event("preflight", PhaseVerified)
}
func (r *transactionRuntime) Quiesce(_ context.Context, p Plan, b Probation) error {
	j, _ := r.journal.Load("install")
	if len(j.Plan.States) != 1 || j.Probation != b || b.Expected != p.Current.Identity {
		return errors.New("missing durable current ticket")
	}
	r.tickets = append(r.tickets, b)
	return r.event("quiesce", PhaseQuiescing, PhaseRecoveryRequired)
}
func (r *transactionRuntime) Stop(context.Context, Plan) error {
	return r.event("stop", PhaseQuiescing, PhaseRollingBack)
}
func (r *transactionRuntime) Snapshot(context.Context, Plan) ([]DBSnapshot, error) {
	return []DBSnapshot{{InstanceID: "app", ConfigSHA256: "config", Schema: 1}}, r.event("snapshot", PhaseStopped)
}
func (r *transactionRuntime) Install(_ context.Context, p Plan, a VerifiedArtifact) error {
	if a == p.PreviousArtifact {
		return r.event("old-install", PhaseRollingBack)
	}
	return r.event("install", PhaseInstalling)
}
func (r *transactionRuntime) StartProbation(_ context.Context, p Plan, b Probation) error {
	j, _ := r.journal.Load("install")
	if j.Probation != b {
		return errors.New("ticket not durable")
	}
	r.tickets = append(r.tickets, b)
	return r.event("start", PhaseStartingProbation, PhaseRollingBack, PhaseRecoveryRequired)
}
func (r *transactionRuntime) Ready(_ context.Context, p Plan, b Probation) ([]Readiness, error) {
	e := r.event("ready", PhaseVerifying, PhaseRollingBack)
	return []Readiness{{InstanceID: "app", JobID: b.JobID, Nonce: b.Nonce, Identity: b.Expected, ConfigSHA256: "config", PID: 1, Schema: 1, Ready: true}}, e
}
func (r *transactionRuntime) Release(context.Context, Plan, Probation) error {
	if r.releaseFail {
		return errors.New("lost release acknowledgement")
	}
	return r.event("release", PhaseSucceeded, PhaseRolledBack, PhaseRecoveryRequired)
}
func (r *transactionRuntime) Restore(context.Context, Plan, []DBSnapshot) error {
	return r.event("restore", PhaseRollingBack)
}
func (r *transactionRuntime) Inspect(_ context.Context, j Job) (RecoveryAction, error) {
	if j.RecoveryDecision == RecoveryRollback {
		return RecoveryRollback, nil
	}
	if j.Phase == PhaseSucceeded {
		return RecoveryComplete, nil
	}
	if j.Phase == PhaseInstalling || j.Phase == PhaseStartingProbation || j.Phase == PhaseVerifying || j.Phase == PhaseRollingBack {
		return RecoveryRollback, nil
	}
	return RecoveryKeepOld, nil
}
func transactionFixture(t *testing.T) (*Coordinator, *transactionRuntime, Job) {
	t.Helper()
	j := testJournal(t)
	source := filepath.Join(j.root, "worker-source")
	if e := os.WriteFile(source, []byte("worker"), 0700); e != nil {
		t.Fatal(e)
	}
	digest, e := j.pinWorker(source)
	if e != nil {
		t.Fatal(e)
	}
	reg := validTestRegistration()
	reg.ID = "install"
	reg.UpdateEnabled = true
	reg.Instances[0].ID = "app"
	job := Job{ID: "job", InstallationID: "install", Protocol: 1, Phase: PhaseVerified, WorkerSHA256: digest, Plan: Plan{Registration: reg, Current: Candidate{Identity: reg.Identity, Manifest: Manifest{TargetSchema: 1}}, Target: Candidate{Identity: Identity{Version: "2.0.0", Commit: reg.Identity.Commit, OS: reg.Identity.OS, Arch: reg.Identity.Arch}, Manifest: Manifest{TargetSchema: 1}}, PreviousArtifact: VerifiedArtifact{Name: "old"}, TargetArtifact: VerifiedArtifact{Name: "new"}}}
	if e = j.Save(job); e != nil {
		t.Fatal(e)
	}
	r := &transactionRuntime{journal: j}
	return NewCoordinator(nil, j, r), r, job
}
func TestTransactionPersistsBeforeEachMutation(t *testing.T) {
	c, r, _ := transactionFixture(t)
	if e := c.Recover(context.Background(), "install"); e != nil {
		t.Fatal(e)
	}
	want := []string{"preflight", "quiesce", "stop", "snapshot", "install", "start", "ready", "release"}
	if !reflect.DeepEqual(r.events, want) {
		t.Fatalf("mutation order: %v want %v", r.events, want)
	}
	if len(r.tickets) != 2 || r.tickets[0].Nonce == r.tickets[1].Nonce {
		t.Fatal("target boot must get a fresh nonce")
	}
}
func TestTransactionPreflightFailureNeverStops(t *testing.T) {
	c, r, _ := transactionFixture(t)
	r.fail = "preflight"
	_ = c.Recover(context.Background(), "install")
	if !reflect.DeepEqual(r.events, []string{"preflight"}) {
		t.Fatal(r.events)
	}
}
func TestSuccessNeverRestoresSnapshot(t *testing.T) {
	c, r, _ := transactionFixture(t)
	r.releaseFail = true
	_ = c.Recover(context.Background(), "install")
	job, _ := c.journal.Load("install")
	if job.Phase != PhaseSucceeded {
		t.Fatalf("success not durable: %s", job.Phase)
	}
	r.releaseFail = false
	if e := c.Recover(context.Background(), "install"); e != nil {
		t.Fatal(e)
	}
	for _, e := range r.events {
		if e == "restore" {
			t.Fatal("restored committed snapshot")
		}
	}
}
func TestRollbackFailureRequiresRecovery(t *testing.T) {
	c, r, job := transactionFixture(t)
	job.Phase = PhaseInstalling
	job.Plan.States = []ServiceState{{InstanceID: "app", Running: true}}
	_ = c.journal.Save(job)
	r.fail = "restore"
	if e := c.Recover(context.Background(), "install"); !errors.Is(e, ErrRecoveryRequired) {
		t.Fatalf("expected recovery required: %v", e)
	}
	j, _ := c.journal.Load("install")
	if j.Phase != PhaseRecoveryRequired {
		t.Fatal(j.Phase)
	}
}
func TestRecoveryServesStartupWhileHoldingInstallLock(t *testing.T) {
	c, _, job := transactionFixture(t)
	job.Phase = PhaseVerifying
	job.Plan.States = []ServiceState{{InstanceID: "app", Running: true}}
	job.Probation = Probation{JobID: "job", Nonce: "boot", Expected: job.Plan.Target.Identity}
	_ = c.journal.Save(job)
	l, e := c.journal.TryLock("install")
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	p, e := c.Startup(context.Background(), "install", "app")
	if e != nil || p == nil || *p != job.Probation {
		t.Fatalf("startup blocked/missing: %v %v", p, e)
	}
	if _, e = c.Begin(context.Background(), "install", BeginRequest{IdempotencyKey: "new"}); !errors.Is(e, ErrBusy) {
		t.Fatalf("begin not busy: %v", e)
	}
}
func TestTransactionDuplicateBeginIsIdempotent(t *testing.T) {
	c, _, job := transactionFixture(t)
	job.IdempotencyKey = "same"
	_ = c.journal.Save(job)
	v, e := c.Begin(context.Background(), "install", BeginRequest{IdempotencyKey: "same"})
	if e != nil || v.ID != job.ID {
		t.Fatalf("duplicate created another job: %+v %v", v, e)
	}
}
func TestTransactionCancelAfterVerifiedConflicts(t *testing.T) {
	c, _, j := transactionFixture(t)
	j.Phase = PhaseQuiescing
	_ = c.journal.Save(j)
	if _, e := c.Cancel(context.Background(), "install", j.ID); !errors.Is(e, ErrBusy) {
		t.Fatalf("late cancellation: %v", e)
	}
}

type stoppedRuntime struct{ *transactionRuntime }

func (r *stoppedRuntime) Preflight(context.Context, Plan) ([]ServiceState, error) {
	return []ServiceState{{InstanceID: "app", Running: false}}, r.event("preflight", PhaseVerified)
}
func (r *stoppedRuntime) Ready(context.Context, Plan, Probation) ([]Readiness, error) {
	return nil, r.event("ready", PhaseVerifying)
}
func TestTransactionStoppedInstanceReportsPendingStart(t *testing.T) {
	c, r, _ := transactionFixture(t)
	c.runtime = &stoppedRuntime{r}
	if e := c.Recover(context.Background(), "install"); e != nil {
		t.Fatal(e)
	}
	j, e := c.journal.Load("install")
	if e != nil {
		t.Fatal(e)
	}
	if j.ErrorMessage != "installed-pending-start" {
		t.Fatalf("missing pending start message: %q", j.ErrorMessage)
	}
	if j.Plan.States[0].Running {
		t.Fatal("invented readiness for stopped instance")
	}
}
