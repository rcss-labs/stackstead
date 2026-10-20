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
	"strings"

	"github.com/rcss-labs/stackstead/internal/apitypes"
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
	CodeOld              = "peer_old"
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

// RemoteError is a Stackstead error answer from the peer (for example a 409
// or field errors) that callers can propagate as is.
type RemoteError struct {
	Status int
	Server string
	Body   apitypes.ErrorBody
}

func (e *RemoteError) Error() string { return e.Server + ": " + e.Body.Error }

// Classify turns the outcome of a peer call into an error, or nil when the
// peer answered normally. Contract:
//   - err != nil: redirect refusal -> peer_proxy, TLS failures -> peer_tls,
//     anything else -> peer_unreachable.
//   - 204, 2xx application/json, 2xx text/event-stream -> nil; any other 2xx
//     content type (an HTML page) -> peer_proxy.
//   - 401 JSON with code "peer_not_paired" -> peer_unpaired (ErrNotPaired).
//   - 3xx and every other 401/403 -> peer_proxy.
//   - other non-2xx with a Stackstead JSON error body -> *RemoteError.
//   - other non-2xx, non-JSON or without "error" -> peer_proxy.
//
// It reads (a bounded part of) the body only for non-2xx answers; callers
// still own and close resp.Body.
func Classify(server string, resp *http.Response, err error) error {
	if err != nil {
		return classifyTransportError(server, err)
	}
	if resp == nil {
		return newPeerError(CodeUnreachable, server, "%s did not answer", nil)
	}
	status := resp.StatusCode
	switch {
	case status == http.StatusNoContent:
		return nil
	case status >= 200 && status < 300:
		if isJSON(resp) || mediaType(resp) == "text/event-stream" {
			return nil
		}
		return proxyError(server)
	case status >= 300 && status < 400, status == http.StatusForbidden:
		return proxyError(server)
	case !isJSON(resp):
		return proxyError(server)
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if status == http.StatusUnauthorized {
		var body struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(data, &body) == nil && body.Code == codeInboundNotPaired {
			return newPeerError(CodeUnpaired, server, "%s no longer recognises this server; pair it again", ErrNotPaired)
		}
		return proxyError(server)
	}
	var body apitypes.ErrorBody
	if json.Unmarshal(data, &body) != nil || body.Error == "" {
		return proxyError(server)
	}
	return &RemoteError{Status: status, Server: server, Body: body}
}

func proxyError(server string) error {
	return newPeerError(CodeProxy, server,
		"something in front of %s (a reverse proxy or login page) answered instead of Stackstead", ErrProxyAnswered)
}

func mediaType(resp *http.Response) string {
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	return mt
}

func isJSON(resp *http.Response) bool {
	mt := mediaType(resp)
	return mt == "application/json" || strings.HasSuffix(mt, "+json")
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
