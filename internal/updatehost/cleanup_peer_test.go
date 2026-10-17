package updatehost

import (
	"context"
	"errors"
	"testing"

	"github.com/rcss-labs/stackstead/internal/maintenance"
	"github.com/rcss-labs/stackstead/internal/update"
	"github.com/rcss-labs/stackstead/internal/updatecontrol"
)

type cleanupSupervisor struct {
	state                  update.ServiceState
	probeError, startError error
}

func (s *cleanupSupervisor) Probe(context.Context, update.Instance) (update.ServiceState, error) {
	return s.state, s.probeError
}
func (s *cleanupSupervisor) Stop(context.Context, update.Instance) error {
	return errors.New("cleanup must not stop")
}
func (s *cleanupSupervisor) Start(context.Context, update.Instance, update.ServiceState) error {
	return s.startError
}
func TestRuntimeScopedCleanupKeepsHealthyPeerIndependent(t *testing.T) {
	for _, failure := range []string{"probe", "identity", "start", "missing-adapter"} {
		t.Run(failure, func(t *testing.T) {
			identity := update.Identity{Version: "current"}
			ticket := update.Probation{JobID: "same-job", Nonce: "same-current-nonce", Expected: identity}
			healthy := update.Instance{ID: "healthy", Supervisor: update.SupervisorSpec{Kind: "custom"}}
			bad := update.Instance{ID: "bad", Supervisor: update.SupervisorSpec{Kind: "systemd"}}
			plan := update.Plan{Current: update.Candidate{Identity: identity}, Registration: update.Registration{Instances: []update.Instance{bad, healthy}}, States: []update.ServiceState{{InstanceID: "bad", Running: true}, {InstanceID: "healthy", Running: true}}}
			control := &stateControl{states: map[string]*updatecontrol.State{}}
			for i, in := range plan.Registration.Instances {
				gate := maintenance.New()
				_ = gate.Freeze()
				state := updatecontrol.NewState(in, gate, &ticket)
				state.MarkReady(update.Readiness{Identity: identity, PID: 10 + i, ConfigSHA256: "config", Ready: true})
				control.states[in.ID] = state
			}
			goodSupervisor := &cleanupSupervisor{state: update.ServiceState{InstanceID: "healthy", Running: true, PID: 11}}
			badSupervisor := &cleanupSupervisor{state: update.ServiceState{InstanceID: "bad", Running: true, PID: 10}}
			supervisors := map[string]Supervisor{"custom": goodSupervisor, "systemd": badSupervisor}
			switch failure {
			case "probe":
				badSupervisor.probeError = update.ErrUnavailable
			case "identity":
				control.states["bad"].MarkReady(update.Readiness{Identity: update.Identity{Version: "unknown"}, PID: 10, ConfigSHA256: "config", Ready: true})
			case "start":
				badSupervisor.state.Running = false
				badSupervisor.startError = update.ErrUnavailable
			case "missing-adapter":
				delete(supervisors, "systemd")
			}
			edge := &runtime{control: control, supervisors: supervisors}
			badPlan := plan
			badPlan.States = plan.States[:1]
			if e := edge.StartProbation(context.Background(), badPlan, ticket); e == nil {
				t.Fatal("bad peer unexpectedly proven")
			}
			goodPlan := plan
			goodPlan.States = plan.States[1:]
			if e := edge.StartProbation(context.Background(), goodPlan, ticket); e != nil {
				t.Fatalf("unrelated peer blocked healthy scoped proof: %v", e)
			}
			if e := edge.Release(context.Background(), goodPlan, ticket); e != nil {
				t.Fatal(e)
			}
			leave, e := control.states["healthy"].Gate.Enter()
			if e != nil {
				t.Fatal("healthy peer remained frozen", e)
			}
			leave()
			if !control.states["bad"].Gate.Frozen() {
				t.Fatal("unknown peer released")
			}
			// The peer recovers; a new runtime consumes exactly the same ticket and plan.
			badSupervisor.probeError = nil
			badSupervisor.startError = nil
			badSupervisor.state.Running = true
			supervisors["systemd"] = badSupervisor
			control.states["bad"].MarkReady(update.Readiness{Identity: identity, PID: 10, ConfigSHA256: "config", Ready: true})
			restarted := &runtime{control: control, supervisors: supervisors}
			if e = restarted.StartProbation(context.Background(), badPlan, ticket); e != nil {
				t.Fatal(e)
			}
			if e = restarted.Release(context.Background(), badPlan, ticket); e != nil {
				t.Fatal(e)
			}
			leave, e = control.states["bad"].Gate.Enter()
			if e != nil {
				t.Fatal("recovered peer remained frozen", e)
			}
			leave()
			if len(plan.States) != 2 || len(plan.Registration.Instances) != 2 {
				t.Fatal("transient peer scope replaced original plan")
			}
		})
	}
}
