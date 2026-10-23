package api_test

import (
	"bufio"
	"context"
	"net/http"
	"testing"
	"time"
)

// openForwardedEvents opens A's forwarded /api/events stream of server peerID
// and returns a channel that closes when the stream ends.
func openForwardedEvents(t *testing.T, a *integrationNode, peerID string) <-chan struct{} {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, "GET", a.url()+"/api/servers/"+peerID+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(a.cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("forwarded events = %d", resp.StatusCode)
	}
	t.Cleanup(func() { resp.Body.Close() })
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if sc.Text() == "event: status" {
			break
		}
	}
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		for sc.Scan() {
		}
	}()
	return ended
}

func pairedNodes(t *testing.T) (a, b *integrationNode, bID string) {
	t.Helper()
	a = newIntegrationNode(t, "mini-a", "web")
	b = newIntegrationNode(t, "mini-b", "db")
	if status, body := a.do("POST", "/api/peers", addBody(b.url(), b.token(), a.url(), true)); status != 201 {
		t.Fatalf("pair = %d %s", status, body)
	}
	return a, b, a.list()[0].ID
}

func requireEnds(t *testing.T, ended <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ended:
	case <-time.After(3 * time.Second):
		t.Fatalf("stream stayed open after %s", what)
	}
}

func TestForwardedStreamEndsWhenPeerIsRemovedLocally(t *testing.T) {
	a, _, bID := pairedNodes(t)
	ended := openForwardedEvents(t, a, bID)
	if err := a.svc.Remove(context.Background(), bID); err != nil {
		t.Fatal(err)
	}
	requireEnds(t, ended, "the peer was removed here")
}

func TestPeerCredentialStreamEndsWhenPeerUnpairs(t *testing.T) {
	a, b, bID := pairedNodes(t)
	ended := openForwardedEvents(t, a, bID)
	// B forgets A without telling A: only B's own inbound stream from A is
	// affected, and A's forwarded stream ends because B closed it.
	aOnB, err := b.store.Peer(context.Background(), b.list()[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.svc.AcceptUnpair(context.Background(), aOnB); err != nil {
		t.Fatal(err)
	}
	requireEnds(t, ended, "the serving peer unpaired")
}
