package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/update"
	"github.com/rcss-labs/stackstead/internal/version"
)

type upgradeFlags struct {
	check, force, yes bool
	version, baseURL  string
}

// Seams so tests need no root, no real installer and no real filesystem layout.
var (
	upgradeExecutable = update.Executable
	upgradeExists     = func(p string) bool { _, err := os.Stat(p); return err == nil }
	upgradeLookPath   = func(name string) (string, error) { return lookInDirs(fixedToolDirs, name) }
	upgradeLocked     = update.DpkgFrontendLocked
	upgradeRunner     = runInstaller
	upgradeTempDir    = func() (string, error) { return os.MkdirTemp("", "stackstead-upgrade.") }
	upgradeGOOS       = runtime.GOOS
	upgradeGOARCH     = runtime.GOARCH
)

func upgradeCommand() *Command {
	var f upgradeFlags
	return &Command{
		Name: "upgrade", Short: "Upgrade this host to the latest release (or check for one)",
		Usage: "upgrade [--check] [--version vX.Y.Z] [--force] [--yes] [--base-url URL] [-o json]",
		Long: "Downloads the release asset that matches how this host was installed (install.sh, .deb, .rpm or .pkg),\n" +
			"verifies it against the release's SHA256SUMS and hands it to the native installer. Nothing is changed\n" +
			"before verification. The installer restarts the service; upgrade never does.\n" +
			"--check only reports whether a newer release exists. Exit codes: 0 done or nothing to do, 1 installer\n" +
			"failed, 2 usage or no terminal without --yes, 3 needs root, 5 downgrade or reinstall without --force or\n" +
			"dpkg busy, 6 verification failed, 7 release source unreachable.",
		MaxArgs: 0,
		Flags: func(fs *flag.FlagSet) {
			fs.BoolVar(&f.check, "check", false, "only report whether an update is available")
			fs.BoolVar(&f.force, "force", false, "reinstall the same version or downgrade (script installs only)")
			fs.BoolVar(&f.yes, "yes", false, "do not ask for confirmation")
			fs.StringVar(&f.version, "version", "", "install this release (vX.Y.Z) instead of the latest")
			fs.StringVar(&f.baseURL, "base-url", "", "release source URL (mirrors and tests)")
		},
		Run: func(ctx context.Context, env *Env, g *globalFlags, _ []string) error {
			return runUpgrade(ctx, env, g, f)
		},
	}
}

// withV renders a version as vX.Y.Z; text that is not a release version
// (a development build) stays as it is.
func withV(s string) string {
	if v, err := update.Parse(s); err == nil {
		return "v" + v.String()
	}
	return s
}

func runUpgrade(ctx context.Context, env *Env, g *globalFlags, f upgradeFlags) error {
	if f.check && (f.force || f.yes) {
		return exitf(ExitUsage, "--check cannot be combined with --force or --yes")
	}
	if f.version != "" {
		if _, err := update.Parse(f.version); err != nil {
			return exitf(ExitUsage, "invalid --version %q: use vX.Y.Z", f.version)
		}
	}
	baseURL := f.baseURL
	if baseURL == "" {
		baseURL = update.DefaultBaseURL
	}
	cur := version.Get().Version
	src, err := update.NewSource(baseURL, "stackstead/"+cur)
	if err != nil {
		return exitf(ExitUsage, "invalid --base-url %q: use https://, or http:// to a loopback host", f.baseURL)
	}
	if !src.IsDefault() {
		fmt.Fprintf(env.Stderr, "warning: using a non-default release source: %s\n", baseURL)
	}

	goos, exe := upgradeGOOS, upgradeExecutable()
	kind := update.Detect(goos, exe, upgradeExists)

	var rel update.Release
	if f.version != "" {
		rel, err = src.Release(ctx, f.version)
	} else {
		rel, err = src.Latest(ctx)
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if f.check && g.Output == "json" {
			// The status says why; the exit code still reports the failure.
			if perr := newPrinter(env, g).JSONValue(checkStatus(cur, kind, goos, exe, update.Release{}, err)); perr != nil {
				return perr
			}
		}
		return exitf(releaseExit(err), "could not resolve the release: %v", err)
	}
	target := "v" + rel.Version.String()
	curV, curErr := update.Parse(cur)
	haveCur := curErr == nil
	order := 0 // installed relative to target: 1 means installed is newer
	if haveCur {
		order = update.Compare(curV, rel.Version)
	}

	if f.check {
		st := checkStatus(cur, kind, goos, exe, rel, nil)
		st.CheckedAt = env.Now()
		return printCheck(env, g, st, cur, target, order)
	}

	switch {
	case !haveCur && !f.force:
		return exitf(ExitConflict, "this is a development build (%s); use --force to install %s over it", cur, target)
	case haveCur && order > 0 && !f.force && kind != update.KindDeb && kind != update.KindRPM:
		return exitf(ExitConflict, "installed %s is newer than %s; use --force to downgrade", withV(cur), target)
	case haveCur && order == 0 && !f.force:
		fmt.Fprintf(env.Stdout, "stackstead %s is already installed\n", target)
		return nil
	}

	asset, err := rel.AssetName(kind, goos, upgradeGOARCH)
	if err != nil {
		return exitf(ExitGeneral, "no release asset for this host: %v", err)
	}
	plan := update.Plan{Kind: kind, Version: rel.Version, Current: curV, HaveCurrent: haveCur, Force: f.force}
	if kind == update.KindScript {
		if plan.Prefix, err = update.PrefixOf(exe); err != nil {
			return exitf(ExitGeneral, "cannot upgrade: stackstead is at %s, expected <prefix>/bin/stackstead", exe)
		}
		// Always explicit, so STACKSTEAD_BASE_URL in the environment cannot
		// redirect install.sh's tarball fetch.
		plan.BaseURL = baseURL
	}
	if kind == update.KindRPM {
		_, dnfErr := upgradeLookPath("dnf")
		plan.HaveDNF = dnfErr == nil
	}

	// Preflight with a placeholder path: Commands is pure and tells us the
	// tool name and whether a package downgrade is refused, before anything
	// is downloaded.
	plan.Asset = "/path/to/" + asset
	argv, _, err := update.Commands(plan)
	if err != nil {
		return upgradePlanError(env, err, kind, asset, rel, baseURL)
	}
	tool := filepath.Base(argv[0])
	if kind == update.KindScript {
		tool = "install.sh"
	}

	if err := checkUpgradePrivileges(env, kind, goos, exe); err != nil {
		return err
	}

	printUpgradeTips(env, kind, goos, exe, plan)
	verb := "Upgrade " + withV(cur) + " → " + target
	switch {
	case !haveCur:
		verb = "Install " + target + " over " + cur
	case order == 0:
		verb = "Reinstall " + target
	case order > 0:
		verb = "Downgrade " + withV(cur) + " → " + target
	}
	ok, err := confirm(env, fmt.Sprintf("%s with %s? The service restarts.", verb, tool), f.yes)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintln(env.Stderr, "aborted")
		return nil
	}

	dir, err := upgradeTempDir()
	if err != nil {
		return exitf(ExitGeneral, "create a temporary directory: %v", err)
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0o755); err != nil { // apt downloads as _apt and must read the file
		return exitf(ExitGeneral, "prepare the temporary directory: %v", err)
	}
	if plan.Asset, err = src.Download(ctx, rel, asset, dir); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return exitf(releaseExit(err), "download %s: %v", asset, err)
	}

	if err := ctx.Err(); err != nil {
		return err // interrupted after the download: do not start an installer
	}
	err = update.Apply(ctx, plan, upgradeRunner, upgradeLocked)
	switch {
	case err == nil:
	case errors.Is(err, update.ErrBusy):
		return exitf(ExitConflict, "another package manager is running (the dpkg lock is held); try again when it finishes")
	case errors.Is(err, update.ErrDowngradeManual):
		return upgradePlanError(env, err, kind, asset, rel, baseURL)
	default:
		code := installerExit(kind, err)
		var coder interface{ ExitCode() int }
		if errors.As(err, &coder) && coder.ExitCode() < 0 {
			return exitf(code, "%s was terminated by a signal", tool)
		}
		if errors.As(err, &coder) {
			return exitf(code, "%s exited %d; see its output above", tool, coder.ExitCode())
		}
		return exitf(code, "%s failed: %v", tool, err)
	}

	if g.Output == "json" {
		return newPrinter(env, g).JSONValue(map[string]string{"from": withV(cur), "to": target, "installer": string(kind)})
	}
	switch {
	case !haveCur:
		fmt.Fprintf(env.Stdout, "installed stackstead %s (was %s)\n", target, cur)
	case order == 0:
		fmt.Fprintf(env.Stdout, "reinstalled stackstead %s\n", target)
	default:
		fmt.Fprintf(env.Stdout, "upgraded stackstead %s → %s\n", withV(cur), target)
	}
	fmt.Fprintln(env.Stdout, "the installer restarted the service if it was running")
	return nil
}

// releaseExit maps a release-source error: unreadable source 7, anything the
// source returned that cannot be trusted 6.
func releaseExit(err error) int {
	var pathErr *fs.PathError
	switch {
	case errors.As(err, &pathErr): // a local file problem (disk full, permissions), not the network
		return ExitGeneral
	case update.ErrorCode(err) == "malformed":
		return ExitValidation
	}
	return ExitUnavailable
}

// upgradePlanError reports a refused package downgrade (exit 5) with how to do
// it by hand: where the asset is, what its checksum must be, the command.
func upgradePlanError(env *Env, err error, kind update.Kind, asset string, rel update.Release, baseURL string) error {
	if !errors.Is(err, update.ErrDowngradeManual) {
		return exitf(ExitGeneral, "cannot plan the upgrade: %v", err)
	}
	install := "sudo apt-get install -y --allow-downgrades ./" + asset
	if kind == update.KindRPM {
		install = "sudo rpm -U --oldpackage ./" + asset
	}
	fmt.Fprintf(env.Stderr, "stackstead does not downgrade packages. To do it by hand:\n")
	fmt.Fprintf(env.Stderr, "  curl -fLO %s/releases/download/v%s/%s\n", strings.TrimRight(baseURL, "/"), rel.Version, asset)
	if sum, serr := update.ChecksumFor(rel.Sums, asset); serr == nil {
		fmt.Fprintf(env.Stderr, "  echo '%s  %s' | sha256sum -c -\n", sum, asset)
	}
	fmt.Fprintf(env.Stderr, "  %s\n", install)
	return &ExitError{Code: ExitConflict, Err: errors.New("package downgrade refused")}
}

func checkUpgradePrivileges(env *Env, kind update.Kind, goos, exe string) error {
	needsRoot := fmt.Sprintf("needs root: run %s", update.UpgradeCommand(kind, goos, exe))
	switch {
	case goos != "darwin", kind == update.KindPkg:
		if geteuid() != 0 {
			return exitf(ExitAuth, "%s", needsRoot)
		}
	case geteuid() == 0:
		// macOS script install: root is only fine through sudo from a user account.
		if u := env.Getenv("SUDO_USER"); u == "" || u == "root" {
			return exitf(ExitAuth, "running as root: run this as your own user, or use 'sudo %s upgrade' from your account so SUDO_USER is set", exe)
		}
	}
	return nil
}

// printUpgradeTips prints the backup suggestion and, for a forced downgrade,
// the schema warning.
func printUpgradeTips(env *Env, kind update.Kind, goos, exe string, plan update.Plan) {
	// UpgradeCommand quotes the path; reuse that for the tip.
	quoted := strings.TrimSuffix(strings.TrimPrefix(update.UpgradeCommand(kind, goos, exe), "sudo "), " upgrade")
	dest := "stackstead-" + env.Now().Format("20060102") + ".db"
	if goos == "darwin" {
		fmt.Fprintf(env.Stderr, "before upgrading you can back up the database: %s db backup ~/%s\n", quoted, dest)
	} else {
		fmt.Fprintf(env.Stderr, "before upgrading you can back up the database: sudo -u stackstead %s db backup /var/backups/%s\n", quoted, dest)
	}
	if plan.HaveCurrent && update.Compare(plan.Current, plan.Version) > 0 {
		fmt.Fprintln(env.Stderr, "warning: downgrading cannot undo database migrations; if the service then refuses to start, restore the backup with 'db restore'")
	}
}

func printCheck(env *Env, g *globalFlags, st apitypes.UpdateStatus, cur, target string, order int) error {
	if g.Output == "json" {
		return newPrinter(env, g).JSONValue(st)
	}
	latest := target
	if st.NotesURL != "" {
		latest += " (" + st.NotesURL + ")"
	}
	fmt.Fprintf(env.Stdout, "installed: %s\nlatest:    %s\n", withV(cur), latest)
	switch st.State {
	case "available":
		fmt.Fprintf(env.Stdout, "update: available — run: %s\n", st.Command)
	case "up-to-date":
		if order == 0 {
			fmt.Fprintf(env.Stdout, "update: none, %s is the latest\n", target)
		} else {
			fmt.Fprintf(env.Stdout, "update: none, installed %s is newer than %s\n", withV(cur), target)
		}
	default:
		fmt.Fprintf(env.Stdout, "update: unknown, this is a development build (%s); run: %s --force\n", cur, st.Command)
	}
	return nil
}

// checkStatus is the UpdateStatus for this host; state comes from comparing
// the installed version with rel. A failed lookup is "failed" with an error code.
func checkStatus(cur string, kind update.Kind, goos, exe string, rel update.Release, err error) apitypes.UpdateStatus {
	st := apitypes.UpdateStatus{
		Current:   strings.TrimPrefix(cur, "v"),
		Installer: string(kind),
		Command:   update.UpgradeCommand(kind, goos, exe),
		State:     "unknown",
	}
	if v, perr := update.Parse(cur); perr == nil {
		st.Current = v.String()
	}
	if err != nil {
		st.State, st.Error = "failed", update.ErrorCode(err)
		return st
	}
	st.Latest, st.NotesURL = rel.Version.String(), rel.NotesURL()
	if v, perr := update.Parse(cur); perr == nil {
		st.State = "up-to-date"
		if update.Compare(v, rel.Version) < 0 {
			st.State = "available"
		}
	}
	return st
}

// installerExit maps an installer's exit status to the CLI exit code. Only
// install.sh speaks the 6/5/8 language (verification, unreachable, needs a
// user); apt-get, dnf, rpm and installer failures are plain 1. A child killed
// by a signal (ExitCode -1) means interrupted.
func installerExit(kind update.Kind, err error) int {
	var coder interface{ ExitCode() int }
	if !errors.As(err, &coder) {
		return ExitGeneral
	}
	code := coder.ExitCode()
	if code < 0 {
		return ExitInterrupted
	}
	if kind != update.KindScript {
		return ExitGeneral
	}
	switch code {
	case 6:
		return ExitValidation
	case 5:
		return ExitUnavailable
	case 8:
		return ExitAuth
	case 130:
		return ExitInterrupted
	}
	return ExitGeneral
}

// runInstaller runs one fixed argv with the terminal's streams. It does not use
// CommandContext: killing dpkg or installer mid-transaction is worse than
// letting it finish. A terminal Ctrl-C still reaches the child through the
// shared process group; we deliberately avoid Setpgid so install.sh's sudo
// prompt on /dev/tty keeps working.
func runInstaller(_ context.Context, name string, args, extraEnv []string) error {
	cmd := exec.Command(name, args...)
	cmd.Env = installerEnv(os.Environ(), geteuid(), extraEnv)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// installerEnvPath is what root's installers resolve helpers through: macOS
// sudo keeps the caller's PATH, which may name user-writable directories.
const installerEnvPath = "/usr/sbin:/usr/bin:/sbin:/bin"

var fixedToolDirs = strings.Split(installerEnvPath, ":")

// lookInDirs finds an executable file in dirs only (never the process PATH).
func lookInDirs(dirs []string, name string) (string, error) {
	for _, d := range dirs {
		p := filepath.Join(d, name)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s not found in %s", name, strings.Join(dirs, ":"))
}

// installerEnv is the child environment: STACKSTEAD_* never reaches an
// installer (STACKSTEAD_BASE_URL would redirect install.sh's own downloads),
// and root gets a fixed PATH.
func installerEnv(base []string, euid int, extra []string) []string {
	env := make([]string, 0, len(base)+len(extra)+1)
	for _, kv := range base {
		if strings.HasPrefix(kv, "STACKSTEAD_") || (euid == 0 && strings.HasPrefix(kv, "PATH=")) {
			continue
		}
		env = append(env, kv)
	}
	if euid == 0 {
		env = append(env, "PATH="+installerEnvPath)
	}
	return append(env, extra...)
}
