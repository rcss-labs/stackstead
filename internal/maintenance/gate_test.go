package maintenance

import (
	"errors"
	"sync"
	"testing"
)

func TestFreezeRefusesActiveOperation(t *testing.T) {
	g := New()
	release, err := g.Enter()
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Freeze(); !errors.Is(err, ErrBusy) {
		t.Fatalf("Freeze with active operation = %v, want ErrBusy", err)
	}
	if g.Frozen() {
		t.Fatal("failed Freeze changed admission")
	}
	release()
	release()
	if err := g.Freeze(); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Enter(); !errors.Is(err, ErrFrozen) {
		t.Fatalf("Enter frozen = %v", err)
	}
	g.Thaw()
	done, err := g.Enter()
	if err != nil {
		t.Fatal(err)
	}
	done()
}
func TestAdmissionFreezeRace(t *testing.T) {
	for range 100 {
		g := New()
		var wg sync.WaitGroup
		wg.Add(2)
		start := make(chan struct{})
		held := make(chan struct{})
		frozen := make(chan error, 1)
		go func() {
			defer wg.Done()
			<-start
			release, err := g.Enter()
			if err == nil {
				<-held
				release()
			}
		}()
		go func() { defer wg.Done(); <-start; frozen <- g.Freeze() }()
		close(start)
		err := <-frozen
		if err == nil {
			if _, e := g.Enter(); e == nil {
				t.Error("entry after successful Freeze")
			}
		}
		close(held)
		wg.Wait()
	}
}
