package cli

import (
	"database/sql"
	"errors"
	"flag"

	"github.com/rcss-labs/stackstead/internal/config"
	"github.com/rcss-labs/stackstead/internal/store"
)

var errNotImplemented = errors.New("not implemented")

// localFlags are the flags every host-side command shares.
type localFlags struct{ Config string }

func (lf *localFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&lf.Config, "config", "", "path to config.yaml (default: "+defaultConfigPath+" if it exists)")
}

// Seams so tests can fake the effective uid and the owner of files.
var geteuid = func() int { return 0 }

func realFileOwner(path string) (int, error) { return 0, errNotImplemented }

var fileOwner = realFileOwner

func loadLocalConfig(env *Env, lf localFlags) (config.Config, string, error) {
	return config.Config{}, "", errNotImplemented
}

func openLocalDB(env *Env, cfg config.Config, opts store.OpenOptions) (*sql.DB, error) {
	return nil, errNotImplemented
}

func withExclusiveLock(cfg config.Config, fn func() error) error { return errNotImplemented }
