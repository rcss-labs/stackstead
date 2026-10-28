// Package client is a typed HTTP and server-sent-events client for the
// Stackstead API. It is built only on internal/apitypes and the standard
// library; deps_test.go enforces that.
package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
)

const (
	sessionCookie = "stackstead_session"
	// Servers reached over HTTPS name the cookie with the __Host- prefix. The
	// client reads either, and always sends the plain name, which servers accept.
	hostSessionCookie = "__Host-" + sessionCookie
	checksHeader      = "X-Stackstead-Checks"
	maxErrorBody      = 1 << 20
	defaultTimeout    = 30 * time.Second
)

// errRedirectHost is returned by CheckRedirect when a redirect leaves the
// configured host, so a session cookie can never be sent elsewhere.
var errRedirectHost = errors.New("refusing to follow a redirect to a different host or scheme")

// Config configures a Client.
type Config struct {
	BaseURL   *url.URL      // scheme://host[:port][/prefix]; the prefix is kept for reverse proxies
	Session   string        // cookie value; "" allowed for Health/SetupStatus/Setup/Login only
	CAFile    string        // PEM bundle; replaces the system roots when set
	Insecure  bool          // skip certificate verification; wins over CAFile
	Timeout   time.Duration // unary requests; 0 = 30s
	UserAgent string
}

// Client talks to one Stackstead server.
type Client struct {
	cfg    Config
	base   *url.URL
	prefix string // escaped path prefix without trailing slash
	unary  *http.Client
	stream *http.Client // no overall timeout: streams are long-lived

	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error // test seam
}

// Partial wraps a result of an edit whose port/IP conflict checks ran
// without the container engine.
type Partial[T any] struct {
	Data          T
	ChecksPartial bool
}

// New builds a Client. A CAFile is read now so a bad file fails early.
func New(cfg Config) (*Client, error) {
	if cfg.BaseURL == nil || cfg.BaseURL.Host == "" || (cfg.BaseURL.Scheme != "http" && cfg.BaseURL.Scheme != "https") {
		return nil, errors.New("client: BaseURL must be an http(s) URL with a host")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	tlsCfg, err := TLSConfig(cfg)
	if err != nil {
		return nil, err
	}
	newTransport := func() *http.Transport {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.TLSClientConfig = tlsCfg
		t.ResponseHeaderTimeout = cfg.Timeout
		return t
	}
	host, scheme := cfg.BaseURL.Host, cfg.BaseURL.Scheme
	checkRedirect := func(req *http.Request, _ []*http.Request) error {
		if req.URL.Host != host || req.URL.Scheme != scheme {
			return fmt.Errorf("%w (%s://%s)", errRedirectHost, req.URL.Scheme, req.URL.Host)
		}
		return nil
	}
	return &Client{
		cfg:    cfg,
		base:   cfg.BaseURL,
		prefix: strings.TrimRight(cfg.BaseURL.EscapedPath(), "/"),
		unary:  &http.Client{Transport: newTransport(), Timeout: cfg.Timeout, CheckRedirect: checkRedirect},
		stream: &http.Client{Transport: newTransport(), CheckRedirect: checkRedirect},
		now:    time.Now,
		sleep: func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		},
	}, nil
}

// TLSConfig builds the TLS settings New uses for cfg (Insecure, else CAFile).
// It is exported so the WebSocket dialer verifies certificates identically.
func TLSConfig(cfg Config) (*tls.Config, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	switch {
	case cfg.Insecure:
		tlsCfg.InsecureSkipVerify = true //nolint:gosec // explicit user opt-in (--insecure)
	case cfg.CAFile != "":
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("CA file %s contains no PEM certificates", cfg.CAFile)
		}
		tlsCfg.RootCAs = pool
	}
	return tlsCfg, nil
}

// Config returns the configuration the Client was built with.
func (c *Client) Config() Config { return c.cfg }

// newRequest builds a request for an already-escaped path (query included).
func (c *Client) newRequest(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	target := c.base.Scheme + "://" + c.base.Host + c.prefix + path
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.cfg.Session != "" {
		req.Header.Set("Cookie", sessionCookie+"="+c.cfg.Session)
	}
	if c.cfg.UserAgent != "" {
		req.Header.Set("User-Agent", c.cfg.UserAgent)
	}
	return req, nil
}

func readError(resp *http.Response) *Error {
	e, _ := ReadErrorBody(resp)
	return e
}

// ReadErrorBody turns a non-2xx response into an *Error and also returns the
// body's machine-readable code ("" when the body is not an API error).
func ReadErrorBody(resp *http.Response) (*Error, string) {
	e := &Error{Status: resp.StatusCode}
	var body apitypes.ErrorBody
	code := ""
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	if json.Unmarshal(data, &body) == nil && body.Error != "" {
		e.Message, e.Fields, code = body.Error, body.Fields, body.Code
	} else {
		e.Message = "HTTP " + strconv.Itoa(resp.StatusCode)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		if secs, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && secs >= 0 {
			e.RetryAfter = time.Duration(secs) * time.Second
		}
	}
	return e, code
}

// do sends one unary request. POST and PUT always carry a JSON body ("{}"
// when in is nil, which the server's CSRF rule requires); DELETE and GET
// carry none. out, when non-nil, receives the decoded 2xx body.
func (c *Client) do(ctx context.Context, method, path string, in, out any) (http.Header, error) {
	var body []byte
	if method == http.MethodPost || method == http.MethodPut {
		body = []byte("{}")
		if in != nil {
			var err error
			if body, err = json.Marshal(in); err != nil {
				return nil, err
			}
		}
	}
	req, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.unary.Do(req)
	if err != nil {
		return nil, wrapNetwork(c.base.Host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return resp.Header, readError(resp)
	}
	if out != nil {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(out); err != nil {
			return resp.Header, fmt.Errorf("decode response from %s: %w", c.base.Host, err)
		}
	} else {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
	}
	return resp.Header, nil
}

// authed is do for methods that need a session.
func (c *Client) authed(ctx context.Context, method, path string, in, out any) (http.Header, error) {
	if c.cfg.Session == "" {
		return nil, ErrNoSession
	}
	return c.do(ctx, method, path, in, out)
}

func esc(s string) string { return url.PathEscape(s) }

func (c *Client) Health(ctx context.Context) error {
	var h apitypes.Health
	if _, err := c.do(ctx, http.MethodGet, "/api/health", nil, &h); err != nil {
		return err
	}
	if !h.OK {
		return errors.New("server reported unhealthy")
	}
	return nil
}

func (c *Client) SetupStatus(ctx context.Context) (apitypes.SetupStatus, error) {
	var out apitypes.SetupStatus
	_, err := c.do(ctx, http.MethodGet, "/api/setup/status", nil, &out)
	return out, err
}

func (c *Client) Setup(ctx context.Context, req apitypes.SetupRequest) error {
	_, err := c.do(ctx, http.MethodPost, "/api/setup", req, nil)
	return err
}

// Login returns the session cookie value and its expiry (now + Max-Age).
func (c *Client) Login(ctx context.Context, username, password string) (string, time.Time, error) {
	hdr, err := c.do(ctx, http.MethodPost, "/api/login", apitypes.LoginRequest{Username: username, Password: password}, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	resp := http.Response{Header: hdr}
	for _, ck := range resp.Cookies() {
		if (ck.Name == sessionCookie || ck.Name == hostSessionCookie) && ck.Value != "" {
			expires := ck.Expires
			if ck.MaxAge > 0 {
				expires = c.now().Add(time.Duration(ck.MaxAge) * time.Second)
			}
			return ck.Value, expires, nil
		}
	}
	return "", time.Time{}, &Error{Status: http.StatusNoContent, Message: "login succeeded but no session cookie was set"}
}

func (c *Client) Logout(ctx context.Context) error {
	_, err := c.authed(ctx, http.MethodPost, "/api/logout", nil, nil)
	return err
}

func (c *Client) Me(ctx context.Context) (apitypes.Me, error) {
	var out apitypes.Me
	_, err := c.authed(ctx, http.MethodGet, "/api/me", nil, &out)
	return out, err
}

func (c *Client) Status(ctx context.Context) (apitypes.Status, error) {
	var out apitypes.Status
	_, err := c.authed(ctx, http.MethodGet, "/api/status", nil, &out)
	return out, err
}

func (c *Client) Stacks(ctx context.Context) ([]apitypes.StackSummary, error) {
	var out []apitypes.StackSummary
	_, err := c.authed(ctx, http.MethodGet, "/api/stacks", nil, &out)
	return out, err
}

func (c *Client) Stack(ctx context.Context, name string) (apitypes.StackDetail, error) {
	var out apitypes.StackDetail
	_, err := c.authed(ctx, http.MethodGet, "/api/stacks/"+esc(name), nil, &out)
	return out, err
}

func (c *Client) Import(ctx context.Context, path string) (Partial[apitypes.StackSummary], error) {
	var out Partial[apitypes.StackSummary]
	hdr, err := c.authed(ctx, http.MethodPost, "/api/stacks/import", apitypes.ImportRequest{Path: path}, &out.Data)
	out.ChecksPartial = hdr.Get(checksHeader) == "partial"
	return out, err
}

func (c *Client) Compose(ctx context.Context, name string) (apitypes.RawCompose, error) {
	var out apitypes.RawCompose
	_, err := c.authed(ctx, http.MethodGet, "/api/stacks/"+esc(name)+"/compose", nil, &out)
	return out, err
}

func (c *Client) SaveCompose(ctx context.Context, name, content, hash string) (Partial[apitypes.HashResponse], error) {
	var out Partial[apitypes.HashResponse]
	hdr, err := c.authed(ctx, http.MethodPut, "/api/stacks/"+esc(name)+"/compose", apitypes.RawCompose{Content: content, Hash: hash}, &out.Data)
	out.ChecksPartial = hdr.Get(checksHeader) == "partial"
	return out, err
}

func (c *Client) Act(ctx context.Context, name, action string) (apitypes.Operation, error) {
	var out apitypes.Operation
	_, err := c.authed(ctx, http.MethodPost, "/api/stacks/"+esc(name)+"/actions/"+esc(action), nil, &out)
	return out, err
}

func (c *Client) Remove(ctx context.Context, name string, volumes bool) (apitypes.Operation, error) {
	path := "/api/stacks/" + esc(name)
	if volumes {
		path += "?volumes=true"
	}
	var out apitypes.Operation
	_, err := c.authed(ctx, http.MethodDelete, path, nil, &out)
	return out, err
}

func (c *Client) StackOperations(ctx context.Context, name string) ([]apitypes.Operation, error) {
	var out []apitypes.Operation
	_, err := c.authed(ctx, http.MethodGet, "/api/stacks/"+esc(name)+"/operations", nil, &out)
	return out, err
}

func (c *Client) Operations(ctx context.Context, state string) ([]apitypes.Operation, error) {
	path := "/api/operations"
	if state != "" {
		path += "?state=" + url.QueryEscape(state)
	}
	var out []apitypes.Operation
	_, err := c.authed(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

func (c *Client) Operation(ctx context.Context, id string) (apitypes.OperationDetail, error) {
	var out apitypes.OperationDetail
	_, err := c.authed(ctx, http.MethodGet, "/api/operations/"+esc(id), nil, &out)
	return out, err
}

func (c *Client) Cancel(ctx context.Context, id string) (apitypes.Operation, error) {
	var out apitypes.Operation
	_, err := c.authed(ctx, http.MethodPost, "/api/operations/"+esc(id)+"/cancel", nil, &out)
	return out, err
}

// Containers lists every container on the host, joined with its stack.
func (c *Client) Containers(ctx context.Context) ([]apitypes.ContainerRow, error) {
	var out []apitypes.ContainerRow
	_, err := c.authed(ctx, http.MethodGet, "/api/containers", nil, &out)
	return out, err
}

func (c *Client) Networks(ctx context.Context) ([]apitypes.Network, error) {
	var out []apitypes.Network
	_, err := c.authed(ctx, http.MethodGet, "/api/networks", nil, &out)
	return out, err
}

// FS lists a directory under the server's allowed roots; "" lists the roots.
func (c *Client) FS(ctx context.Context, path string) ([]apitypes.FSEntry, error) {
	p := "/api/fs"
	if path != "" {
		p += "?path=" + url.QueryEscape(path)
	}
	var out []apitypes.FSEntry
	_, err := c.authed(ctx, http.MethodGet, p, nil, &out)
	return out, err
}

// MetricsQuery selects a metrics window; zero values are omitted.
type MetricsQuery struct {
	From, To time.Time
	Step     time.Duration
	Service  string
}

func (c *Client) MetricsHistory(ctx context.Context, name string, q MetricsQuery) (apitypes.MetricsHistory, error) {
	v := url.Values{}
	if !q.From.IsZero() {
		v.Set("from", q.From.UTC().Format(time.RFC3339))
	}
	if !q.To.IsZero() {
		v.Set("to", q.To.UTC().Format(time.RFC3339))
	}
	if q.Step > 0 {
		v.Set("step", strconv.FormatInt(int64((q.Step+time.Second-1)/time.Second), 10))
	}
	if q.Service != "" {
		v.Set("service", q.Service)
	}
	path := "/api/stacks/" + esc(name) + "/metrics"
	if len(v) > 0 {
		path += "?" + v.Encode()
	}
	var out apitypes.MetricsHistory
	_, err := c.authed(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

// HealthHistory fetches the stack's per-service state timeline; a zero span
// lets the server use its default.
func (c *Client) HealthHistory(ctx context.Context, name string, span time.Duration) (apitypes.HealthHistory, error) {
	path := "/api/stacks/" + esc(name) + "/health"
	if span > 0 {
		path += "?" + url.Values{"span": {span.String()}}.Encode()
	}
	var out apitypes.HealthHistory
	_, err := c.authed(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

func (c *Client) MetricsTokenStatus(ctx context.Context) (apitypes.MetricsTokenStatus, error) {
	var out apitypes.MetricsTokenStatus
	_, err := c.authed(ctx, http.MethodGet, "/api/settings/metrics-token", nil, &out)
	return out, err
}

func (c *Client) CreateMetricsToken(ctx context.Context) (apitypes.MetricsToken, error) {
	var out apitypes.MetricsToken
	_, err := c.authed(ctx, http.MethodPost, "/api/settings/metrics-token", nil, &out)
	return out, err
}

func (c *Client) DeleteMetricsToken(ctx context.Context) error {
	_, err := c.authed(ctx, http.MethodDelete, "/api/settings/metrics-token", nil, nil)
	return err
}

func (c *Client) ChangePassword(ctx context.Context, current, next string) error {
	_, err := c.authed(ctx, http.MethodPost, "/api/password", apitypes.ChangePasswordRequest{Current: current, New: next}, nil)
	return err
}

// LogsQuery selects what the log stream returns.
type LogsQuery struct {
	Follow  bool
	Tail    string
	Since   string
	Service string
}

func (c *Client) StreamOperation(ctx context.Context, id string, opts StreamOptions, fn func(Event) error) error {
	return c.Stream(ctx, "/api/operations/"+esc(id)+"/stream", opts, fn)
}

func (c *Client) StreamLogs(ctx context.Context, name string, q LogsQuery, opts StreamOptions, fn func(Event) error) error {
	v := url.Values{}
	if q.Follow {
		v.Set("follow", "true")
	}
	if q.Tail != "" {
		v.Set("tail", q.Tail)
	}
	if q.Since != "" {
		v.Set("since", q.Since)
	}
	if q.Service != "" {
		v.Set("service", q.Service)
	}
	path := "/api/stacks/" + esc(name) + "/logs"
	if len(v) > 0 {
		path += "?" + v.Encode()
	}
	return c.Stream(ctx, path, opts, fn)
}

func (c *Client) StreamMetricsLive(ctx context.Context, name string, opts StreamOptions, fn func(Event) error) error {
	return c.Stream(ctx, "/api/stacks/"+esc(name)+"/metrics/live", opts, fn)
}

func (c *Client) StreamEvents(ctx context.Context, opts StreamOptions, fn func(Event) error) error {
	return c.Stream(ctx, "/api/events", opts, fn)
}

func (c *Client) StorageOverview(ctx context.Context) (apitypes.StorageOverview, error) {
	var out apitypes.StorageOverview
	_, err := c.authed(ctx, http.MethodGet, "/api/storage", nil, &out)
	return out, err
}
func (c *Client) CreateStoragePlan(ctx context.Context, req apitypes.StoragePlanRequest) (apitypes.StoragePlan, error) {
	var out apitypes.StoragePlan
	_, err := c.authed(ctx, http.MethodPost, "/api/storage/plans", req, &out)
	return out, err
}
func (c *Client) ConfirmStoragePlan(ctx context.Context, id, phrase string) (apitypes.Operation, error) {
	var out apitypes.Operation
	_, err := c.authed(ctx, http.MethodPost, "/api/storage/plans/"+esc(id)+"/confirm", apitypes.StorageConfirmRequest{Confirm: phrase}, &out)
	return out, err
}
func (c *Client) RefreshStorage(ctx context.Context) (apitypes.StorageRefresh, error) {
	var out apitypes.StorageRefresh
	_, err := c.authed(ctx, http.MethodPost, "/api/storage/refresh", nil, &out)
	return out, err
}

// ActionsQuery filters persisted actions independently of Docker.
type ActionsQuery struct {
	Kind, Scope string
	Limit       int
}

func (c *Client) RecentActions(ctx context.Context, q ActionsQuery) ([]apitypes.ActionEntry, error) {
	v := url.Values{}
	if q.Kind != "" {
		v.Set("kind", q.Kind)
	}
	if q.Scope != "" {
		v.Set("scope", q.Scope)
	}
	if q.Limit != 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	path := "/api/actions"
	if len(v) > 0 {
		path += "?" + v.Encode()
	}
	var out []apitypes.ActionEntry
	_, err := c.authed(ctx, http.MethodGet, path, nil, &out)
	return out, err
}
