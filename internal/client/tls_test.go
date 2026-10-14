package client

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
)

func tlsServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(jsonReply(200, apitypes.Health{OK: true}))
	t.Cleanup(srv.Close)
	return srv
}

func writeCert(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeUnrelatedCA writes a fresh self-signed certificate (httptest servers
// all share one built-in certificate, so they cannot stand in for "another CA").
func writeUnrelatedCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "unrelated"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "other.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func tlsClient(t *testing.T, srv *httptest.Server, cfg Config) (*Client, error) {
	t.Helper()
	u, _ := url.Parse(srv.URL)
	cfg.BaseURL = u
	return New(cfg)
}

func TestCAFileVerifies(t *testing.T) {
	srv := tlsServer(t)
	c, err := tlsClient(t, srv, Config{CAFile: writeCert(t, srv)})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Health(context.Background()); err != nil {
		t.Fatalf("Health with matching CA: %v", err)
	}

	c, err = tlsClient(t, srv, Config{CAFile: writeUnrelatedCA(t)})
	if err != nil {
		t.Fatal(err)
	}
	err = c.Health(context.Background())
	var unknown x509.UnknownAuthorityError
	if !errors.As(err, &unknown) {
		t.Fatalf("Health with a different CA: err = %v, want x509.UnknownAuthorityError", err)
	}
}

func TestInsecureSkipsVerify(t *testing.T) {
	srv := tlsServer(t)
	c, err := tlsClient(t, srv, Config{Insecure: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Health(context.Background()); err != nil {
		t.Fatalf("Health with Insecure: %v", err)
	}
}

func TestInsecureWinsOverCAFile(t *testing.T) {
	srv := tlsServer(t)
	c, err := tlsClient(t, srv, Config{Insecure: true, CAFile: writeUnrelatedCA(t)})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Health(context.Background()); err != nil {
		t.Fatalf("Insecure should win: %v", err)
	}
}

func TestSystemRootsByDefault(t *testing.T) {
	srv := tlsServer(t)
	c, err := tlsClient(t, srv, Config{})
	if err != nil {
		t.Fatal(err)
	}
	var unknown x509.UnknownAuthorityError
	if err := c.Health(context.Background()); !errors.As(err, &unknown) {
		t.Fatalf("err = %v, want x509.UnknownAuthorityError", err)
	}
}

func TestBadCAFileFailsInNew(t *testing.T) {
	u, _ := url.Parse("https://example.invalid")
	if _, err := New(Config{BaseURL: u, CAFile: filepath.Join(t.TempDir(), "missing.pem")}); err == nil {
		t.Error("missing CA file accepted")
	}
	empty := filepath.Join(t.TempDir(), "empty.pem")
	_ = os.WriteFile(empty, []byte("not a certificate"), 0o600)
	if _, err := New(Config{BaseURL: u, CAFile: empty}); err == nil {
		t.Error("CA file without certificates accepted")
	}
}

func TestStreamDoesNotRetryCertificateErrors(t *testing.T) {
	srv := tlsServer(t)
	c, err := tlsClient(t, srv, Config{Session: "s"})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	err = c.Stream(context.Background(), "/api/events", StreamOptions{OnReconnect: func(int, error) { calls++ }}, func(Event) error { return nil })
	var unknown x509.UnknownAuthorityError
	if !errors.As(err, &unknown) || calls != 0 {
		t.Fatalf("err = %v, reconnects = %d", err, calls)
	}
}

func TestCAFileReplacesSystemRoots(t *testing.T) {
	srv := tlsServer(t)
	path := writeCert(t, srv)
	c, err := tlsClient(t, srv, Config{CAFile: path})
	if err != nil {
		t.Fatal(err)
	}
	want := x509.NewCertPool()
	pemBytes, _ := os.ReadFile(path)
	want.AppendCertsFromPEM(pemBytes)
	for name, cl := range map[string]*http.Client{"unary": c.unary, "stream": c.stream} {
		cfg := cl.Transport.(*http.Transport).TLSClientConfig
		if cfg.RootCAs == nil || !cfg.RootCAs.Equal(want) {
			t.Errorf("%s RootCAs is not exactly the CA file's pool", name)
		}
	}
}

func TestTLSMinVersion12(t *testing.T) {
	c, err := tlsClient(t, tlsServer(t), Config{})
	if err != nil {
		t.Fatal(err)
	}
	if v := c.unary.Transport.(*http.Transport).TLSClientConfig.MinVersion; v < tls.VersionTLS12 {
		t.Errorf("MinVersion = %#x", v)
	}
	old := httptest.NewUnstartedServer(jsonReply(200, apitypes.Health{OK: true}))
	old.TLS = &tls.Config{MaxVersion: tls.VersionTLS11}
	old.StartTLS()
	t.Cleanup(old.Close)
	c, err = tlsClient(t, old, Config{Insecure: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Health(context.Background()); err == nil {
		t.Error("handshake with a TLS 1.1-only server succeeded")
	}
}
