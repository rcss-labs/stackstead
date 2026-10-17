package update

import (
	"context"
	"fmt"
)

type RecoveryAction string

const (
	RecoveryKeepOld        RecoveryAction = "keep-old"
	RecoveryRollback       RecoveryAction = "rollback"
	RecoveryComplete       RecoveryAction = "complete"
	RecoveryRepairRequired RecoveryAction = "repair-required"
)

type Runtime interface {
	Preflight(context.Context, Plan) ([]ServiceState, error)
	Quiesce(context.Context, Plan, Probation) error
	Stop(context.Context, Plan) error
	Snapshot(context.Context, Plan) ([]DBSnapshot, error)
	Install(context.Context, Plan, VerifiedArtifact) error
	StartProbation(context.Context, Plan, Probation) error
	Ready(context.Context, Plan, Probation) ([]Readiness, error)
	Release(context.Context, Plan, Probation) error
	Restore(context.Context, Plan, []DBSnapshot) error
	Inspect(context.Context, Job) (RecoveryAction, error)
}

func (c *Coordinator) ticket(j *Job, identity Identity) error {
	nonce, e := randomID()
	if e != nil {
		return e
	}
	j.Probation = Probation{JobID: j.ID, Nonce: nonce, Expected: identity}
	return nil
}
func (c *Coordinator) execute(ctx context.Context, l *InstallationLock, j *Job) error {
	if _, e := c.journal.selectWorker(j); e != nil {
		return e
	}
	states, e := c.runtime.Preflight(ctx, j.Plan)
	if e != nil {
		j.ErrorCode = "preflight-failed"
		j.ErrorMessage = "Update preflight failed"
		return c.save(l, j, PhaseCancelled)
	}
	j.Plan.States = states
	if e = c.save(l, j, PhasePreflighted); e != nil {
		return e
	}
	if e = c.ticket(j, j.Plan.Current.Identity); e != nil {
		return e
	}
	if e = c.save(l, j, PhaseQuiescing); e != nil {
		return e
	}
	if e = c.runtime.Quiesce(ctx, j.Plan, j.Probation); e != nil {
		return c.keepOld(ctx, l, j)
	}
	if e = c.runtime.Stop(ctx, j.Plan); e != nil {
		return c.requireRecovery(l, j, e)
	}
	if e = c.save(l, j, PhaseStopped); e != nil {
		return e
	}
	j.Snapshots, e = c.runtime.Snapshot(ctx, j.Plan)
	if e != nil {
		return c.keepOld(ctx, l, j)
	}
	if e = c.save(l, j, PhaseSnapshotted); e != nil {
		return e
	}
	if e = c.save(l, j, PhaseInstalling); e != nil {
		return e
	}
	if e = c.runtime.Install(ctx, j.Plan, j.Plan.TargetArtifact); e != nil {
		return c.rollback(ctx, l, j)
	}
	if e = c.ticket(j, j.Plan.Target.Identity); e != nil {
		return e
	}
	if e = c.save(l, j, PhaseStartingProbation); e != nil {
		return e
	}
	if e = c.runtime.StartProbation(ctx, j.Plan, j.Probation); e != nil {
		return c.rollback(ctx, l, j)
	}
	if e = c.save(l, j, PhaseVerifying); e != nil {
		return e
	}
	if e = c.readiness(ctx, *j); e != nil {
		return c.rollback(ctx, l, j)
	}
	for _, state := range j.Plan.States {
		if !state.Running {
			j.ErrorMessage = "installed-pending-start"
			break
		}
	}
	j.RecoveryDecision = RecoveryComplete
	j.ReleasePending = true
	if e = c.save(l, j, PhaseSucceeded); e != nil {
		return e
	}
	return c.finishRelease(ctx, l, j)
}
func (c *Coordinator) readiness(ctx context.Context, j Job) error {
	rows, e := c.runtime.Ready(ctx, j.Plan, j.Probation)
	if e != nil {
		return e
	}
	found := map[string]bool{}
	for _, r := range rows {
		if found[r.InstanceID] || !r.Ready || r.JobID != j.Probation.JobID || r.Nonce != j.Probation.Nonce || r.Identity != j.Probation.Expected || r.PID <= 0 || r.ConfigSHA256 == "" {
			return ErrIncompatible
		}
		found[r.InstanceID] = true
		schema := j.Plan.Target.Manifest.TargetSchema
		if j.Probation.Expected == j.Plan.Current.Identity {
			schema = j.Plan.Current.Manifest.TargetSchema
		}
		if r.Schema != schema {
			return ErrIncompatible
		}
		match := false
		for _, s := range j.Snapshots {
			if s.InstanceID == r.InstanceID {
				match = s.ConfigSHA256 == r.ConfigSHA256
			}
		}
		if !match {
			return ErrIncompatible
		}
	}
	for _, s := range j.Plan.States {
		if s.Running && !found[s.InstanceID] {
			return ErrIncompatible
		}
	}
	return nil
}
func (c *Coordinator) finishRelease(ctx context.Context, l *InstallationLock, j *Job) error {
	if !j.ReleasePending {
		return nil
	}
	reg := j.Plan.Registration
	if j.Phase == PhaseSucceeded {
		reg.Identity = j.Plan.Target.Identity
	} else {
		reg.Identity = j.Plan.Current.Identity
	}
	if e := l.SaveRegistration(reg); e != nil {
		return e
	}
	if e := c.runtime.Release(ctx, j.Plan, j.Probation); e != nil {
		return e
	}
	j.ReleasePending = false
	return c.save(l, j, j.Phase)
}
func (c *Coordinator) requireRecovery(l *InstallationLock, j *Job, cause error) error {
	if j.RecoveryDecision == "" {
		j.RecoveryDecision = RecoveryRepairRequired
	}
	j.ErrorCode = "recovery-required"
	j.ErrorMessage = "Update recovery requires attention"
	if e := c.save(l, j, PhaseRecoveryRequired); e != nil {
		return e
	}
	return fmt.Errorf("%w: %v", ErrRecoveryRequired, cause)
}
