package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/auth"
	"github.com/rcss-labs/stackstead/internal/peers"
)

const (
	peerUserHeader   = "X-Stackstead-Peer-User"
	maxPeerUserBytes = 64
	codeNotPaired    = "peer_not_paired"
	codeForbidden    = "peer_forbidden"
)

// peerDeniedPrefixes are the routes a peer credential may never reach: a
// paired server gets everything an operator does on stacks and storage, but
// not settings, credentials, pairing management or onward proxying.
var peerDeniedPrefixes = []string{
	"/api/peers", "/api/settings", "/api/password", "/api/logout", "/api/servers", "/api/fleet",
}

// isPeerDenied reports whether a peer credential is refused on path. It
// matches the cleaned path so "//", "/./" and "/../" cannot sidestep it.
func isPeerDenied(p string) bool {
	c := path.Clean("/" + p)
	if strings.HasPrefix(c, "/api/setup") {
		return true
	}
	for _, prefix := range peerDeniedPrefixes {
		if c == prefix || strings.HasPrefix(c, prefix+"/") {
			return true
		}
	}
	return false
}

func writeCodedError(w http.ResponseWriter, status int, msg, code string) {
	writeJSON(w, status, apitypes.ErrorBody{Error: msg, Code: code})
}

func writeNotPaired(w http.ResponseWriter) {
	writeCodedError(w, http.StatusUnauthorized, "not paired", codeNotPaired)
}

func writePeerForbidden(w http.ResponseWriter) {
	writeCodedError(w, http.StatusForbidden, "this server cannot do that over a server connection", codeForbidden)
}

// usesPeerCredential is true for a request that carries a peer credential and
// no session cookie. A cookie always wins, so browsers behave as before.
func (s *server) usesPeerCredential(r *http.Request) bool {
	if s.opts.Peers == nil || r.Header.Get(peers.PeerHeader) == "" {
		return false
	}
	_, ok := sessionToken(r)
	return !ok
}

// denyPeerRoutes refuses denylisted routes before the mux sees them. The mux
// would otherwise answer "/api//settings/x" with a redirect, and open routes
// such as /api/setup are not behind requireSession at all.
func (s *server) denyPeerRoutes(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.usesPeerCredential(r) && isPeerDenied(r.URL.Path) {
			writePeerForbidden(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// authenticatePeerRequest handles a request with a peer credential: nil
// means it was refused and the response is already written.
func (s *server) authenticatePeerRequest(w http.ResponseWriter, r *http.Request) (*auth.User, peers.Peer) {
	if isPeerDenied(r.URL.Path) {
		writePeerForbidden(w)
		return nil, peers.Peer{}
	}
	peer, err := s.opts.Peers.Authenticate(r.Context(), r.Header.Get(peers.PeerHeader), false)
	if errors.Is(err, peers.ErrNotPaired) {
		writeNotPaired(w)
		return nil, peers.Peer{}
	}
	if err != nil {
		s.internalError(w, r, err)
		return nil, peers.Peer{}
	}
	return &auth.User{ID: 0, Username: peerActor(peer.Name, r.Header.Get(peerUserHeader))}, peer
}

func peerActor(peerName, rawUser string) string {
	var b strings.Builder
	for _, c := range strings.TrimSpace(rawUser) {
		if c >= 0x20 && c <= 0x7e && b.Len() < maxPeerUserBytes {
			b.WriteRune(c)
		}
	}
	user := strings.TrimSpace(b.String())
	if user == "" {
		user = "unknown"
	}
	return fmt.Sprintf("peer:%s (%s)", peerName, user)
}

func (s *server) registerPeerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/peer/pair", s.handlePeerPair)
	mux.HandleFunc("GET /api/peer/hello", s.requirePeer(true, s.handlePeerHello))
	mux.HandleFunc("POST /api/peer/update", s.requirePeer(false, s.handlePeerUpdate))
	mux.HandleFunc("POST /api/peer/unpair", s.requirePeer(false, s.handlePeerUnpair))
	s.registerPeerManagementRoutes(mux)
}

// requirePeer authenticates the X-Stackstead-Peer credential; cookies never apply.
func (s *server) requirePeer(helloOnly bool, next func(http.ResponseWriter, *http.Request, peers.Peer)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		peer, err := s.opts.Peers.Authenticate(r.Context(), r.Header.Get(peers.PeerHeader), helloOnly)
		if errors.Is(err, peers.ErrNotPaired) {
			writeNotPaired(w)
			return
		}
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		next(w, r, peer)
	}
}

func (s *server) handlePeerPair(w http.ResponseWriter, r *http.Request) {
	var req apitypes.PeerPairRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	resp, err := s.opts.Peers.AcceptPair(r.Context(), r.Header.Get(peers.PeerHeader), req)
	if err != nil {
		s.writePairError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, resp)
}

func (s *server) writePairError(w http.ResponseWriter, r *http.Request, err error) {
	var pe *peers.PeerError
	switch {
	case errors.Is(err, peers.ErrTokenInvalid):
		writeNotPaired(w)
	case errors.Is(err, peers.ErrSelfPair):
		writeError(w, http.StatusConflict, "cannot pair with itself")
	case errors.Is(err, peers.ErrAlreadyPaired):
		writeError(w, http.StatusConflict, "this server is already connected")
	case errors.Is(err, peers.ErrInvalidRequest):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.As(err, &pe):
		writeCodedError(w, http.StatusBadGateway, pe.Msg, pe.Code)
	default:
		s.internalError(w, r, err)
	}
}

func (s *server) handlePeerHello(w http.ResponseWriter, r *http.Request, _ peers.Peer) {
	hello, err := s.opts.Peers.Hello(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	hello.StackCount = s.stackCount(r.Context())
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, hello)
}

func (s *server) stackCount(ctx context.Context) int {
	if s.opts.Stacks == nil {
		return 0
	}
	list, err := s.opts.Stacks.List(ctx)
	if err != nil {
		return 0
	}
	return len(list)
}

func (s *server) handlePeerUpdate(w http.ResponseWriter, r *http.Request, peer peers.Peer) {
	var u apitypes.PeerUpdate
	if !decodeJSON(w, r, &u) {
		return
	}
	if err := s.opts.Peers.ApplyUpdate(r.Context(), peer, u); err != nil {
		s.writePairError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handlePeerUnpair(w http.ResponseWriter, r *http.Request, peer peers.Peer) {
	if err := s.opts.Peers.AcceptUnpair(r.Context(), peer); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
