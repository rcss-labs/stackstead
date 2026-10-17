package update

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"
)

func beginFixture(t *testing.T) (*Coordinator, *transactionRuntime) {
	t.Helper()
	c, r, j := transactionFixture(t)
	reg := validTestRegistration()
	reg.ID = "install"
	reg.UpdateEnabled = true
	reg.Identity.Version = "1.0.0"
	reg.Instances[0].ID = "app"
	active, _ := json.Marshal(j.WorkerSHA256)
	if e := c.journal.store.atomic("workers", "active.json", active, 0600); e != nil {
		t.Fatal(e)
	}
	j.Phase = PhaseCancelled
	if e := c.journal.Save(j); e != nil {
		t.Fatal(e)
	}
	if e := c.journal.writeRegistration(reg); e != nil {
		t.Fatal(e)
	}
	releases, _, _ := releaseFixture(t)
	c.releases = releases
	r.fail = "preflight"
	if _, e := c.Check(context.Background(), "install"); e != nil {
		t.Fatal(e)
	}
	return c, r
}
func TestTransactionRequestCancellationDoesNotCancelHelperJob(t *testing.T) {
	c, _ := beginFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	v, e := c.Begin(ctx, "install", BeginRequest{CandidateID: c.candidates["install"].ID, IdempotencyKey: "begin"})
	if e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		j, e := c.journal.Load("install")
		if e != nil {
			t.Fatal(e)
		}
		if j.Phase == PhaseCancelled {
			if j.ErrorCode != "preflight-failed" || j.ID != v.ID {
				t.Fatalf("request cancelled helper: %+v", j)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("helper job did not finish")
}
func TestTransactionCancelIsDurableBeforeReturning(t *testing.T) {
	c, _ := beginFixture(t)
	transport := c.releases.client.Transport
	entered := make(chan struct{})
	var once bool
	c.releases.client.Transport = releaseTransport(func(r *http.Request) (*http.Response, error) {
		if !once {
			once = true
			close(entered)
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return transport.RoundTrip(r)
	})
	v, e := c.Begin(context.Background(), "install", BeginRequest{CandidateID: c.candidates["install"].ID, IdempotencyKey: "cancel"})
	if e != nil {
		t.Fatal(e)
	}
	<-entered
	got, e := c.Cancel(context.Background(), "install", v.ID)
	if e != nil {
		t.Fatal(e)
	}
	if got.Phase != PhaseCancelled {
		t.Fatalf("cancel not durable: %s", got.Phase)
	}
	j, e := c.journal.Load("install")
	if e != nil || j.Phase != PhaseCancelled {
		t.Fatalf("durable cancellation: %s %v", j.Phase, e)
	}
	if _, e = c.Cancel(context.Background(), "install", v.ID); !errors.Is(e, ErrBusy) {
		t.Fatalf("terminal cancel: %v", e)
	}
}
