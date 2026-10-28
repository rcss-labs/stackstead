package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
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
	passphraseFile          string
	allowUnencrypted        bool
}

func restoreCommand() *Command {
	var lf localFlags
	var opts restoreOptions
	return &Command{
		Name: "restore", Short: "Restore this host from a backup archive (server must be stopped)",
		Usage:   "restore <s3://bucket/key | path> [--yes] [--no-start] [--passphrase-file P] [--allow-unencrypted] [--s3-endpoint U] [--s3-region R] [--s3-path-style] [--config P]",
		MinArgs: 1, MaxArgs: 1,
		Long: "Restores the database, stacks, volumes and images of a whole-host archive and installs the restored database, " +
			"then exits; stacks that were running start when the server next starts. S3 credentials are read from " +
			"AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY, never from flags. An encrypted archive (.tar.gz.enc) needs " +
			"its passphrase: --passphrase-file, else STACKSTEAD_BACKUP_PASSPHRASE, else the key file in the data " +
			"directory, else a prompt on a terminal. The whole archive is checked before anything changes; a modified " +
			"archive is refused. An unencrypted archive cannot be authenticated and is refused unless you pass " +
			"--allow-unencrypted. Exits 5 while the server runs, 6 for an archive from a newer Stackstead, a damaged " +
			"or modified one, a wrong or missing passphrase, or an unencrypted one without --allow-unencrypted.",
		Flags: func(fs *flag.FlagSet) {
			lf.register(fs)
			fs.BoolVar(&opts.yes, "yes", false, "do not ask for confirmation")
			fs.BoolVar(&opts.noStart, "no-start", false, "do not start the stacks that were running")
			fs.StringVar(&opts.endpoint, "s3-endpoint", "", "S3-compatible endpoint URL (default: AWS)")
			fs.StringVar(&opts.region, "s3-region", "", "S3 region")
			fs.BoolVar(&opts.pathStyle, "s3-path-style", false, "use path-style S3 addressing")
			fs.StringVar(&opts.passphraseFile, "passphrase-file", "", "file holding the archive passphrase (first line)")
			fs.BoolVar(&opts.allowUnencrypted, "allow-unencrypted", false, "restore an archive that is neither encrypted nor authenticated")
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
		Out: func(line string) {
			fmt.Fprintln(env.Stdout, line)
		},
	}
}

const (
	passphraseEnv         = "STACKSTEAD_BACKUP_PASSPHRASE"
	hintPassphrase        = "pass --passphrase-file or set " + passphraseEnv
	hintAllowUnencrypted  = "pass --allow-unencrypted if you trust the source of this archive"
	unencryptedNoticeLine = "encryption  none (unauthenticated archive)"
)

// restoreExitError maps a refused restore to an exit code.
func restoreExitError(err error) error {
	switch {
	case errors.Is(err, backup.ErrUnencryptedArchive):
		return &ExitError{Code: ExitValidation, Err: fmt.Errorf("%w; %s", err, hintAllowUnencrypted)}
	case errors.Is(err, backup.ErrPassphraseRequired), errors.Is(err, backup.ErrWrongPassphrase):
		return &ExitError{Code: ExitValidation, Err: fmt.Errorf("%w; %s", err, hintPassphrase)}
	case errors.Is(err, backup.ErrArchiveNewer), errors.Is(err, backup.ErrArchiveDamaged), errors.Is(err, backup.ErrArchiveTampered),
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
	if opts.passphraseFile != "" {
		pass, err := readSecret(ctx, env, secretSource{File: opts.passphraseFile}, "", false)
		if err != nil {
			return err
		}
		src.Passphrase = secret.Secret(pass)
	} else if pass := env.Getenv(passphraseEnv); pass != "" {
		src.Passphrase = secret.Secret(pass)
	}
	// A replacement host has no data directory yet; the stacks directories are created as stacks are restored.
	// Root must not create it: the service user would then be locked out of it.
	if _, err := os.Stat(cfg.DataDir); errors.Is(err, fs.ErrNotExist) && geteuid() == 0 {
		return exitf(ExitAuth, "data directory %s does not exist; create it owned by the service user (sudo install -d -o stackstead -m 0700 %s), then run the restore as that user: sudo -u stackstead stackstead restore …", cfg.DataDir, cfg.DataDir)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	return withExclusiveLock(cfg, func() error {
		hostEnv := hostRestoreEnv(env, cfg)
		sum, err := restoreInspect(ctx, src, hostEnv)
		if errors.Is(err, backup.ErrPassphraseRequired) && stdinIsTerminal(env) {
			pass, perr := readSecret(ctx, env, secretSource{}, "Archive passphrase: ", false)
			if perr != nil {
				return perr
			}
			src.Passphrase = secret.Secret(pass)
			sum, err = restoreInspect(ctx, src, hostEnv)
		}
		if err != nil {
			return restoreExitError(err)
		}
		printRestoreSummary(env, arg, sum)
		// Refused before the confirmation: confirming cannot make an
		// unauthenticated archive trustworthy, only the flag says the operator
		// vouches for where it came from.
		if !sum.Encrypted && !opts.allowUnencrypted {
			return restoreExitError(backup.ErrUnencryptedArchive)
		}
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
		report, err := restoreRun(ctx, src, hostEnv, backup.HostRestoreOptions{StartStacks: !opts.noStart, AllowUnencrypted: opts.allowUnencrypted, ConfirmedDigest: sum.ConflictsDigest})
		if err != nil {
			return restoreExitError(err)
		}
		// Install the staged database now: the next start only has to run the stacks.
		if _, err := restoreApply(cfg.DataDir, env.Now()); err != nil {
			return fmt.Errorf("install restored database: %w", err)
		}
		problems := printRestoreProblems(env, report)
		if problems > 0 {
			return exitf(ExitOperationFailed, "restore completed with %d problem(s); the database is installed, see the items above", problems)
		}
		fmt.Fprintln(env.Stdout, "restore complete; start the Stackstead service to bring the stacks back up")
		return nil
	})
}

// printRestoreProblems lists the items that did not restore cleanly and
// returns how many of them failed.
func printRestoreProblems(env *Env, report backup.HostReport) (failed int) {
	for _, it := range report.Items {
		if it.Status == "ok" {
			continue
		}
		if it.Status == "failed" {
			failed++
		}
		fmt.Fprintf(env.Stdout, "  %-8s %-7s %s: %s\n", it.Status, it.Kind, strings.Join(nonEmpty(it.Stack, it.Name), "/"), it.Note)
	}
	return failed
}

func nonEmpty(parts ...string) []string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func printRestoreSummary(env *Env, arg string, sum backup.HostSummary) {
	m := sum.Manifest
	volumes, images := 0, 0
	for _, st := range m.Stacks {
		volumes += len(st.Volumes)
		images += len(st.Images)
	}
	fmt.Fprintf(env.Stdout, "archive    %s\nhost       %s (Stackstead %s, %s)\nstatus     %s\nstacks     %d\nvolumes    %d\nimages     %d\n",
		arg, m.Host, m.StacksteadVersion, m.CreatedAt.Format(time.RFC3339), m.Status, len(m.Stacks), volumes, images)
	if sum.Encrypted {
		fmt.Fprintf(env.Stdout, "encryption  key %s\n", sum.KeyID)
	} else {
		fmt.Fprintln(env.Stdout, unencryptedNoticeLine)
	}
	fmt.Fprintln(env.Stdout, "This host will take over the backed-up host's identity; don't run both at once.")
	if len(sum.Conflicts) == 0 {
		fmt.Fprintln(env.Stdout, "conflicts  none")
		return
	}
	fmt.Fprintln(env.Stdout, "conflicts")
	for _, c := range sum.Conflicts {
		fmt.Fprintf(env.Stdout, "  %-9s %-9s %s\n", c.Action, c.Kind, c.Name)
	}
}
