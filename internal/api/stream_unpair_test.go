package api_test

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/images"
	"github.com/rcss-labs/stackstead/internal/peers"
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

// stallingTar yields one chunk, then blocks until Close, like an engine
// stream that has gone quiet mid-download.
type stallingTar struct {
	sent   atomic.Bool
	closed chan struct{}
	once   sync.Once
}

func (s *stallingTar) Read(p []byte) (int, error) {
	if s.sent.CompareAndSwap(false, true) {
		return copy(p, "FIRST-CHUNK"), nil
	}
	<-s.closed
	return 0, io.ErrClosedPipe
}

func (s *stallingTar) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

func TestPeerImageTarEndsWhenPeerUnpairs(t *testing.T) {
	n := newImagesPeerNode(t)
	if _, err := n.svc.Create(context.Background(), "admin", images.CreateRequest{Name: "app", Source: images.SourceUpload}); err != nil {
		t.Fatal(err)
	}
	n.build("app", "1")
	stream := &stallingTar{closed: make(chan struct{})}
	n.eng.saveStream = stream

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, "GET", n.url()+"/api/peer/images/app/versions/1/tar", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(peers.PeerHeader, n.credential)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tar = %d", resp.StatusCode)
	}
	first := make([]byte, len("FIRST-CHUNK"))
	if _, err := io.ReadFull(resp.Body, first); err != nil {
		t.Fatalf("first chunk: %v", err)
	}
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		io.Copy(io.Discard, resp.Body)
	}()

	list, err := n.store.Peers(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("peers = %v err %v", list, err)
	}
	if err := n.peerNode.svc.AcceptUnpair(context.Background(), list[0]); err != nil {
		t.Fatal(err)
	}
	requireEnds(t, ended, "the downloading peer unpaired")
	select {
	case <-stream.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("engine stream stayed open after the peer unpaired")
	}
}
