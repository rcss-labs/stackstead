// Command stackstead runs the Stackstead web server.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/rcss-labs/stackstead/internal/api"
	"github.com/rcss-labs/stackstead/internal/auth"
	"github.com/rcss-labs/stackstead/internal/composecli"
	"github.com/rcss-labs/stackstead/internal/config"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/metrics"
	"github.com/rcss-labs/stackstead/internal/stacks"
	"github.com/rcss-labs/stackstead/internal/state"
	"github.com/rcss-labs/stackstead/internal/store"
	"github.com/rcss-labs/stackstead/web"
)

const (
	httpShutdownGrace = 10 * time.Second
	engineTimeout     = 2 * time.Second
	probeTimeout      = 3 * time.Second
	versionCacheTTL   = 5 * time.Second
	defaultConfigPath = "/etc/stackstead/config.yaml"
	readTimeout       = 30 * time.Second
	idleTimeout       = 120 * time.Second
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(ctx, os.Args[1:], logger); err != nil {
		logger.Error("stackstead exited", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, logger *slog.Logger) error {
	flags := flag.NewFlagSet("stackstead", flag.ContinueOnError)
	configPath := flags.String("config", "", "path to config.yaml (default: "+defaultConfigPath+" if it exists; STACKSTEAD_* env vars override it)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	path := *configPath
	if path == "" {
		if _, err := os.Stat(defaultConfigPath); err == nil {
			path = defaultConfigPath
		}
	}
	cfg, err := config.Load(path, os.Getenv)
	if err != nil {
		return err
	}
	if path != "" {
		logger.Info("loaded config file", "path", path)
	} else {
		logger.Info("no config file; using defaults and STACKSTEAD_* environment")
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	db, err := store.Open(filepath.Join(cfg.DataDir, "stackstead.db"))
	if err != nil {
		return err
	}
	defer db.Close()

	authSvc, err := auth.NewService(db, time.Now)
	if err != nil {
		return err
	}
	hasAdmin, err := authSvc.HasAdmin(ctx)
	if err != nil {
		return err
	}
	var setupToken string
	if !hasAdmin {
		setupToken = rand.Text()
		logger.Warn("no admin account yet: open the UI from this machine, or enter this setup token",
			"setup_token", setupToken)
	}

	dockerEngine := engine.New(cfg.DockerSocket, engineTimeout)
	// One cache serves the status probe and the service, so polling the
	// status does not spawn `docker compose version` every time.
	runner := stacks.NewVersionCache(buildRunner(cfg), versionCacheTTL, time.Now)
	// Docker or compose being unavailable is not fatal: the service starts
	// and lifecycle endpoints answer 503 until they come back.
	dockerStatus := func(ctx context.Context) (docker, compose string) {
		ctx, cancel := context.WithTimeout(ctx, probeTimeout)
		defer cancel()
		docker, compose = "unavailable", "unavailable"
		if dockerEngine.Ping(ctx) == nil {
			docker = "ok"
		}
		if v, err := runner.Version(ctx); err == nil && v != "" {
			compose = v
		}
		return docker, compose
	}
	if docker, compose := dockerStatus(ctx); docker != "ok" || compose == "unavailable" {
		logger.Warn("docker or compose is unavailable; stack lifecycle actions will fail until it is",
			"docker", docker, "compose", compose, "socket", cfg.DockerSocket)
	}

	if err := os.MkdirAll(cfg.StacksDir, 0o755); err != nil {
		return fmt.Errorf("create stacks dir: %w", err)
	}
	// The cache follows Docker events and serves container status to the
	// stack service, the sampler and the observability routes.
	cache := state.New(dockerEngine, logger, state.Backoff{})
	metricsStore := metrics.NewStore(db, time.Now)
	sampler := metrics.NewSampler(metricsStore, cache, dockerEngine, cfg.MetricsInterval, cfg.MetricsRetention, logger, time.Now)
	// A service context separate from the signal context: background work
	// must outlive the signal until HTTP and operations have drained.
	bg := startBackground(context.Background(), cache.Run, sampler.Run)
	defer bg.stop(time.Second) // safety net for early returns; the normal path stops it in order below

	ops := stacks.NewOps(cfg.OperationTimeout, time.Now)
	stackSvc := stacks.NewService(stacks.Deps{
		Registry:     stacks.NewRegistry(db, time.Now),
		Ops:          ops,
		Compose:      runner,
		Engine:       dockerEngine,
		Containers:   cache,
		StacksDir:    cfg.StacksDir,
		AllowedRoots: cfg.AllowedRoots,
		Logger:       logger,
		Now:          time.Now,
	})
	// Sync returns joined per-folder warnings; none of them stop startup.
	if err := stackSvc.Sync(ctx); err != nil {
		logger.Warn("stack sync reported problems", "err", err)
	}

	closing := make(chan struct{})
	srv := &http.Server{
		Addr: cfg.Listen,
		Handler: api.New(api.Options{
			Auth:       authSvc,
			Limiter:    auth.NewLoginLimiter(5, 15*time.Minute, time.Now),
			SetupToken: setupToken,
			Static:     web.Dist(),
			Logger:     logger,
			Stacks:     stackSvc,
			Operations: ops,
			FSRoots:    cfg.AllowedRoots,
			Status:     dockerStatus,
			Closing:    closing,
			Observe: api.ObserveSources{
				Cache: cache, Sampler: sampler, Store: metricsStore, Engine: dockerEngine,
			},
		}),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       readTimeout,
		IdleTimeout:       idleTimeout,
		// WriteTimeout stays unset: SSE streams (Plan 4) need long-lived writes.
	}

	// Ends SSE streams as soon as Shutdown starts; they would otherwise
	// hold it until the grace expires.
	srv.RegisterOnShutdown(func() { close(closing) })

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()
	logger.Info("stackstead listening", "addr", cfg.Listen)

	select {
	case err := <-serveErr:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}
	logger.Info("shutting down")
	// Order: stop accepting HTTP (Closing ends streams), cancel running
	// operations (compose process groups get SIGTERM then SIGKILL), stop the
	// cache and sampler, then the deferred db.Close. Containers are never
	// stopped.
	return shutdownServices(cfg.ShutdownGrace, func() error {
		httpCtx, cancelHTTP := context.WithTimeout(context.Background(), httpShutdownGrace)
		defer cancelHTTP()
		err := srv.Shutdown(httpCtx)
		if errors.Is(err, context.DeadlineExceeded) {
			// Long-lived SSE streams outlive the grace; drop them so the
			// operations below can finish.
			err = srv.Close()
		}
		return err
	}, ops.Shutdown, bg)
}

// background runs long-lived goroutines (status cache, metrics sampler)
// under their own context so shutdown can stop them after HTTP and operations.
type background struct {
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func startBackground(parent context.Context, runners ...func(context.Context)) *background {
	ctx, cancel := context.WithCancel(parent)
	b := &background{cancel: cancel}
	for _, run := range runners {
		b.wg.Add(1)
		go func() {
			defer b.wg.Done()
			run(ctx)
		}()
	}
	return b
}

// stop cancels the runners and waits up to grace for them to return. It
// reports whether they all did.
func (b *background) stop(grace time.Duration) bool {
	b.cancel()
	done := make(chan struct{})
	go func() { b.wg.Wait(); close(done) }()
	t := time.NewTimer(grace)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}

// shutdownServices stops the pieces in dependency order and returns the HTTP
// shutdown error. The caller closes the database afterwards.
func shutdownServices(grace time.Duration, shutdownHTTP func() error, shutdownOps func(context.Context), bg *background) error {
	httpErr := shutdownHTTP()
	opsCtx, cancelOps := context.WithTimeout(context.Background(), grace)
	defer cancelOps()
	shutdownOps(opsCtx)
	bg.stop(grace)
	return httpErr
}

const (
	maxComposeGrace = 10 * time.Second
	minComposeGrace = 500 * time.Millisecond
)

// buildRunner makes docker_socket the single source of truth: compose talks
// to the same engine as the API client, regardless of an inherited
// DOCKER_HOST or the CLI's current context. The SIGTERM-to-SIGKILL delay is
// kept within half of shutdown_grace so that a shutdown that only waits
// shutdown_grace still gets to SIGKILL compose's process groups.
func buildRunner(cfg config.Config) *composecli.Runner {
	grace := min(maxComposeGrace, cfg.ShutdownGrace/2)
	grace = max(grace, minComposeGrace)
	return &composecli.Runner{
		Env:   []string{"DOCKER_HOST=unix://" + cfg.DockerSocket},
		Grace: grace,
	}
}
