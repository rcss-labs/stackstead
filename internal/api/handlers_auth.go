package api

import (
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/auth"
	"github.com/rcss-labs/stackstead/internal/peers"
)

type (
	setupRequest = apitypes.SetupRequest
	loginRequest = apitypes.LoginRequest
)

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, apitypes.Health{OK: true, Peering: peers.Protocol})
}

func (s *server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	has, err := s.opts.Auth.HasAdmin(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"needsSetup": !has})
}

func (s *server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var req setupRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !s.setupAllowed(r, req.SetupToken) {
		writeError(w, http.StatusForbidden, "setup is only allowed from this machine or with the setup token from the service log")
		return
	}
	err := s.opts.Auth.CreateAdmin(r.Context(), req.Username, req.Password)
	switch {
	case errors.Is(err, auth.ErrAdminExists):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, auth.ErrInvalidUsername), errors.Is(err, auth.ErrWeakPassword):
		writeError(w, http.StatusBadRequest, err.Error())
	case err != nil:
		s.internalError(w, r, err)
	default:
		s.removeRestoreUpload()
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *server) setupAllowed(r *http.Request, token string) bool {
	if s.opts.SetupToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.opts.SetupToken)) == 1 {
		return true
	}
	return isDirectLoopback(r)
}

// isDirectLoopback reports whether the request came from this machine without
// passing through a proxy. A reverse proxy on the same host connects from
// loopback too, so any forwarding header means the real client may be remote.
// The Host must also name this machine: otherwise a DNS-rebinding page or a
// proxy that adds no forwarding headers could ride the loopback connection.
func isDirectLoopback(r *http.Request) bool {
	for _, h := range []string{"Forwarded", "X-Forwarded-For", "X-Real-Ip"} {
		if r.Header.Get(h) != "" {
			return false
		}
	}
	if !isLoopbackHostname(hostnameOf(r.Host)) {
		return false
	}
	ip := net.ParseIP(remoteHost(r))
	return ip != nil && ip.IsLoopback()
}

// hostnameOf strips an optional port, handling bracketed IPv6 like "[::1]:8420".
func hostnameOf(hostport string) string {
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		return host
	}
	return strings.Trim(hostport, "[]")
}

func isLoopbackHostname(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// clientIP identifies the caller for rate limiting. X-Forwarded-For is read
// only when the TCP peer is a configured trusted proxy, and then only the
// rightmost entry that is not itself a trusted proxy counts: entries further
// left are client-supplied and could be rotated freely. Any malformed entry
// falls back to the peer address, so forged input can never mint new buckets.
func (s *server) clientIP(r *http.Request) string {
	peer := remoteHost(r)
	peerAddr, err := netip.ParseAddr(peer)
	if err != nil || !s.isTrustedProxy(peerAddr) {
		return peer
	}
	var hops []string
	for _, line := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(line, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return peer
		}
		if !s.isTrustedProxy(hop) {
			return hop.Unmap().String()
		}
	}
	return peer
}

func (s *server) isTrustedProxy(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, prefix := range s.opts.TrustedProxies {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ip := s.clientIP(r)
	if !s.opts.Limiter.Attempt(ip) {
		writeError(w, http.StatusTooManyRequests, "too many failed login attempts, try again later")
		return
	}
	token, err := s.opts.Auth.Login(r.Context(), req.Username, req.Password)
	if errors.Is(err, auth.ErrInvalidCredentials) {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.opts.Limiter.Reset(ip)
	setSessionCookie(w, r, token, auth.SessionTTL)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, _ := r.Cookie(sessionCookieName) // present: requireSession checked it
	if err := s.opts.Auth.Logout(r.Context(), cookie.Value); err != nil {
		s.internalError(w, r, err)
		return
	}
	clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"username": userFrom(r.Context()).Username})
}
