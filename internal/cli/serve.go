package cli

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/rcss-labs/stackstead/internal/alerts"
	"github.com/rcss-labs/stackstead/internal/api"
	"github.com/rcss-labs/stackstead/internal/auth"
	"github.com/rcss-labs/stackstead/internal/composecli"
	"github.com/rcss-labs/stackstead/internal/config"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/host"
	"github.com/rcss-labs/stackstead/internal/lockfile"
	"github.com/rcss-labs/stackstead/internal/maintenance"
	"github.com/rcss-labs/stackstead/internal/metrics"
	"github.com/rcss-labs/stackstead/internal/stacks"
	"github.com/rcss-labs/stackstead/internal/state"
	"github.com/rcss-labs/stackstead/internal/store"
	"github.com/rcss-labs/stackstead/internal/update"
	"github.com/rcss-labs/stackstead/internal/updatecontrol"
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

// StartupProvider is installed only by trusted updater composition. Nil
// probation preserves ordinary startup; lookup errors fail before migration.
type StartupProvider interface {
	StartupProbation(context.Context) (*update.Probation, error)
}

// ServeOptions injects trusted startup negotiation and its shared control gate.
// Production control must authenticate the job before thawing this gate.
type ServeOptions struct {
	Startup     StartupProvider
	Maintenance *maintenance.Gate
}

func ServeWithOptions(ctx context.Context, env *Env, args []string, opts ServeOptions) error {
	return serveWithOptions(ctx, env, args, opts)
}

// deferredStartup keeps probation writers dormant without holding up HTTP or
// shutdown. Ordinary startup retains its synchronous sync and runner ordering.
func deferredStartup(ctx context.Context, g *maintenance.Gate, syncStacks func(context.Context), runners ...func(context.Context)) *background {
	if release, err := g.Enter(); err == nil {
		bg := startBackground(ctx, runners...)
		syncStacks(ctx)
		release()
		return bg
	}
	return startBackground(ctx, func(ctx context.Context) {
		for {
			if err := g.Wait(ctx); err != nil {
				return
			}
			release, err := g.Enter()
			if err != nil {
				continue
			}
			if ctx.Err() != nil {
				release()
				return
			}
			syncStacks(ctx)
			release()
			break
		}
		child := startBackground(ctx, runners...)
		defer child.cancel()
		// Keep this wrapper in the outer group until every runner exits.
		// The outer stop owns the grace; inherited cancellation stops children.
		child.wg.Wait()
	})
}

func serve(ctx context.Context, env *Env, args []string) error {
	return serveWithOptions(ctx, env, args, ServeOptions{})
}

func serveWithOptions(ctx context.Context, env *Env, args []string, opts ServeOptions) error {
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
	gate := opts.Maintenance
	var locator *update.ClientLocator
	if opts.Startup == nil {
		resolvedPath := path
		if path != "" {
			resolvedPath, err = filepath.Abs(path)
			if err == nil {
				resolvedPath, err = filepath.EvalSymlinks(resolvedPath)
			}
			if err != nil {
				return fmt.Errorf("resolve updater config: %w", err)
			}
		}
		locator, err = lookupUpdater(resolvedPath, uint32(os.Getuid()), updaterLocatorReader)
		if err != nil {
			return fmt.Errorf("trusted updater locator: %w", err)
		}
		if locator != nil {
			opts.Startup = locatorStartup{locator: *locator}
			if gate == nil {
				gate = maintenance.New()
			}
		}
	}
	var bootTicket *update.Probation
	if opts.Startup != nil {
		probation, err := opts.Startup.StartupProbation(ctx)
		if err != nil {
			return fmt.Errorf("startup probation: %w", err)
		}
		bootTicket = probation
		if probation != nil {
			if gate == nil {
				return errors.New("startup probation requires a shared maintenance gate")
			}
			if err := gate.Freeze(); err != nil {
				return fmt.Errorf("freeze startup probation: %w", err)
			}
		}
	}

	db, err := store.Open(filepath.Join(cfg.DataDir, "stackstead.db"))
	if err != nil {
		return err
	}
	defer db.Close()

	authSvc, err := auth.NewService(db, time.Now, auth.WithMaintenance(gate))
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

	startupRelease, admissionErr := gate.Enter()
	probationStartup := errors.Is(admissionErr, maintenance.ErrFrozen)
	if !probationStartup {
		if admissionErr != nil {
			return admissionErr
		}
		err := os.MkdirAll(cfg.StacksDir, 0o755)
		startupRelease()
		if err != nil {
			return fmt.Errorf("create stacks dir: %w", err)
		}
	}
	// The cache follows Docker events and serves container status to the
	// stack service, the sampler and the observability routes.
	cache := state.New(dockerEngine, logger, state.Backoff{})
	metricsStore := metrics.NewStore(db, time.Now)
	sampler := metrics.NewSampler(admittedMetricsStore{Store: metricsStore, gate: gate}, cache, dockerEngine, cfg.MetricsInterval, cfg.MetricsRetention, logger, time.Now)
	// A service context separate from the signal context: background work
	// must outlive the signal until HTTP and operations have drained.
	ops := stacks.NewOps(cfg.OperationTimeout, time.Now)
	ops.SetMaintenance(gate)
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
	syncStacks := func(syncCtx context.Context) {
		if probationStartup {
			if err := os.MkdirAll(cfg.StacksDir, 0o755); err != nil {
				logger.Warn("create stacks dir after probation", "err", err)
				return
			}
		} else {
			syncCtx = ctx // preserve ordinary startup cancellation during sync
		}
		if err := stackSvc.Sync(syncCtx); err != nil {
			logger.Warn("stack sync reported problems", "err", err)
		}
	}
	bg := deferredStartup(context.Background(), gate, syncStacks, cache.Run, sampler.Run)
	defer bg.stop(time.Second)

	// The collector and alerts engine do nothing until a request arrives:
	// both cache for 5 s and start no goroutines.
	collector := host.New(host.Config{
		ProcRoot: "/proc", SysRoot: "/sys", StacksDir: cfg.StacksDir,
		Docker: dockerEngine, Sampler: sampler, Containers: cache,
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
			Auth:             authSvc,
			Maintenance:      gate,
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

	var control *updatecontrol.State
	if locator != nil {
		in := update.Instance{ID: locator.InstanceID, UID: uint32(os.Getuid()), ControlSocket: locator.ControlSocket}
		l, e := updatecontrol.Listen(in)
		if e != nil {
			return fmt.Errorf("app control socket: %w", e)
		}
		control = updatecontrol.NewState(in, gate, bootTicket)
		controlCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		defer l.Close()
		go updatecontrol.Serve(controlCtx, l, control)
	}
	listener, err := listenForReadiness(cfg.Listen, func() error {
		if control == nil {
			return nil
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return fmt.Errorf("read readiness config: %w", e)
		}
		hash := sha256.Sum256(b)
		identity := version.Get()
		control.MarkReady(update.Readiness{InstanceID: locator.InstanceID, Identity: update.Identity{Version: strings.TrimPrefix(identity.Version, "v"), Commit: identity.Commit, OS: identity.OS, Arch: identity.Arch}, PID: os.Getpid(), Schema: store.SupportedSchema(), ConfigSHA256: hex.EncodeToString(hash[:]), Ready: true})
		return nil
	})
	if err != nil {
		return fmt.Errorf("http listener: %w", err)
	}
	defer listener.Close()
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(listener) }()
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

// admittedMetricsStore serializes persistence with maintenance admission.
type admittedMetricsStore struct {
	*metrics.Store
	gate *maintenance.Gate
}

// InsertBatch and Prune skip persistence while frozen, including subsequent
// freezes after startup. In-flight DB writes retain admission until return.
func (s admittedMetricsStore) InsertBatch(ctx context.Context, samples []metrics.Sample) error {
	release, err := s.gate.Enter()
	if errors.Is(err, maintenance.ErrFrozen) {
		return nil
	}
	if err != nil {
		return err
	}
	defer release()
	return s.Store.InsertBatch(ctx, samples)
}
func (s admittedMetricsStore) Prune(ctx context.Context, olderThan time.Time) (int64, error) {
	release, err := s.gate.Enter()
	if errors.Is(err, maintenance.ErrFrozen) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer release()
	return s.Store.Prune(ctx, olderThan)
}
