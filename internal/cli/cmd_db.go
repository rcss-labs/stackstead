package cli

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/rcss-labs/stackstead/internal/config"
	"github.com/rcss-labs/stackstead/internal/store"
)

// dbCheckReport is the JSON of `db check`.
type dbCheckReport struct {
	Path            string           `json:"path"`
	Schema          int              `json:"schema"`
	SupportedSchema int              `json:"supportedSchema"`
	SizeBytes       int64            `json:"sizeBytes"`
	WalBytes        int64            `json:"walBytes"`
	OK              bool             `json:"ok"`
	Problems        []string         `json:"problems"`
	Counts          map[string]int64 `json:"counts"`
}

var dbCountTables = []string{"users", "sessions", "stacks", "metric_samples", "settings"}

func dbCommand() *Command {
	var lf localFlags
	var force, yes bool
	return &Command{
		Name: "db", Short: "Local database commands (run as the service user)",
		Sub: []*Command{
			{Name: "init", Short: "Create and migrate the database", Usage: "db init [--config P]", Flags: lf.register,
				Run: func(_ context.Context, env *Env, _ *globalFlags, _ []string) error {
					cfg, _, err := loadLocalConfig(env, lf)
					if err != nil {
						return err
					}
					return dbInit(env, cfg)
				}},
			{Name: "check", Short: "Check database integrity", Usage: "db check [--config P]", Flags: lf.register,
				Run: func(ctx context.Context, env *Env, g *globalFlags, _ []string) error {
					cfg, _, err := loadLocalConfig(env, lf)
					if err != nil {
						return err
					}
					return dbCheck(ctx, env, g, cfg)
				}},
			{Name: "backup", Short: "Back up the database (online-safe)", Usage: "db backup <dest> [--force] [--config P]", MinArgs: 1, MaxArgs: 1,
				Flags: func(fs *flag.FlagSet) {
					lf.register(fs)
					fs.BoolVar(&force, "force", false, "replace an existing destination")
				},
				Run: func(ctx context.Context, env *Env, _ *globalFlags, args []string) error {
					cfg, _, err := loadLocalConfig(env, lf)
					if err != nil {
						return err
					}
					return dbBackup(ctx, env, cfg, args[0], force)
				}},
			{Name: "restore", Short: "Restore the database (server must be stopped)", Usage: "db restore <src> [--yes] [--config P]", MinArgs: 1, MaxArgs: 1,
				Flags: func(fs *flag.FlagSet) { lf.register(fs); fs.BoolVar(&yes, "yes", false, "do not ask for confirmation") },
				Run: func(ctx context.Context, env *Env, _ *globalFlags, args []string) error {
					cfg, _, err := loadLocalConfig(env, lf)
					if err != nil {
						return err
					}
					return dbRestore(ctx, env, cfg, args[0], yes)
				}},
		},
	}
}

func dbInit(env *Env, cfg config.Config) error {
	if err := checkOwnership(cfg); err != nil {
		return err
	}
	if _, err := os.Stat(cfg.DataDir); errors.Is(err, fs.ErrNotExist) && geteuid() == 0 {
		// Root would create the directory root-owned and lock the service out.
		return exitf(ExitAuth, "%s does not exist; create it as the service user: sudo -u <service user> stackstead db init …", cfg.DataDir)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	path := dbPathOf(cfg)
	db, err := store.Open(path)
	if err != nil {
		return err
	}
	defer db.Close()
	version, err := store.SchemaVersion(context.Background(), db)
	if err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "database %s at schema %d\n", path, version)
	return nil
}

func fileSize(path string) int64 {
	if info, err := os.Stat(path); err == nil {
		return info.Size()
	}
	return 0
}

func dbCheck(ctx context.Context, env *Env, g *globalFlags, cfg config.Config) error {
	db, err := openLocalDB(env, cfg, store.OpenOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer db.Close()
	path := dbPathOf(cfg)
	report := dbCheckReport{
		Path: path, SupportedSchema: store.SupportedSchema(),
		SizeBytes: fileSize(path), WalBytes: fileSize(path + "-wal"),
		Counts: map[string]int64{}, Problems: []string{},
	}
	if report.Schema, err = store.SchemaVersion(ctx, db); err != nil {
		return err
	}
	problems, err := store.QuickCheck(ctx, db)
	if err != nil {
		return err
	}
	report.OK = len(problems) == 0
	report.Problems = append(report.Problems, problems...)
	for _, table := range dbCountTables {
		var n int64
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
			return fmt.Errorf("count %s: %w", table, err)
		}
		report.Counts[table] = n
	}

	p := newPrinter(env, g)
	if p.JSON {
		if err := p.JSONValue(report); err != nil {
			return err
		}
	} else {
		state := "ok"
		if !report.OK {
			state = "PROBLEMS: " + strings.Join(report.Problems, "; ")
		}
		fmt.Fprintf(env.Stdout, "database   %s\nschema     %d (this binary supports %d)\nsize       %s\nwal        %s\nquick_check  %s\n",
			path, report.Schema, report.SupportedSchema, formatBytes(uint64(report.SizeBytes)), formatBytes(uint64(report.WalBytes)), state)
		rows := make([][]string, 0, len(dbCountTables))
		for _, table := range dbCountTables {
			rows = append(rows, []string{table, fmt.Sprint(report.Counts[table])})
		}
		p.Table([]string{"table", "rows"}, rows)
	}
	if !report.OK {
		return &ExitError{Code: ExitGeneral}
	}
	return nil
}

func dbBackup(ctx context.Context, env *Env, cfg config.Config, dest string, force bool) error {
	if err := refuseLiveDestination(cfg, dest); err != nil {
		return err
	}
	db, err := openLocalDB(env, cfg, store.OpenOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer db.Close()

	if _, err := os.Stat(dest); err == nil && !force {
		return exitf(ExitConflict, "%s already exists (use --force to replace it)", dest)
	}
	// Backup refuses an existing destination, so --force writes a sibling
	// and renames it over the old file only once it has been verified.
	target := dest
	if force {
		target = fmt.Sprintf("%s.new-%d", dest, os.Getpid())
		defer os.Remove(target)
	}
	if err := store.Backup(ctx, db, target); err != nil {
		var mismatch *store.SchemaMismatchError
		if errors.As(err, &mismatch) {
			return &ExitError{Code: ExitConflict, Err: err}
		}
		return err
	}
	schema, size, err := verifyDatabase(ctx, target)
	if err != nil {
		if !force {
			os.Remove(target)
		}
		return err
	}
	if force {
		if err := os.Rename(target, dest); err != nil {
			return err
		}
		syncDir(filepath.Dir(dest))
	}
	fmt.Fprintf(env.Stdout, "backup %s (%s, schema %d)\n", dest, formatBytes(uint64(size)), schema)
	return nil
}

// Seams for tests: the checkpoint and verification of a fresh copy. The rename
// used to publish is the shared renameFile seam in contexts.go.
var (
	checkpointDatabase = checkpointCurrent
	verifyDatabase     = verifyDatabaseFile
)

// verifyDatabaseFile opens a copy read-only and runs quick_check on it.
func verifyDatabaseFile(ctx context.Context, path string) (schema int, size int64, err error) {
	db, err := store.OpenExisting(path, store.OpenOptions{ReadOnly: true, AllowOlderSchema: true})
	if err != nil {
		return 0, 0, fmt.Errorf("verify %s: %w", path, err)
	}
	defer db.Close()
	problems, err := store.QuickCheck(ctx, db)
	if err != nil {
		return 0, 0, fmt.Errorf("verify %s: %w", path, err)
	}
	if len(problems) > 0 {
		return 0, 0, exitf(ExitGeneral, "%s failed quick_check: %s", path, strings.Join(problems, "; "))
	}
	if schema, err = store.SchemaVersion(ctx, db); err != nil {
		return 0, 0, err
	}
	return schema, fileSize(path), nil
}

func dbRestore(ctx context.Context, env *Env, cfg config.Config, src string, yes bool) error {
	if err := checkOwnership(cfg); err != nil {
		return err
	}
	current := dbPathOf(cfg)
	return withExclusiveLock(cfg, func() error {
		srcDB, err := openRestoreSource(ctx, src)
		if err != nil {
			return err
		}
		defer srcDB.Close()
		if !yes {
			if !stdinIsTerminal(env) {
				return exitf(ExitUsage, "restore replaces %s; pass --yes to confirm", current)
			}
			question := fmt.Sprintf("Replace %s with %s? The current database is kept as a .bak file; sessions will be whatever the backup holds.", current, src)
			if ok, err := confirm(env, question, false); err != nil {
				return err
			} else if !ok {
				return exitf(ExitGeneral, "restore cancelled")
			}
		}
		// The copy is made by SQLite from the validated connection, so it is
		// self-contained (a -wal next to src is folded in) and what is checked
		// below is exactly what gets installed.
		tmp := current + ".restore-tmp"
		os.Remove(tmp)
		defer os.Remove(tmp) // no-op once renamed into place
		if err := store.Backup(ctx, srcDB, tmp); err != nil {
			return fmt.Errorf("copy backup: %w", err)
		}
		schema, _, err := verifyDatabase(ctx, tmp)
		if err != nil {
			return err
		}
		if err := checkpointDatabase(ctx, env, current); err != nil {
			return err
		}
		bak, err := swapInDatabase(env, current, tmp)
		if err != nil {
			return err
		}
		if bak != "" {
			fmt.Fprintf(env.Stdout, "previous database kept as %s\n", bak)
		}
		fmt.Fprintf(env.Stdout, "restored %s from %s (schema %d)\n", current, src, schema)
		return nil
	})
}

// openRestoreSource opens src read-only and refuses anything that is not a
// healthy database this binary can run: older is fine (the server migrates at
// start), newer is not.
func openRestoreSource(ctx context.Context, src string) (*sql.DB, error) {
	db, err := store.OpenExisting(src, store.OpenOptions{ReadOnly: true, AllowOlderSchema: true})
	var mismatch *store.SchemaMismatchError
	switch {
	case errors.Is(err, store.ErrNoDatabase):
		return nil, exitf(ExitNotFound, "no such backup file: %s", src)
	case errors.As(err, &mismatch):
		return nil, &ExitError{Code: ExitValidation, Err: err}
	case err != nil:
		return nil, exitf(ExitGeneral, "%s is not a usable database: %v", src, err)
	}
	problems, err := store.QuickCheck(ctx, db)
	if err == nil && len(problems) > 0 {
		err = fmt.Errorf("failed quick_check: %s", strings.Join(problems, "; "))
	}
	if err != nil {
		db.Close()
		return nil, exitf(ExitGeneral, "%s is not a usable database: %v", src, err)
	}
	return db, nil
}

// checkpointCurrent folds a WAL left by a crashed server into the current
// database file, so committed rows live in the main file that is kept as the
// .bak and are not orphaned in a -wal if the swap is interrupted. Without a
// -wal there is nothing to do. A database that cannot be opened or
// checkpointed (garbage, corrupt, newer schema) is exactly what restore is
// for, so that only warns: the swap then moves the -wal next to the .bak. A
// busy database is refused, since a server may still be using it.
func checkpointCurrent(ctx context.Context, env *Env, current string) error {
	if _, err := os.Stat(current + "-wal"); err != nil {
		return nil
	}
	if _, err := os.Stat(current); err != nil {
		return nil
	}
	warn := func(err error) error {
		fmt.Fprintf(env.Stderr, "warning: could not checkpoint %s before restoring (%v); any -wal is kept next to the backup\n", current, err)
		return nil
	}
	// Plain read-write open: no schema gate, no migration, never creates.
	escaped := strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23").Replace(current)
	db, err := sql.Open("sqlite", "file:"+escaped+"?mode=rw&_pragma=busy_timeout(5000)")
	if err != nil {
		return warn(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var busy, logFrames, checkpointed int
	if err := db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
		if isBusyError(err) {
			return fmt.Errorf("checkpoint the current database before restoring: database is busy (is the server still running?): %w", err)
		}
		return warn(err)
	}
	if busy != 0 {
		return fmt.Errorf("checkpoint the current database before restoring: database is busy (is the server still running?)")
	}
	return nil
}

// swapInDatabase installs the verified file tmp as the current database. The
// database path always exists: the old file is hard-linked to its .bak name
// and tmp is renamed over it, so any failure before that rename leaves the
// current database (and its -wal) as it was. It returns the path the previous
// database was kept at ("" when there was none).
func swapInDatabase(env *Env, current, tmp string) (bak string, err error) {
	_, statErr := os.Stat(current)
	hadDB := statErr == nil
	wal, shm := current+"-wal", current+"-shm"

	// Sidecars first: a stale WAL must never be replayed into the new file. A
	// WAL left by a crash belongs to the old file, so it is kept with it.
	walMoved := false
	if hadDB {
		bak = fmt.Sprintf("%s.bak-%s", current, env.Now().UTC().Format("20060102T150405Z"))
		if err := renameFile(wal, bak+"-wal"); err == nil {
			walMoved = true
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
	} else if err := os.Remove(wal); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	undo := func(cause error) (string, error) {
		errs := []error{cause}
		if walMoved {
			if err := renameFile(bak+"-wal", wal); err != nil {
				errs = append(errs, fmt.Errorf("could not put %s back: %w", wal, err))
			}
		}
		return "", errors.Join(errs...)
	}
	if err := os.Remove(shm); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return undo(err)
	}
	if hadDB {
		if err := os.Link(current, bak); err != nil {
			return undo(err)
		}
	}
	if err := renameFile(tmp, current); err != nil {
		if hadDB {
			if rerr := os.Remove(bak); rerr != nil {
				err = errors.Join(err, rerr)
			}
		}
		return undo(err)
	}
	if err := syncDirChecked(filepath.Dir(current)); err != nil {
		return bak, err
	}
	return bak, nil
}

// resolvePath makes p absolute and resolves symlinks as far as they exist.
func resolvePath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	if full, err := filepath.EvalSymlinks(abs); err == nil {
		return full
	}
	if dir, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		return filepath.Join(dir, filepath.Base(abs))
	}
	return abs
}

// refuseLiveDestination keeps a backup from being written over the live
// database, its WAL/SHM sidecars or the server lock, however it is spelled
// (relative, through a symlink or a hard link).
func refuseLiveDestination(cfg config.Config, dest string) error {
	db := dbPathOf(cfg)
	protected := []string{db, db + "-wal", db + "-shm", filepath.Join(cfg.DataDir, "stackstead.lock")}
	resolved := resolvePath(dest)
	destInfo, destErr := os.Stat(dest)
	for _, p := range protected {
		same := resolved == resolvePath(p)
		if info, err := os.Stat(p); err == nil && destErr == nil && os.SameFile(destInfo, info) {
			same = true
		}
		if same {
			return exitf(ExitUsage, "%s is the live database or one of its files; choose another destination", dest)
		}
	}
	return nil
}

// isBusyError reports SQLITE_BUSY / SQLITE_LOCKED errors.
func isBusyError(err error) bool {
	if err == nil {
		return false
	}
	var coded interface{ Code() int }
	if errors.As(err, &coded) {
		switch coded.Code() & 0xff { // primary result code
		case 5, 6: // SQLITE_BUSY, SQLITE_LOCKED
			return true
		}
	}
	msg := err.Error()
	return strings.Contains(msg, "SQLITE_BUSY") || strings.Contains(msg, "SQLITE_LOCKED") || strings.Contains(msg, "is locked")
}
