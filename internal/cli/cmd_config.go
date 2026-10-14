package cli

import (
	"context"
	"flag"
)

func configCommand() *Command {
	var lf localFlags
	var initPath, listen, stacksDir, dataDir string
	var force bool
	stub := func(context.Context, *Env, *globalFlags, []string) error { return errNotImplemented }
	return &Command{
		Name: "config", Short: "Create, validate and show the config file",
		Sub: []*Command{
			{Name: "init", Short: "Write a commented config file", Usage: "config init [--path P] [--listen A] [--stacks-dir D] [--data-dir D] [--force]",
				Flags: func(fs *flag.FlagSet) {
					fs.StringVar(&initPath, "path", "/etc/stackstead/config.yaml", "where to write the config file")
					fs.StringVar(&listen, "listen", "", "listen address")
					fs.StringVar(&stacksDir, "stacks-dir", "", "stacks directory")
					fs.StringVar(&dataDir, "data-dir", "", "data directory")
					fs.BoolVar(&force, "force", false, "overwrite an existing file")
				}, Run: stub},
			{Name: "validate", Short: "Check the config file", Usage: "config validate [--config P]", Flags: lf.register, Run: stub},
			{Name: "show", Short: "Show effective settings and their sources", Usage: "config show [--config P]", Flags: lf.register, Run: stub},
		},
	}
}
