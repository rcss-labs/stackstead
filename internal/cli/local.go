package cli

import (
	"bufio"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/rcss-labs/stackstead/internal/config"
	"github.com/rcss-labs/stackstead/internal/lockfile"
	"github.com/rcss-labs/stackstead/internal/store"
)

// localFlags are the flags every host-side command shares.
type localFlags struct{ Config string }

func (lf *localFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&lf.Config, "config", "", "path to config.yaml (default: "+defaultConfigPath+" if it exists)")
}

// Seams so tests can fake the effective uid and the owner of files.
var geteuid = os.Geteuid

var fileOwner = realFileOwner

func realFileOwner(path string) (int, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("cannot determine the owner of %s", path)
	}
	return int(st.Uid), nil
}

// loadLocalConfig loads the config the way serve does: --config, else the
// default path when it exists, else defaults plus STACKSTEAD_* environment.
// An invalid config is a validation error (exit 6).
func loadLocalConfig(env *Env, lf localFlags) (config.Config, string, error) {
	path := lf.Config
	if path == "" {
		if _, err := os.Stat(defaultConfigPath); err == nil {
			path = defaultConfigPath
		}
	}
	cfg, err := config.Load(path, env.Getenv)
	if err != nil {
		return config.Config{}, path, &ExitError{Code: ExitValidation, Err: err}
	}
	return cfg, path, nil
}

func dbPathOf(cfg config.Config) string { return filepath.Join(cfg.DataDir, "stackstead.db") }

// foreignOwner reports the uid that owns the database (or, before it exists,
// the data directory) when the process runs as root and that owner is not
// root. Root must not open or create files in a directory the service user
// owns: the files would end up root-owned and lock the service out.
func foreignOwner(cfg config.Config) (uid int, foreign bool, err error) {
	if geteuid() != 0 {
		return 0, false, nil
	}
	for _, path := range []string{dbPathOf(cfg), cfg.DataDir} {
		owner, err := fileOwner(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, false, err
		}
		return owner, owner != 0, nil
	}
	return 0, false, nil
}

func ownerName(uid int) string {
	if u, err := user.LookupId(strconv.Itoa(uid)); err == nil {
		return u.Username
	}
	return strconv.Itoa(uid)
}

// checkOwnership is the root-over-foreign-owner refusal (exit 3).
func checkOwnership(cfg config.Config) error {
	uid, foreign, err := foreignOwner(cfg)
	if err != nil {
		return err
	}
	if !foreign {
		return nil
	}
	return exitf(ExitAuth, "%s belongs to uid %d; run this as that user: sudo -u %s stackstead …", dbPathOf(cfg), uid, ownerName(uid))
}

// openLocalDB opens the existing database without ever creating or migrating
// it, after the ownership check.
func openLocalDB(env *Env, cfg config.Config, opts store.OpenOptions) (*sql.DB, error) {
	if err := checkOwnership(cfg); err != nil {
		return nil, err
	}
	path := dbPathOf(cfg)
	db, err := store.OpenExisting(path, opts)
	var mismatch *store.SchemaMismatchError
	switch {
	case err == nil:
		return db, nil
	case errors.Is(err, store.ErrNoDatabase):
		return nil, exitf(ExitNotFound, "no database at %s: start the server once or run stackstead db init", path)
	case errors.As(err, &mismatch):
		return nil, &ExitError{Code: ExitConflict, Err: err}
	}
	return nil, err
}

// withExclusiveLock runs fn while holding <data_dir>/stackstead.lock. Callers
// run the ownership check first: a root-owned lock file would keep the
// service from starting.
func withExclusiveLock(cfg config.Config, fn func() error) error {
	lock, err := lockfile.Acquire(filepath.Join(cfg.DataDir, "stackstead.lock"))
	if err != nil {
		var held *lockfile.HeldError
		switch {
		case errors.As(err, &held):
			return exitf(ExitConflict, "the stackstead server is running (pid %d): stop it first", held.PID)
		case errors.Is(err, fs.ErrNotExist):
			return exitf(ExitNotFound, "data directory %s does not exist", cfg.DataDir)
		}
		return err
	}
	defer lock.Release()
	return fn()
}

// stdinIsTerminal asks the Env about stdin (fd 0 when stdin is not a file).
func stdinIsTerminal(env *Env) bool {
	if env.IsTerminal == nil {
		return false
	}
	fd := uintptr(0)
	if f, ok := env.Stdin.(*os.File); ok {
		fd = f.Fd()
	}
	return env.IsTerminal(fd)
}

// askYesNo prints prompt and reads one line from stdin. Used only until the
// shared prompt helpers land; anything but y/yes declines.
func askYesNo(env *Env, prompt string) bool {
	fmt.Fprint(env.Stdout, prompt+" [y/N] ")
	line, _ := bufio.NewReader(env.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// syncDir fsyncs a directory so renames inside it are durable.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}
