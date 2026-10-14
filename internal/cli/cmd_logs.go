package cli

import (
	"context"
	"flag"
	"sync"
	"time"
)

// liveStreamBackoff is the reconnect backoff of logs, metrics --live and
// events; tests shrink it.
var liveStreamBackoff = struct{ Min, Max time.Duration }{time.Second, 30 * time.Second}

type logCursors struct {
	mu  sync.Mutex
	ids map[string]string
}

func (l *logCursors) Set(id string)  {}
func (l *logCursors) String() string { return "" }

func logsCommand() *Command {
	var follow, timestamps, raw bool
	var tail, since, service string
	return &Command{
		Name: "logs", Short: "Show container logs of a stack",
		Usage:   "logs <stack> [-f] [--tail N|all] [--since X] [--service S] [--timestamps] [--raw]",
		MinArgs: 1, MaxArgs: 1,
		Flags: func(fs *flag.FlagSet) {
			fs.BoolVar(&follow, "f", false, "")
			fs.BoolVar(&follow, "follow", false, "")
			fs.StringVar(&tail, "tail", "", "")
			fs.StringVar(&since, "since", "", "")
			fs.StringVar(&service, "service", "", "")
			fs.BoolVar(&timestamps, "timestamps", false, "")
			fs.BoolVar(&raw, "raw", false, "")
		},
		Run: func(ctx context.Context, env *Env, g *globalFlags, args []string) error { return nil },
	}
}
