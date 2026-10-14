package api

import (
	"errors"
	"net/http"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/auth"
)

type changePasswordRequest = apitypes.ChangePasswordRequest

// handleChangePassword changes the signed-in user's password. Wrong current
// passwords count against the same per-IP limiter as logins; it answers 403
// rather than 401 because the client treats 401 as "session gone".
func (s *server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req changePasswordRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ip := remoteHost(r)
	if !s.opts.Limiter.Attempt(ip) {
		writeError(w, http.StatusTooManyRequests, "too many failed login attempts, try again later")
		return
	}
	cookie, _ := r.Cookie(sessionCookieName) // present: requireSession checked it
	err := s.opts.Auth.ChangePassword(r.Context(), userFrom(r.Context()).ID, req.Current, req.New, cookie.Value)
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		writeError(w, http.StatusForbidden, "current password is incorrect")
	case errors.Is(err, auth.ErrWeakPassword), errors.Is(err, auth.ErrSamePassword):
		// The current password was verified, so this is not a guessing attempt.
		s.opts.Limiter.Reset(ip)
		writeError(w, http.StatusBadRequest, err.Error())
	case err != nil:
		s.internalError(w, r, err)
	default:
		s.opts.Limiter.Reset(ip)
		w.WriteHeader(http.StatusNoContent)
	}
}
