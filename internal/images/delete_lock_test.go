package images

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/imageref"
)

// blockFirstUsageCall makes the first usage check stop until release is
// called and reports on entered when it has started.
func (h *harness) blockFirstUsageCall() (entered <-chan struct{}, release func()) {
	in, gate := make(chan struct{}), make(chan struct{})
	h.usage.onCall = func(call int) {
		if call == 1 {
			close(in)
			<-gate
		}
	}
	return in, func() { close(gate) }
}

// updateDuringDelete runs an image edit while a delete is stuck in its usage
// check and fails the test if the edit has to wait for the delete.
func (h *harness) updateDuringDelete(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	<-entered
	done := make(chan error, 1)
	go func() {
		_, err := h.svc.Update(context.Background(), "alice", "app", UpdateRequest{Name: "app"})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("update during delete: %v", err)
		}
	case <-time.After(5 * time.Second): // failure detector only; the wait is the channel
		t.Fatal("image edit blocked behind a delete that is waiting for the usage check")
	}
}

func TestDeleteVersionDoesNotHoldImageLockDuringUsageCheck(t *testing.T) {
	h := newHarness(t)
	h.create(t, "app")
	v := h.build(t, "app", "1", nil)
	entered, release := h.blockFirstUsageCall()
	errc := make(chan error, 1)
	go func() { errc <- h.svc.DeleteVersion(context.Background(), "alice", "app", v.ID) }()
	h.updateDuringDelete(t, entered)
	release()
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.store.VersionByID(context.Background(), v.ID); !errors.Is(err, ErrVersionNotFound) {
		t.Fatalf("version still there: %v", err)
	}
}

func TestDeleteImageDoesNotHoldImageLockDuringUsageCheck(t *testing.T) {
	h := newHarness(t)
	h.create(t, "app")
	h.build(t, "app", "1", nil)
	entered, release := h.blockFirstUsageCall()
	errc := make(chan error, 1)
	go func() { errc <- h.svc.Delete(context.Background(), "alice", "app") }()
	h.updateDuringDelete(t, entered)
	release()
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Get(context.Background(), "app"); !errors.Is(err, ErrImageNotFound) {
		t.Fatalf("image still there: %v", err)
	}
}

func TestDeleteVersionRefusedWhenUsedAfterTheCheck(t *testing.T) {
	h := newHarness(t)
	h.create(t, "app")
	v := h.build(t, "app", "1", nil)
	entered, release := h.blockFirstUsageCall()
	errc := make(chan error, 1)
	go func() { errc <- h.svc.DeleteVersion(context.Background(), "alice", "app", v.ID) }()
	<-entered
	h.usage.usages = []imageref.Usage{{Stack: "web", Service: "api", Ref: imageref.Ref{Owner: testOwner, Slug: "app", Tag: "1"}}}
	release()
	var in *InUseError
	if err := <-errc; !errors.As(err, &in) {
		t.Fatalf("err = %v; want InUseError", err)
	}
	if !h.eng.present["stackstead.invalid/"+testOwner+"/app:1"] {
		t.Fatal("tag removed although a stack started using it")
	}
}
