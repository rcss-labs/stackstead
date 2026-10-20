package peers

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

type pinMismatchError struct{}

func (*pinMismatchError) Error() string {
	return "the certificate matches neither the trusted roots nor the pinned fingerprint"
}

// NewHTTPClient builds the client for one peer. It never follows redirects
// and sets no overall Timeout so streams can run; timeout bounds the wait for
// response headers.
func NewHTTPClient(base *url.URL, opts TLSOptions, timeout time.Duration) (*http.Client, error) {
	t := &http.Transport{
		Proxy:                 nil,
		ResponseHeaderTimeout: timeout,
		TLSHandshakeTimeout:   timeout,
		ForceAttemptHTTP2:     true,
	}
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	if base.Scheme == "http" {
		// Check the IP actually dialed, so DNS changing after validation
		// cannot send a plaintext credential to a public address.
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil || !IsPrivate(ap.Addr()) {
				return errors.New("refusing a plain http connection to a non-private address")
			}
			return nil
		}
	} else {
		cfg, err := tlsConfig(opts)
		if err != nil {
			return nil, err
		}
		t.TLSClientConfig = cfg
	}
	t.DialContext = dialer.DialContext
	return &http.Client{
		Transport: t,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return ErrProxyAnswered
		},
	}, nil
}

func tlsConfig(opts TLSOptions) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	var roots *x509.CertPool // nil means system roots
	if opts.CAPEM != "" {
		roots = x509.NewCertPool()
		if !roots.AppendCertsFromPEM([]byte(opts.CAPEM)) {
			return nil, errors.New("the CA certificate is not valid PEM")
		}
		cfg.RootCAs = roots
	}
	pin := normalizeFingerprint(opts.PinSHA256)
	if pin == "" {
		return cfg, nil
	}
	// With a pin we verify ourselves: the pin only applies when normal
	// verification fails.
	cfg.InsecureSkipVerify = true //nolint:gosec // VerifyConnection below does the verification
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return &pinMismatchError{}
		}
		if verifyChain(cs.PeerCertificates, cs.ServerName, roots) == nil {
			return nil
		}
		if fingerprint(cs.PeerCertificates[0]) == pin {
			return nil
		}
		return &pinMismatchError{}
	}
	return cfg, nil
}

func verifyChain(certs []*x509.Certificate, serverName string, roots *x509.CertPool) error {
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	_, err := certs[0].Verify(x509.VerifyOptions{DNSName: serverName, Roots: roots, Intermediates: inter})
	return err
}

func fingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	return hex.EncodeToString(sum[:])
}

func normalizeFingerprint(s string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), ":", ""))
}

// ProbeResult describes what a peer's /api/health and certificate show.
type ProbeResult struct {
	Peering           int
	FingerprintSHA256 string
	SystemTrusted     bool
}

// Probe reads /api/health with verification off, only to capture the leaf
// certificate, then reports whether the system roots verify it. Nothing
// secret is sent.
func Probe(ctx context.Context, base *url.URL) (ProbeResult, error) {
	return probe(ctx, base, nil)
}

func probe(ctx context.Context, base *url.URL, roots *x509.CertPool) (ProbeResult, error) {
	var res ProbeResult
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy:           nil,
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}, //nolint:gosec // probe only reads the certificate
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return ErrProxyAnswered },
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String()+"/api/health", nil)
	if err != nil {
		return res, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return res, classifyTransportError(base.Host, err)
	}
	defer resp.Body.Close()
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		certs := resp.TLS.PeerCertificates
		res.FingerprintSHA256 = fingerprint(certs[0])
		res.SystemTrusted = verifyChain(certs, base.Hostname(), roots) == nil
	}
	if err := Classify(base.Host, resp, nil); err != nil {
		return res, err
	}
	var health struct {
		Peering int `json:"peering"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		return res, fmt.Errorf("%s returned an unreadable health response", base.Host)
	}
	res.Peering = health.Peering
	return res, nil
}
