package updatehost

import (
	"context"
	"errors"
	"time"

	"github.com/rcss-labs/stackstead/internal/update"
	"github.com/rcss-labs/stackstead/internal/updatecontrol"
)

type runtime struct {
	adapters    map[update.InstallerKind]PackageAdapter
	supervisors map[string]Supervisor
	control     controlClient
}

func NewRuntime(a map[update.InstallerKind]PackageAdapter, s map[string]Supervisor, c *updatecontrol.Client) update.Runtime {
	if c == nil {
		return &runtime{adapters: a, supervisors: s}
	}
	return &runtime{a, s, c}
}

// Preflight remains unavailable until native identity/executable probes, actual
// schema inspection and the two-artifact plus DB disk budget are wired in Task 13.
func (r *runtime) Preflight(context.Context, update.Plan) ([]update.ServiceState, error) {
	return nil, update.ErrUnavailable
}
func (r *runtime) Quiesce(ctx context.Context, p update.Plan, b update.Probation) error {
	if r.control == nil || b.JobID == "" || b.Nonce == "" {
		return update.ErrUnavailable
	}
	for _, in := range active(p) {
		if e := r.control.Freeze(ctx, in); e != nil {
			return e
		}
		if e := r.control.Prepare(ctx, in, b); e != nil {
			return e
		}
	}
	return nil
}
func (r *runtime) Stop(ctx context.Context, p update.Plan) error {
	if e := r.validateSupervisors(p); e != nil {
		return e
	}
	var result error
	for _, in := range active(p) {
		result = errors.Join(result, r.supervisors[in.Supervisor.Kind].Stop(ctx, in))
	}
	return result
}

// Snapshot and Restore require the fixed bounded child role under the registered
// app UID/GID with inherited streams or validated app-accessible staging.
// Root-private paths must never be passed to an app child assuming traversal.
func (r *runtime) Snapshot(context.Context, update.Plan) ([]update.DBSnapshot, error) {
	return nil, update.ErrUnavailable
}
func (r *runtime) Install(ctx context.Context, p update.Plan, a update.VerifiedArtifact) error {
	adapter := r.adapters[p.Registration.Installer]
	if adapter == nil {
		return update.ErrUnavailable
	}
	if a != p.TargetArtifact && a != p.PreviousArtifact {
		return update.ErrUnauthorized
	}
	if e := adapter.Validate(ctx, p, a); e != nil {
		return e
	}
	return adapter.Install(ctx, p, a)
}
func (r *runtime) StartProbation(ctx context.Context, p update.Plan, b update.Probation) error {
	if r.control == nil || b.JobID == "" || b.Nonce == "" {
		return update.ErrUnavailable
	}
	if e := r.validateSupervisors(p); e != nil {
		return e
	}
	for _, in := range active(p) {
		supervisor := r.supervisors[in.Supervisor.Kind]
		state, e := supervisor.Probe(ctx, in)
		if e != nil {
			return e
		}
		if state.Running {
			proof, e := r.control.Ready(ctx, in)
			if e != nil || proof.Identity != b.Expected || proof.PID != state.PID || proof.PID <= 0 {
				return update.ErrRecoveryRequired
			}
			continue
		}
		for _, original := range p.States {
			if original.InstanceID == in.ID {
				if e = supervisor.Start(ctx, in, original); e != nil {
					return e
				}
				break
			}
		}
	}
	return nil
}
func (r *runtime) Ready(ctx context.Context, p update.Plan, b update.Probation) ([]update.Readiness, error) {
	if r.control == nil {
		return nil, update.ErrUnavailable
	}
	var rows []update.Readiness
	for _, in := range active(p) {
		row, e := r.waitReady(ctx, in, b)
		if e != nil {
			return nil, e
		}
		rows = append(rows, row)
	}
	return rows, nil
}
func (r *runtime) waitReady(ctx context.Context, in update.Instance, b update.Probation) (update.Readiness, error) {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	for {
		row, e := r.control.Ready(ctx, in)
		if e == nil && row.Ready && row.JobID == b.JobID && row.Nonce == b.Nonce && row.Identity == b.Expected {
			return row, nil
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return update.Readiness{}, ctx.Err()
		case <-timer.C:
		}
	}
}
func (r *runtime) validateSupervisors(p update.Plan) error {
	if len(p.Registration.Instances) == 0 {
		return update.ErrUnavailable
	}
	for _, in := range p.Registration.Instances {
		if r.supervisors[in.Supervisor.Kind] == nil {
			return update.ErrUnavailable
		}
	}
	return nil
}
func (r *runtime) Release(ctx context.Context, p update.Plan, b update.Probation) error {
	if r.control == nil || b.JobID == "" || b.Nonce == "" {
		return update.ErrUnavailable
	}
	var result error
	for _, in := range active(p) {
		if e := r.control.Freeze(ctx, in); e != nil {
			result = errors.Join(result, e)
			continue
		}
		if e := r.control.Prepare(ctx, in, b); e != nil {
			result = errors.Join(result, e)
			continue
		}
		result = errors.Join(result, r.control.Release(ctx, in, b.JobID))
	}
	return result
}
func (r *runtime) Restore(context.Context, update.Plan, []update.DBSnapshot) error {
	return update.ErrUnavailable
}
func (r *runtime) Inspect(context.Context, update.Job) (update.RecoveryAction, error) {
	return update.RecoveryRepairRequired, update.ErrUnavailable
}

type controlClient interface {
	Freeze(context.Context, update.Instance) error
	Prepare(context.Context, update.Instance, update.Probation) error
	Release(context.Context, update.Instance, string) error
	Ready(context.Context, update.Instance) (update.Readiness, error)
}

func active(p update.Plan) []update.Instance {
	var instances []update.Instance
	for _, in := range p.Registration.Instances {
		for _, state := range p.States {
			if state.InstanceID == in.ID && state.Running {
				instances = append(instances, in)
				break
			}
		}
	}
	return instances
}
