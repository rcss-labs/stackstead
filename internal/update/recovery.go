package update

import (
	"context"
	"errors"
	"os"
)

// RecoveryStep records the next rollback action. Advancing it is durable before
// another action begins; after start-old no database restoration is permitted.
type RecoveryStep string

const (
	RecoveryStop       RecoveryStep = "stop"
	RecoveryInstallOld RecoveryStep = "install-old"
	RecoveryRestoreDB  RecoveryStep = "restore-db"
	RecoveryStartOld   RecoveryStep = "start-old"
	RecoveryVerifyOld  RecoveryStep = "verify-old"
	RecoveryFinalize   RecoveryStep = "finalize"
)

func validRecovery(j Job) bool {
	switch j.Phase {
	case PhaseAccepted, PhaseResolving, PhaseDownloading, PhaseVerified, PhasePreflighted, PhaseQuiescing, PhaseStopped, PhaseSnapshotted, PhaseInstalling, PhaseStartingProbation, PhaseVerifying, PhaseSucceeded, PhaseRollingBack, PhaseRolledBack, PhaseRecoveryRequired, PhaseCancelled:
	default:
		return false
	}
	switch j.RecoveryDecision {
	case "":
		return j.RecoveryStep == "" && !j.ReleasePending && j.Phase != PhaseRecoveryRequired && j.Phase != PhaseRollingBack && j.Phase != PhaseSucceeded && j.Phase != PhaseRolledBack
	case RecoveryKeepOld:
		return j.RecoveryStep == "" && (j.Phase == PhaseRecoveryRequired || j.Phase == PhaseRolledBack) && j.Probation.Expected == j.Plan.Current.Identity
	case RecoveryComplete:
		return j.RecoveryStep == "" && j.Phase == PhaseSucceeded && j.Probation.Expected == j.Plan.Target.Identity
	case RecoveryRepairRequired:
		return j.RecoveryStep == "" && j.Phase == PhaseRecoveryRequired && !j.ReleasePending
	case RecoveryRollback:
		if j.Phase != PhaseRollingBack && j.Phase != PhaseRecoveryRequired && j.Phase != PhaseRolledBack {
			return false
		}
		switch j.RecoveryStep {
		case RecoveryStop, RecoveryInstallOld, RecoveryRestoreDB:
			return !j.ReleasePending && j.Phase != PhaseRolledBack
		case RecoveryStartOld, RecoveryVerifyOld:
			return !j.ReleasePending && j.Phase != PhaseRolledBack && j.Probation.Expected == j.Plan.Current.Identity
		case RecoveryFinalize:
			return j.Probation.Expected == j.Plan.Current.Identity
		}
	}
	return false
}
func (c *Coordinator) Recover(ctx context.Context, id string) error {
	if c.journal == nil || c.runtime == nil {
		return ErrUnavailable
	}
	l, e := c.journal.TryLock(id)
	if e != nil {
		return e
	}
	defer l.Close()
	pending, e := c.journal.PendingJob()
	if e != nil {
		return e
	}
	if pending != nil && pending.InstallationID != id {
		return ErrBusy
	}
	j, e := c.journal.Load(id)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	if !validRecovery(j) {
		return ErrRecoveryRequired
	}
	if _, e = c.journal.selectWorker(&j); e != nil {
		return e
	}
	if j.Phase == PhaseSucceeded {

		return c.finishRelease(ctx, l, &j)
	}
	if j.Phase == PhaseRolledBack {
		return c.finishRelease(ctx, l, &j)
	}
	if j.Phase == PhaseCancelled {
		return nil
	}
	switch j.RecoveryDecision {
	case RecoveryKeepOld:
		return c.keepOld(ctx, l, &j)
	case RecoveryRollback:
		if j.RecoveryStep == RecoveryStop || j.RecoveryStep == RecoveryInstallOld || j.RecoveryStep == RecoveryRestoreDB {
			action, e := c.runtime.Inspect(ctx, j)
			if e != nil || action != RecoveryRollback {
				return c.requireRecovery(l, &j, ErrRecoveryRequired)
			}
		}
		return c.rollback(ctx, l, &j)
	}
	switch j.Phase {
	case PhaseAccepted, PhaseResolving, PhaseDownloading:
		j.ErrorCode = "interrupted"
		j.ErrorMessage = "Update preparation interrupted"
		return c.save(l, &j, PhaseCancelled)
	case PhaseVerified:
		return c.execute(ctx, l, &j)
	case PhasePreflighted:
		return c.save(l, &j, PhaseCancelled)
	}
	postInstall := j.Phase == PhaseInstalling || j.Phase == PhaseStartingProbation || j.Phase == PhaseVerifying
	if postInstall {
		j.RecoveryDecision = RecoveryRollback
		j.RecoveryStep = RecoveryStop
		if e = c.save(l, &j, PhaseRollingBack); e != nil {
			return e
		}
	}
	action, e := c.runtime.Inspect(ctx, j)
	if e != nil {
		return c.requireRecovery(l, &j, e)
	}
	switch action {
	case RecoveryKeepOld:
		if postInstall {
			return c.requireRecovery(l, &j, ErrRecoveryRequired)
		}
		return c.keepOld(ctx, l, &j)
	case RecoveryRollback:
		return c.rollback(ctx, l, &j)
	default:
		return c.requireRecovery(l, &j, ErrRecoveryRequired)
	}
}
func (c *Coordinator) keepOld(ctx context.Context, l *InstallationLock, j *Job) error {
	j.RecoveryDecision = RecoveryKeepOld
	j.ReleasePending = true
	if e := c.save(l, j, PhaseRecoveryRequired); e != nil {
		return e
	}
	// Peer routing is transient: retain the complete original registration/state
	// set in the journal, and never release a peer whose probe/start is unproven.
	var unresolved error
	registered := false
	reg := j.Plan.Registration
	reg.Identity = j.Plan.Current.Identity
	for _, state := range j.Plan.States {
		if !state.Running {
			continue
		}
		peerPlan := j.Plan
		peerPlan.States = []ServiceState{state}
		if e := c.runtime.StartProbation(ctx, peerPlan, j.Probation); e != nil {
			unresolved = errors.Join(unresolved, e)
			continue
		}
		if !registered {
			if e := l.SaveRegistration(reg); e != nil {
				return c.requireRecovery(l, j, e)
			}
			registered = true
		}
		if e := c.runtime.Release(ctx, peerPlan, j.Probation); e != nil {
			unresolved = errors.Join(unresolved, e)
		}
	}
	if unresolved != nil {
		return c.requireRecovery(l, j, unresolved)
	}
	if !registered {
		if e := l.SaveRegistration(reg); e != nil {
			return c.requireRecovery(l, j, e)
		}
	}

	j.ReleasePending = false
	j.ErrorCode = "update-aborted"
	j.ErrorMessage = "Update aborted; previous release retained"
	return c.save(l, j, PhaseRolledBack)
}
func (c *Coordinator) rollback(ctx context.Context, l *InstallationLock, j *Job) error {
	if j.Phase == PhaseSucceeded || j.RecoveryDecision == RecoveryComplete {
		return ErrRecoveryRequired
	}
	j.RecoveryDecision = RecoveryRollback
	if j.RecoveryStep == "" {
		j.RecoveryStep = RecoveryStop
	}
	if e := c.save(l, j, PhaseRollingBack); e != nil {
		return e
	}
	advance := func(next RecoveryStep) error { j.RecoveryStep = next; return c.save(l, j, PhaseRollingBack) }
	for {
		switch j.RecoveryStep {
		case RecoveryStop:
			if e := c.runtime.Stop(ctx, j.Plan); e != nil {
				return c.requireRecovery(l, j, e)
			}
			if e := advance(RecoveryInstallOld); e != nil {
				return e
			}
		case RecoveryInstallOld:
			// Reassert the stopped boundary before retrying an ambiguous native install.
			if e := c.runtime.Stop(ctx, j.Plan); e != nil {
				return c.requireRecovery(l, j, e)
			}
			if e := c.runtime.Install(ctx, j.Plan, j.Plan.PreviousArtifact); e != nil {
				return c.requireRecovery(l, j, e)
			}
			if e := advance(RecoveryRestoreDB); e != nil {
				return e
			}
		case RecoveryRestoreDB:
			if e := c.runtime.Stop(ctx, j.Plan); e != nil {
				return c.requireRecovery(l, j, e)
			}
			if e := c.runtime.Restore(ctx, j.Plan, j.Snapshots); e != nil {
				return c.requireRecovery(l, j, e)
			}
			if e := c.ticket(j, j.Plan.Current.Identity); e != nil {
				return e
			}
			if e := advance(RecoveryStartOld); e != nil {
				return e
			}
		case RecoveryStartOld:
			if e := c.runtime.StartProbation(ctx, j.Plan, j.Probation); e != nil {
				return c.requireRecovery(l, j, e)
			}
			if e := advance(RecoveryVerifyOld); e != nil {
				return e
			}
		case RecoveryVerifyOld:
			if e := c.readiness(ctx, *j); e != nil {
				return c.requireRecovery(l, j, e)
			}
			if e := advance(RecoveryFinalize); e != nil {
				return e
			}
		case RecoveryFinalize:
			j.ReleasePending = true
			j.ErrorCode = "update-rolled-back"
			j.ErrorMessage = "Update failed; previous release restored"
			if e := c.save(l, j, PhaseRolledBack); e != nil {
				return e
			}
			return c.finishRelease(ctx, l, j)
		default:
			return ErrRecoveryRequired
		}
	}
}
