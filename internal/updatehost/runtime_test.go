package updatehost

import (
	"context"
	"errors"
	"github.com/rcss-labs/stackstead/internal/update"
	"reflect"
	"testing"
)

func TestRuntimeUnconfiguredFailsClosed(t *testing.T) {
	r := NewRuntime(nil, nil, nil)
	p := update.Plan{}
	if _, e := r.Preflight(context.Background(), p); !errors.Is(e, update.ErrUnavailable) {
		t.Fatalf("preflight: %v", e)
	}
	if e := r.Quiesce(context.Background(), p, update.Probation{}); !errors.Is(e, update.ErrUnavailable) {
		t.Fatalf("quiesce: %v", e)
	}
	if _, e := r.Snapshot(context.Background(), p); !errors.Is(e, update.ErrUnavailable) {
		t.Fatalf("snapshot: %v", e)
	}
}

type controlEdge struct {
	events []string
	fail   string
}

func (c *controlEdge) Freeze(_ context.Context, in update.Instance) error {
	c.events = append(c.events, "freeze-"+in.ID)
	if c.fail == "freeze-"+in.ID {
		return errors.New("lost ack")
	}
	return nil
}
func (c *controlEdge) Prepare(_ context.Context, in update.Instance, p update.Probation) error {
	c.events = append(c.events, "prepare-"+in.ID)
	if p.JobID == "" {
		return update.ErrUnauthorized
	}
	if c.fail == "prepare-"+in.ID {
		return errors.New("lost ack")
	}
	return nil
}
func (c *controlEdge) Release(_ context.Context, in update.Instance, j string) error {
	c.events = append(c.events, "release-"+in.ID)
	if j == "" {
		return update.ErrUnauthorized
	}
	if c.fail == "release-"+in.ID {
		return errors.New("lost ack")
	}
	return nil
}
func (c *controlEdge) Ready(context.Context, update.Instance) (update.Readiness, error) {
	return update.Readiness{}, nil
}
func TestRuntimePartialQuiesceCleanupContinuesAfterPeerFailure(t *testing.T) {
	c := &controlEdge{fail: "freeze-one"}
	r := &runtime{control: c}
	p := update.Plan{Registration: update.Registration{Instances: []update.Instance{{ID: "one"}, {ID: "two"}}}, States: []update.ServiceState{{InstanceID: "one", Running: true}, {InstanceID: "two", Running: true}}}
	ticket := update.Probation{JobID: "job", Nonce: "nonce"}
	if e := r.Quiesce(context.Background(), p, ticket); e == nil {
		t.Fatal("lost ack accepted")
	}
	c.events = nil
	if e := r.Release(context.Background(), p, ticket); e == nil {
		t.Fatal("unconfirmed cleanup accepted")
	}
	want := []string{"freeze-one", "freeze-two", "prepare-two", "release-two"}
	if !reflect.DeepEqual(c.events, want) {
		t.Fatalf("cleanup abandoned peer: %v", c.events)
	}
}
func TestRuntimeReleaseUsesPersistedJobAfterRestart(t *testing.T) {
	for _, fail := range []string{"freeze-one", "prepare-one", "release-one"} {
		t.Run(fail, func(t *testing.T) {
			c := &controlEdge{fail: fail}
			p := update.Plan{Current: update.Candidate{Identity: update.Identity{Version: "old"}}, Registration: update.Registration{Instances: []update.Instance{{ID: "one"}}}, States: []update.ServiceState{{InstanceID: "one", Running: true}}}
			ticket := update.Probation{JobID: "persisted-job", Nonce: "persisted-nonce", Expected: p.Current.Identity}
			r := &runtime{control: c}
			if e := r.Release(context.Background(), p, ticket); e == nil {
				t.Fatal("ambiguous result accepted")
			}
			r = &runtime{control: c}
			c.fail = ""
			if e := r.Release(context.Background(), p, ticket); e != nil {
				t.Fatal(e)
			}
		})
	}
}

type supervisorEdge struct {
	state         update.ServiceState
	starts, stops int
}

func (s *supervisorEdge) Probe(context.Context, update.Instance) (update.ServiceState, error) {
	return s.state, nil
}
func (s *supervisorEdge) Stop(context.Context, update.Instance) error { s.stops++; return nil }
func (s *supervisorEdge) Start(_ context.Context, _ update.Instance, original update.ServiceState) error {
	s.starts++
	if original.Enabled {
		return errors.New("fixture must preserve disabled state")
	}
	return nil
}
func TestRuntimeStartsOnlyMissingOriginallyRunningInstances(t *testing.T) {
	s := &supervisorEdge{}
	r := &runtime{supervisors: map[string]Supervisor{"custom": s}, control: &controlEdge{}}
	p := update.Plan{Registration: update.Registration{Instances: []update.Instance{{ID: "one", Supervisor: update.SupervisorSpec{Kind: "custom"}}, {ID: "stopped", Supervisor: update.SupervisorSpec{Kind: "custom"}}}}, States: []update.ServiceState{{InstanceID: "one", Running: true}, {InstanceID: "stopped"}}}
	if e := r.StartProbation(context.Background(), p, update.Probation{JobID: "job", Nonce: "nonce"}); e != nil {
		t.Fatal(e)
	}
	if s.starts != 1 {
		t.Fatalf("started originally stopped or skipped active: %d", s.starts)
	}
}
func TestRuntimeUnknownSupervisorMakesNoChanges(t *testing.T) {
	s := &supervisorEdge{}
	r := &runtime{supervisors: map[string]Supervisor{"custom": s}}
	p := update.Plan{Registration: update.Registration{Instances: []update.Instance{{ID: "one", Supervisor: update.SupervisorSpec{Kind: "custom"}}, {ID: "two", Supervisor: update.SupervisorSpec{Kind: "unknown"}}}}, States: []update.ServiceState{{InstanceID: "one", Running: true}, {InstanceID: "two", Running: true}}}
	if e := r.Stop(context.Background(), p); !errors.Is(e, update.ErrUnavailable) {
		t.Fatalf("unknown supervisor: %v", e)
	}
	if s.stops != 0 {
		t.Fatal("mutation before all supervisors validated")
	}
}
