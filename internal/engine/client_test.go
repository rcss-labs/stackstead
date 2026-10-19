package engine

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fake is a fake Engine API on a unix socket.
type fake struct {
	sock    string
	mu      sync.Mutex
	paths   []string
	version string      // Api-Version header on /_ping; "" omits it
	hang    atomic.Bool // /_ping blocks until the client gives up
}

func (f *fake) pings() int {
	n := 0
	for _, p := range f.seen() {
		if p == "/_ping" {
			n++
		}
	}
	return n
}

func (f *fake) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths...)
}

func serve(t *testing.T, h http.Handler) string { return serveVer(t, "1.41", h).sock }

func serveVer(t *testing.T, version string, h http.Handler) *fake {
	t.Helper()
	dir, err := os.MkdirTemp("", "eng")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	f := &fake{sock: filepath.Join(dir, "d.sock"), version: version}
	l, err := net.Listen("unix", f.sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.Path)
		f.mu.Unlock()
		if r.URL.Path == "/_ping" {
			if f.hang.Load() {
				select {
				case <-r.Context().Done():
				case <-time.After(3 * time.Second):
				}
				return
			}
			if f.version != "" {
				w.Header().Set("Api-Version", f.version)
			}
			w.Write([]byte("OK"))
			return
		}
		h.ServeHTTP(w, r)
	})}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return f
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
			http.Error(w, err.Error(), 500)
			return
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
	if len(cs) != 3 {
		t.Fatalf("got %d containers", len(cs))
	}
	c := cs[0]
	if c.Name != "demo-postgres-1" || c.State != "running" || c.Image != "postgres:16.9" || !strings.HasPrefix(c.Status, "Up") {
		t.Errorf("fields: %+v", c)
	}
	if c.Project() != "demo" || c.ComposeService() != "postgres" {
		t.Errorf("project/service: %q %q", c.Project(), c.ComposeService())
	}
	if want := time.Unix(1760000000, 0).UTC(); !c.Created.Equal(want) {
		t.Errorf("Created = %v, want %v", c.Created, want)
	}
	if !cs[1].Created.IsZero() {
		t.Errorf("missing Created should be zero, got %v", cs[1].Created)
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
	if c := cs[2]; c.State != "exited" || c.Networks["demo_default"] != "172.18.0.50" {
		t.Errorf("stopped container should keep its reserved IP: %+v", c.Networks)
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
	if len(ns) != 6 {
		t.Fatalf("got %d networks", len(ns))
	}
	if b := by["bridge"]; b.Driver != "bridge" || len(b.Subnets) != 1 ||
		b.Subnets[0].Prefix != netip.MustParsePrefix("172.17.0.0/16") || b.Subnets[0].Gateway != netip.MustParseAddr("172.17.0.1") {
		t.Errorf("bridge: %+v", b)
	}
	if n := by["demo_default"]; len(n.Subnets) != 1 || n.Subnets[0].Prefix != netip.MustParsePrefix("172.18.0.0/16") {
		t.Errorf("IPv6 should be ignored: %+v", n)
	}
	for _, name := range []string{"host", "none"} {
		if len(by[name].Subnets) != 0 {
			t.Errorf("%s subnets: %+v", name, by[name].Subnets)
		}
	}
	if n := by["broken"]; len(n.Subnets) != 1 || n.Subnets[0].Prefix != netip.MustParsePrefix("10.5.0.0/24") {
		t.Errorf("malformed subnet should be skipped: %+v", n)
	}
	for _, n := range ns {
		if n.Options == nil || n.Labels == nil || n.Containers != nil {
			t.Errorf("maps/containers = %+v", n)
		}
	}
	if b := by["bridge"]; len(b.Options) != 0 || len(b.Labels) != 0 || b.Subnets[0].IPRange.IsValid() {
		t.Errorf("bridge metadata = %+v", b)
	}
	if n := by["lan"]; n.Scope != "local" || n.Internal || n.Parent() != "enp3s0" || n.Project() != "dns" || n.Subnets[0].IPRange != netip.MustParsePrefix("192.168.0.128/25") {
		t.Errorf("lan = %+v", n)
	}
	if !by["demo_default"].Internal || by["broken"].Subnets[0].IPRange.IsValid() {
		t.Error("internal or malformed range decoding")
	}
}

func TestInspectNetworkDecodes(t *testing.T) {
	sock := serve(t, fileHandler(t, map[string]string{"/v1.41/networks/lan": "inspect_network.json"}))
	got, err := New(sock, time.Second).InspectNetwork(ctx(t), "lan")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "lan" || got.Scope != "local" || got.Parent() != "enp3s0" || got.Project() != "dns" || len(got.Subnets) != 1 {
		t.Fatalf("network = %+v", got)
	}
	want := []NetworkEndpoint{{ID: "aaa", Name: "alpha", IPv4: "192.168.0.30"}, {ID: "bbb", Name: "zebra", IPv4: ""}}
	if !reflect.DeepEqual(got.Containers, want) {
		t.Errorf("containers = %+v", got.Containers)
	}
}

func TestInspectNetworkNotFound(t *testing.T) {
	for _, status := range []int{404, 400} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			sock := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				w.Write([]byte(`{"message":"network lookup failed"}`))
			}))
			_, err := New(sock, time.Second).InspectNetwork(ctx(t), "missing")
			var ae *ActionError
			if !errors.As(err, &ae) || ae.Status != status || ae.Message != "network lookup failed" || errors.Is(err, ErrNetworkNotFound) != (status == 404) {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestCreateNetworkBody(t *testing.T) {
	for _, driver := range []string{"macvlan", "ipvlan", "bridge"} {
		t.Run(driver, func(t *testing.T) {
			spec := NetworkSpec{Name: "lan", Driver: driver, Internal: true}
			want := map[string]any{"Name": "lan", "Driver": driver, "CheckDuplicate": true, "Internal": true, "IPAM": map[string]any{"Driver": "default"}, "Labels": map[string]any{"dev.stackstead.created": "true"}}
			if driver != "bridge" {
				spec.Subnet, spec.Gateway, spec.IPRange, spec.Parent = "192.168.0.0/24", "192.168.0.1", "192.168.0.128/25", "enp3s0"
				want["IPAM"].(map[string]any)["Config"] = []any{map[string]any{"Subnet": spec.Subnet, "Gateway": spec.Gateway, "IPRange": spec.IPRange}}
				options := map[string]any{"parent": "enp3s0"}
				if driver == "ipvlan" {
					spec.IPvlanMode = "l2"
					options["ipvlan_mode"] = "l2"
				}
				want["Options"] = options
			}
			sock := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1.41/networks/create" || r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("request = %s %s, content type %q", r.Method, r.URL.Path, r.Header.Get("Content-Type"))
				}
				var got map[string]any
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Error(err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("body = %#v, want %#v", got, want)
				}
				w.WriteHeader(201)
				w.Write([]byte(`{"Id":"new-network"}`))
			}))
			id, err := New(sock, time.Second).CreateNetwork(ctx(t), spec)
			if err != nil || id != "new-network" {
				t.Errorf("id = %q err = %v", id, err)
			}
		})
	}
}

func TestCreateNetworkMapsErrors(t *testing.T) {
	for _, status := range []int{409, 400, 404} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			sock := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				w.Write([]byte(`{"message":"network with name lan already exists"}`))
			}))
			_, err := New(sock, time.Second).CreateNetwork(ctx(t), NetworkSpec{Name: "lan"})
			var ae *ActionError
			if !errors.As(err, &ae) || ae.Status != status || ae.Message != "network with name lan already exists" || errors.Is(err, ErrNetworkNotFound) {
				t.Errorf("err = %v", err)
			}
		})
	}
	_, err := New(filepath.Join(t.TempDir(), "missing.sock"), time.Second).CreateNetwork(ctx(t), NetworkSpec{})
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v", err)
	}
}

func TestRemoveNetworkMapsStatus(t *testing.T) {
	for _, status := range []int{204, 404, 409, 403} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			sock := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete || r.URL.Path != "/v1.41/networks/lan" {
					t.Errorf("request = %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(status)
				if status != 204 {
					w.Write([]byte(`{"message":"has active endpoints"}`))
				}
			}))
			err := New(sock, time.Second).RemoveNetwork(ctx(t), "lan")
			if status == 204 {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var ae *ActionError
			if !errors.As(err, &ae) || ae.Status != status || ae.Message != "has active endpoints" || errors.Is(err, ErrNetworkNotFound) != (status == 404) {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestPing(t *testing.T) {
	f := serveVer(t, "1.41", http.NotFoundHandler())
	if err := New(f.sock, time.Second).Ping(ctx(t)); err != nil {
		t.Fatal(err)
	}
	if got := f.seen(); len(got) == 0 || got[0] != "/_ping" {
		t.Errorf("Ping must use the unversioned path: %v", got)
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
	if !errors.Is(err, ErrUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
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
	var called atomic.Bool
	sock := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called.Store(true) }))
	// The client timeout supplies a deadline even when the caller's context has none.
	c := New(sock, time.Second)
	var sawDeadline bool
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		_, sawDeadline = r.Context().Deadline()
		return nil, errors.New("stop")
	})
	_, _ = c.Containers(context.Background())
	if !sawDeadline || called.Load() {
		t.Errorf("deadline=%v called=%v", sawDeadline, called.Load())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCallerCancelIsNotUnavailable(t *testing.T) {
	started := make(chan struct{})
	sock := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	c := New(sock, 5*time.Second)
	cctx, cancel := context.WithCancel(context.Background())
	go func() { <-started; cancel() }()
	_, err := c.Networks(cctx)
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrUnavailable) {
		t.Errorf("mid-request cancel: %v", err)
	}
	_, err = c.Networks(cctx) // already cancelled
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrUnavailable) {
		t.Errorf("pre-cancelled: %v", err)
	}
}

func TestVersionNegotiation(t *testing.T) {
	cases := []struct{ header, wantPrefix string }{
		{"1.51", "/v1.47/"},
		{"1.43", "/v1.43/"},
		{"", "/v1.41/"},
	}
	for _, tc := range cases {
		t.Run("header="+tc.header, func(t *testing.T) {
			f := serveVer(t, tc.header, fileHandler(t, map[string]string{
				tc.wantPrefix + "networks": "networks.json",
			}))
			c := New(f.sock, 2*time.Second)
			for i := 0; i < 2; i++ {
				if _, err := c.Networks(ctx(t)); err != nil {
					t.Fatal(err)
				}
			}
			var pings int
			for _, p := range f.seen() {
				if p == "/_ping" {
					pings++
				} else if p != tc.wantPrefix+"networks" {
					t.Errorf("unexpected path %s", p)
				}
			}
			if pings != 1 {
				t.Errorf("version should be negotiated once, pings=%d", pings)
			}
		})
	}
}

func TestVersionTooOld(t *testing.T) {
	f := serveVer(t, "1.40", http.NotFoundHandler())
	_, err := New(f.sock, time.Second).Containers(ctx(t))
	if err == nil || !strings.Contains(err.Error(), "older than the minimum 1.41") || errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v", err)
	}
}

func TestNegotiationRetriedAfterFailure(t *testing.T) {
	dir, err := os.MkdirTemp("", "eng")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "late.sock")
	c := New(sock, time.Second)
	if _, err := c.Networks(ctx(t)); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	h := fileHandler(t, map[string]string{"/v1.41/networks": "networks.json"})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_ping" {
			w.Header().Set("Api-Version", "1.41")
			return
		}
		h.ServeHTTP(w, r)
	})}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	if _, err := c.Networks(ctx(t)); err != nil {
		t.Errorf("failure must not be cached: %v", err)
	}
}

func TestResponseSizeCap(t *testing.T) {
	sock := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("[" + strings.Repeat(" ", 200) + "]"))
	}))
	c := New(sock, time.Second)
	c.maxBody = 100
	_, err := c.Networks(ctx(t))
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("err = %v", err)
	}
}

func TestDefaultTimeout(t *testing.T) {
	sock := serve(t, fileHandler(t, map[string]string{"/v1.41/networks": "networks.json"}))
	if _, err := New(sock, 0).Networks(ctx(t)); err != nil {
		t.Errorf("zero timeout must default, got %v", err)
	}
}

func TestConcurrentFirstCallsPingOnce(t *testing.T) {
	f := serveVer(t, "1.45", fileHandler(t, map[string]string{"/v1.45/networks": "networks.json"}))
	c := New(f.sock, 2*time.Second)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Networks(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := f.pings(); n != 1 {
		t.Errorf("pings = %d, want 1", n)
	}
}

func TestHungNegotiationDoesNotSerializeCallers(t *testing.T) {
	const timeout = 300 * time.Millisecond
	f := serveVer(t, "1.45", http.NotFoundHandler())
	f.hang.Store(true)
	c := New(f.sock, timeout)
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Containers(context.Background()); !errors.Is(err, ErrUnavailable) {
				t.Errorf("err = %v", err)
			}
		}()
	}
	wg.Wait()
	if d := time.Since(start); d > timeout+250*time.Millisecond {
		t.Errorf("5 concurrent calls took %v, want about %v", d, timeout)
	}
}

func TestWaiterHonoursOwnContext(t *testing.T) {
	f := serveVer(t, "1.45", http.NotFoundHandler())
	f.hang.Store(true)
	c := New(f.sock, 2*time.Second)
	go c.Containers(context.Background()) // leader, hangs in negotiation
	time.Sleep(50 * time.Millisecond)
	short, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.Containers(short)
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v", err)
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Errorf("waiter took %v", d)
	}
}

func TestFirstCallBoundedByOneTimeout(t *testing.T) {
	const timeout = 300 * time.Millisecond
	f := serveVer(t, "1.45", http.NotFoundHandler())
	f.hang.Store(true)
	start := time.Now()
	_, err := New(f.sock, timeout).Containers(context.Background())
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v", err)
	}
	if d := time.Since(start); d > timeout+150*time.Millisecond {
		t.Errorf("took %v, want about %v", d, timeout)
	}
}

func TestMalformedApiVersion(t *testing.T) {
	for _, h := range []string{"abc", "1.47.1", "v1.45", "1.45-rc"} {
		t.Run(h, func(t *testing.T) {
			f := serveVer(t, h, http.NotFoundHandler())
			_, err := New(f.sock, time.Second).Containers(ctx(t))
			if err == nil || !strings.Contains(err.Error(), "malformed Api-Version header") || errors.Is(err, ErrUnavailable) {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestVersionDecodes(t *testing.T) {
	f := serveVer(t, "1.46", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/version" {
			w.Write([]byte(`{"Version":"27.1.1","ApiVersion":"1.46"}`))
			return
		}
		http.NotFound(w, r)
	}))
	got, err := New(f.sock, time.Second).Version(ctx(t))
	if err != nil || got != "27.1.1" {
		t.Fatalf("Version = %q, %v; want 27.1.1", got, err)
	}
	if seen := f.seen(); len(seen) == 0 || seen[len(seen)-1] != "/version" {
		t.Errorf("Version must use the unversioned path: %v", seen)
	}
}

func TestVersionErrorsOnServerError(t *testing.T) {
	sock := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"message":"boom"}`))
	}))
	if _, err := New(sock, time.Second).Version(ctx(t)); err == nil {
		t.Fatal("want an error on a 500")
	}
	bad := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("not json")) }))
	if _, err := New(bad, time.Second).Version(ctx(t)); err == nil {
		t.Fatal("want an error on an undecodable body")
	}
}

func infoHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/info" && r.URL.Path != "/v1.41/info" {
			http.NotFound(w, r)
			return
		}
		b, _ := os.ReadFile("testdata/info.json")
		w.Write(b)
	})
}

func TestInfoDecodes(t *testing.T) {
	f := serveVer(t, "1.41", infoHandler())
	got, err := New(f.sock, time.Second).Info(ctx(t))
	if err != nil || got.NCPU != 8 || got.MemTotal != 16777216000 {
		t.Fatalf("Info = %+v, %v", got, err)
	}
}

func TestInfoHTTPError(t *testing.T) {
	f := serveVer(t, "1.41", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"boom"}`, 500)
	}))
	if _, err := New(f.sock, time.Second).Info(ctx(t)); err == nil {
		t.Fatal("want error")
	}
}

func TestVolumesDiskUsageTyped(t *testing.T) {
	var query string
	f := serveVer(t, "1.45", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		fileHandler(t, map[string]string{"/v1.45/system/df": "system_df_volumes.json"}).ServeHTTP(w, r)
	}))
	got, err := New(f.sock, time.Second).VolumesDiskUsage(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if got != (VolumeUsage{Count: 3, Size: 1500, Dangling: 2}) {
		t.Errorf("usage = %+v", got)
	}
	if query != "type=volume" {
		t.Errorf("query = %q, want type=volume", query)
	}
}

func TestVolumesDiskUsageOldAPIUsesUntyped(t *testing.T) {
	var query string
	f := serveVer(t, "1.44", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		fileHandler(t, map[string]string{"/v1.44/system/df": "system_df_legacy.json"}).ServeHTTP(w, r)
	}))
	got, err := New(f.sock, time.Second).VolumesDiskUsage(ctx(t))
	if err != nil || got.Count != 3 || query != "" {
		t.Errorf("usage = %+v, %v, query %q", got, err, query)
	}
}

func TestVolumesDiskUsageLegacyFallback(t *testing.T) {
	var queries []string
	f := serveVer(t, "1.45", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		if r.URL.RawQuery != "" {
			http.Error(w, `{"message":"bad param"}`, 400)
			return
		}
		fileHandler(t, map[string]string{"/v1.45/system/df": "system_df_legacy.json"}).ServeHTTP(w, r)
	}))
	got, err := New(f.sock, time.Second).VolumesDiskUsage(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if got != (VolumeUsage{Count: 3, Size: 1500, Dangling: 2}) {
		t.Errorf("usage = %+v", got)
	}
	if len(queries) != 2 || queries[0] != "type=volume" || queries[1] != "" {
		t.Errorf("queries = %q", queries)
	}
}

func TestDockerCallsHonourTimeout(t *testing.T) {
	f := serveVer(t, "1.41", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(3 * time.Second):
		case <-r.Context().Done():
		}
	}))
	start := time.Now()
	_, err := New(f.sock, 150*time.Millisecond).Info(ctx(t))
	if err == nil || time.Since(start) > 2*time.Second {
		t.Errorf("err = %v after %v", err, time.Since(start))
	}
}

// A background refresh passes its own, longer deadline; the short client
// timeout must not cut it off.
func TestVolumesDiskUsageHonoursCallerDeadlineBeyondClientTimeout(t *testing.T) {
	f := serveVer(t, "1.45", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond) // slower than the client timeout
		fileHandler(t, map[string]string{"/v1.45/system/df": "system_df_volumes.json"}).ServeHTTP(w, r)
	}))
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := New(f.sock, 150*time.Millisecond).VolumesDiskUsage(c)
	if err != nil {
		t.Fatalf("df with a 5s deadline failed: %v", err)
	}
	if got.Count != 3 {
		t.Errorf("usage = %+v", got)
	}
}

func TestVolumesDiskUsageStillBoundedByCallerDeadline(t *testing.T) {
	f := serveVer(t, "1.45", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(600 * time.Millisecond)
	}))
	c, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := New(f.sock, 5*time.Second).VolumesDiskUsage(c); err == nil {
		t.Fatal("want a timeout error")
	}
}

func TestContainerActionMapsStatus(t *testing.T) {
	cases := []struct {
		name, action string
		status       int
		body         string
		wantT        string // expected t query, "" for none
		check        func(t *testing.T, err error)
	}{
		{"start ok", "start", 204, "", "", func(t *testing.T, err error) {
			if err != nil {
				t.Errorf("err = %v", err)
			}
		}},
		{"stop already stopped", "stop", 304, "", "10", func(t *testing.T, err error) {
			if err != nil {
				t.Errorf("304 should be success, got %v", err)
			}
		}},
		{"restart missing", "restart", 404, `{"message":"No such container: x"}`, "10", func(t *testing.T, err error) {
			if !errors.Is(err, ErrNotFound) {
				t.Errorf("err = %v", err)
			}
		}},
		{"start refused", "start", 500, `{"message":"port is already allocated"}`, "", func(t *testing.T, err error) {
			var ae *ActionError
			if !errors.As(err, &ae) || ae.Status != 500 || ae.Message != "port is already allocated" {
				t.Errorf("err = %#v", err)
			}
			if errors.Is(err, ErrUnavailable) {
				t.Error("a refusal is not unavailability")
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var method, path string
			var query url.Values
			sock := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				method, path, query = r.Method, r.URL.Path, r.URL.Query()
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			err := New(sock, 2*time.Second).ContainerAction(ctx(t), "abc", tc.action, 10)
			tc.check(t, err)
			if method != http.MethodPost || path != "/v1.41/containers/abc/"+tc.action {
				t.Errorf("request = %s %s", method, path)
			}
			if got := query.Get("t"); got != tc.wantT {
				t.Errorf("t = %q, want %q", got, tc.wantT)
			}
		})
	}
}

func TestContainerActionUnavailable(t *testing.T) {
	err := New(filepath.Join(t.TempDir(), "none.sock"), time.Second).ContainerAction(ctx(t), "abc", "start", 10)
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v", err)
	}
}

func TestContainerActionRejectsUnknownAction(t *testing.T) {
	f := serveVer(t, "1.41", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s", r.URL)
	}))
	err := New(f.sock, time.Second).ContainerAction(ctx(t), "abc", "kill", 10)
	if err == nil {
		t.Fatal("want error")
	}
	if n := len(f.seen()); n != 0 {
		t.Errorf("%d requests made", n)
	}
}

func inspectClient(t *testing.T) *Client {
	sock := serve(t, fileHandler(t, map[string]string{"/v1.41/containers/abc/json": "inspect_container.json"}))
	return New(sock, 2*time.Second)
}

func TestInspectContainerDecodes(t *testing.T) {
	got, err := inspectClient(t).InspectContainer(ctx(t), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "abc123def456" || got.Name != "demo-postgres-1" || got.Image != "postgres:16.9" || got.ImageID != "sha256:0123456789abcdef" ||
		got.State != "running" || got.Status != "running" || got.Health != "healthy" || !got.Running || got.ExitCode != 0 {
		t.Errorf("scalars: %+v", got)
	}
	if want := time.Date(2026, 10, 14, 8, 30, 0, 123456789, time.UTC); !got.StartedAt.Equal(want) {
		t.Errorf("StartedAt = %v", got.StartedAt)
	}
	if !got.FinishedAt.IsZero() {
		t.Errorf("FinishedAt = %v, want zero", got.FinishedAt)
	}
	if got.RestartPolicy != "unless-stopped" {
		t.Errorf("RestartPolicy = %q", got.RestartPolicy)
	}
	if got.MemoryLimit != 536870912 {
		t.Errorf("MemoryLimit = %d, want 536870912", got.MemoryLimit)
	}
	if got.Labels["com.docker.compose.project"] != "demo" || len(got.Labels) != 2 {
		t.Errorf("labels: %+v", got.Labels)
	}
	if len(got.Ports) != 1 || got.Ports[0] != (Port{IP: "0.0.0.0", PrivatePort: 5432, PublicPort: 5432, Type: "tcp"}) {
		t.Errorf("ports: %+v", got.Ports)
	}
	if len(got.Networks) != 2 || got.Networks["demo_default"] != "172.18.0.2" || got.Networks["demo_backend"] != "172.19.0.5" {
		t.Errorf("networks: %+v", got.Networks)
	}
	wantMounts := []Mount{
		{Type: "volume", Name: "pgdata", Source: "/var/lib/docker/volumes/pgdata/_data", Destination: "/var/lib/postgresql/data"},
		{Type: "bind", Source: "/etc/ssl", Destination: "/certs", ReadOnly: true},
	}
	if !reflect.DeepEqual(got.Mounts, wantMounts) {
		t.Errorf("mounts: %+v", got.Mounts)
	}
}

func TestInspectContainerDropsSecrets(t *testing.T) {
	got, err := inspectClient(t).InspectContainer(ctx(t), "abc")
	if err != nil {
		t.Fatal(err)
	}
	var walk func(v reflect.Value)
	walk = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.String:
			for _, secret := range []string{"hunter2", "--token", "docker-entrypoint"} {
				if strings.Contains(v.String(), secret) {
					t.Errorf("secret %q leaked into %q", secret, v.String())
				}
			}
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				walk(v.Field(i))
			}
		case reflect.Slice, reflect.Array:
			for i := 0; i < v.Len(); i++ {
				walk(v.Index(i))
			}
		case reflect.Map:
			for _, k := range v.MapKeys() {
				walk(k)
				walk(v.MapIndex(k))
			}
		}
	}
	walk(reflect.ValueOf(got))
	for _, banned := range []string{"Env", "Cmd", "Entrypoint", "User"} {
		if _, ok := reflect.TypeOf(got).FieldByName(banned); ok {
			t.Errorf("ContainerInspect has field %s", banned)
		}
	}
}

func TestInspectContainerNotFound(t *testing.T) {
	sock := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte(`{"message":"No such container: nope"}`))
	}))
	_, err := New(sock, time.Second).InspectContainer(ctx(t), "nope")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v", err)
	}
}
