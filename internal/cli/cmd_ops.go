package cli

import (
	"context"
	"flag"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/client"
)

func streamOperation(ctx context.Context, env *Env, p *Printer, c *client.Client, op apitypes.Operation) error {
	return nil
}

func opsCommand() *Command {
	var running bool
	noop := func(context.Context, *Env, *globalFlags, []string) error { return nil }
	return &Command{
		Name: "ops", Short: "List, follow and cancel operations",
		Sub: []*Command{
			{Name: "ls", Usage: "ops ls [--running]", Flags: func(fs *flag.FlagSet) { fs.BoolVar(&running, "running", false, "") }, Run: noop},
			{Name: "show", Usage: "ops show <id>", MinArgs: 1, MaxArgs: 1, Run: noop},
			{Name: "stream", Usage: "ops stream <id>", MinArgs: 1, MaxArgs: 1, Run: noop},
			{Name: "cancel", Usage: "ops cancel <id>", MinArgs: 1, MaxArgs: 1, Run: noop},
		},
	}
}

func networksCommand() *Command {
	return &Command{
		Name: "networks", Short: "List Docker networks",
		Sub: []*Command{{Name: "ls", Usage: "networks ls", Run: func(context.Context, *Env, *globalFlags, []string) error { return nil }}},
	}
}

// opStreamBackoff tunes reconnect delays of operation streams; zero values
// use the client defaults. Tests shrink it.
var opStreamBackoff struct{ Min, Max time.Duration }
