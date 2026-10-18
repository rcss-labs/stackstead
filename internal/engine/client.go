// Package engine is a minimal Docker Engine API client over the unix socket.
// It models only the fields Stackstead reads, which keeps the binary small.
package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrUnavailable means the Docker engine could not be reached in time.
var ErrUnavailable = errors.New("docker engine is unavailable")

// ErrNotFound is wrapped by errors for HTTP 404 answers about a container.
var ErrNotFound = errors.New("docker engine: no such container")

// ErrNetworkNotFound is wrapped for Docker 404 answers about a network.
var ErrNetworkNotFound = errors.New("docker engine: no such network")

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

// Client talks to the Docker Engine API. Every request has a deadline,
// using the client timeout unless a method specifies its own bound.
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
	snapshot, err := c.DiskUsage(ctx, DFVolumes)
	if err != nil {
		return VolumeUsage{}, err
	}
	u := VolumeUsage{Count: len(snapshot.Volumes)}
	for _, v := range snapshot.Volumes {
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
		ct := Container{ID: r.ID, Image: r.Image, ImageID: r.ImageID, State: r.State, Status: r.Status, Labels: r.Labels}
		if r.Created > 0 {
			ct.Created = time.Unix(r.Created, 0).UTC()
		}
		if len(r.Names) > 0 {
			ct.Name = strings.TrimPrefix(r.Names[0], "/")
		}
		for _, m := range r.Mounts {
			ct.Mounts = append(ct.Mounts, m.toMount())
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
		n := r.toNetwork()
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
	return c.send(caller, ctx, req)
}

// postJSON sends a JSON body through the same bounded response/error path.
func (c *Client) postJSON(caller, ctx context.Context, path string, payload any) (*response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode docker request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://docker"+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.send(caller, ctx, req)
}

// send reads, caps and classifies a request's response under its deadline.
func (c *Client) send(caller, ctx context.Context, req *http.Request) (*response, error) {
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

// InspectNetwork reads a network and its attached containers with the client timeout.
func (c *Client) InspectNetwork(ctx context.Context, id string) (Network, error) {
	body, err := c.get(ctx, "/networks/"+url.PathEscape(id))
	if err != nil {
		return Network{}, networkResult(err)
	}
	var raw apiNetwork
	if err := json.Unmarshal(body, &raw); err != nil {
		return Network{}, fmt.Errorf("decode network: %w", err)
	}
	n := raw.toNetwork()
	n.Containers = make([]NetworkEndpoint, 0, len(raw.Containers))
	for id, endpoint := range raw.Containers {
		ip, _, _ := strings.Cut(endpoint.IPv4Address, "/")
		n.Containers = append(n.Containers, NetworkEndpoint{ID: id, Name: endpoint.Name, IPv4: ip})
	}
	sort.Slice(n.Containers, func(i, j int) bool {
		if n.Containers[i].Name == n.Containers[j].Name {
			return n.Containers[i].ID < n.Containers[j].ID
		}
		return n.Containers[i].Name < n.Containers[j].Name
	})
	return n, nil
}

// CreateNetwork creates a network bounded by the caller's deadline, or the client timeout.
func (c *Client) CreateNetwork(ctx context.Context, spec NetworkSpec) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("docker engine request: %w", err)
	}
	reqCtx, cancel := context.WithTimeout(ctx, c.callerBound(ctx))
	defer cancel()
	v, err := c.apiVersion(ctx, reqCtx)
	if err != nil {
		return "", err
	}
	ipam := struct {
		Driver string
		Config []map[string]string `json:",omitempty"`
	}{Driver: "default"}
	if spec.Subnet != "" {
		config := map[string]string{"Subnet": spec.Subnet}
		if spec.Gateway != "" {
			config["Gateway"] = spec.Gateway
		}
		if spec.IPRange != "" {
			config["IPRange"] = spec.IPRange
		}
		ipam.Config = []map[string]string{config}
	}
	options := make(map[string]string)
	if spec.Parent != "" {
		options["parent"] = spec.Parent
	}
	if spec.IPvlanMode != "" {
		options["ipvlan_mode"] = spec.IPvlanMode
	}
	payload := struct {
		Name           string
		CheckDuplicate bool
		Driver         string
		Internal       bool
		IPAM           any
		Options        map[string]string `json:",omitempty"`
		Labels         map[string]string
	}{Name: spec.Name, CheckDuplicate: true, Driver: spec.Driver, Internal: spec.Internal, IPAM: ipam, Options: options, Labels: map[string]string{labelCreated: "true"}}
	resp, err := c.postJSON(ctx, reqCtx, "/v"+v+"/networks/create", payload)
	if err != nil {
		return "", err
	}
	var result struct {
		ID string `json:"Id"`
	}
	if err := json.Unmarshal(resp.body, &result); err != nil {
		return "", fmt.Errorf("decode created network: %w", err)
	}
	return result.ID, nil
}

// RemoveNetwork removes a network, preserving Docker's refusal message.
func (c *Client) RemoveNetwork(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("docker engine request: %w", err)
	}
	reqCtx, cancel := context.WithTimeout(ctx, c.callerBound(ctx))
	defer cancel()
	v, err := c.apiVersion(ctx, reqCtx)
	if err != nil {
		return err
	}
	_, err = c.request(ctx, reqCtx, http.MethodDelete, "/v"+v+"/networks/"+url.PathEscape(id))
	return networkResult(err)
}

func networkResult(err error) error {
	var ae *ActionError
	if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
		return fmt.Errorf("%w: %w", ErrNetworkNotFound, ae)
	}
	return err
}

// ErrImageNotFound identifies Docker 404 answers about an image.
var ErrImageNotFound = errors.New("docker engine: no such image")

// ErrVolumeNotFound identifies Docker 404 answers about a volume.
var ErrVolumeNotFound = errors.New("docker engine: no such volume")

// Images lists all local images, dropping Docker's placeholder tags.
func (c *Client) Images(ctx context.Context) ([]Image, error) {
	body, err := c.get(ctx, "/images/json")
	if err != nil {
		return nil, err
	}
	var raw []apiImage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode images: %w", err)
	}
	out := make([]Image, 0, len(raw))
	for _, r := range raw {
		out = append(out, r.toImage())
	}
	return out, nil
}

// InspectImage reads safe image details. User is retained for internal use only.
func (c *Client) InspectImage(ctx context.Context, id string) (ImageInspect, error) {
	body, err := c.get(ctx, "/images/"+url.PathEscape(id)+"/json")
	if err != nil {
		return ImageInspect{}, storageResult(err, ErrImageNotFound)
	}
	var raw apiImageInspect
	if err := json.Unmarshal(body, &raw); err != nil {
		return ImageInspect{}, fmt.Errorf("decode image: %w", err)
	}
	return raw.toInspect(), nil
}

// Volumes lists volumes without credential-bearing driver options.
func (c *Client) Volumes(ctx context.Context) ([]Volume, error) {
	body, err := c.get(ctx, "/volumes")
	if err != nil {
		return nil, err
	}
	var raw struct{ Volumes []apiVolume }
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode volumes: %w", err)
	}
	out := make([]Volume, 0, len(raw.Volumes))
	for _, r := range raw.Volumes {
		out = append(out, r.toVolume())
	}
	return out, nil
}

// InspectVolume reads volume metadata and only the type driver option.
func (c *Client) InspectVolume(ctx context.Context, name string) (VolumeInspect, error) {
	body, err := c.get(ctx, "/volumes/"+url.PathEscape(name))
	if err != nil {
		return VolumeInspect{}, storageResult(err, ErrVolumeNotFound)
	}
	var raw apiVolume
	if err := json.Unmarshal(body, &raw); err != nil {
		return VolumeInspect{}, fmt.Errorf("decode volume: %w", err)
	}
	return raw.toVolume(), nil
}

// RemoveImage removes exactly the given reference without forcing. Docker can
// untag one reference or delete image layers; both outcomes are returned.
func (c *Client) RemoveImage(ctx context.Context, ref string) (ImageDeleteResult, error) {
	body, err := c.deleteStorage(ctx, "/images/"+strings.ReplaceAll(url.PathEscape(ref), ":", "%3A"))
	if err != nil {
		return ImageDeleteResult{}, storageResult(err, ErrImageNotFound)
	}
	var raw []struct{ Untagged, Deleted string }
	if err := json.Unmarshal(body, &raw); err != nil {
		return ImageDeleteResult{}, fmt.Errorf("decode image deletion: %w", err)
	}
	out := ImageDeleteResult{Untagged: make([]string, 0), Deleted: make([]string, 0)}
	for _, r := range raw {
		if r.Untagged != "" {
			out.Untagged = append(out.Untagged, r.Untagged)
		}
		if r.Deleted != "" {
			out.Deleted = append(out.Deleted, r.Deleted)
		}
	}
	return out, nil
}

// RemoveVolume removes exactly the named volume without forcing.
func (c *Client) RemoveVolume(ctx context.Context, name string) error {
	_, err := c.deleteStorage(ctx, "/volumes/"+url.PathEscape(name))
	return storageResult(err, ErrVolumeNotFound)
}

// deleteStorage gives removals their own bound, shared by negotiation and the
// request, while preserving an earlier caller deadline.
func (c *Client) deleteStorage(caller context.Context, path string) ([]byte, error) {
	if err := caller.Err(); err != nil {
		return nil, fmt.Errorf("docker engine request: %w", err)
	}
	ctx, cancel := context.WithTimeout(caller, 60*time.Second)
	defer cancel()
	v, err := c.apiVersion(caller, ctx)
	if err != nil {
		return nil, err
	}
	response, err := c.request(caller, ctx, http.MethodDelete, "/v"+v+path)
	if err != nil {
		return nil, err
	}
	return response.body, nil
}
func storageResult(err error, notFound error) error {
	var ae *ActionError
	if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
		return fmt.Errorf("%w: %w", notFound, ae)
	}
	return err
}

// Build-cache ID filtering remains disabled until a separate verified change.
const buildPruneByIDMinAPIVersion = "1.48"

// DiskUsage reads one category, falling back to all categories on older engines.
// Slow reads honor the caller's deadline rather than the short client timeout.
func (c *Client) DiskUsage(ctx context.Context, kind DFKind) (DFSnapshot, error) {
	switch kind {
	case DFImages, DFContainers, DFVolumes, DFBuildCache:
	default:
		return DFSnapshot{}, fmt.Errorf("invalid disk usage kind %q", kind)
	}
	v, err := c.negotiatedVersion(ctx)
	if err != nil {
		return DFSnapshot{}, err
	}
	kinds := []DFKind{DFImages, DFContainers, DFVolumes, DFBuildCache}
	var body []byte
	typedDF, _ := parseVersion("1.45")
	if !v.less(typedDF) {
		body, err = c.getWithin(ctx, "/system/df?type="+url.QueryEscape(string(kind)), c.callerBound(ctx))
		var action *ActionError
		if err != nil && (!errors.As(err, &action) || action.Status != http.StatusBadRequest) {
			return DFSnapshot{}, err
		}
		if err == nil {
			kinds = []DFKind{kind}
		}
	}
	if len(kinds) == 4 {
		body, err = c.getWithin(ctx, "/system/df", c.callerBound(ctx))
		if err != nil {
			return DFSnapshot{}, err
		}
	}
	var snapshot DFSnapshot
	if err := json.Unmarshal(body, &snapshot); err != nil {
		return DFSnapshot{}, fmt.Errorf("decode system df: %w", err)
	}
	snapshot.Kinds = kinds
	if len(kinds) == 1 {
		if kind != DFImages {
			snapshot.Images = nil
		}
		if kind != DFContainers {
			snapshot.Containers = nil
		}
		if kind != DFVolumes {
			snapshot.Volumes = nil
		}
		if kind != DFBuildCache {
			snapshot.BuildCache = nil
		}
	}
	return snapshot, nil
}

// BuildPruneByID compares API versions only; it never probes a prune endpoint.
func (c *Client) BuildPruneByID(ctx context.Context) (bool, error) {
	return c.buildPruneByID(ctx, buildPruneByIDMinAPIVersion)
}
func (c *Client) buildPruneByID(ctx context.Context, minimum string) (bool, error) {
	v, err := c.negotiatedVersion(ctx)
	if err != nil {
		return false, err
	}
	minVersion, ok := parseVersion(minimum)
	if !ok {
		return false, fmt.Errorf("invalid build prune API version %q", minimum)
	}
	return !v.less(minVersion), nil
}

// PruneBuildCache prunes all unused records for nil ids, or filters by exact
// IDs when supplied. A nonnil empty slice is a no-op, including negotiation.
// Callers must check BuildPruneByID before presenting a filtered prune as exact.
func (c *Client) PruneBuildCache(caller context.Context, ids []string) (int64, error) {
	if ids != nil && len(ids) == 0 {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(caller, 10*time.Minute)
	defer cancel()
	v, err := c.apiVersion(caller, ctx)
	if err != nil {
		return 0, err
	}
	query := url.Values{"all": {"true"}}
	if ids != nil {
		filters, err := json.Marshal(map[string][]string{"id": ids})
		if err != nil {
			return 0, fmt.Errorf("encode prune filters: %w", err)
		}
		query.Set("filters", string(filters))
	}
	response, err := c.request(caller, ctx, http.MethodPost, "/v"+v+"/build/prune?"+query.Encode())
	if err != nil {
		return 0, err
	}
	var result struct{ SpaceReclaimed int64 }
	if err := json.Unmarshal(response.body, &result); err != nil {
		return 0, fmt.Errorf("decode build prune: %w", err)
	}
	return result.SpaceReclaimed, nil
}
