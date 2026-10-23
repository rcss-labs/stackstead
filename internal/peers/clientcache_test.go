package peers

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func countingServer(t *testing.T) (*url.URL, *atomic.Int32) {
	t.Helper()
	var conns atomic.Int32
	srv := httptest.NewUnstartedServer(jsonHandler(`{"ok":true}`))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	return u, &conns
}

func getOnce(t *testing.T, c *http.Client, u *url.URL) {
	t.Helper()
	resp, err := c.Get(u.String())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body) // draining lets the connection be reused
	resp.Body.Close()
}

func TestClientCacheReusesConnections(t *testing.T) {
	u, conns := countingServer(t)
	var cache ClientCache
	p := Peer{InstanceID: "a", URL: u.String(), TLS: TLSOptions{Plaintext: true}}
	for range 3 {
		c, err := cache.Get(p, u, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		getOnce(t, c, u)
	}
	if n := conns.Load(); n != 1 {
		t.Fatalf("new connections = %d, want 1", n)
	}
}

func TestClientCacheReplacesClientWhenTrustChanges(t *testing.T) {
	u, _ := countingServer(t)
	var cache ClientCache
	p := Peer{InstanceID: "a", URL: u.String(), TLS: TLSOptions{Plaintext: true}}
	first, _ := cache.Get(p, u, time.Second)
	same, _ := cache.Get(p, u, time.Second)
	if first != same {
		t.Fatal("unchanged peer got a new client")
	}
	p.TLS.PinSHA256 = strings.Repeat("ab", 32)
	changed, _ := cache.Get(p, u, time.Second)
	if changed == first {
		t.Fatal("changed pin kept the old client")
	}
	cache.Forget("a")
	after, _ := cache.Get(p, u, time.Second)
	if after == changed {
		t.Fatal("forgotten peer kept its client")
	}
}

func TestClientCacheKeepsTimeoutsApart(t *testing.T) {
	u, _ := countingServer(t)
	var cache ClientCache
	p := Peer{InstanceID: "a", URL: u.String(), TLS: TLSOptions{Plaintext: true}}
	short, _ := cache.Get(p, u, time.Second)
	long, _ := cache.Get(p, u, time.Minute)
	if short == long {
		t.Fatal("different header timeouts shared a client")
	}
	if again, _ := cache.Get(p, u, time.Second); again != short {
		t.Fatal("short-timeout client was not reused")
	}
}
