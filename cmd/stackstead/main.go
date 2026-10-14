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
	"syscall"
	"time"

	"github.com/rcss-labs/stackstead/internal/api"
	"github.com/rcss-labs/stackstead/internal/auth"
	"github.com/rcss-labs/stackstead/internal/composecli"
	"github.com/rcss-labs/stackstead/internal/config"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/stacks"
	"github.com/rcss-labs/stackstead/internal/store"
	"github.com/rcss-labs/stackstead/web"
)

const (
	httpShutdownGrace = 10 * time.Second
	engineTimeout     = 2 * time.Second
	probeTimeout      = 3 * time.Second
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
	runner := &composecli.Runner{}
	if cfg.DockerSocket != config.Defaults().DockerSocket {
		// Keep compose on the same engine as the API client.
		runner.Env = []string{"DOCKER_HOST=unix://" + cfg.DockerSocket}
	}
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
	ops := stacks.NewOps(cfg.OperationTimeout, time.Now)
	stackSvc := stacks.NewService(stacks.Deps{
		Registry:     stacks.NewRegistry(db, time.Now),
		Ops:          ops,
		Compose:      runner,
		Engine:       dockerEngine,
		StacksDir:    cfg.StacksDir,
		AllowedRoots: cfg.AllowedRoots,
		Now:          time.Now,
	})
	// Sync returns joined per-folder warnings; none of them stop startup.
	if err := stackSvc.Sync(ctx); err != nil {
		logger.Warn("stack sync reported problems", "err", err)
	}

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
		}),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       readTimeout,
		IdleTimeout:       idleTimeout,
		// WriteTimeout stays unset: SSE streams (Plan 4) need long-lived writes.
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()
	logger.Info("stackstead listening", "addr", cfg.Listen)

	select {
	case err := <-serveErr:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}
	logger.Info("shutting down")
	// Order: stop accepting HTTP, then cancel running operations (compose
	// process groups get SIGTERM then SIGKILL), then the deferred db.Close.
	// Containers are never stopped.
	httpCtx, cancelHTTP := context.WithTimeout(context.Background(), httpShutdownGrace)
	defer cancelHTTP()
	httpErr := srv.Shutdown(httpCtx)
	if errors.Is(httpErr, context.DeadlineExceeded) {
		// Long-lived SSE streams outlive the grace; drop them so the
		// operations below can finish.
		httpErr = srv.Close()
	}
	opsCtx, cancelOps := context.WithTimeout(context.Background(), cfg.ShutdownGrace)
	defer cancelOps()
	ops.Shutdown(opsCtx)
	return httpErr
}
