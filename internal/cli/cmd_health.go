package cli

import (
	"context"
)

func healthCommand() *Command {
	var lf localFlags
	return &Command{
		Name: "health", Short: "Check config, database, server, Docker and compose", Usage: "health [--config P]",
		Flags: lf.register,
		Run:   func(context.Context, *Env, *globalFlags, []string) error { return errNotImplemented },
	}
}
