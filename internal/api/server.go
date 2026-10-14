// Package api is Stackstead's HTTP layer: JSON endpoints under /api and the
// embedded single-page app for everything else.
package api

import (
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/rcss-labs/stackstead/internal/auth"
)

type Options struct {
	Auth       *auth.Service
	Limiter    *auth.LoginLimiter
	SetupToken string // empty once an admin exists
	Static     fs.FS  // SPA build output; must contain index.html
	Logger     *slog.Logger

	// Stacks backs the /api/stacks, /api/operations, /api/fs, /api/networks
	// and /api/status routes; they are not registered when it is nil.
	Stacks StacksAPI
	// SSEHeartbeat is the idle interval between ": ping" comments on
	// operation streams (default 15s) and SSEWriteTimeout bounds each write
	// to a stream (default 10s), since the HTTP server has no WriteTimeout.
	SSEHeartbeat    time.Duration
	SSEWriteTimeout time.Duration
}

type server struct {
	opts Options
}

func New(opts Options) http.Handler {
	s := &server{opts: opts}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/setup/status", s.handleSetupStatus)
	mux.HandleFunc("POST /api/setup", s.handleSetup)
	mux.HandleFunc("POST /api/login", s.handleLogin)

	mux.Handle("POST /api/logout", s.requireSession(s.handleLogout))
	mux.Handle("GET /api/me", s.requireSession(s.handleMe))

	if opts.Stacks != nil {
		s.registerStackRoutes(mux)
	}

	// Unknown API paths get a JSON 404 (after auth, so they don't reveal which
	// routes exist) instead of falling through to the SPA's index.html.
	mux.Handle("/api/", s.requireSession(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	}))
	mux.Handle("/", spaHandler(opts.Static))
	return mux
}
