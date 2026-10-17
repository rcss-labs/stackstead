package updatehost

import (
	"context"
	"errors"
	"github.com/rcss-labs/stackstead/internal/maintenance"
	"github.com/rcss-labs/stackstead/internal/update"
	"github.com/rcss-labs/stackstead/internal/updatecontrol"
	"testing"
)

type stateControl struct {
	states map[string]*updatecontrol.State
	fail   string
	after  bool
}

func (c *stateControl) operation(name string, in update.Instance, apply func() error) error {
	if c.fail == name+"-"+in.ID && !c.after {
		return errors.New("transport before apply")
	}
	if e := apply(); e != nil {
		return e
	}
	if c.fail == name+"-"+in.ID {
		return errors.New("transport lost acknowledgement")
	}
	return nil
}
func (c *stateControl) Freeze(_ context.Context, in update.Instance) error {
	return c.operation("freeze", in, c.states[in.ID].Gate.Freeze)
}
func (c *stateControl) Prepare(_ context.Context, in update.Instance, p update.Probation) error {
	return c.operation("prepare", in, func() error { return c.states[in.ID].Apply(p) })
}
func (c *stateControl) Release(_ context.Context, in update.Instance, j string) error {
	return c.operation("release", in, func() error { return c.states[in.ID].Release(j) })
}
func (c *stateControl) Ready(_ context.Context, in update.Instance) (update.Readiness, error) {
	return c.states[in.ID].Ready(), nil
}
func TestRuntimeCleanupReopensRealGatesAfterAmbiguousControl(t *testing.T) {
	for _, failure := range []struct {
		name  string
		after bool
	}{{"freeze-two", false}, {"freeze-two", true}, {"prepare-two", false}, {"prepare-two", true}, {"release-two", true}} {
		t.Run(failure.name+map[bool]string{true: "-after", false: "-before"}[failure.after], func(t *testing.T) {
			p := update.Plan{Registration: update.Registration{Instances: []update.Instance{{ID: "one"}, {ID: "two"}}}, States: []update.ServiceState{{InstanceID: "one", Running: true}, {InstanceID: "two", Running: true}}}
			ticket := update.Probation{JobID: "durable-job", Nonce: "durable-current"}
			control := &stateControl{states: map[string]*updatecontrol.State{}, fail: failure.name, after: failure.after}
			for _, in := range p.Registration.Instances {
				control.states[in.ID] = updatecontrol.NewState(in, maintenance.New(), nil)
			}
			r := &runtime{control: control}
			if failure.name != "release-two" {
				if e := r.Quiesce(context.Background(), p, ticket); e == nil {
					t.Fatal("injected ambiguous failure accepted")
				}
			} else {
				if e := r.Quiesce(context.Background(), p, ticket); e != nil {
					t.Fatal(e)
				}
			}
			if e := r.Release(context.Background(), p, ticket); e == nil {
				t.Fatal("unconfirmed cleanup accepted")
			}
			control.fail = ""
			r = &runtime{control: control}
			if e := r.Release(context.Background(), p, ticket); e != nil {
				t.Fatal(e)
			}
			for _, state := range control.states {
				leave, e := state.Gate.Enter()
				if e != nil {
					t.Fatalf("old app still frozen: %v", e)
				}
				leave()
			}
		})
	}
}
func TestRuntimeCleanupRetainsConflictingPeer(t *testing.T) {
	in := update.Instance{ID: "one"}
	gate := maintenance.New()
	_ = gate.Freeze()
	foreign := update.Probation{JobID: "other-job", Nonce: "foreign"}
	control := &stateControl{states: map[string]*updatecontrol.State{"one": updatecontrol.NewState(in, gate, &foreign)}}
	r := &runtime{control: control}
	p := update.Plan{Registration: update.Registration{Instances: []update.Instance{in}}, States: []update.ServiceState{{InstanceID: in.ID, Running: true}}}
	if e := r.Release(context.Background(), p, update.Probation{JobID: "job", Nonce: "current"}); !errors.Is(e, update.ErrUnauthorized) {
		t.Fatal(e)
	}
	if !gate.Frozen() {
		t.Fatal("conflicting peer thawed")
	}
}
