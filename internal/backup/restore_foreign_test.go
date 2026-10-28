package backup

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/rcss-labs/stackstead/internal/stacks"
)

const privilegedCompose = "services:\n  app:\n    image: nginx\n    privileged: true\n"

func TestInspectHostReportsForeignAndRisks(t *testing.T) {
	for _, withSidecar := range []bool{true, false} {
		s := newHostSetup(t)
		s.env.InstanceID = "mine"
		s.fixture.instanceID = "other"
		s.fixture.files["base/stacks/web/compose.yaml"] = privilegedCompose
		path := s.fixture.build(t, t.TempDir(), withSidecar)

		sum, err := InspectHost(context.Background(), pathSource(path), s.env)
		if err != nil {
			t.Fatal(err)
		}
		want := ComposeRisk{Stack: "web", File: "compose.yaml", Service: "app", Kind: "privileged", Detail: "true"}
		if !sum.Foreign || !slices.Contains(sum.Risks, want) || len(sum.Risks) != 1 {
			t.Errorf("sidecar=%v: foreign=%v risks=%+v, want foreign with %+v only", withSidecar, sum.Foreign, sum.Risks, want)
		}
	}
}

func TestInspectHostSameInstanceIsNotForeign(t *testing.T) {
	s := newHostSetup(t)
	s.env.InstanceID = "mine"
	s.fixture.instanceID = "mine"
	path := s.fixture.build(t, t.TempDir(), true)
	sum, err := InspectHost(context.Background(), pathSource(path), s.env)
	if err != nil || sum.Foreign || len(sum.Risks) != 0 {
		t.Errorf("foreign=%v risks=%+v err=%v, want a plain own archive", sum.Foreign, sum.Risks, err)
	}

	// A host without an id yet (a fresh one) cannot own any archive.
	s.env.InstanceID = ""
	if sum, err = InspectHost(context.Background(), pathSource(path), s.env); err != nil || !sum.Foreign {
		t.Errorf("fresh host: foreign=%v err=%v, want foreign", sum.Foreign, err)
	}
}

func TestInspectHostScansOverrideFiles(t *testing.T) {
	s := newHostSetup(t)
	s.fixture.files["base/stacks/web/compose.override.yaml"] = "services:\n  app:\n    cap_add: [SYS_ADMIN]\n"
	s.fixture.files["base/stacks/web/conf/compose.yaml"] = privilegedCompose // not a stack file
	path := s.fixture.build(t, t.TempDir(), true)
	sum, err := InspectHost(context.Background(), pathSource(path), s.env)
	if err != nil {
		t.Fatal(err)
	}
	want := []ComposeRisk{{Stack: "web", File: "compose.override.yaml", Service: "app", Kind: "cap_add", Detail: "SYS_ADMIN"}}
	if !slices.Equal(sum.Risks, want) {
		t.Errorf("risks = %+v, want %+v", sum.Risks, want)
	}
}

// runExact is run without the default of starting the stack.
func (e *restoreEnv) runExact(t *testing.T, req StackRestoreRequest) stacks.Operation {
	t.Helper()
	req.AllowUnencrypted = req.AllowUnencrypted || !IsEncryptedName(req.Archive.Name)
	op, err := e.runner.RestoreStack(context.Background(), req, "alice")
	if err != nil {
		t.Fatalf("RestoreStack: %v", err)
	}
	e.deps.Ops.Wait(op.ID)
	final, err := e.deps.Ops.Get(op.ID)
	if err != nil {
		t.Fatal(err)
	}
	return final
}

func TestStackRestoreDoesNotStartWhenNotRequested(t *testing.T) {
	e := newRestoreEnv(t)
	compose := e.existingStack(t, "web", "compose.yaml", "services: {app: {image: old}}\n", true)
	ref := e.addArchive(t, webFixture(compose, true))

	op := e.runExact(t, StackRestoreRequest{Archive: ref, Stack: "web", Files: true})
	if op.State != stacks.OpSucceeded {
		t.Fatalf("operation = %+v", op)
	}
	calls := e.eng.changes()
	if indexOf(calls, "stack-stop:web") < 0 || indexOf(calls, "stack-start:web") >= 0 {
		t.Errorf("calls = %v, want the stack stopped and left stopped", calls)
	}
	if got := readFile(t, compose); got != "services: {app: {image: new}}\n" {
		t.Errorf("compose = %q, want the restored file", got)
	}
	if acts := e.restoreActions(t); len(acts) != 1 || !strings.Contains(acts[0].Detail, "left stopped as requested") {
		t.Errorf("action log = %+v, want the skipped start in the detail", acts)
	}
}

func TestStackRestoreStartsWhenRequested(t *testing.T) {
	e := newRestoreEnv(t)
	compose := e.existingStack(t, "web", "compose.yaml", "services: {app: {image: old}}\n", true)
	ref := e.addArchive(t, webFixture(compose, true))

	op := e.runExact(t, StackRestoreRequest{Archive: ref, Stack: "web", Files: true, Start: true})
	if op.State != stacks.OpSucceeded || indexOf(e.eng.changes(), "stack-start:web") < 0 {
		t.Errorf("operation = %+v, calls = %v, want the stack started", op, e.eng.changes())
	}
}

func TestPlanRestoreReportsForeignAndRisks(t *testing.T) {
	e := newRestoreEnv(t) // this host's instance id is inst-1
	compose := e.existingStack(t, "web", "compose.yaml", "services: {app: {image: old}}\n", false)
	f := webFixture(compose, false)
	f.files["base/stacks/web/compose.yaml"] = privilegedCompose
	f.files["base/stacks/web/compose.override.yaml"] = "services:\n  app:\n    pid: host\n"

	f.instanceID = "other"
	plan, err := e.runner.PlanRestore(context.Background(), StackRestoreRequest{Archive: e.addArchive(t, f), Stack: "web", Files: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []ComposeRisk{
		{Stack: "web", File: "compose.override.yaml", Service: "app", Kind: "host_namespace", Detail: "pid: host"},
		{Stack: "web", File: "compose.yaml", Service: "app", Kind: "privileged", Detail: "true"},
	}
	if !plan.Foreign || !slices.Equal(plan.Risks, want) {
		t.Errorf("foreign=%v risks=%+v, want foreign with %+v", plan.Foreign, plan.Risks, want)
	}

	f.instanceID = "inst-1"
	plan, err = e.runner.PlanRestore(context.Background(), StackRestoreRequest{Archive: e.addArchive(t, f), Stack: "web", Files: true})
	if err != nil || plan.Foreign {
		t.Errorf("own archive: foreign=%v err=%v, want not foreign", plan.Foreign, err)
	}
}
