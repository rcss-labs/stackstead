package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/rcss-labs/stackstead/internal/auth"
)

const (
	sessionCookieName     = "stackstead_session"
	hostSessionCookieName = "__Host-" + sessionCookieName
)

type userKey struct{}

func userFrom(ctx context.Context) auth.User {
	u, _ := ctx.Value(userKey{}).(auth.User)
	return u
}

// isHTTPS reports whether the browser reached the server over TLS: directly,
// or through a proxy that says so with X-Forwarded-Proto. The header is
// trusted from any peer, not only trusted_proxies: a client that forges it
// merely gets a Secure cookie its own browser will not send over plain HTTP,
// while ignoring it would hand a TLS-terminating proxy a non-Secure cookie.
func (s *server) isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

// sessionToken returns the session token from the request's cookie. Both
// names are accepted, whatever the scheme the server believes it is on: the
// CLI always sends the plain name, and a proxy that does not announce HTTPS
// makes the server see plain HTTP while the browser holds a __Host- cookie.
// The prefixed cookie wins when both are present.
func sessionToken(r *http.Request) (string, bool) {
	for _, name := range []string{hostSessionCookieName, sessionCookieName} {
		if c, err := r.Cookie(name); err == nil && c.Value != "" {
			return c.Value, true
		}
	}
	return "", false
}

// sessionCookie builds the cookie for the request's scheme. Over HTTPS it
// carries the __Host- prefix, which browsers only accept with Secure, Path=/
// and no Domain, so a sibling service on the same host cannot overwrite it.
// Over plain HTTP browsers reject that prefix, so the plain name is kept.
func (s *server) sessionCookie(r *http.Request, value string, maxAge int) *http.Cookie {
	https := s.isHTTPS(r)
	name := sessionCookieName
	if https {
		name = hostSessionCookieName
	}
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   https,
		SameSite: http.SameSiteStrictMode,
	}
}

func (s *server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, ttl time.Duration) {
	http.SetCookie(w, s.sessionCookie(r, token, int(ttl.Seconds())))
}

// clearSessionCookie expires the cookie under both names, so a session
// started before an HTTPS switch (or the other way round) does not linger.
// The __Host- name is only cleared over HTTPS: browsers ignore it otherwise.
func (s *server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, s.sessionCookie(r, "", -1))
	if s.isHTTPS(r) {
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookieName,
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteStrictMode,
		})
	}
}

func (s *server) requireSession(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := sessionToken(r)
		if !ok {
			if s.usesPeerCredential(r) {
				user, peer := s.authenticatePeerRequest(w, r)
				if user != nil {
					// Unpairing ends the peer's open streams.
					ctx, stop := s.opts.Peers.WatchPeer(r.Context(), peer.InstanceID)
					defer stop()
					next(w, r.WithContext(context.WithValue(ctx, userKey{}, *user)))
				}
				return
			}
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		user, err := s.opts.Auth.Authenticate(r.Context(), token)
		if errors.Is(err, auth.ErrInvalidSession) {
			s.clearSessionCookie(w, r)
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if s.opts.Peers != nil {
			s.opts.Peers.NoteSessionActivity()
		}
		// Long-lived streams run on this context, so ending the session
		// (logout, password change, expiry) ends them too.
		ctx, stop := s.opts.Auth.WatchSession(r.Context(), token, user)
		defer stop()
		next(w, r.WithContext(context.WithValue(ctx, userKey{}, user)))
	})
}
