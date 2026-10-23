package peers

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  string
}

func newTestCA(t *testing.T) testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return testCA{cert, key, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))}
}

func (ca testCA) issue(t *testing.T, dns []string, ips []net.IP) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "leaf"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dns,
		IPAddresses:  ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func serveTLS(t *testing.T, network, addr string, cert tls.Certificate, version uint16) *url.URL {
	t.Helper()
	ln, err := net.Listen(network, addr)
	if err != nil {
		t.Skipf("cannot listen on %s: %v", addr, err)
	}
	srv := httptest.NewUnstartedServer(jsonHandler(`{"ok":true}`))
	srv.Listener.Close()
	srv.Listener = ln
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: version, MaxVersion: version}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	u, _ := url.Parse("https://" + ln.Addr().String())
	return u
}

func TestPinnedPeerVerifiesURLIdentity(t *testing.T) {
	ca := newTestCA(t)
	wrongPin := strings.Repeat("ab", 32)
	hosts := []struct {
		name, network, addr string
		ip                  net.IP
	}{
		{"ipv4", "tcp4", "127.0.0.1:0", net.ParseIP("127.0.0.1")},
		{"ipv6", "tcp6", "[::1]:0", net.ParseIP("::1")},
	}
	versions := map[string]uint16{"tls12": tls.VersionTLS12, "tls13": tls.VersionTLS13}
	for _, h := range hosts {
		for vname, v := range versions {
			t.Run(h.name+"/"+vname, func(t *testing.T) {
				t.Run("trusted cert for another name and wrong pin is rejected", func(t *testing.T) {
					u := serveTLS(t, h.network, h.addr, ca.issue(t, []string{"other.example"}, nil), v)
					if err := doGet(t, u, TLSOptions{CAPEM: ca.pem, PinSHA256: wrongPin}); err == nil {
						t.Fatal("certificate for a different identity was accepted")
					}
				})
				t.Run("trusted cert with the IP SAN is accepted", func(t *testing.T) {
					u := serveTLS(t, h.network, h.addr, ca.issue(t, nil, []net.IP{h.ip}), v)
					if err := doGet(t, u, TLSOptions{CAPEM: ca.pem, PinSHA256: wrongPin}); err != nil {
						t.Fatalf("err = %v", err)
					}
				})
				t.Run("exact pin accepts an untrusted cert", func(t *testing.T) {
					cert := ca.issue(t, []string{"other.example"}, nil)
					u := serveTLS(t, h.network, h.addr, cert, v)
					leaf, _ := x509.ParseCertificate(cert.Certificate[0])
					if err := doGet(t, u, TLSOptions{PinSHA256: fingerprint(leaf)}); err != nil {
						t.Fatalf("err = %v", err)
					}
				})
			})
		}
	}
}
