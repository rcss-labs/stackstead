package engine

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// serve starts a fake Engine API on a unix socket and returns its path.
func serve(t *testing.T, h http.Handler) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "eng")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return sock
}

func fileHandler(t *testing.T, routes map[string]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		file, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		b, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	})
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestContainers(t *testing.T) {
	var gotQuery string
	h := fileHandler(t, map[string]string{"/v1.41/containers/json": "containers.json"})
	sock := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		h.ServeHTTP(w, r)
	}))
	cs, err := New(sock, 2*time.Second).Containers(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery != "all=1" {
		t.Errorf("query = %q", gotQuery)
	}
	if len(cs) != 2 {
		t.Fatalf("got %d containers", len(cs))
	}
	c := cs[0]
	if c.Name != "demo-postgres-1" || c.State != "running" || c.Image != "postgres:16.9" || !strings.HasPrefix(c.Status, "Up") {
		t.Errorf("fields: %+v", c)
	}
	if c.Project() != "demo" || c.ComposeService() != "postgres" {
		t.Errorf("project/service: %q %q", c.Project(), c.ComposeService())
	}
	if len(c.Ports) != 3 {
		t.Fatalf("ports: %+v", c.Ports)
	}
	if p := c.Ports[0]; p.IP != "0.0.0.0" || p.PrivatePort != 5432 || p.PublicPort != 5432 || p.Type != "tcp" {
		t.Errorf("port0: %+v", p)
	}
	if p := c.Ports[2]; p.PrivatePort != 9187 || p.PublicPort != 0 {
		t.Errorf("unpublished port: %+v", p)
	}
	if len(c.Networks) != 2 || c.Networks["demo_default"] != "172.18.0.2" || c.Networks["demo_backend"] != "172.19.0.5" {
		t.Errorf("networks: %+v", c.Networks)
	}
	if cs[1].Name != "standalone" || cs[1].Project() != "" || cs[1].ComposeService() != "" {
		t.Errorf("standalone: %+v", cs[1])
	}
}

func TestNetworks(t *testing.T) {
	sock := serve(t, fileHandler(t, map[string]string{"/v1.41/networks": "networks.json"}))
	ns, err := New(sock, 2*time.Second).Networks(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Network{}
	for _, n := range ns {
		by[n.Name] = n
	}
	if len(ns) != 5 {
		t.Fatalf("got %d networks", len(ns))
	}
	if b := by["bridge"]; b.Driver != "bridge" || len(b.Subnets) != 1 ||
		b.Subnets[0] != netip.MustParsePrefix("172.17.0.0/16") || b.Gateways[0] != netip.MustParseAddr("172.17.0.1") {
		t.Errorf("bridge: %+v", b)
	}
	if n := by["demo_default"]; len(n.Subnets) != 1 || n.Subnets[0] != netip.MustParsePrefix("172.18.0.0/16") || len(n.Gateways) != 1 {
		t.Errorf("IPv6 should be ignored: %+v", n)
	}
	for _, name := range []string{"host", "none"} {
		if len(by[name].Subnets) != 0 {
			t.Errorf("%s subnets: %+v", name, by[name].Subnets)
		}
	}
	if n := by["broken"]; len(n.Subnets) != 1 || n.Subnets[0] != netip.MustParsePrefix("10.5.0.0/24") {
		t.Errorf("malformed subnet should be skipped: %+v", n)
	}
}

func TestPing(t *testing.T) {
	sock := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1.41/_ping" {
			t.Errorf("path %s", r.URL.Path)
		}
		w.Write([]byte("OK"))
	}))
	if err := New(sock, time.Second).Ping(ctx(t)); err != nil {
		t.Fatal(err)
	}
}

func TestSocketMissing(t *testing.T) {
	c := New(filepath.Join(t.TempDir(), "nope.sock"), time.Second)
	if err := c.Ping(ctx(t)); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Ping: %v", err)
	}
	if _, err := c.Containers(ctx(t)); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Containers: %v", err)
	}
}

func TestTimeout(t *testing.T) {
	sock := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	}))
	_, err := New(sock, 100*time.Millisecond).Networks(ctx(t))
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v", err)
	}
}

func TestServerError(t *testing.T) {
	sock := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"message":"boom"}`))
	}))
	_, err := New(sock, time.Second).Containers(ctx(t))
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	if errors.Is(err, ErrUnavailable) {
		t.Error("a 500 reply is not unavailability")
	}
}

func TestNoRequestWithoutDeadline(t *testing.T) {
	called := false
	sock := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	// The client timeout supplies a deadline even when the caller's context has none.
	c := New(sock, time.Second)
	var sawDeadline bool
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		_, sawDeadline = r.Context().Deadline()
		return nil, errors.New("stop")
	})
	_, _ = c.Containers(context.Background())
	if !sawDeadline || called {
		t.Errorf("deadline=%v called=%v", sawDeadline, called)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
