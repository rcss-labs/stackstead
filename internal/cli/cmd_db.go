package cli

import (
	"context"
	"flag"
)

func dbCommand() *Command {
	var lf localFlags
	var force, yes bool
	stub := func(context.Context, *Env, *globalFlags, []string) error { return errNotImplemented }
	return &Command{
		Name: "db", Short: "Local database commands (run as the service user)",
		Sub: []*Command{
			{Name: "init", Short: "Create and migrate the database", Usage: "db init [--config P]", Flags: lf.register, Run: stub},
			{Name: "check", Short: "Check database integrity", Usage: "db check [--config P]", Flags: lf.register, Run: stub},
			{Name: "backup", Short: "Back up the database (online-safe)", Usage: "db backup <dest> [--force] [--config P]", MinArgs: 1, MaxArgs: 1,
				Flags: func(fs *flag.FlagSet) {
					lf.register(fs)
					fs.BoolVar(&force, "force", false, "replace an existing destination")
				}, Run: stub},
			{Name: "restore", Short: "Restore the database (server must be stopped)", Usage: "db restore <src> [--yes] [--config P]", MinArgs: 1, MaxArgs: 1,
				Flags: func(fs *flag.FlagSet) { lf.register(fs); fs.BoolVar(&yes, "yes", false, "do not ask for confirmation") }, Run: stub},
		},
	}
}
