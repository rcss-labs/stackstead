package cli

import (
	"context"
	"flag"
)

func metricsCommand() *Command {
	var from, to, service string
	var step string
	var live bool
	return &Command{
		Name: "metrics", Short: "Show resource usage of a stack",
		Usage:   "metrics <stack> [--from T] [--to T] [--step D] [--service S] [--live]",
		MinArgs: 1, MaxArgs: 1,
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&from, "from", "", "")
			fs.StringVar(&to, "to", "", "")
			fs.StringVar(&step, "step", "", "")
			fs.StringVar(&service, "service", "", "")
			fs.BoolVar(&live, "live", false, "")
		},
		Run: func(ctx context.Context, env *Env, g *globalFlags, args []string) error { return nil },
	}
}

func eventsCommand() *Command {
	return &Command{
		Name: "events", Short: "Follow container changes", Usage: "events",
		MaxArgs: 0,
		Run:     func(ctx context.Context, env *Env, g *globalFlags, args []string) error { return nil },
	}
}
