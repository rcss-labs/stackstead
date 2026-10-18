package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// DefaultBaseURL is the only release source the server ever uses.
const DefaultBaseURL = "https://github.com/rcss-labs/stackstead"

const (
	documentLimit  int64 = 1 << 20
	assetLimit     int64 = 512 << 20
	requestTimeout       = 20 * time.Second
	dialTimeout          = 10 * time.Second
	maxRedirects         = 10
)

var (
	ErrUnavailable = errors.New("release source unavailable")
	ErrNotStable   = errors.New("latest release is not a stable version")
	ErrBadBaseURL  = errors.New("invalid release base URL")
)

// Source resolves releases from one base URL and downloads verified assets.
type Source struct {
	base       string
	client     *http.Client // small documents: bounded by requestTimeout
	dlClient   *http.Client // assets: bounded by ctx and the transport's phase timeouts only
	ua         string
	docLimit   int64
	assetLimit int64
}

// Release is a resolved release: its version and the raw SHA256SUMS document.
type Release struct {
	Version Version
	Sums    []byte
	base    string
}

var redirectHosts = map[string]bool{
	"github.com":                           true,
	"objects.githubusercontent.com":        true,
	"release-assets.githubusercontent.com": true,
}

func isLoopbackHost(h string) bool { return h == "127.0.0.1" || h == "localhost" || h == "::1" }

// NewSource accepts https, or plain http to a loopback host, and no userinfo
// (the check_base_url rule of install.sh).
func NewSource(baseURL, userAgent string) (*Source, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.User != nil || u.Host == "" || u.Hostname() == "" {
		return nil, ErrBadBaseURL
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && isLoopbackHost(u.Hostname()):
	default:
		return nil, ErrBadBaseURL
	}
	s := &Source{
		base:       strings.TrimRight(baseURL, "/"),
		ua:         userAgent,
		docLimit:   documentLimit,
		assetLimit: assetLimit,
	}
	loopbackOrigin := ""
	if u.Scheme == "http" {
		loopbackOrigin = u.Host
	}
	checkRedirect := func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return errors.New("too many release redirects")
		}
		if req.URL.User != nil {
			return errors.New("unsafe release redirect")
		}
		sameLoopback := loopbackOrigin != "" && req.URL.Scheme == "http" && req.URL.Host == loopbackOrigin
		trusted := req.URL.Scheme == "https" && req.URL.Port() == "" && redirectHosts[req.URL.Hostname()]
		if !sameLoopback && !trusted {
			return errors.New("untrusted release redirect")
		}
		req.Header.Del("Authorization")
		req.Header.Del("Proxy-Authorization")
		req.Header.Del("Cookie")
		return nil
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}).DialContext
	transport.TLSHandshakeTimeout = dialTimeout
	transport.ResponseHeaderTimeout = requestTimeout
	s.client = &http.Client{Transport: transport, Timeout: requestTimeout, CheckRedirect: checkRedirect}
	s.dlClient = &http.Client{Transport: transport, CheckRedirect: checkRedirect}
	return s, nil
}

func (s *Source) IsDefault() bool { return s.base == DefaultBaseURL }

var tarballLine = regexp.MustCompile(`^stackstead_(.+)_(?:linux|darwin)_[a-z0-9]+\.tar\.gz$`)

// Latest resolves the newest stable release from the latest SHA256SUMS.
func (s *Source) Latest(ctx context.Context) (Release, error) {
	sums, err := s.fetchDocument(ctx, s.base+"/releases/latest/download/SHA256SUMS")
	if err != nil {
		return Release{}, err
	}
	if _, err := ChecksumFor(sums, ""); !errors.Is(err, ErrAssetMissing) {
		return Release{}, ErrMalformed
	}
	for _, l := range strings.Split(string(sums), "\n") {
		if len(l) < 67 {
			continue
		}
		m := tarballLine.FindStringSubmatch(l[66:])
		if m == nil {
			continue
		}
		v, err := Parse(m[1])
		if err != nil {
			return Release{}, ErrMalformed
		}
		if !v.IsStable() {
			return Release{}, ErrNotStable
		}
		return Release{Version: v, Sums: sums, base: s.base}, nil
	}
	return Release{}, ErrMalformed
}

// Release fetches the SHA256SUMS of one pinned version.
func (s *Source) Release(ctx context.Context, version string) (Release, error) {
	v, err := Parse(version)
	if err != nil {
		return Release{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	sums, err := s.fetchDocument(ctx, s.base+"/releases/download/v"+v.String()+"/SHA256SUMS")
	if err != nil {
		return Release{}, err
	}
	if _, err := ChecksumFor(sums, ""); !errors.Is(err, ErrAssetMissing) {
		return Release{}, ErrMalformed
	}
	return Release{Version: v, Sums: sums, base: s.base}, nil
}

// NotesURL is the release page on the default source, empty for any mirror.
func (r Release) NotesURL() string {
	if r.base != DefaultBaseURL {
		return ""
	}
	return DefaultBaseURL + "/releases/tag/v" + r.Version.String()
}

// AssetName is the release asset an installer kind consumes. These names are a
// contract with the release build (see deploy/release/RELEASING.md).
func (r Release) AssetName(kind Kind, goos, goarch string) (string, error) {
	ver := r.Version.String()
	switch kind {
	case KindScript:
		return "install.sh", nil
	case KindDeb:
		if goarch != "amd64" && goarch != "arm64" {
			return "", fmt.Errorf("no deb for architecture %q", goarch)
		}
		return "stackstead_" + ver + "_" + goarch + ".deb", nil
	case KindRPM:
		switch goarch {
		case "amd64":
			return "stackstead-" + ver + "-1.x86_64.rpm", nil
		case "arm64":
			return "stackstead-" + ver + "-1.aarch64.rpm", nil
		}
		return "", fmt.Errorf("no rpm for architecture %q", goarch)
	case KindPkg:
		return "stackstead_" + ver + "_darwin.pkg", nil
	}
	return "", fmt.Errorf("unknown installer kind %q", kind)
}

func (s *Source) get(ctx context.Context, client *http.Client, rawURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", s.ua)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%w: HTTP %d", ErrUnavailable, resp.StatusCode)
	}
	return resp, nil
}

func (s *Source) fetchDocument(ctx context.Context, rawURL string) ([]byte, error) {
	resp, err := s.get(ctx, s.client, rawURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, s.docLimit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > s.docLimit {
		return nil, ErrMalformed
	}
	return data, nil
}

// Download streams a listed asset into dir, hashing as it goes. The file is
// created exclusively and removed on any failure, so dir never holds an
// unverified asset. The caller removes dir itself.
func (s *Source) Download(ctx context.Context, r Release, name, dir string) (string, error) {
	want, err := ChecksumFor(r.Sums, name)
	if err != nil {
		return "", err
	}
	resp, err := s.get(ctx, s.dlClient, r.base+"/releases/download/v"+r.Version.String()+"/"+name)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	fail := func(err error) (string, error) {
		f.Close()
		os.Remove(path)
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, s.assetLimit+1))
	if err != nil {
		return fail(err)
	}
	if n > s.assetLimit {
		return fail(ErrMalformed)
	}
	if hex.EncodeToString(h.Sum(nil)) != want {
		return fail(ErrChecksum)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

// ErrorCode maps an error to the code that crosses the API:
// network, timeout, http or malformed ("" for nil or anything else).
func ErrorCode(err error) string {
	var ne net.Error
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrMalformed), errors.Is(err, ErrNotStable), errors.Is(err, ErrAssetMissing), errors.Is(err, ErrChecksum):
		return "malformed"
	case errors.Is(err, ErrUnavailable):
		return "http"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return "timeout"
	}
	return "network"
}
