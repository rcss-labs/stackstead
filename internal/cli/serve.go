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
	"sync/atomic"
	"syscall"
	"time"

	"github.com/rcss-labs/stackstead/internal/actionlog"
	"github.com/rcss-labs/stackstead/internal/alerts"
	"github.com/rcss-labs/stackstead/internal/api"
	"github.com/rcss-labs/stackstead/internal/auth"
	"github.com/rcss-labs/stackstead/internal/backup"
	"github.com/rcss-labs/stackstead/internal/composecli"
	"github.com/rcss-labs/stackstead/internal/config"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/history"
	"github.com/rcss-labs/stackstead/internal/host"
	"github.com/rcss-labs/stackstead/internal/images"
	"github.com/rcss-labs/stackstead/internal/images/github"
	"github.com/rcss-labs/stackstead/internal/lockfile"
	"github.com/rcss-labs/stackstead/internal/metrics"
	"github.com/rcss-labs/stackstead/internal/peers"
	"github.com/rcss-labs/stackstead/internal/registry"
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

// pauseSchedule pauses a restored backup schedule; tests make it fail.
var pauseSchedule = backup.PauseScheduleAfterRestore

// startBackupScheduler runs the backup scheduler in the background group;
// tests count its starts.
var startBackupScheduler = func(bg *background, s *backup.Scheduler) { bg.add(s.Run) }

// newAPIHandler builds the HTTP handler; tests capture its Options.
var newAPIHandler = api.New

// reexec replaces this process with a fresh copy of the same binary, same
// arguments and environment. Tests replace it so they never exec.
var reexec = func() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find own executable: %w", err)
	}
	return syscall.Exec(exe, os.Args, os.Environ())
}

// serve runs the server and, when a staged host restore asked for it,
// re-executes the binary once everything is shut down and the lock released.
func serve(ctx context.Context, env *Env, args []string) error {
	var restart atomic.Bool
	if err := runServer(ctx, env, args, &restart); err != nil || !restart.Load() {
		return err
	}
	return reexec()
}

func runServer(ctx context.Context, env *Env, args []string, restart *atomic.Bool) error {
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
	// A host restore staged by the previous process is installed before the
	// database is opened. A half-installed one must not be served.
	installed, err := backup.ApplyPending(cfg.DataDir, time.Now())
	if err != nil {
		logger.Error("could not finish the pending host restore", "err", err)
		return fmt.Errorf("finish the pending host restore: %w", err)
	}
	// What is left of a host restore: recorded by ApplyPending, and kept
	// until each step has been done, so a crash or a failure is retried.
	// Unreadable, it could hide a schedule pause: a clone must not run with
	// the old host's schedule live.
	restoreWork, err := backup.ReadPostApply(cfg.DataDir)
	if err != nil {
		logger.Error("could not read the remaining host restore work", "err", err)
		return fmt.Errorf("finish the pending host restore: %w", err)
	}
	saveRestoreWork := func() {
		if err := backup.WritePostApply(cfg.DataDir, *restoreWork); err != nil {
			logger.Error("could not record the remaining host restore work", "err", err)
		}
	}
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
	if installed != nil {
		logger.Info("installed the restored database")
	}
	if restoreWork != nil && restoreWork.PauseSchedule {
		if err := pauseSchedule(ctx, db); err != nil {
			logger.Error("could not pause the restored backup schedule; retrying at the next start", "err", err)
		} else {
			logger.Info("paused the restored backup schedule")
			restoreWork.PauseSchedule = false
			saveRestoreWork()
		}
	}

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
	hist := history.NewStore(db)
	recorder := history.NewRecorder(cache, hist, logger, time.Now)
	sampler := metrics.NewSampler(metricsStore, cache, dockerEngine, cfg.MetricsInterval, cfg.MetricsRetention, logger, time.Now,
		func(ctx context.Context) error {
			_, err := actions.Prune(ctx, time.Now().Add(-365*24*time.Hour))
			return err
		},
		func(ctx context.Context) error {
			_, err := hist.Prune(ctx, time.Now().Add(-cfg.MetricsRetention))
			return err
		})
	checker, err := newUpdateChecker(cfg, logger)
	if err != nil {
		return err
	}
	// One peer store and service per process; the service's checker runs in
	// the background group so it stops before the database closes.
	peerStore := peers.NewStore(db, time.Now)
	peersSvc := peers.NewService(peers.ServiceConfig{
		Store:    peerStore,
		Version:  version.Get().Version,
		Hostname: os.Hostname,
		Now:      time.Now,
		Logger:   logger,
	})
	// A service context separate from the signal context: background work
	// must outlive the signal until HTTP and operations have drained.
	bg := startBackground(context.Background(), cache.Run, recorder.Run, sampler.Run, checker.Run, peersSvc.Run)
	defer bg.stop(time.Second) // safety net for early returns; the normal path stops it in order below

	stackRegistry := stacks.NewRegistry(db, time.Now)
	ops := stacks.NewOps(cfg.OperationTimeout, time.Now)
	storageSvc := storage.NewService(storage.ServiceConfig{Engine: dockerEngine, Registry: stackRegistry, Declarations: storage.NewDeclarationCache(buildRunner(cfg), logger), Now: time.Now, Ops: ops, Actions: actions})
	// The library service and the stack service need each other: stacks fetch
	// library images before up, the library asks stacks which images are used.
	registries := registry.NewService(registry.NewStore(db, time.Now), map[registry.Kind]registry.Provider{
		registry.KindDockerHub: &registry.HubProvider{},
		registry.KindECR:       &registry.ECRProvider{},
	}, actions, logger)
	// This server's identity; the hostname names it until the operator does.
	hostIdentity := func(ctx context.Context) (peers.Identity, error) {
		fallback, err := os.Hostname()
		if err != nil || fallback == "" {
			fallback = "stackstead"
		}
		return peerStore.Identity(ctx, fallback)
	}
	imagesDir := filepath.Join(cfg.DataDir, "images")
	imagesSvc := images.NewService(images.Deps{
		Registries: registries,
		Store:      images.NewStore(db, time.Now),
		GitHub:     &github.Client{},
		Settings:   images.NewSettingsStore(db),
		Ops:        ops,
		Engine:     dockerEngine,
		Dir:        imagesDir,
		Limits:     images.Limits{MaxFileBytes: cfg.ImageMaxFileBytes, MaxContextBytes: cfg.ImageMaxContextBytes},
		OpTimeout:  cfg.ImageOperationTimeout,
		Actions:    actions,
		Self: func(ctx context.Context) (images.Self, error) {
			id, err := hostIdentity(ctx)
			if err != nil {
				return images.Self{}, err
			}
			return images.Self{InstanceID: id.InstanceID, Name: id.Name, Slug: id.Slug}, nil
		},
		Peers:  peersSvc,
		Now:    time.Now,
		Logger: logger,
	})
	stackSvc := stacks.NewService(stacks.Deps{
		Library: imagesSvc,
		// A fetch runs inside the stack operation, so the operation gets the
		// longer image timeout when it has library images to ensure.
		LibraryTimeout: cfg.ImageOperationTimeout,
		Registry:       stackRegistry,
		Ops:            ops,
		Compose:        runner,
		Engine:         dockerEngine,
		Containers:     cache,
		StacksDir:      cfg.StacksDir,
		AllowedRoots:   cfg.AllowedRoots,
		Logger:         logger,
		Now:            time.Now,
	})
	imagesSvc.SetUsage(stackSvc)
	// Sync returns joined per-folder warnings; none of them stop startup.
	if err := stackSvc.Sync(ctx); err != nil {
		logger.Warn("stack sync reported problems", "err", err)
	}
	if err := imagesSvc.Start(ctx); err != nil {
		return fmt.Errorf("start image library: %w", err)
	}
	bg.add((&images.Sweeper{Service: imagesSvc}).Run)

	// Backups. A run interrupted by a restart is closed out before the
	// scheduler can start another.
	backupSettings := backup.NewConfigStore(db)
	backupRuns := backup.NewRunStore(db, time.Now)
	if err := backupRuns.MarkInterrupted(ctx); err != nil {
		logger.Warn("mark interrupted backup runs", "err", err)
	}
	backupHTTP := &http.Client{Transport: backupTransport()}
	backupRunner := backup.NewRunner(backup.Deps{
		DB:         db,
		Config:     backupSettings,
		Runs:       backupRuns,
		Stacks:     stackSvc,
		Registry:   stackRegistry,
		Engine:     dockerEngine,
		Ops:        ops,
		Actions:    actions,
		Identity:   hostIdentity,
		DataDir:    cfg.DataDir,
		ConfigPath: path,
		ImagesDir:  imagesDir,
		Version:    version.Version,
		Now:        time.Now,
		HTTP:       backupHTTP,
		Logger:     logger,
	})
	backupScheduler := &backup.Scheduler{
		Runner: backupRunner, Config: backupSettings, Now: time.Now, After: time.After, Loc: time.Local, Logger: logger,
	}
	backupSvc := &backup.Service{
		Settings: backupSettings, History: backupRuns, Runner: backupRunner, Scheduler: backupScheduler,
		Stacks: stackSvc, Sizes: dockerEngine, HTTP: backupHTTP, Now: time.Now, Loc: time.Local, Logger: logger,
	}
	if restoreWork != nil && restoreWork.PauseSchedule {
		// Running it now could write over the old host's archives.
		logger.Error("backup scheduler not started: the restored schedule could not be paused; restart Stackstead to retry")
	} else {
		startBackupScheduler(bg, backupScheduler)
	}

	hostRestorer := backup.HostRestorer{Env: backup.HostEnv{
		Engine: dockerEngine, DataDir: cfg.DataDir, AllowedRoots: cfg.AllowedRoots,
		Version: version.Version, HTTP: backupHTTP,
	}}
	restartRequested := make(chan struct{})
	var restartOnce sync.Once
	requestRestart := func() {
		restartOnce.Do(func() {
			restart.Store(true)
			close(restartRequested)
		})
	}

	// The collector and alerts engine do nothing until a request arrives:
	// both cache for 5 s and start no goroutines.
	collector := host.New(host.Config{
		ProcRoot: "/proc", SysRoot: "/sys", StacksDir: cfg.StacksDir,
		Docker: dockerEngine, Sampler: sampler, Containers: cache, Volumes: storageSvc,
		Statfs: host.OSStatfs{}, Now: time.Now, Hostname: os.Hostname, GOOS: runtime.GOOS,
	})
	alertsEngine := alerts.NewEngine(stackSvc, cache, collector, cache, sampler, time.Now, backupRunner)

	if !web.Built() {
		logger.Warn("web UI not built; serving placeholder page (run make ui)")
	}

	closing := make(chan struct{})
	srv := &http.Server{
		Addr: cfg.Listen,
		Handler: newAPIHandler(api.Options{
			Storage:          storageSvc,
			Actions:          actions,
			Auth:             authSvc,
			Limiter:          auth.NewLoginLimiter(5, 15*time.Minute, time.Now),
			TrustedProxies:   cfg.TrustedProxyPrefixes(),
			SetupToken:       setupToken,
			Static:           web.Dist(),
			Logger:           logger,
			Stacks:           stackSvc,
			Images:           imagesSvc,
			Registries:       registries,
			Backups:          backupSvc,
			HostRestore:      hostRestorer,
			RestoreOps:       ops,
			Restart:          requestRestart,
			Arch:             dockerEngine.Arch,
			Operations:       ops,
			FSRoots:          cfg.AllowedRoots,
			Status:           dockerStatus,
			MetricsInterval:  cfg.MetricsInterval,
			MetricsRetention: cfg.MetricsRetention,
			Closing:          closing,
			Peers:            peersSvc,
			Exec:             api.ExecOptions{Enabled: cfg.ExecEnabled, MaxSessions: cfg.ExecMaxSessions, IdleTimeout: cfg.ExecIdleTimeout},
			Updates:          checker,
			Version:          version.Get().Version,
			Host:             collector,
			Interfaces:       host.InterfaceLister{ProcRoot: "/proc", GOOS: runtime.GOOS},
			Alerts:           alertsEngine,
			Summary:          api.SummarySources{Store: metricsStore, Sampler: sampler},
			History:          hist,
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
	if restoreWork != nil && len(restoreWork.StartStacks) > 0 {
		bg.add(func(ctx context.Context) {
			if startRestoredStacks(ctx, stackSvc, actions, restoreWork.StartStacks, logger) {
				restoreWork.StartStacks = nil
				saveRestoreWork()
			}
		})
	}

	select {
	case err := <-serveErr:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	case <-restartRequested:
		logger.Info("restarting to finish the host restore")
	}
	logger.Info("shutting down")
	// Order: stop accepting HTTP (Closing ends streams), cancel running
	// operations (compose process groups get SIGTERM then SIGKILL), stop the
	// cache and sampler, then the deferred db.Close. Containers are never
	// stopped.
	shutdownErr := shutdownServices(cfg.ShutdownGrace, func() error {
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
	if shutdownErr != nil && restart.Load() {
		// No re-exec follows an error; the staged restore waits for a start.
		logger.Error("restore staged; start Stackstead again to finish it", "err", shutdownErr)
	}
	return shutdownErr
}

type stackStarter interface {
	ActWait(ctx context.Context, stack, action string, out func(string)) error
}

type actionAppender interface {
	Append(ctx context.Context, e actionlog.Entry) error
}

// startRestoredStacks brings up, one at a time, the stacks that were running
// when the restored backup was taken. A stack that fails is logged to the
// action log and the rest still start. It reports whether every stack was
// attempted, rather than cut short by shutdown.
func startRestoredStacks(ctx context.Context, starter stackStarter, actions actionAppender, names []string, logger *slog.Logger) bool {
	for _, name := range names {
		if ctx.Err() != nil {
			return false
		}
		err := starter.ActWait(ctx, name, "up", func(string) {})
		if err == nil {
			logger.Info("started restored stack", "stack", name)
			continue
		}
		logger.Warn("start restored stack failed", "stack", name, "err", err)
		entry := actionlog.Entry{
			Username: "system", Kind: "backup.restore", Scope: name, Target: name,
			Detail: "start after host restore: " + err.Error(), Result: "failed",
		}
		if aerr := actions.Append(context.WithoutCancel(ctx), entry); aerr != nil {
			logger.Warn("record restored stack start failure", "stack", name, "err", aerr)
		}
	}
	return ctx.Err() == nil
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
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// add starts one more runner under the same context, for services that exist
// only after the first runners were started.
func (b *background) add(run func(context.Context)) {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		run(b.ctx)
	}()
}

func startBackground(parent context.Context, runners ...func(context.Context)) *background {
	ctx, cancel := context.WithCancel(parent)
	b := &background{ctx: ctx, cancel: cancel}
	for _, run := range runners {
		b.add(run)
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

// backupTransport bounds connecting and waiting for a response, but not the
// transfer itself: an archive upload to S3 can legitimately take hours.
func backupTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSHandshakeTimeout = 15 * time.Second
	t.ResponseHeaderTimeout = 2 * time.Minute
	return t
}
