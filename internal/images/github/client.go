// Package github is a minimal GitHub REST client for image sources and the
// safe extraction of the repository tarball it downloads.
package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/rcss-labs/stackstead/internal/secret"
)

const (
	defaultBase = "https://api.github.com"
	maxFileSize = 1 << 20 // 1 MiB
	maxJSONBody = 4 << 20 // base64 of 1 MiB plus envelope
)

var (
	ErrUnauthorized = errors.New("github: token rejected or lacks access")
	ErrNotFound     = errors.New("github: not found")
	ErrTooLarge     = errors.New("github: file is larger than 1 MiB")
	ErrRateLimited  = errors.New("github: rate limit reached")
	errStalled      = errors.New("github: download stalled")
)

// apiTimeout bounds each User, Head and File call.
const apiTimeout = 30 * time.Second

// idleTimeout is how long a tarball download may go without receiving bytes.
var idleTimeout = 2 * time.Minute

var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// ValidateRepo checks an "owner/name" repository.
func ValidateRepo(repo string) error {
	owner, name, _ := strings.Cut(repo, "/")
	if !repoPattern.MatchString(repo) || owner == "." || owner == ".." || name == "." || name == ".." {
		return errors.New("github: repository must look like owner/name")
	}
	return nil
}

// ValidateBranch checks a branch, tag or commit reference.
func ValidateBranch(branch string) error {
	switch {
	case branch == "":
		return errors.New("github: branch is empty")
	case len(branch) > 255:
		return errors.New("github: branch is longer than 255 bytes")
	case strings.HasPrefix(branch, "-"):
		return errors.New("github: branch must not start with -")
	case strings.Contains(branch, ".."):
		return errors.New("github: branch must not contain ..")
	case strings.ContainsAny(branch, "\x00\\ ?#"):
		return errors.New("github: branch contains an invalid character")
	}
	return nil
}

// Client talks to the GitHub REST API. The zero value uses api.github.com.
type Client struct {
	Base string
	HTTP *http.Client
}

func (c *Client) base() string {
	if c.Base == "" {
		return defaultBase
	}
	return strings.TrimRight(c.Base, "/")
}

// redirectPolicy allows at most one redirect. The token survives only a
// redirect to the same scheme and host; Tarball may follow one redirect to a
// download host (without the token), other calls refuse it.
func redirectPolicy(tarball bool) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) > 1 {
			return errors.New("github: too many redirects")
		}
		if req.URL.Scheme != via[0].URL.Scheme || req.URL.Host != via[0].URL.Host {
			if !tarball {
				return errors.New("github: refusing a redirect to another host")
			}
			req.Header.Del("Authorization")
		}
		return nil
	}
}

// httpClient copies the configured client so the redirect policy is always
// ours. A tarball download has no overall timeout (the idle watchdog guards it).
func (c *Client) httpClient(tarball bool) *http.Client {
	var hc http.Client
	if c.HTTP != nil {
		hc = *c.HTTP
	}
	if tarball {
		hc.Timeout = 0
	}
	hc.CheckRedirect = redirectPolicy(tarball)
	return &hc
}

func (c *Client) get(ctx context.Context, token secret.Secret, p string, tarball bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base()+p, nil)
	if err != nil {
		return nil, errors.New("github: bad request")
	}
	req.Header.Set("Authorization", "Bearer "+token.Reveal())
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "stackstead")
	resp, err := c.httpClient(tarball).Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// url.Error would echo the URL; keep only the cause.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("github: request failed: %w", err)
	}
	switch {
	case resp.StatusCode == 429 || (resp.StatusCode == 403 && resp.Header.Get("X-RateLimit-Remaining") == "0"):
		resp.Body.Close()
		return nil, ErrRateLimited
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		resp.Body.Close()
		return nil, ErrUnauthorized
	case resp.StatusCode == 404:
		resp.Body.Close()
		return nil, ErrNotFound
	case resp.StatusCode != 200:
		resp.Body.Close()
		// Never include the body: it can echo credentials.
		return nil, fmt.Errorf("github: unexpected status %d", resp.StatusCode)
	}
	return resp, nil
}

func (c *Client) getJSON(ctx context.Context, token secret.Secret, p string, out any) error {
	resp, err := c.get(ctx, token, p, false)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJSONBody)).Decode(out); err != nil {
		return errors.New("github: unexpected response")
	}
	return nil
}

func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// User returns the login the token belongs to.
func (c *Client) User(ctx context.Context, token secret.Secret) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	var u struct {
		Login string `json:"login"`
	}
	if err := c.getJSON(ctx, token, "/user", &u); err != nil {
		return "", err
	}
	if u.Login == "" {
		return "", errors.New("github: unexpected response")
	}
	return u.Login, nil
}

// Head returns the commit SHA at the tip of branch.
func (c *Client) Head(ctx context.Context, token secret.Secret, repo, branch string) (string, error) {
	if err := validateTarget(repo, branch); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	var r struct {
		SHA string `json:"sha"`
	}
	if err := c.getJSON(ctx, token, "/repos/"+repo+"/commits/"+escapePath(branch), &r); err != nil {
		return "", err
	}
	if !shaPattern.MatchString(r.SHA) {
		return "", errors.New("github: unexpected response")
	}
	return r.SHA, nil
}

// File returns the content of one file (at most 1 MiB) at ref.
func (c *Client) File(ctx context.Context, token secret.Secret, repo, ref, path string) ([]byte, error) {
	if err := validateTarget(repo, ref); err != nil {
		return nil, err
	}
	path, err := validatePath(path)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	resp, err := c.get(ctx, token, "/repos/"+repo+"/contents/"+escapePath(path)+"?ref="+url.QueryEscape(ref), false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var f struct {
		Type     string `json:"type"`
		Encoding string `json:"encoding"`
		Size     int64  `json:"size"`
		Content  string `json:"content"`
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONBody))
	if err != nil {
		return nil, errors.New("github: unexpected response")
	}
	if err := json.Unmarshal(raw, &f); err != nil || f.Type != "file" {
		// A directory answers with a JSON array; treat it as "no such file".
		return nil, ErrNotFound
	}
	if f.Size > maxFileSize {
		return nil, ErrTooLarge
	}
	if f.Encoding != "base64" {
		return nil, errors.New("github: unexpected response")
	}
	b, err := base64.StdEncoding.DecodeString(strings.NewReplacer("\n", "", "\r", "").Replace(f.Content))
	if err != nil {
		return nil, errors.New("github: unexpected response")
	}
	if len(b) > maxFileSize {
		return nil, ErrTooLarge
	}
	return b, nil
}

// Tarball streams the gzip tarball of ref. GitHub redirects once to a
// download host; the token is not sent there. The caller closes the result.
func (c *Client) Tarball(ctx context.Context, token secret.Secret, repo, ref string) (io.ReadCloser, error) {
	if err := validateTarget(repo, ref); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	var stalled sync.Once
	var stalledFlag bool
	var mu sync.Mutex
	timer := time.AfterFunc(idleTimeout, func() {
		stalled.Do(func() { mu.Lock(); stalledFlag = true; mu.Unlock() })
		cancel()
	})
	resp, err := c.get(ctx, token, "/repos/"+repo+"/tarball/"+escapePath(ref), true)
	if err != nil {
		timer.Stop()
		cancel()
		mu.Lock()
		defer mu.Unlock()
		if stalledFlag {
			return nil, errStalled
		}
		return nil, err
	}
	timer.Reset(idleTimeout)
	return &idleBody{rc: resp.Body, timer: timer, cancel: cancel, stalled: func() bool { mu.Lock(); defer mu.Unlock(); return stalledFlag }}, nil
}

// idleBody resets a watchdog on every read; if no bytes arrive before it
// fires, the request is cancelled and the read fails.
type idleBody struct {
	rc      io.ReadCloser
	timer   *time.Timer
	cancel  context.CancelFunc
	stalled func() bool
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if n > 0 {
		b.timer.Reset(idleTimeout)
	}
	if err != nil && err != io.EOF && b.stalled() {
		err = errStalled
	}
	return n, err
}

func (b *idleBody) Close() error {
	b.timer.Stop()
	err := b.rc.Close()
	b.cancel()
	return err
}

func validateTarget(repo, ref string) error {
	if err := ValidateRepo(repo); err != nil {
		return err
	}
	return ValidateBranch(ref)
}
