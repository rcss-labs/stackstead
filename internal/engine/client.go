// Package engine is a minimal Docker Engine API client over the unix socket.
// It models only the fields Stackstead reads, which keeps the binary small.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrUnavailable means the Docker engine could not be reached in time.
var ErrUnavailable = errors.New("docker engine is unavailable")

// The client negotiates an API version in [minAPIVersion, maxAPIVersion]: at
// least v1.41 (Docker 20.10+) so field shapes are stable, capped at what we
// have verified. Engines that raised their minimum (Docker 29+: 1.44) still work.
const (
	minAPIVersion  = "1.41"
	maxAPIVersion  = "1.47"
	defaultTimeout = 5 * time.Second
	maxBodyBytes   = 32 << 20
)

// Client talks to the Docker Engine API. Every request is bounded by the
// client's timeout, even when the caller's context has no deadline.
type Client struct {
	http    *http.Client
	timeout time.Duration
	maxBody int64

	mu      sync.Mutex
	version string // negotiated; empty until the first success
}

// New returns a client for the Docker socket at socketPath.
func New(socketPath string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	dialer := &net.Dialer{}
	return &Client{
		timeout: timeout,
		maxBody: maxBodyBytes,
		http: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "unix", socketPath)
			},
			DisableKeepAlives: true,
		}},
	}
}

// Ping checks that the engine answers.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.do(ctx, "/_ping")
	return err
}

// Containers lists all containers, running or not.
func (c *Client) Containers(ctx context.Context) ([]Container, error) {
	body, err := c.get(ctx, "/containers/json?all=1")
	if err != nil {
		return nil, err
	}
	var raw []apiContainer
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode containers: %w", err)
	}
	out := make([]Container, 0, len(raw))
	for _, r := range raw {
		ct := Container{ID: r.ID, Image: r.Image, State: r.State, Status: r.Status, Labels: r.Labels}
		if len(r.Names) > 0 {
			ct.Name = strings.TrimPrefix(r.Names[0], "/")
		}
		for _, p := range r.Ports {
			ct.Ports = append(ct.Ports, Port(p))
		}
		ct.Networks = make(map[string]string, len(r.NetworkSettings.Networks))
		for name, n := range r.NetworkSettings.Networks {
			ip := n.IPAddress
			if ip == "" && n.IPAMConfig != nil {
				ip = n.IPAMConfig.IPv4Address // stopped containers keep only the reservation
			}
			ct.Networks[name] = ip
		}
		out = append(out, ct)
	}
	return out, nil
}

// Networks lists networks with their IPv4 subnets. IPv6 and malformed
// subnets are skipped rather than failing the call.
func (c *Client) Networks(ctx context.Context) ([]Network, error) {
	body, err := c.get(ctx, "/networks")
	if err != nil {
		return nil, err
	}
	var raw []apiNetwork
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode networks: %w", err)
	}
	out := make([]Network, 0, len(raw))
	for _, r := range raw {
		n := Network{ID: r.ID, Name: r.Name, Driver: r.Driver}
		for _, cfg := range r.IPAM.Config {
			prefix, err := netip.ParsePrefix(cfg.Subnet)
			if err != nil || !prefix.Addr().Is4() {
				continue
			}
			sn := Subnet{Prefix: prefix.Masked()}
			if gw, err := netip.ParseAddr(cfg.Gateway); err == nil && gw.Is4() {
				sn.Gateway = gw
			}
			n.Subnets = append(n.Subnets, sn)
		}
		out = append(out, n)
	}
	return out, nil
}

// get performs a versioned GET, negotiating the API version on first use.
func (c *Client) get(ctx context.Context, path string) ([]byte, error) {
	v, err := c.apiVersion(ctx)
	if err != nil {
		return nil, err
	}
	return c.do(ctx, "/v"+v+path)
}

// apiVersion returns the cached version, negotiating via GET /_ping if
// needed. Failures are not cached.
func (c *Client) apiVersion(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.version != "" {
		return c.version, nil
	}
	resp, err := c.request(ctx, "/_ping")
	if err != nil {
		return "", err
	}
	server := resp.header.Get("Api-Version")
	if server == "" {
		server = minAPIVersion
	}
	if compareVersions(server, minAPIVersion) < 0 {
		return "", fmt.Errorf("docker engine API %s is older than the minimum %s", server, minAPIVersion)
	}
	if compareVersions(server, maxAPIVersion) > 0 {
		server = maxAPIVersion
	}
	c.version = server
	return server, nil
}

// compareVersions compares "major.minor" strings; unparsable input sorts lowest.
func compareVersions(a, b string) int {
	pa, pb := parseVersion(a), parseVersion(b)
	for i := range pa {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func parseVersion(s string) [2]int {
	var v [2]int
	major, minor, _ := strings.Cut(s, ".")
	v[0], _ = strconv.Atoi(major)
	v[1], _ = strconv.Atoi(minor)
	return v
}

type response struct {
	header http.Header
	body   []byte
}

func (c *Client) do(ctx context.Context, path string) ([]byte, error) {
	r, err := c.request(ctx, path)
	if err != nil {
		return nil, err
	}
	return r.body, nil
}

func (c *Client) request(parent context.Context, path string) (*response, error) {
	if err := parent.Err(); err != nil {
		return nil, fmt.Errorf("docker engine request: %w", err)
	}
	ctx, cancel := context.WithTimeout(parent, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, c.classify(parent, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBody+1))
	if err != nil {
		return nil, c.classify(parent, err)
	}
	if int64(len(body)) > c.maxBody {
		return nil, fmt.Errorf("docker engine: response too large (over %d bytes)", c.maxBody)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct{ Message string }
		if json.Unmarshal(body, &e) == nil && e.Message != "" {
			return nil, fmt.Errorf("docker engine: %s (HTTP %d)", e.Message, resp.StatusCode)
		}
		return nil, fmt.Errorf("docker engine: HTTP %d", resp.StatusCode)
	}
	return &response{header: resp.Header, body: body}, nil
}

// classify keeps a caller's own cancellation distinct from the engine being
// unreachable, and preserves the underlying cause either way.
func (c *Client) classify(parent context.Context, err error) error {
	if perr := parent.Err(); perr != nil {
		return fmt.Errorf("docker engine request: %w", perr)
	}
	return fmt.Errorf("%w: %w", ErrUnavailable, err)
}
