package engine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rcss-labs/stackstead/internal/secret"
)

// BuildOptions selects how an image is built from a tar build context.
type BuildOptions struct {
	Tags       []string
	Dockerfile string // path inside the context; "" = Docker's default
	BuildArgs  map[string]string
	Labels     map[string]string
}

// BuildResult describes a finished build.
type BuildResult struct{ ImageID string }

// BuildError is a build failure reported by Docker inside the stream.
type BuildError struct{ Message string }

func (e *BuildError) Error() string { return "build failed: " + e.Message }

// RegistryAuth is the credential Docker presents to the registry on a push.
// It never appears in errors or logs.
type RegistryAuth struct {
	Username      string
	Password      secret.Secret
	ServerAddress string
}

// header encodes the credential as the X-Registry-Auth value.
func (a RegistryAuth) header() string {
	raw, _ := json.Marshal(struct {
		Username      string `json:"username"`
		Password      string `json:"password"`
		ServerAddress string `json:"serveraddress"`
	}{a.Username, a.Password.Reveal(), a.ServerAddress})
	return base64.URLEncoding.EncodeToString(raw)
}

// loadHeaderWait is how long Docker may take to answer an image load, which
// answers only after the whole archive is imported; ctx still bounds it.
const loadHeaderWait = 24 * time.Hour

// escapedRef escapes a reference for a URL path, keeping its slashes.
func escapedRef(ref string) string {
	return (&url.URL{Path: ref}).EscapedPath()
}

// lineSplitter turns stream chunks into whole lines, holding a trailing partial line.
type lineSplitter struct {
	partial string
	emit    func(string)
}

func (s *lineSplitter) write(text string) {
	if s.emit == nil || text == "" {
		return
	}
	text = s.partial + text
	for {
		i := strings.IndexByte(text, '\n')
		if i < 0 {
			break
		}
		s.emit(text[:i])
		text = text[i+1:]
	}
	s.partial = text
}

func (s *lineSplitter) flush() {
	if s.emit != nil && s.partial != "" {
		s.emit(s.partial)
	}
	s.partial = ""
}

// streamResult maps a failed stream read: Docker-side errors pass through, the
// rest keeps the caller's cancellation distinct from an unreachable engine.
func (c *Client) streamResult(ctx context.Context, err error) error {
	var syntax *json.SyntaxError
	if errors.Is(err, errJSONLineTooLong) || errors.As(err, &syntax) || strings.HasPrefix(err.Error(), "decode docker message") {
		return err
	}
	return c.classify(ctx, err)
}

// Build sends contextTar to Docker and builds it, passing output lines to out
// (which may be nil). A Docker-reported failure is a *BuildError.
func (c *Client) Build(ctx context.Context, contextTar io.Reader, opts BuildOptions, out func(line string)) (BuildResult, error) {
	q := url.Values{"rm": {"1"}, "forcerm": {"1"}}
	for _, t := range opts.Tags {
		q.Add("t", t)
	}
	if opts.Dockerfile != "" {
		q.Set("dockerfile", opts.Dockerfile)
	}
	for key, m := range map[string]map[string]string{"buildargs": opts.BuildArgs, "labels": opts.Labels} {
		if len(m) == 0 {
			continue
		}
		enc, err := json.Marshal(m)
		if err != nil {
			return BuildResult{}, fmt.Errorf("encode build %s: %w", key, err)
		}
		q.Set(key, string(enc))
	}
	resp, cancel, err := c.postStream(ctx, "/build?"+q.Encode(), contextTar, "application/x-tar", nil)
	if err != nil {
		return BuildResult{}, storageResult(err, ErrImageNotFound)
	}
	defer cancel()
	defer resp.Body.Close()
	var res BuildResult
	lines := lineSplitter{emit: out}
	err = decodeJSONStream(resp.Body, func(m jsonMessage) error {
		lines.write(m.Stream)
		if msg := m.errorText(); msg != "" {
			lines.flush()
			return &BuildError{Message: msg}
		}
		if len(m.Aux) > 0 {
			var aux struct{ ID string }
			if json.Unmarshal(m.Aux, &aux) == nil && aux.ID != "" {
				res.ImageID = aux.ID
			}
		}
		return nil
	})
	lines.flush()
	if err != nil {
		var be *BuildError
		if errors.As(err, &be) {
			return BuildResult{}, err
		}
		return BuildResult{}, c.streamResult(ctx, err)
	}
	if res.ImageID == "" {
		return BuildResult{}, errors.New("docker engine: build finished without an image ID")
	}
	return res, nil
}

// splitRef separates "repo:tag"; the tag is "latest" when ref has none.
func splitRef(ref string) (repo, tag string) {
	if i := strings.LastIndexByte(ref, ':'); i > strings.LastIndexByte(ref, '/') {
		return ref[:i], ref[i+1:]
	}
	return ref, "latest"
}

// Push uploads ref ("repo:tag") to its registry and returns the pushed digest.
// Progress goes to out (which may be nil).
func (c *Client) Push(ctx context.Context, ref string, auth RegistryAuth, out func(line string)) (digest string, err error) {
	repo, tag := splitRef(ref)
	headers := http.Header{"X-Registry-Auth": {auth.header()}}
	resp, cancel, err := c.postStream(ctx, "/images/"+escapedRef(repo)+"/push?"+url.Values{"tag": {tag}}.Encode(), nil, "", headers)
	if err != nil {
		return "", storageResult(err, ErrImageNotFound)
	}
	defer cancel()
	defer resp.Body.Close()
	err = decodeJSONStream(resp.Body, func(m jsonMessage) error {
		if msg := m.errorText(); msg != "" {
			return fmt.Errorf("push failed: %s", msg)
		}
		if len(m.Aux) > 0 {
			var aux struct{ Digest string }
			if json.Unmarshal(m.Aux, &aux) == nil && aux.Digest != "" {
				digest = aux.Digest
			}
		}
		if m.Status != "" && out != nil {
			line := m.Status
			if m.Progress != "" {
				line += " " + m.Progress
			}
			if m.ID != "" {
				line = m.ID + ": " + line
			}
			out(line)
		}
		return nil
	})
	if err != nil {
		if strings.HasPrefix(err.Error(), "push failed: ") {
			return "", err
		}
		return "", c.streamResult(ctx, err)
	}
	if digest == "" {
		return "", errors.New("push failed: the stream ended without a digest")
	}
	return digest, nil
}

// TagImage adds repo:tag to the image source.
func (c *Client) TagImage(ctx context.Context, source, repo, tag string) error {
	resp, cancel, err := c.postStream(ctx, "/images/"+escapedRef(source)+"/tag?"+url.Values{"repo": {repo}, "tag": {tag}}.Encode(), nil, "", nil)
	if err != nil {
		return storageResult(err, ErrImageNotFound)
	}
	defer cancel()
	resp.Body.Close()
	return nil
}

// cancelOnClose ends the request context when the body is closed.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b cancelOnClose) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

// SaveImage streams ref as an image tarball. The body is bound by ctx only;
// the caller must close it.
func (c *Client) SaveImage(ctx context.Context, ref string) (io.ReadCloser, error) {
	resp, cancel, err := c.openRequest(ctx, http.MethodGet, "/images/"+escapedRef(ref)+"/get", nil, "", nil, c.timeout, true)
	if err != nil {
		return nil, storageResult(err, ErrImageNotFound)
	}
	return cancelOnClose{resp.Body, cancel}, nil
}

// LoadImage imports an image tarball such as SaveImage produces.
func (c *Client) LoadImage(ctx context.Context, tar io.Reader) error {
	resp, cancel, err := c.openRequest(ctx, http.MethodPost, "/images/load?quiet=1", tar, "application/x-tar", nil, loadHeaderWait, true)
	if err != nil {
		return err
	}
	defer cancel()
	defer resp.Body.Close()
	err = decodeJSONStream(resp.Body, func(m jsonMessage) error {
		if msg := m.errorText(); msg != "" {
			return fmt.Errorf("load failed: %s", msg)
		}
		return nil
	})
	if err != nil {
		if strings.HasPrefix(err.Error(), "load failed: ") {
			return err
		}
		return c.streamResult(ctx, err)
	}
	return nil
}

// Arch returns the engine's architecture as /version reports it (amd64, arm64, ...).
func (c *Client) Arch(ctx context.Context) (string, error) {
	body, err := c.get(ctx, "/version")
	if err != nil {
		return "", err
	}
	var v struct{ Arch string }
	if err := json.Unmarshal(body, &v); err != nil {
		return "", fmt.Errorf("decode version: %w", err)
	}
	if v.Arch == "" {
		return "", errors.New("docker engine: version has no architecture")
	}
	return v.Arch, nil
}
