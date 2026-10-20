package peers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func tlsServer(t *testing.T, h http.Handler) (*httptest.Server, *url.URL, string, string) {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	cert := srv.Certificate()
	sum := sha256.Sum256(cert.Raw)
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}))
	return srv, u, hex.EncodeToString(sum[:]), caPEM
}

func jsonHandler(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
}

func doGet(t *testing.T, u *url.URL, opts TLSOptions) error {
	t.Helper()
	c, err := NewHTTPClient(u, opts, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Get(u.String() + "/api/health")
	if resp != nil {
		defer resp.Body.Close()
	}
	return Classify("srv", resp, err)
}

func TestPinnedCertificate(t *testing.T) {
	_, u, pin, caPEM := tlsServer(t, jsonHandler(`{"ok":true}`))
	upper := strings.ToUpper(pin)
	colons := ""
	for i := 0; i < len(pin); i += 2 {
		colons += upper[i:i+2] + ":"
	}
	colons = strings.TrimSuffix(colons, ":")
	cases := []struct {
		name string
		opts TLSOptions
		code string
	}{
		{"right pin", TLSOptions{PinSHA256: pin}, ""},
		{"pin upper with colons", TLSOptions{PinSHA256: colons}, ""},
		{"wrong pin", TLSOptions{PinSHA256: strings.Repeat("ab", 32)}, "peer_tls"},
		{"ca pem", TLSOptions{CAPEM: caPEM}, ""},
		{"no options", TLSOptions{}, "peer_tls"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := doGet(t, u, c.opts)
			if c.code == "" {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			var pe *PeerError
			if !errors.As(err, &pe) || pe.Code != c.code {
				t.Fatalf("err = %v, want %s", err, c.code)
			}
		})
	}
}

func TestBadCAPEM(t *testing.T) {
	u, _ := url.Parse("https://x")
	if _, err := NewHTTPClient(u, TLSOptions{CAPEM: "junk"}, time.Second); err == nil {
		t.Error("want error for unparsable CA")
	}
}

func TestRedirectIsProxyAnswer(t *testing.T) {
	_, u, pin, _ := tlsServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login", http.StatusFound)
	}))
	err := doGet(t, u, TLSOptions{PinSHA256: pin})
	var pe *PeerError
	if !errors.As(err, &pe) || pe.Code != "peer_proxy" {
		t.Fatalf("err = %v, want peer_proxy", err)
	}
}

func TestPlaintextDialerRefusesPublicIP(t *testing.T) {
	// A hostname-less public literal bypasses ValidateAddress here on purpose:
	// the dialer must refuse independently.
	u, _ := url.Parse("http://203.0.113.7:9")
	c, err := NewHTTPClient(u, TLSOptions{Plaintext: true}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Get(u.String())
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "non-private") {
		t.Fatalf("err = %v, want non-private refusal", err)
	}
}

func TestPlaintextToLoopback(t *testing.T) {
	srv := httptest.NewServer(jsonHandler(`{}`))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	if err := doGet(t, u, TLSOptions{Plaintext: true}); err != nil {
		t.Fatal(err)
	}
}

func TestProbeReportsFingerprint(t *testing.T) {
	_, u, pin, _ := tlsServer(t, jsonHandler(`{"ok":true,"peering":1}`))
	res, err := Probe(t.Context(), u)
	if err != nil {
		t.Fatal(err)
	}
	if res.Peering != 1 || res.FingerprintSHA256 != pin || res.SystemTrusted {
		t.Errorf("got %+v, want peering 1, fp %s, untrusted", res, pin)
	}

	_, u2, _, _ := tlsServer(t, jsonHandler(`{"ok":true}`))
	res, err = Probe(t.Context(), u2)
	if err != nil || res.Peering != 0 {
		t.Errorf("got %+v, %v; want peering 0", res, err)
	}
}
