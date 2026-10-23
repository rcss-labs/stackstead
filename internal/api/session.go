package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/rcss-labs/stackstead/internal/auth"
)

const sessionCookieName = "stackstead_session"

type userKey struct{}

func userFrom(ctx context.Context) auth.User {
	u, _ := ctx.Value(userKey{}).(auth.User)
	return u
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteStrictMode,
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *server) requireSession(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil {
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
		user, err := s.opts.Auth.Authenticate(r.Context(), cookie.Value)
		if errors.Is(err, auth.ErrInvalidSession) {
			clearSessionCookie(w, r)
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
		ctx, stop := s.opts.Auth.WatchSession(r.Context(), cookie.Value, user)
		defer stop()
		next(w, r.WithContext(context.WithValue(ctx, userKey{}, user)))
	})
}
