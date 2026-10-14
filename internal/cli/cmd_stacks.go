package cli

import (
	"context"
	"flag"

	"github.com/rcss-labs/stackstead/internal/apitypes"
)

func stacksCommand() *Command {
	noop := func(context.Context, *Env, *globalFlags, []string) error { return nil }
	var detach, volumes, yes bool
	var file, hash string
	lifecycle := func(action string) *Command {
		return &Command{Name: action, Usage: "stacks " + action + " <name> [--detach]", MinArgs: 1, MaxArgs: 1,
			Flags: func(fs *flag.FlagSet) { fs.BoolVar(&detach, "detach", false, "") }, Run: noop}
	}
	_ = apitypes.Actions
	return &Command{
		Name: "stacks", Short: "Manage stacks",
		Sub: []*Command{
			{Name: "ls", Usage: "stacks ls", Run: noop},
			{Name: "show", Usage: "stacks show <name>", MinArgs: 1, MaxArgs: 1, Run: noop},
			lifecycle("up"), lifecycle("down"), lifecycle("restart"), lifecycle("pull"), lifecycle("build"),
			{Name: "rm", Usage: "stacks rm <name> [--volumes] [--yes] [--detach]", MinArgs: 1, MaxArgs: 1,
				Flags: func(fs *flag.FlagSet) {
					fs.BoolVar(&detach, "detach", false, "")
					fs.BoolVar(&volumes, "volumes", false, "")
					fs.BoolVar(&yes, "yes", false, "")
				}, Run: noop},
			{Name: "import", Usage: "stacks import <path>", MinArgs: 1, MaxArgs: 1, Run: noop},
			{Name: "compose", Short: "Read and edit a stack's compose file", Sub: []*Command{
				{Name: "get", Usage: "stacks compose get <name>", MinArgs: 1, MaxArgs: 1, Run: noop},
				{Name: "edit", Usage: "stacks compose edit <name>", MinArgs: 1, MaxArgs: 1, Run: noop},
				{Name: "put", Usage: "stacks compose put <name> -f FILE --hash H", MinArgs: 1, MaxArgs: 1,
					Flags: func(fs *flag.FlagSet) {
						fs.StringVar(&file, "f", "", "")
						fs.StringVar(&hash, "hash", "", "")
					}, Run: noop},
			}},
		},
	}
}
