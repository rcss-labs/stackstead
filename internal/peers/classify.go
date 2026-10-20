package peers

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
)

// Peer error codes surfaced to the UI and API.
const (
	CodeUnreachable      = "peer_unreachable"
	CodeTLS              = "peer_tls"
	CodeUnpaired         = "peer_unpaired"
	CodeProxy            = "peer_proxy"
	CodeVersion          = "peer_version"
	CodeWrongInstance    = "peer_wrong_instance"
	CodeUnknown          = "peer_unknown"
	codeInboundNotPaired = "peer_not_paired"
)

var (
	// ErrNotPaired is the peer's own "I do not know you" answer.
	ErrNotPaired = errors.New("peer does not recognise this server")
	// ErrProxyAnswered means something between us and the peer (a redirect,
	// an HTML login page) answered instead of Stackstead.
	ErrProxyAnswered = errors.New("a reverse proxy answered instead of the peer")
)

// PeerError is a classified failure talking to a peer. Msg names the server
// and never contains a URL query or credential.
type PeerError struct {
	Code   string
	Server string
	Msg    string
	cause  error
}

func (e *PeerError) Error() string { return e.Msg }
func (e *PeerError) Unwrap() error { return e.cause }

func newPeerError(code, server, format string, cause error) *PeerError {
	return &PeerError{Code: code, Server: server, Msg: fmt.Sprintf(format, server), cause: cause}
}

// Classify turns the outcome of a peer call into a PeerError, or nil when the
// peer answered with JSON. It only reads the body of a 401, to look for the
// peer_not_paired code; callers still own and close resp.Body.
func Classify(server string, resp *http.Response, err error) error {
	if err != nil {
		return classifyTransportError(server, err)
	}
	if resp == nil {
		return newPeerError(CodeUnreachable, server, "%s did not answer", nil)
	}
	if resp.StatusCode == http.StatusUnauthorized && isJSON(resp) {
		var body struct {
			Code string `json:"code"`
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if json.Unmarshal(data, &body) == nil && body.Code == codeInboundNotPaired {
			return newPeerError(CodeUnpaired, server, "%s no longer recognises this server; pair it again", ErrNotPaired)
		}
		return proxyError(server)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden ||
		(resp.StatusCode >= 300 && resp.StatusCode < 400) || !isJSON(resp) {
		return proxyError(server)
	}
	return nil
}

func proxyError(server string) error {
	return newPeerError(CodeProxy, server,
		"something in front of %s (a reverse proxy or login page) answered instead of Stackstead", ErrProxyAnswered)
}

func isJSON(resp *http.Response) bool {
	mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	return err == nil && (mt == "application/json" || (len(mt) > 5 && mt[len(mt)-5:] == "+json"))
}

// classifyTransportError never wraps err into the message: Go's url.Error
// includes the full request URL, which may carry secrets.
func classifyTransportError(server string, err error) error {
	var (
		unknownAuth x509.UnknownAuthorityError
		certInvalid x509.CertificateInvalidError
		hostErr     x509.HostnameError
		verifyErr   *tls.CertificateVerificationError
		alert       tls.AlertError
		recHdr      tls.RecordHeaderError
		pin         *pinMismatchError
	)
	switch {
	case errors.Is(err, ErrProxyAnswered):
		return proxyError(server)
	case errors.As(err, &pin), errors.As(err, &unknownAuth), errors.As(err, &certInvalid),
		errors.As(err, &hostErr), errors.As(err, &verifyErr), errors.As(err, &alert), errors.As(err, &recHdr):
		return newPeerError(CodeTLS, server, "the TLS certificate of %s could not be verified", errTLS)
	}
	return newPeerError(CodeUnreachable, server, "%s could not be reached", errUnreachable)
}

var (
	errTLS         = errors.New("tls verification failed")
	errUnreachable = errors.New("peer unreachable")
)
