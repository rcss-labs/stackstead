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
	"strings"
	"time"
)

// ErrUnavailable means the Docker engine could not be reached in time.
var ErrUnavailable = errors.New("docker engine is unavailable")

// apiBase pins Engine API v1.41 (Docker 20.10+) so field shapes are stable.
const apiBase = "http://docker/v1.41"

// Client talks to the Docker Engine API. Every request is bounded by the
// client's timeout, even when the caller's context has no deadline.
type Client struct {
	http    *http.Client
	timeout time.Duration
}

// New returns a client for the Docker socket at socketPath.
func New(socketPath string, timeout time.Duration) *Client {
	dialer := &net.Dialer{}
	return &Client{
		timeout: timeout,
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
	_, err := c.get(ctx, "/_ping")
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
			ct.Networks[name] = n.IPAddress
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
			n.Subnets = append(n.Subnets, prefix.Masked())
			if gw, err := netip.ParseAddr(cfg.Gateway); err == nil && gw.Is4() {
				n.Gateways = append(n.Gateways, gw)
			}
		}
		out = append(out, n)
	}
	return out, nil
}

func (c *Client) get(ctx context.Context, path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: reading response: %v", ErrUnavailable, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct{ Message string }
		if json.Unmarshal(body, &e) == nil && e.Message != "" {
			return nil, fmt.Errorf("docker engine: %s (HTTP %d)", e.Message, resp.StatusCode)
		}
		return nil, fmt.Errorf("docker engine: HTTP %d", resp.StatusCode)
	}
	return body, nil
}
