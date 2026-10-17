package update

import (
	"context"
	"errors"
	"testing"

	"github.com/rcss-labs/stackstead/internal/maintenance"
)

type cleanupPeerRuntime struct {
	*transactionRuntime
	gates      map[string]*maintenance.Gate
	failedPeer string
	released   map[string]int
}

func (r *cleanupPeerRuntime) StartProbation(_ context.Context, p Plan, b Probation) error {
	durable, e := r.journal.Load("install")
	if e != nil {
		return e
	}
	if len(durable.Plan.States) != 2 || len(p.Registration.Instances) != 2 || durable.Probation != b {
		return errors.New("lost durable full peer set or ticket")
	}
	for _, s := range p.States {
		if s.Running && s.InstanceID == r.failedPeer {
			return errors.New("peer probe/start or identity unconfirmed")
		}
	}
	return nil
}
func (r *cleanupPeerRuntime) Release(_ context.Context, p Plan, b Probation) error {
	for _, s := range p.States {
		if !s.Running {
			continue
		}
		if s.InstanceID == r.failedPeer {
			return errors.New("unproven peer must not be released")
		}
		r.released[s.InstanceID]++
		r.gates[s.InstanceID].Thaw()
	}
	return nil
}
func TestRecoveryKeepOldCleansHealthyPeerDespiteUnprovenPeer(t *testing.T) {
	c, base, j := transactionFixture(t)
	bad := j.Plan.Registration.Instances[0]
	bad.ID = "bad"
	bad.ConfigPath = "/etc/stackstead/bad.yaml"
	bad.DataDir = "/var/lib/stackstead-bad"
	bad.ControlSocket = "/run/stackstead-bad.sock"
	j.Plan.Registration.Instances = append(j.Plan.Registration.Instances, bad)
	// Put the failed peer first so its error must not abort the healthy peer.
	j.Plan.States = []ServiceState{{InstanceID: "bad", Running: true}, {InstanceID: "app", Running: true}}
	j.Phase = PhaseQuiescing
	j.Probation = Probation{JobID: j.ID, Nonce: "durable-current-ticket", Expected: j.Plan.Current.Identity}
	if e := c.journal.Save(j); e != nil {
		t.Fatal(e)
	}
	edge := &cleanupPeerRuntime{transactionRuntime: base, gates: map[string]*maintenance.Gate{"app": maintenance.New(), "bad": maintenance.New()}, failedPeer: "bad", released: map[string]int{}}
	for _, gate := range edge.gates {
		if e := gate.Freeze(); e != nil {
			t.Fatal(e)
		}
	}
	c.runtime = edge
	if e := c.Recover(context.Background(), "install"); !errors.Is(e, ErrRecoveryRequired) {
		t.Fatalf("unresolved peer must retain recovery: %v", e)
	}
	leave, e := edge.gates["app"].Enter()
	if e != nil {
		t.Fatalf("healthy peer remains frozen behind bad peer: %v", e)
	}
	leave()
	if !edge.gates["bad"].Frozen() || edge.released["bad"] != 0 {
		t.Fatal("unproven peer was released")
	}
	got, e := c.journal.Load("install")
	if e != nil {
		t.Fatal(e)
	}
	if got.ID != j.ID || got.Probation != j.Probation || len(got.Plan.States) != 2 || got.Phase != PhaseRecoveryRequired || !got.ReleasePending || got.RecoveryDecision != RecoveryKeepOld {
		t.Fatalf("lost unresolved cleanup identity: %+v", got)
	}
	for _, event := range base.events {
		if event == "stop" || event == "install" || event == "old-install" || event == "restore" {
			t.Fatalf("preinstall cleanup mutated data: %v", base.events)
		}
	}
	edge.failedPeer = ""
	c = NewCoordinator(nil, c.journal, edge)
	if e = c.Recover(context.Background(), "install"); e != nil {
		t.Fatal(e)
	}
	got, e = c.journal.Load("install")
	if e != nil {
		t.Fatal(e)
	}
	if got.ID != j.ID || got.Probation != j.Probation || got.Phase != PhaseRolledBack || got.ReleasePending {
		t.Fatalf("retry changed identity or stayed unresolved: %+v", got)
	}
	for _, gate := range edge.gates {
		leave, e := gate.Enter()
		if e != nil {
			t.Fatal("recovered peer still frozen", e)
		}
		leave()
	}
}
