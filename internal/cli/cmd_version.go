package cli

import (
	"context"
	"flag"
	"fmt"

	"github.com/rcss-labs/stackstead/internal/version"
)

func versionCommand() *Command {
	var short bool
	return &Command{
		Name:    "version",
		Short:   "Print the version and build information",
		Usage:   "version [--short]",
		MaxArgs: 0,
		Flags:   func(fs *flag.FlagSet) { fs.BoolVar(&short, "short", false, "print only the version string") },
		Run: func(_ context.Context, env *Env, g *globalFlags, _ []string) error {
			info := version.Get()
			switch {
			case short && g.Output == "json":
				return exitf(ExitUsage, "--short cannot be combined with -o json")
			case short:
				fmt.Fprintln(env.Stdout, info.Version)
			case g.Output == "json":
				return newPrinter(env, g).JSONValue(info)
			default:
				fmt.Fprintln(env.Stdout, info.String())
			}
			return nil
		},
	}
}
