package stacks_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/maintenance"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

func TestAdmissionSurvivesAsyncOperationAcceptance(t *testing.T) {
	gate := maintenance.New()
	ops := stacks.NewOps(time.Second, time.Now)
	ops.SetMaintenance(gate)
	stop := make(chan struct{})
	started := make(chan struct{})
	op, err := ops.Start("web", "up", func(context.Context, func(string)) error { close(started); <-stop; return nil })
	if err != nil {
		t.Fatal(err)
	}
	<-started
	_, _, _, done, cancel, err := ops.Subscribe(op.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if err := gate.Freeze(); !errors.Is(err, maintenance.ErrBusy) {
		t.Errorf("Freeze after async acceptance = %v; active op lost admission", err)
	}
	close(stop)
	<-done
	deadline := time.Now().Add(time.Second)
	for {
		if err := gate.Freeze(); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("completed operation leaked admission")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := ops.Start("other", "up", func(context.Context, func(string)) error { return nil }); !errors.Is(err, maintenance.ErrFrozen) {
		t.Fatalf("Start in probation = %v", err)
	}
}
