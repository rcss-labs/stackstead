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
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrUnavailable means the Docker engine could not be reached in time.
var ErrUnavailable = errors.New("docker engine is unavailable")

// ErrNotFound is wrapped by errors for HTTP 404 answers about a container.
var ErrNotFound = errors.New("docker engine: no such container")

// ActionError is a non-2xx answer from Docker, with the message from its
// {"message": ...} body when it sent one.
type ActionError struct {
	Status  int
	Message string
}

func (e *ActionError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("docker engine: %s (HTTP %d)", e.Message, e.Status)
	}
	return fmt.Sprintf("docker engine: HTTP %d", e.Status)
}

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
	version string  // negotiated; empty until the first success
	flight  *flight // in-progress negotiation, if any
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

// Version returns the engine's version string (e.g. "27.1.1"). It uses the
// unversioned path, like Ping, so it works before API negotiation.
func (c *Client) Version(ctx context.Context) (string, error) {
	body, err := c.do(ctx, "/version")
	if err != nil {
		return "", err
	}
	var v struct {
		Version string `json:"Version"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return "", fmt.Errorf("decode version: %w", err)
	}
	return v.Version, nil
}

// Info is the part of /info the host collector reads.
type Info struct {
	NCPU     int
	MemTotal uint64
}

// Info returns the engine's CPU count and total memory.
func (c *Client) Info(ctx context.Context) (Info, error) {
	body, err := c.get(ctx, "/info")
	if err != nil {
		return Info{}, err
	}
	var raw Info
	if err := json.Unmarshal(body, &raw); err != nil {
		return Info{}, fmt.Errorf("decode info: %w", err)
	}
	return raw, nil
}

// VolumeUsage summarises named volumes from /system/df.
type VolumeUsage struct {
	Count    int
	Size     uint64 // bytes, skipping volumes whose size was not computed
	Dangling int    // volumes no container references
}

// VolumesDiskUsage reads volume usage, bounded by ctx's deadline (the client
// timeout when ctx has none). The type=volume filter exists from API
// 1.45; on older engines, or when the typed call is rejected with HTTP 400,
// it falls back to the full /system/df.
func (c *Client) VolumesDiskUsage(ctx context.Context) (VolumeUsage, error) {
	body, err := c.dfBody(ctx)
	if err != nil {
		return VolumeUsage{}, err
	}
	var raw struct {
		Volumes []struct {
			UsageData *struct {
				RefCount int64
				Size     int64
			}
		}
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return VolumeUsage{}, fmt.Errorf("decode system df: %w", err)
	}
	u := VolumeUsage{Count: len(raw.Volumes)}
	for _, v := range raw.Volumes {
		if v.UsageData == nil {
			continue
		}
		if v.UsageData.Size >= 0 {
			u.Size += uint64(v.UsageData.Size)
		}
		if v.UsageData.RefCount == 0 {
			u.Dangling++
		}
	}
	return u, nil
}

func (c *Client) dfBody(ctx context.Context) ([]byte, error) {
	v, err := c.negotiatedVersion(ctx)
	if err != nil {
		return nil, err
	}
	if typedDF, _ := parseVersion("1.45"); !v.less(typedDF) {
		body, err := c.getWithin(ctx, "/system/df?type=volume", c.callerBound(ctx))
		if err == nil || !strings.Contains(err.Error(), "HTTP 400") {
			return body, err
		}
	}
	return c.getWithin(ctx, "/system/df", c.callerBound(ctx))
}

// callerBound is the deadline for the slow, background-only calls (system df,
// the events window): the caller's own deadline when it set one, otherwise the
// client timeout. `docker system df` takes seconds on a busy host, so the
// short client timeout that suits Info must not cap it.
func (c *Client) callerBound(ctx context.Context) time.Duration {
	if dl, ok := ctx.Deadline(); ok {
		return time.Until(dl)
	}
	return c.timeout
}

// negotiatedVersion returns the parsed API version, negotiating if needed.
func (c *Client) negotiatedVersion(caller context.Context) (version, error) {
	ctx, cancel := context.WithTimeout(caller, c.timeout)
	defer cancel()
	s, err := c.apiVersion(caller, ctx)
	if err != nil {
		return version{}, err
	}
	v, _ := parseVersion(s)
	return v, nil
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
		if r.Created > 0 {
			ct.Created = time.Unix(r.Created, 0).UTC()
		}
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
// One client-timeout deadline bounds negotiation and request together.
func (c *Client) get(caller context.Context, path string) ([]byte, error) {
	return c.getWithin(caller, path, c.timeout)
}

// getWithin is get with an explicit deadline in place of the client timeout.
func (c *Client) getWithin(caller context.Context, path string, timeout time.Duration) ([]byte, error) {
	if err := caller.Err(); err != nil {
		return nil, fmt.Errorf("docker engine request: %w", err)
	}
	ctx, cancel := context.WithTimeout(caller, timeout)
	defer cancel()
	v, err := c.apiVersion(caller, ctx)
	if err != nil {
		return nil, err
	}
	r, err := c.request(caller, ctx, http.MethodGet, "/v"+v+path)
	if err != nil {
		return nil, err
	}
	return r.body, nil
}

// flight is one in-progress version negotiation shared by all waiting callers.
type flight struct {
	done    chan struct{}
	version string
	err     error
}

// apiVersion returns the cached version, or joins (or starts) a single
// negotiation. No lock is held during I/O and each caller waits only as long
// as its own ctx allows. The negotiation runs detached with its own timeout so
// one caller's cancellation cannot fail the others. Failures are not cached.
func (c *Client) apiVersion(caller, ctx context.Context) (string, error) {
	c.mu.Lock()
	if c.version != "" {
		v := c.version
		c.mu.Unlock()
		return v, nil
	}
	f := c.flight
	if f == nil {
		f = &flight{done: make(chan struct{})}
		c.flight = f
		go c.negotiate(f)
	}
	c.mu.Unlock()

	select {
	case <-f.done:
		return f.version, f.err
	case <-ctx.Done():
		return "", c.classify(caller, ctx.Err())
	}
}

func (c *Client) negotiate(f *flight) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	v, err := c.negotiateVersion(ctx)
	c.mu.Lock()
	if err == nil {
		c.version = v
	}
	c.flight = nil
	f.version, f.err = v, err
	c.mu.Unlock()
	close(f.done)
}

func (c *Client) negotiateVersion(ctx context.Context) (string, error) {
	resp, err := c.request(context.Background(), ctx, http.MethodGet, "/_ping")
	if err != nil {
		return "", err
	}
	server := resp.header.Get("Api-Version")
	if server == "" {
		return minAPIVersion, nil
	}
	sv, ok := parseVersion(server)
	if !ok {
		return "", fmt.Errorf("docker engine: malformed Api-Version header %q", server)
	}
	if min, _ := parseVersion(minAPIVersion); sv.less(min) {
		return "", fmt.Errorf("docker engine API %s is older than the minimum %s", server, minAPIVersion)
	}
	if max, _ := parseVersion(maxAPIVersion); max.less(sv) {
		return maxAPIVersion, nil
	}
	return server, nil
}

type version [2]int

func (a version) less(b version) bool {
	return a[0] < b[0] || (a[0] == b[0] && a[1] < b[1])
}

// parseVersion accepts strictly "major.minor" with decimal digits.
func parseVersion(s string) (version, bool) {
	major, minor, ok := strings.Cut(s, ".")
	if !ok {
		return version{}, false
	}
	var v version
	var err1, err2 error
	v[0], err1 = strconv.Atoi(major)
	v[1], err2 = strconv.Atoi(minor)
	if err1 != nil || err2 != nil || major[0] == '+' || major[0] == '-' || minor[0] == '+' || minor[0] == '-' {
		return version{}, false
	}
	return v, true
}

type response struct {
	header http.Header
	body   []byte
}

// do performs an unversioned GET bounded by the client timeout.
func (c *Client) do(caller context.Context, path string) ([]byte, error) {
	if err := caller.Err(); err != nil {
		return nil, fmt.Errorf("docker engine request: %w", err)
	}
	ctx, cancel := context.WithTimeout(caller, c.timeout)
	defer cancel()
	r, err := c.request(caller, ctx, http.MethodGet, path)
	if err != nil {
		return nil, err
	}
	return r.body, nil
}

// request issues one request under ctx, which must already carry a deadline.
// caller is the original caller context, used only to classify failures.
// Non-2xx answers come back as *ActionError.
func (c *Client) request(caller, ctx context.Context, method, path string) (*response, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, c.classify(caller, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBody+1))
	if err != nil {
		return nil, c.classify(caller, err)
	}
	if int64(len(body)) > c.maxBody {
		return nil, fmt.Errorf("docker engine: response too large (over %d bytes)", c.maxBody)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct{ Message string }
		_ = json.Unmarshal(body, &e)
		return nil, &ActionError{Status: resp.StatusCode, Message: e.Message}
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

// ContainerAction starts, stops or restarts a container. stopTimeout is the
// grace period in seconds for stop and restart. Docker's 304 (already in that
// state) counts as success; the call is bounded by ctx's deadline, or the
// client timeout when ctx has none.
func (c *Client) ContainerAction(ctx context.Context, id, action string, stopTimeout int) error {
	path := "/containers/" + url.PathEscape(id) + "/" + action
	switch action {
	case "start":
	case "stop", "restart":
		path += "?t=" + strconv.Itoa(stopTimeout)
	default:
		return fmt.Errorf("docker engine: unsupported container action %q", action)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("docker engine request: %w", err)
	}
	reqCtx, cancel := context.WithTimeout(ctx, c.callerBound(ctx))
	defer cancel()
	v, err := c.apiVersion(ctx, reqCtx)
	if err != nil {
		return err
	}
	_, err = c.request(ctx, reqCtx, http.MethodPost, "/v"+v+path)
	return actionResult(err)
}

// actionResult maps Docker's 304 to success and 404 to ErrNotFound.
func actionResult(err error) error {
	var ae *ActionError
	if !errors.As(err, &ae) {
		return err
	}
	switch ae.Status {
	case http.StatusNotModified:
		return nil
	case http.StatusNotFound:
		return fmt.Errorf("%w: %w", ErrNotFound, ae)
	}
	return err
}

// InspectContainer returns the safe subset of GET /containers/{id}/json. It
// never decodes Env, Cmd, Entrypoint or User, so they cannot leak.
func (c *Client) InspectContainer(ctx context.Context, id string) (ContainerInspect, error) {
	body, err := c.get(ctx, "/containers/"+url.PathEscape(id)+"/json")
	if err != nil {
		var ae *ActionError
		if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
			return ContainerInspect{}, fmt.Errorf("%w: %w", ErrNotFound, ae)
		}
		return ContainerInspect{}, err
	}
	var raw apiInspect
	if err := json.Unmarshal(body, &raw); err != nil {
		return ContainerInspect{}, fmt.Errorf("decode container: %w", err)
	}
	return raw.toInspect(), nil
}
