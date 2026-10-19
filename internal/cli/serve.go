package cli

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/rcss-labs/stackstead/internal/actionlog"
	"github.com/rcss-labs/stackstead/internal/alerts"
	"github.com/rcss-labs/stackstead/internal/api"
	"github.com/rcss-labs/stackstead/internal/auth"
	"github.com/rcss-labs/stackstead/internal/composecli"
	"github.com/rcss-labs/stackstead/internal/config"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/host"
	"github.com/rcss-labs/stackstead/internal/lockfile"
	"github.com/rcss-labs/stackstead/internal/metrics"
	"github.com/rcss-labs/stackstead/internal/stacks"
	"github.com/rcss-labs/stackstead/internal/state"
	"github.com/rcss-labs/stackstead/internal/storage"
	"github.com/rcss-labs/stackstead/internal/store"
	"github.com/rcss-labs/stackstead/internal/update"
	"github.com/rcss-labs/stackstead/internal/version"
	"github.com/rcss-labs/stackstead/web"
)

// defaultConfigPath is a variable so tests can point it away from the host.
var defaultConfigPath = "/etc/stackstead/config.yaml"

const (
	httpShutdownGrace = 10 * time.Second
	engineTimeout     = 2 * time.Second
	probeTimeout      = 3 * time.Second
	versionCacheTTL   = 5 * time.Second
	readTimeout       = 30 * time.Second
	idleTimeout       = 120 * time.Second
)

// serve runs the HTTP server until ctx is cancelled.
func serveCommand() *Command {
	return &Command{
		Name:    "serve",
		Short:   "Run the Stackstead server",
		Usage:   "serve [--config <path>]",
		Long:    "Runs the HTTP server in the foreground until interrupted. The config file defaults to " + defaultConfigPath + " when it exists; STACKSTEAD_* environment variables override it.",
		RawArgs: true,
		Flags:   func(fs *flag.FlagSet) { fs.String("config", "", "path to config.yaml") },
		Run: func(ctx context.Context, env *Env, _ *globalFlags, args []string) error {
			return serveRun(ctx, env, args)
		},
	}
}

// serveRun is a variable so tests can stub the server.
var serveRun func(ctx context.Context, env *Env, args []string) error

// Assigned in init: serve prints serveCommand's usage, which refers to serveRun.
func init() { serveRun = serve }

func serve(ctx context.Context, env *Env, args []string) error {
	logger := slog.New(slog.NewTextHandler(env.Stderr, nil))
	flags := flag.NewFlagSet("stackstead serve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "path to config.yaml (default: "+defaultConfigPath+" if it exists; STACKSTEAD_* env vars override it)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printUsage(env.Stdout, env, serveCommand(), []string{"serve"})
			return nil
		}
		return exitf(ExitUsage, "%v", err)
	}
	if flags.NArg() > 0 {
		return exitf(ExitUsage, "unexpected argument %q", flags.Arg(0))
	}

	path := *configPath
	if path == "" {
		if _, err := os.Stat(defaultConfigPath); err == nil {
			path = defaultConfigPath
		}
	}
	cfg, err := config.Load(path, env.Getenv)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	// One server per data directory; db restore takes the same lock. Held until
	// serve returns, after the database is closed.
	lock, err := lockfile.Acquire(filepath.Join(cfg.DataDir, "stackstead.lock"))
	if err != nil {
		var held *lockfile.HeldError
		if errors.As(err, &held) {
			return exitf(ExitConflict, "another stackstead server is running (pid %d)", held.PID)
		}
		return err
	}
	defer lock.Release()
	if path != "" {
		logger.Info("loaded config file", "path", path)
	} else {
		logger.Info("no config file; using defaults and STACKSTEAD_* environment")
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
	dockerStatus := func(ctx context.Context) api.StatusReport {
		ctx, cancel := context.WithTimeout(ctx, probeTimeout)
		defer cancel()
		return probeStatus(ctx, dockerEngine, runner)
	}
	if report := dockerStatus(ctx); report.Docker != "ok" || report.Compose == "unavailable" {
		logger.Warn("docker or compose is unavailable; stack lifecycle actions will fail until it is",
			"docker", report.Docker, "compose", report.Compose, "socket", cfg.DockerSocket)
	}

	if err := os.MkdirAll(cfg.StacksDir, 0o755); err != nil {
		return fmt.Errorf("create stacks dir: %w", err)
	}
	// The cache follows Docker events and serves container status to the
	// stack service, the sampler and the observability routes.
	cache := state.New(dockerEngine, logger, state.Backoff{})
	metricsStore := metrics.NewStore(db, time.Now)
	actions := actionlog.NewStore(db, time.Now)
	sampler := metrics.NewSampler(metricsStore, cache, dockerEngine, cfg.MetricsInterval, cfg.MetricsRetention, logger, time.Now, func(ctx context.Context) error {
		_, err := actions.Prune(ctx, time.Now().Add(-365*24*time.Hour))
		return err
	})
	checker, err := newUpdateChecker(cfg, logger)
	if err != nil {
		return err
	}
	// A service context separate from the signal context: background work
	// must outlive the signal until HTTP and operations have drained.
	bg := startBackground(context.Background(), cache.Run, sampler.Run, checker.Run)
	defer bg.stop(time.Second) // safety net for early returns; the normal path stops it in order below

	registry := stacks.NewRegistry(db, time.Now)
	ops := stacks.NewOps(cfg.OperationTimeout, time.Now)
	storageSvc := storage.NewService(storage.ServiceConfig{Engine: dockerEngine, Registry: registry, Declarations: storage.NewDeclarationCache(buildRunner(cfg), logger), Now: time.Now, Ops: ops, Actions: actions})
	stackSvc := stacks.NewService(stacks.Deps{
		Registry:     registry,
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

	// The collector and alerts engine do nothing until a request arrives:
	// both cache for 5 s and start no goroutines.
	collector := host.New(host.Config{
		ProcRoot: "/proc", SysRoot: "/sys", StacksDir: cfg.StacksDir,
		Docker: dockerEngine, Sampler: sampler, Containers: cache, Volumes: storageSvc,
		Statfs: host.OSStatfs{}, Now: time.Now, Hostname: os.Hostname, GOOS: runtime.GOOS,
	})
	alertsEngine := alerts.NewEngine(stackSvc, cache, collector, cache, sampler, time.Now)

	if !web.Built() {
		logger.Warn("web UI not built; serving placeholder page (run make ui)")
	}

	closing := make(chan struct{})
	srv := &http.Server{
		Addr: cfg.Listen,
		Handler: api.New(api.Options{
			Storage:          storageSvc,
			Actions:          actions,
			Auth:             authSvc,
			Limiter:          auth.NewLoginLimiter(5, 15*time.Minute, time.Now),
			SetupToken:       setupToken,
			Static:           web.Dist(),
			Logger:           logger,
			Stacks:           stackSvc,
			Operations:       ops,
			FSRoots:          cfg.AllowedRoots,
			Status:           dockerStatus,
			MetricsInterval:  cfg.MetricsInterval,
			MetricsRetention: cfg.MetricsRetention,
			Closing:          closing,
			Updates:          checker,
			Version:          version.Get().Version,
			Host:             collector,
			Interfaces:       host.InterfaceLister{ProcRoot: "/proc", GOOS: runtime.GOOS},
			Alerts:           alertsEngine,
			Summary:          api.SummarySources{Store: metricsStore, Sampler: sampler},
			Restarts:         collector,
			ContainerEngine:  dockerEngine,
			NetworkEngine:    dockerEngine,
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
	logger.Info("stackstead listening", "addr", cfg.Listen, "version", version.Get().Version)

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

// newUpdateChecker builds the release checker. The server always uses the
// default release source; only update_check turns it off. A disabled checker
// or a dev build never touches the network.
func newUpdateChecker(cfg config.Config, logger *slog.Logger) (*update.Checker, error) {
	current := version.Get().Version
	source, err := update.NewSource(update.DefaultBaseURL, "stackstead/"+current)
	if err != nil {
		return nil, fmt.Errorf("update source: %w", err)
	}
	exe := update.Executable()
	exists := func(path string) bool { _, err := os.Stat(path); return err == nil }
	if cfg.UpdateCheck {
		logger.Info("update checks enabled")
	} else {
		logger.Info("update checks disabled")
	}
	return update.NewChecker(update.CheckerOptions{
		Source:  source,
		Enabled: cfg.UpdateCheck,
		Current: current,
		Kind:    update.Detect(runtime.GOOS, exe, exists),
		GOOS:    runtime.GOOS,
		Exe:     exe,
		Logger:  logger,
	}), nil
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

type dockerProber interface {
	Version(ctx context.Context) (string, error)
	Ping(ctx context.Context) error
}

type composeVersioner interface {
	Version(ctx context.Context) (string, error)
}

// probeStatus reports engine and compose health. A failing /version must not
// mark Docker down, so it falls back to a ping and then reports no version.
func probeStatus(ctx context.Context, docker dockerProber, compose composeVersioner) api.StatusReport {
	report := api.StatusReport{Docker: "unavailable", Compose: "unavailable"}
	if v, err := docker.Version(ctx); err == nil {
		report.Docker, report.DockerVersion = "ok", v
	} else if docker.Ping(ctx) == nil {
		report.Docker = "ok"
	}
	if v, err := compose.Version(ctx); err == nil && v != "" {
		report.Compose = v
	}
	return report
}
