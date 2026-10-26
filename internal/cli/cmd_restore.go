package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/rcss-labs/stackstead/internal/backup"
	"github.com/rcss-labs/stackstead/internal/config"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/secret"
	"github.com/rcss-labs/stackstead/internal/version"
)

// Seams for tests: the engine calls a host restore makes.
var (
	restoreInspect = backup.InspectHost
	restoreRun     = backup.RestoreHost
	restoreApply   = backup.ApplyPending
)

// restoreOptions are the flags of `restore`.
type restoreOptions struct {
	yes, noStart, pathStyle bool
	endpoint, region        string
}

func restoreCommand() *Command {
	var lf localFlags
	var opts restoreOptions
	return &Command{
		Name: "restore", Short: "Restore this host from a backup archive (server must be stopped)",
		Usage:   "restore <s3://bucket/key | path> [--yes] [--no-start] [--s3-endpoint U] [--s3-region R] [--s3-path-style] [--config P]",
		MinArgs: 1, MaxArgs: 1,
		Long: "Restores the database, stacks, volumes and images of a whole-host archive and installs the restored database, " +
			"then exits; stacks that were running start when the server next starts. S3 credentials are read from " +
			"AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY, never from flags. Exits 5 while the server runs, 6 for an " +
			"archive from a newer Stackstead or a damaged one.",
		Flags: func(fs *flag.FlagSet) {
			lf.register(fs)
			fs.BoolVar(&opts.yes, "yes", false, "do not ask for confirmation")
			fs.BoolVar(&opts.noStart, "no-start", false, "do not start the stacks that were running")
			fs.StringVar(&opts.endpoint, "s3-endpoint", "", "S3-compatible endpoint URL (default: AWS)")
			fs.StringVar(&opts.region, "s3-region", "", "S3 region")
			fs.BoolVar(&opts.pathStyle, "s3-path-style", false, "use path-style S3 addressing")
		},
		Run: func(ctx context.Context, env *Env, _ *globalFlags, args []string) error {
			cfg, _, err := loadLocalConfig(env, lf)
			if err != nil {
				return err
			}
			return hostRestore(ctx, env, cfg, args[0], opts)
		},
	}
}

// parseRestoreSource turns the argument into a source: s3://bucket/key reads
// its keys from the environment, anything else is an archive path.
func parseRestoreSource(env *Env, arg string, opts restoreOptions) (backup.HostSource, error) {
	if !strings.HasPrefix(arg, "s3://") {
		if opts.endpoint != "" || opts.region != "" || opts.pathStyle {
			return backup.HostSource{}, exitf(ExitUsage, "--s3-* flags only apply to an s3:// source")
		}
		abs, err := filepath.Abs(arg)
		if err != nil {
			return backup.HostSource{}, err
		}
		return backup.HostSource{Kind: backup.SourcePath, Path: abs}, nil
	}
	u, err := url.Parse(arg)
	key := strings.TrimPrefix(u.Path, "/")
	if err != nil || u.Host == "" || key == "" {
		return backup.HostSource{}, exitf(ExitUsage, "%q is not s3://bucket/key", arg)
	}
	id, sk := env.Getenv("AWS_ACCESS_KEY_ID"), env.Getenv("AWS_SECRET_ACCESS_KEY")
	if id == "" || sk == "" {
		return backup.HostSource{}, exitf(ExitUsage, "an s3:// source needs AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY in the environment")
	}
	return backup.HostSource{Kind: backup.SourceS3, Key: key, S3: backup.S3Dest{
		Enabled: true, Bucket: u.Host, Region: opts.region, Endpoint: opts.endpoint, PathStyle: opts.pathStyle,
		AccessKeyID: id, SecretKey: secret.Secret(sk),
	}}, nil
}

func hostRestoreEnv(env *Env, cfg config.Config) backup.HostEnv {
	return backup.HostEnv{
		Engine: engine.New(cfg.DockerSocket, 2*time.Second), DataDir: cfg.DataDir, AllowedRoots: cfg.AllowedRoots,
		Version: version.Version, HTTP: &http.Client{Transport: backupTransport()},
		Out: func(line string) { fmt.Fprintln(env.Stdout, line) },
	}
}

// restoreExitError maps a refused restore to an exit code.
func restoreExitError(err error) error {
	switch {
	case errors.Is(err, backup.ErrArchiveNewer), errors.Is(err, backup.ErrArchiveDamaged),
		errors.Is(err, backup.ErrManifestMismatch), errors.Is(err, backup.ErrUnsafePath):
		return &ExitError{Code: ExitValidation, Err: err}
	case errors.Is(err, backup.ErrConflictsChanged), errors.Is(err, backup.ErrRestorePending):
		return &ExitError{Code: ExitConflict, Err: err}
	}
	return err
}

func hostRestore(ctx context.Context, env *Env, cfg config.Config, arg string, opts restoreOptions) error {
	src, err := parseRestoreSource(env, arg, opts)
	if err != nil {
		return err
	}
	if err := checkOwnership(cfg); err != nil {
		return err
	}
	return withExclusiveLock(cfg, func() error {
		hostEnv := hostRestoreEnv(env, cfg)
		sum, err := restoreInspect(ctx, src, hostEnv)
		if err != nil {
			return restoreExitError(err)
		}
		printRestoreSummary(env, arg, sum)
		if !opts.yes {
			if !stdinIsTerminal(env) {
				return exitf(ExitUsage, "restore overwrites the conflicts above; pass --yes to confirm")
			}
			ok, err := confirm(env, "Restore this host from the archive?", false)
			if err != nil {
				return err
			}
			if !ok {
				return exitf(ExitGeneral, "restore cancelled")
			}
		}
		if _, err := restoreRun(ctx, src, hostEnv, !opts.noStart, sum.ConflictsDigest); err != nil {
			return restoreExitError(err)
		}
		// Install the staged database now: the next start only has to run the stacks.
		if _, err := restoreApply(cfg.DataDir, env.Now()); err != nil {
			return fmt.Errorf("install restored database: %w", err)
		}
		fmt.Fprintln(env.Stdout, "restored; start the server to bring the stacks back up")
		return nil
	})
}

func printRestoreSummary(env *Env, arg string, sum backup.HostSummary) {
	m := sum.Manifest
	fmt.Fprintf(env.Stdout, "archive    %s\nhost       %s (Stackstead %s, %s)\nstacks     %d\n",
		arg, m.Host, m.StacksteadVersion, m.CreatedAt.Format(time.RFC3339), len(m.Stacks))
	if len(sum.Conflicts) == 0 {
		fmt.Fprintln(env.Stdout, "conflicts  none")
		return
	}
	fmt.Fprintln(env.Stdout, "conflicts")
	for _, c := range sum.Conflicts {
		fmt.Fprintf(env.Stdout, "  %-9s %-9s %s\n", c.Action, c.Kind, c.Name)
	}
}
