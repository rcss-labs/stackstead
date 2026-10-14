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
	"github.com/rcss-labs/stackstead/internal/config"
	"github.com/rcss-labs/stackstead/internal/store"
	"github.com/rcss-labs/stackstead/web"
)

const shutdownGrace = 10 * time.Second

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
	configPath := flags.String("config", "", "path to config.yaml (optional; STACKSTEAD_* env vars override it)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	cfg, err := config.Load(*configPath, os.Getenv)
	if err != nil {
		return err
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

	srv := &http.Server{
		Addr: cfg.Listen,
		Handler: api.New(api.Options{
			Auth:       authSvc,
			Limiter:    auth.NewLoginLimiter(5, 15*time.Minute, time.Now),
			SetupToken: setupToken,
			Static:     web.Dist(),
			Logger:     logger,
		}),
		ReadHeaderTimeout: 10 * time.Second,
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
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
