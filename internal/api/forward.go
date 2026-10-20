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
	client, err := peers.NewHTTPClient(base, p.TLS, 0)
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

func (s *server) handleForward(w http.ResponseWriter, r *http.Request) {
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

	username := userFrom(r.Context()).Username
	credential := peer.OutCredential.Reveal()
	rest := r.PathValue("rest")
	proxy := &httputil.ReverseProxy{
		Transport:     client.Transport,
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			out := pr.Out
			out.URL.Scheme = base.Scheme
			out.URL.Host = base.Host
			out.URL.Path = strings.TrimRight(base.Path, "/") + "/api/" + rest
			out.URL.RawPath = ""
			out.URL.RawQuery = pr.In.URL.RawQuery
			out.Host = ""
			h := out.Header
			// Rewrite already dropped Forwarded and X-Forwarded-*.
			h.Del("Cookie")
			h.Del("Authorization")
			h.Del(peers.PeerHeader)
			h.Del(peerUserHeader)
			h.Set(peers.PeerHeader, credential)
			h.Set(peerUserHeader, username)
		},
		ModifyResponse: func(resp *http.Response) error {
			resp.Header.Del("Set-Cookie")
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
				return // the browser left or the server is closing
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
	proxy.ServeHTTP(&deadlineWriter{ResponseWriter: w, rc: http.NewResponseController(w), timeout: s.sseWriteTimeout()}, r)
}
