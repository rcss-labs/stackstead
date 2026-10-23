package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/rcss-labs/stackstead/internal/peers"
)

// registerForwardRoutes adds the session-only route that carries the web
// app's calls to a connected server. The web app is same-origin with a
// SameSite=Strict cookie, so the local server must make every remote call.
func (s *server) registerForwardRoutes(mux *http.ServeMux) {
	mux.Handle("/api/servers/{id}/api/{rest...}", s.requireSession(s.handleForward))
}

// forwardHeaderTimeout bounds the wait for a forwarded call's response
// headers; stack actions on a busy host can take longer than the 30 s that
// suits hello and fleet calls.
const forwardHeaderTimeout = 60 * time.Second

// forwardTransports keeps one transport per peer so connections are reused.
// An entry is replaced (and its idle connections closed) when the peer's
// address or trust settings change.
type forwardTransports struct {
	mu      sync.Mutex
	entries map[string]forwardEntry
}

type forwardEntry struct {
	key    string
	client *http.Client
}

func (t *forwardTransports) get(p peers.Peer, base *url.URL) (*http.Client, error) {
	key := strings.Join([]string{p.URL, p.TLS.CAPEM, p.TLS.PinSHA256, boolString(p.TLS.Plaintext)}, "\x00")
	t.mu.Lock()
	defer t.mu.Unlock()
	if e, ok := t.entries[p.InstanceID]; ok && e.key == key {
		return e.client, nil
	}
	client, err := peers.NewHTTPClient(base, p.TLS, forwardHeaderTimeout)
	if err != nil {
		return nil, err
	}
	if old, ok := t.entries[p.InstanceID]; ok {
		old.client.CloseIdleConnections()
	}
	if t.entries == nil {
		t.entries = map[string]forwardEntry{}
	}
	t.entries[p.InstanceID] = forwardEntry{key: key, client: client}
	return client, nil
}

func boolString(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// deadlineWriter gives every write its own deadline, so a stalled browser
// cuts a forwarded stream instead of holding it open.
type deadlineWriter struct {
	http.ResponseWriter
	rc      *http.ResponseController
	timeout time.Duration
}

func (d *deadlineWriter) arm() {
	// http.ErrNotSupported (recorders, wrapped writers) just means no deadline.
	_ = d.rc.SetWriteDeadline(time.Now().Add(d.timeout))
}

func (d *deadlineWriter) WriteHeader(status int) {
	d.arm()
	d.ResponseWriter.WriteHeader(status)
}

func (d *deadlineWriter) Write(b []byte) (int, error) {
	d.arm()
	return d.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach Flush on the real writer.
func (d *deadlineWriter) Unwrap() http.ResponseWriter { return d.ResponseWriter }

// forwardTail returns the escaped part of the request path after
// /api/servers/{id}/api/ and its decoded form. Anything that could climb out
// of the peer's /api/ prefix once a path-normalizing proxy sits in front of it
// is refused: empty, "." and ".." segments, encoded slashes and backslashes.
// So is the peer protocol itself (/api/peer/...).
func forwardTail(r *http.Request) (escaped, decoded string, ok bool) {
	p := strings.TrimPrefix(r.URL.EscapedPath(), "/api/servers/")
	_, escaped, found := strings.Cut(p, "/api/")
	if !found || escaped == "" {
		return "", "", false
	}
	if first, _, _ := strings.Cut(escaped, "/"); first == "peer" {
		// /api/peer/* is the pairing protocol between servers, not something to relay.
		return "", "", false
	}
	for _, seg := range strings.Split(escaped, "/") {
		d, err := url.PathUnescape(seg)
		if err != nil || d == "" || d == "." || d == ".." || strings.ContainsAny(d, `/\`) {
			return "", "", false
		}
	}
	decoded, err := url.PathUnescape(escaped)
	return escaped, decoded, err == nil
}

// hostileResponseHeaders are upstream headers that must not act on the
// browser's view of this origin.
var hostileResponseHeaders = []string{
	"Set-Cookie", "Clear-Site-Data", "Strict-Transport-Security",
	peers.PeerHeader, peerUserHeader,
}

func (s *server) handleForward(w http.ResponseWriter, r *http.Request) {
	tailEscaped, tailDecoded, ok := forwardTail(r)
	if !ok {
		writeCodedError(w, http.StatusBadRequest, "invalid path", "peer_bad_path")
		return
	}
	peer, err := s.opts.Peers.Usable(r.PathValue("id"))
	if err != nil {
		s.writePeersError(w, r, err)
		return
	}
	base, err := url.Parse(peer.URL)
	if err != nil {
		s.writePeersError(w, r, &peers.PeerError{Code: peers.CodeUnreachable, Server: peer.Name, Msg: peer.Name + " could not be reached"})
		return
	}
	client, err := s.forwards.get(peer, base)
	if err != nil {
		s.opts.Logger.Warn("could not prepare a connection to a server", "peer", peer.Name)
		s.writePeersError(w, r, &peers.PeerError{Code: peers.CodeUnreachable, Server: peer.Name, Msg: peer.Name + " could not be reached"})
		return
	}

	// Cancel the outbound call when the server starts shutting down, so an
	// open stream cannot hold srv.Shutdown past its grace period.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		select {
		case <-s.opts.Closing:
			cancel()
		case <-ctx.Done():
		}
	}()
	r = r.WithContext(ctx)

	upgrade := isWebSocketUpgrade(r)
	if upgrade && !originAllowed(r) {
		// Same check the exec route makes, done here so a foreign page never
		// causes a call to the peer.
		writeCodedError(w, http.StatusForbidden, "cross-origin requests are not allowed", "bad_origin")
		return
	}

	username := userFrom(r.Context()).Username
	credential := peer.OutCredential.Reveal()
	// Request body limits are enforced by the peer's own handlers.
	proxy := &httputil.ReverseProxy{
		Transport:     client.Transport,
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			out := pr.Out
			out.URL.Scheme = base.Scheme
			out.URL.Host = base.Host
			out.URL.Path = strings.TrimRight(base.Path, "/") + "/api/" + tailDecoded
			out.URL.RawPath = strings.TrimRight(base.EscapedPath(), "/") + "/api/" + tailEscaped
			out.URL.RawQuery = pr.In.URL.RawQuery
			out.Host = ""
			h := out.Header
			// Rewrite already dropped Forwarded and X-Forwarded-*.
			h.Del("Cookie")
			if upgrade {
				// The peer sees a credentialed server call, not a browser.
				h.Del("Origin")
			}
			h.Del("Authorization")
			h.Del(peers.PeerHeader)
			h.Del(peerUserHeader)
			h.Set(peers.PeerHeader, credential)
			h.Set(peerUserHeader, username)
		},
		ModifyResponse: func(resp *http.Response) error {
			for _, name := range hostileResponseHeaders {
				resp.Header.Del(name)
			}
			for name := range resp.Header {
				if strings.HasPrefix(http.CanonicalHeaderKey(name), "Access-Control-") {
					resp.Header.Del(name)
				}
			}
			resp.Header.Set("Content-Security-Policy", "sandbox; default-src 'none'")
			switch {
			case resp.StatusCode == http.StatusUnauthorized:
				// Never a 401 to the browser: the web app would sign the user out.
				return &peers.PeerError{Code: peers.CodeUnpaired, Server: peer.Name,
					Msg: peer.Name + " no longer recognises this server; pair it again"}
			case resp.StatusCode >= 300 && resp.StatusCode < 400:
				return &peers.PeerError{Code: peers.CodeProxy, Server: peer.Name,
					Msg: "something in front of " + peer.Name + " (a reverse proxy or login page) answered instead of Stackstead"}
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			if errors.Is(err, context.Canceled) && req.Context().Err() != nil {
				select {
				case <-s.opts.Closing:
					writeError(w, http.StatusServiceUnavailable, "server is shutting down")
				default: // the browser left; nobody to answer
				}
				return
			}
			var pe *peers.PeerError
			if !errors.As(err, &pe) {
				// Classify never puts the raw error (which carries the URL) in its message.
				if cerr := peers.Classify(peer.Name, nil, err); !errors.As(cerr, &pe) {
					pe = &peers.PeerError{Code: peers.CodeUnreachable, Server: peer.Name, Msg: peer.Name + " could not be reached"}
				}
			}
			s.opts.Logger.Warn("forwarded call failed", "peer", peer.Name, "code", pe.Code)
			writeCodedError(w, http.StatusBadGateway, pe.Msg, pe.Code)
		},
	}
	if upgrade {
		// A write deadline armed on the 101 would survive the hijack and cut
		// quiet sessions, so upgrades get the raw writer.
		proxy.ServeHTTP(w, r)
		return
	}
	proxy.ServeHTTP(&deadlineWriter{ResponseWriter: w, rc: http.NewResponseController(w), timeout: s.sseWriteTimeout()}, r)
}

// isWebSocketUpgrade reports whether r asks for a WebSocket upgrade.
func isWebSocketUpgrade(r *http.Request) bool {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return false
	}
	for _, v := range r.Header.Values("Connection") {
		for _, tok := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(tok), "upgrade") {
				return true
			}
		}
	}
	return false
}
