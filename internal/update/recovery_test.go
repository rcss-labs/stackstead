package update

import (
	"context"
	"errors"
	"testing"
)

func TestRecoveryAtEveryPhase(t *testing.T) {
	for _, phase := range []Phase{PhaseQuiescing, PhaseStopped, PhaseSnapshotted, PhaseInstalling, PhaseStartingProbation, PhaseVerifying, PhaseSucceeded, PhaseRollingBack, PhaseRolledBack, PhaseCancelled} {
		t.Run(string(phase), func(t *testing.T) {
			c, r, j := transactionFixture(t)
			j.Phase = phase
			j.Plan.States = []ServiceState{{InstanceID: "app", Running: true}}
			j.Probation = Probation{JobID: j.ID, Nonce: "previous-ticket", Expected: j.Plan.Current.Identity}
			j.Snapshots = []DBSnapshot{{InstanceID: "app", ConfigSHA256: "config", Schema: 1}}
			if phase == PhaseRolledBack {
				j.RecoveryDecision = RecoveryKeepOld
			}
			if phase == PhaseSucceeded {
				j.RecoveryDecision = RecoveryComplete
				j.ReleasePending = true
				j.Probation.Expected = j.Plan.Target.Identity
			}
			if phase == PhaseRollingBack {
				j.RecoveryDecision = RecoveryRollback
				j.RecoveryStep = RecoveryStop
			}
			if e := c.journal.Save(j); e != nil {
				t.Fatal(e)
			}
			if e := c.Recover(context.Background(), "install"); e != nil {
				t.Fatal(e)
			}
			got, _ := c.journal.Load("install")
			if got.Phase != PhaseRolledBack && got.Phase != PhaseSucceeded && got.Phase != PhaseCancelled {
				t.Fatalf("unfinished %s events %v", got.Phase, r.events)
			}
			if phase == PhaseQuiescing || phase == PhaseStopped || phase == PhaseSnapshotted {
				for _, event := range r.events {
					if event == "install" || event == "restore" || event == "old-install" {
						t.Fatalf("preinstall recovery mutated data: %v", r.events)
					}
				}
			}
		})
	}
}
func TestRecoveryPartialQuiesceUsesDurableSameTicket(t *testing.T) {
	c, r, _ := transactionFixture(t)
	r.fail = "quiesce"
	r.releaseFail = true
	if e := c.Recover(context.Background(), "install"); !errors.Is(e, ErrRecoveryRequired) {
		t.Fatalf("must retain failed cleanup: %v", e)
	}
	j, _ := c.journal.Load("install")
	ticket := j.Probation
	if ticket.Nonce == "" || j.Phase != PhaseRecoveryRequired {
		t.Fatalf("lost cleanup state %+v", j)
	}
	r.fail = ""
	r.releaseFail = false
	if e := c.Recover(context.Background(), "install"); e != nil {
		t.Fatal(e)
	}
	for _, p := range r.tickets {
		if p != ticket {
			t.Fatal("cleanup changed ticket")
		}
	}
	for _, event := range r.events {
		if event == "stop" || event == "install" || event == "restore" {
			t.Fatal(r.events)
		}
	}
}
func TestRecoveryRejectsMismatchedReadiness(t *testing.T) {
	c, r, j := transactionFixture(t)
	r.fail = "ready"
	if e := c.Recover(context.Background(), "install"); !errors.Is(e, ErrRecoveryRequired) {
		t.Fatalf("failed target and old readiness must require recovery: %v", e)
	}
	got, _ := c.journal.Load(j.InstallationID)
	if got.Phase != PhaseRecoveryRequired {
		t.Fatal(got.Phase)
	}
}
func TestRecoveryUnknownDecisionFailsClosed(t *testing.T) {
	c, r, j := transactionFixture(t)
	j.RecoveryDecision = "unknown"
	_ = c.journal.Save(j)
	if e := c.Recover(context.Background(), "install"); !errors.Is(e, ErrRecoveryRequired) {
		t.Fatal(e)
	}
	if len(r.events) != 0 {
		t.Fatal(r.events)
	}
}
func TestRecoveryDiagnosticsCannotChangeDecision(t *testing.T) {
	c, r, j := transactionFixture(t)
	j.Phase = PhaseRecoveryRequired
	j.RecoveryDecision = RecoveryKeepOld
	j.ReleasePending = true
	j.ErrorCode = "unrelated-diagnostic"
	j.Plan.States = []ServiceState{{InstanceID: "app", Running: true}}
	j.Probation = Probation{JobID: j.ID, Nonce: "current", Expected: j.Plan.Current.Identity}
	_ = c.journal.Save(j)
	if e := c.Recover(context.Background(), "install"); e != nil {
		t.Fatal(e)
	}
	for _, event := range r.events {
		if event == "install" || event == "restore" || event == "stop" {
			t.Fatal(r.events)
		}
	}
}
func TestRollbackResumesAfterRestoreWithoutRepeatingIt(t *testing.T) {
	for _, step := range []RecoveryStep{RecoveryStartOld, RecoveryVerifyOld, RecoveryFinalize} {
		t.Run(string(step), func(t *testing.T) {
			c, r, j := transactionFixture(t)
			j.Phase = PhaseRecoveryRequired
			j.RecoveryDecision = RecoveryRollback
			j.RecoveryStep = step
			j.Plan.States = []ServiceState{{InstanceID: "app", Running: true}}
			j.Probation = Probation{JobID: j.ID, Nonce: "old-boot", Expected: j.Plan.Current.Identity}
			j.Snapshots = []DBSnapshot{{InstanceID: "app", ConfigSHA256: "config", Schema: 1}}
			_ = c.journal.Save(j)
			if e := c.Recover(context.Background(), "install"); e != nil {
				t.Fatal(e)
			}
			for _, event := range r.events {
				if event == "restore" || event == "old-install" || event == "stop" {
					t.Fatalf("destructive replay after restore: %v", r.events)
				}
			}
		})
	}
}
func TestRecoveryRejectsContradictoryDurableState(t *testing.T) {
	for _, mutate := range []func(*Job){func(j *Job) { j.Phase = PhaseSucceeded }, func(j *Job) { j.Phase = PhaseRolledBack }, func(j *Job) { j.RecoveryDecision = RecoveryKeepOld; j.Phase = PhaseInstalling }, func(j *Job) { j.RecoveryDecision = RecoveryRollback; j.RecoveryStep = "unknown" }, func(j *Job) { j.RecoveryDecision = RecoveryComplete; j.Phase = PhaseRollingBack }, func(j *Job) { j.RecoveryDecision = RecoveryKeepOld; j.RecoveryStep = RecoveryRestoreDB }} {
		c, r, j := transactionFixture(t)
		mutate(&j)
		_ = c.journal.Save(j)
		if _, e := c.journal.Load("install"); !errors.Is(e, ErrRecoveryRequired) {
			t.Fatalf("contradictory journal accepted: %+v %v", j, e)
		}
		if e := c.Recover(context.Background(), "install"); !errors.Is(e, ErrRecoveryRequired) {
			t.Fatal(e)
		}
		if len(r.events) != 0 {
			t.Fatal(r.events)
		}
	}
}
func TestSuccessRegistrationFailureRetriesWithoutRestore(t *testing.T) {
	c, r, _ := transactionFixture(t)
	failed := false
	c.journal.store.beforeRename = func() error {
		j, e := c.journal.Load("install")
		if e == nil && j.Phase == PhaseSucceeded && !failed {
			failed = true
			return errors.New("registration disk failure")
		}
		return nil
	}
	if e := c.Recover(context.Background(), "install"); e == nil {
		t.Fatal("registration failure ignored")
	}
	j, e := c.journal.Load("install")
	if e != nil || j.Phase != PhaseSucceeded || !j.ReleasePending {
		t.Fatalf("lost committed recovery: %+v %v", j, e)
	}
	for _, event := range r.events {
		if event == "release" {
			t.Fatal("released before registration commit")
		}
	}
	c.journal.store.beforeRename = nil
	if e = c.Recover(context.Background(), "install"); e != nil {
		t.Fatal(e)
	}
	reg, e := readRegistration(c.journal.store, "install")
	if e != nil || reg.Identity != j.Plan.Target.Identity {
		t.Fatalf("identity not reconciled: %+v %v", reg, e)
	}
	for _, event := range r.events {
		if event == "restore" {
			t.Fatal("committed data restored")
		}
	}
}

type crashRuntime struct {
	Runtime
	after string
}

func (r *crashRuntime) StartProbation(ctx context.Context, p Plan, b Probation) error {
	e := r.Runtime.StartProbation(ctx, p, b)
	if e == nil && r.after == "start" {
		panic("helper crash after boot")
	}
	return e
}
func (r *crashRuntime) Release(ctx context.Context, p Plan, b Probation) error {
	e := r.Runtime.Release(ctx, p, b)
	if e == nil && r.after == "release" {
		panic("helper crash after release")
	}
	return e
}
func TestRollbackCrashAfterBootOrReleaseNeverRestoresAgain(t *testing.T) {
	for _, after := range []string{"start", "release"} {
		t.Run(after, func(t *testing.T) {
			c, r, j := transactionFixture(t)
			j.Phase = PhaseInstalling
			j.Plan.States = []ServiceState{{InstanceID: "app", Running: true}}
			j.Snapshots = []DBSnapshot{{InstanceID: "app", ConfigSHA256: "config", Schema: 1}}
			_ = c.journal.Save(j)
			c.runtime = &crashRuntime{Runtime: r, after: after}
			func() {
				defer func() {
					if recover() == nil {
						t.Error("crash not injected")
					}
				}()
				_ = c.Recover(context.Background(), "install")
			}()
			n := len(r.events)
			c = NewCoordinator(nil, c.journal, r)
			if e := c.Recover(context.Background(), "install"); e != nil {
				t.Fatal(e)
			}
			for _, event := range r.events[n:] {
				if event == "restore" || event == "old-install" {
					t.Fatal("replayed completed destructive action", r.events)
				}
			}
		})
	}
}

type startupRuntime struct {
	*transactionRuntime
	coordinator *Coordinator
	t           *testing.T
}

func (r *startupRuntime) StartProbation(ctx context.Context, p Plan, b Probation) error {
	ticket, e := r.coordinator.Startup(ctx, "install", "app")
	if e != nil || ticket == nil || *ticket != b {
		r.t.Fatalf("startup while transaction lock held: %v %v", ticket, e)
	}
	if _, e = r.coordinator.Status(ctx, "install"); e != nil {
		r.t.Fatal(e)
	}
	if _, e = r.coordinator.Begin(ctx, "install", BeginRequest{IdempotencyKey: "other"}); !errors.Is(e, ErrBusy) {
		r.t.Fatalf("concurrent begin: %v", e)
	}
	return r.transactionRuntime.StartProbation(ctx, p, b)
}
func TestRecoveryStartupNegotiatesDuringRealRecovery(t *testing.T) {
	c, r, j := transactionFixture(t)
	lock, e := c.journal.TryLock("install")
	if e != nil {
		t.Fatal(e)
	}
	if e = lock.SaveRegistration(j.Plan.Registration); e != nil {
		t.Fatal(e)
	}
	if e = lock.Close(); e != nil {
		t.Fatal(e)
	}
	j.Phase = PhaseInstalling
	j.Plan.States = []ServiceState{{InstanceID: "app", Running: true}}
	j.Snapshots = []DBSnapshot{{InstanceID: "app", ConfigSHA256: "config", Schema: 1}}
	_ = c.journal.Save(j)
	c.runtime = &startupRuntime{transactionRuntime: r, coordinator: c, t: t}
	if e := c.Recover(context.Background(), "install"); e != nil {
		t.Fatal(e)
	}
}

type inspectRuntime struct {
	Runtime
	action RecoveryAction
	err    error
}

func (r *inspectRuntime) Inspect(context.Context, Job) (RecoveryAction, error) {
	return r.action, r.err
}
func TestRecoveryCannotKeepOldAfterInstallationBegan(t *testing.T) {
	c, r, j := transactionFixture(t)
	j.Phase = PhaseInstalling
	j.Plan.States = []ServiceState{{InstanceID: "app", Running: true}}
	j.Probation = Probation{JobID: j.ID, Nonce: "before-install", Expected: j.Plan.Current.Identity}
	_ = c.journal.Save(j)
	c.runtime = &inspectRuntime{Runtime: r, action: RecoveryKeepOld}
	if e := c.Recover(context.Background(), "install"); !errors.Is(e, ErrRecoveryRequired) {
		t.Fatalf("postinstall kept old without restore: %v", e)
	}
	if len(r.events) != 0 {
		t.Fatal("mutated after contradictory inspection", r.events)
	}
}
func TestRecoveryInspectionFailureRetainsPostInstallOrigin(t *testing.T) {
	c, r, j := transactionFixture(t)
	j.Phase = PhaseInstalling
	j.Plan.States = []ServiceState{{InstanceID: "app", Running: true}}
	j.Probation = Probation{JobID: j.ID, Nonce: "before-install", Expected: j.Plan.Current.Identity}
	_ = c.journal.Save(j)
	edge := &inspectRuntime{Runtime: r, err: ErrUnavailable}
	c.runtime = edge
	if e := c.Recover(context.Background(), "install"); !errors.Is(e, ErrRecoveryRequired) {
		t.Fatal(e)
	}
	edge.err = nil
	edge.action = RecoveryKeepOld
	if e := c.Recover(context.Background(), "install"); !errors.Is(e, ErrRecoveryRequired) {
		t.Fatalf("forgot postinstall origin: %v", e)
	}
	if len(r.events) != 0 {
		t.Fatal(r.events)
	}
}
