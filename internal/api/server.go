// Package api is Stackstead's HTTP layer: JSON endpoints under /api and the
// embedded single-page app for everything else.
package api

import (
	"io/fs"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/rcss-labs/stackstead/internal/auth"
)

type Options struct {
	Auth       *auth.Service
	Limiter    *auth.LoginLimiter
	SetupToken string // empty once an admin exists
	Static     fs.FS  // SPA build output; must contain index.html
	Logger     *slog.Logger

	// Stacks and Operations back the /api/stacks, /api/operations, /api/fs,
	// /api/networks and /api/status routes; they are not registered unless
	// both are set. FSRoots are the directories the file browser may enter,
	// Status feeds /api/status.
	Stacks     StacksAPI
	Operations OperationsAPI
	FSRoots    []string
	Status     StatusFunc
	// Observe backs the logs, metrics, status-event and /metrics routes; nil
	// disables them (and /api/status reports events as disconnected).
	Observe ObserveAPI
	// SSEHeartbeat is the idle interval between ": ping" comments on
	// operation streams (default 15s) and SSEWriteTimeout bounds each write
	// to a stream (default 10s), since the HTTP server has no WriteTimeout.
	// Closing, when closed, ends open operation streams so graceful HTTP
	// shutdown does not wait for them.
	Closing <-chan struct{}

	SSEHeartbeat    time.Duration
	SSEWriteTimeout time.Duration
}

type server struct {
	opts Options

	// tokenCache is the /metrics token hash, loaded lazily (see tokenHash).
	tokenMu     sync.Mutex
	tokenCache  string
	tokenLoaded bool
}

func New(opts Options) http.Handler {
	s := &server{opts: opts}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/setup/status", s.handleSetupStatus)
	mux.HandleFunc("POST /api/setup", s.handleSetup)
	mux.HandleFunc("POST /api/login", s.handleLogin)

	mux.Handle("POST /api/logout", s.requireSession(s.handleLogout))
	mux.Handle("GET /api/me", s.requireSession(s.handleMe))
	mux.Handle("POST /api/password", s.requireSession(s.handleChangePassword))

	if opts.Stacks != nil && opts.Operations != nil {
		s.registerStackRoutes(mux)
	}

	if opts.Observe != nil {
		s.registerObserveRoutes(mux)
	} else {
		// Without this /metrics would fall through to the SPA's index.html.
		mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusNotFound, "not found")
		})
	}

	// Unknown API paths get a JSON 404 (after auth, so they don't reveal which
	// routes exist) instead of falling through to the SPA's index.html.
	mux.Handle("/api/", s.requireSession(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	}))
	mux.Handle("/", spaHandler(opts.Static))
	return nosniff(mux)
}

// nosniff stops browsers from second-guessing Content-Type on any response.
func nosniff(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}
