// Package cli is the stackstead command line: the command framework, the
// server (`serve`) and every client and admin command.
package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
)

// Env carries everything a command touches outside its arguments so tests
// can run commands without a process, a terminal or a home directory.
type Env struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	Getenv         func(string) string
	Now            func() time.Time
	IsTerminal     func(fd uintptr) bool // x/term in OSEnv; tests inject
	// ConfigDir overrides the CLI's config directory; "" means
	// $XDG_CONFIG_HOME/stackstead or ~/.config/stackstead.
	ConfigDir string
	Args0     string // program name for usage
}

// OSEnv is the Env of the real process.
func OSEnv() *Env {
	return &Env{
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
		Getenv: os.Getenv, Now: time.Now,
		IsTerminal: func(fd uintptr) bool { return term.IsTerminal(int(fd)) },
		Args0:      "stackstead",
	}
}

// Command is one node of the command tree: a group (Sub) or a leaf (Run).
type Command struct {
	Name, Short string
	Usage       string // "stacks up <name> [--detach]"
	Long        string // optional paragraph
	Flags       func(fs *flag.FlagSet)
	// MinArgs and MaxArgs bound the positional arguments; MaxArgs -1 is unbounded.
	MinArgs, MaxArgs int
	Run              func(ctx context.Context, env *Env, g *globalFlags, args []string) error
	Sub              []*Command
	Hidden           bool
	// RawArgs hands Run the arguments untouched, without flag parsing (the
	// command parses its own). Only `-h`/`--help` as the first argument is
	// intercepted to print usage.
	RawArgs bool
}

// globalFlags are accepted by every command, before or after the subcommand.
type globalFlags struct {
	Context string
	Output  string // table|json
	NoColor bool
	Timeout time.Duration
}

const defaultTimeout = 30 * time.Second

func (g *globalFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&g.Context, "context", g.Context, "use this saved context instead of the current one (env STACKSTEAD_CONTEXT)")
	fs.StringVar(&g.Output, "o", g.Output, "output format: table or json (short for -output)")
	fs.StringVar(&g.Output, "output", g.Output, "output format: table or json")
	fs.BoolVar(&g.NoColor, "no-color", g.NoColor, "disable colour (also NO_COLOR, TERM=dumb)")
	fs.DurationVar(&g.Timeout, "timeout", g.Timeout, "timeout of each API request")
}

func (c *Command) find(name string) *Command {
	for _, s := range c.Sub {
		if s.Name == name {
			return s
		}
	}
	return nil
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// Run resolves the command named by args under root, parses its flags and
// runs it. Usage problems come back as ExitUsage errors whose message
// carries the usage line, so Main prints them once.
func Run(ctx context.Context, root *Command, args []string, env *Env) error {
	g := &globalFlags{Context: env.Getenv("STACKSTEAD_CONTEXT"), Output: "table", Timeout: defaultTimeout}
	cmd, path, rest := root, []string{}, args

	for len(cmd.Sub) > 0 {
		fs := newFlagSet(cmd.Name)
		g.register(fs)
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				printUsage(env.Stdout, env, cmd, path)
				return nil
			}
			return exitf(ExitUsage, "%v\n\n%s", err, usageText(env, cmd, path))
		}
		rest = fs.Args()
		if len(rest) == 0 {
			printUsage(env.Stderr, env, cmd, path)
			return &ExitError{Code: ExitUsage}
		}
		name := rest[0]
		rest = rest[1:]
		sub := cmd.find(name)
		if sub == nil && name == "help" {
			return printHelp(env, root, rest)
		}
		if sub == nil {
			return exitf(ExitUsage, "unknown command %q\n\n%s", name, usageText(env, cmd, path))
		}
		cmd, path = sub, append(path, name)
	}
	if cmd.Run == nil {
		printUsage(env.Stderr, env, cmd, path)
		return &ExitError{Code: ExitUsage}
	}

	if cmd.RawArgs {
		if len(rest) > 0 && (rest[0] == "-h" || rest[0] == "--help") {
			printUsage(env.Stdout, env, cmd, path)
			return nil
		}
		return cmd.Run(ctx, env, g, rest)
	}

	fs := newFlagSet(cmd.Name)
	if cmd.Flags != nil {
		cmd.Flags(fs)
	}
	g.register(fs)
	positional, err := parseInterspersed(fs, rest)
	if errors.Is(err, flag.ErrHelp) {
		printUsage(env.Stdout, env, cmd, path)
		return nil
	}
	if err != nil {
		return exitf(ExitUsage, "%v\n\n%s", err, usageText(env, cmd, path))
	}
	if g.Output != "table" && g.Output != "json" {
		return exitf(ExitUsage, "invalid output format %q: use table or json", g.Output)
	}
	if len(positional) < cmd.MinArgs || (cmd.MaxArgs >= 0 && len(positional) > cmd.MaxArgs) {
		return exitf(ExitUsage, "wrong number of arguments (%d)\n\n%s", len(positional), usageText(env, cmd, path))
	}
	return cmd.Run(ctx, env, g, positional)
}

func printHelp(env *Env, root *Command, names []string) error {
	cmd, path := root, []string{}
	for _, n := range names {
		sub := cmd.find(n)
		if sub == nil {
			return exitf(ExitUsage, "unknown command %q\n\n%s", n, usageText(env, cmd, path))
		}
		cmd, path = sub, append(path, n)
	}
	printUsage(env.Stdout, env, cmd, path)
	return nil
}

func printUsage(w io.Writer, env *Env, cmd *Command, path []string) {
	io.WriteString(w, usageText(env, cmd, path))
}

func usageText(env *Env, cmd *Command, path []string) string {
	prog := env.Args0
	if prog == "" {
		prog = "stackstead"
	}
	var b strings.Builder
	switch {
	case cmd.Usage != "":
		fmt.Fprintf(&b, "Usage: %s %s\n", prog, cmd.Usage)
	case len(cmd.Sub) > 0:
		fmt.Fprintf(&b, "Usage: %s <command> [flags]\n", strings.Join(append([]string{prog}, path...), " "))
	default:
		fmt.Fprintf(&b, "Usage: %s [flags]\n", strings.Join(append([]string{prog}, path...), " "))
	}
	if cmd.Short != "" {
		fmt.Fprintf(&b, "\n%s\n", cmd.Short)
	}
	if cmd.Long != "" {
		fmt.Fprintf(&b, "\n%s\n", cmd.Long)
	}
	if len(cmd.Sub) > 0 {
		b.WriteString("\nCommands:\n")
		width := 0
		for _, s := range cmd.Sub {
			if !s.Hidden {
				width = max(width, len(s.Name))
			}
		}
		for _, s := range cmd.Sub {
			if !s.Hidden {
				fmt.Fprintf(&b, "  %-*s  %s\n", width, s.Name, s.Short)
			}
		}
	}
	if cmd.Flags != nil {
		local := flag.NewFlagSet(cmd.Name, flag.ContinueOnError)
		cmd.Flags(local)
		b.WriteString("\nFlags:\n" + defaults(local))
	}
	global := flag.NewFlagSet("global", flag.ContinueOnError)
	(&globalFlags{Output: "table", Timeout: defaultTimeout}).register(global)
	b.WriteString("\nGlobal flags:\n" + defaults(global))
	return b.String()
}

func defaults(fs *flag.FlagSet) string {
	var buf bytes.Buffer
	fs.SetOutput(&buf)
	fs.PrintDefaults()
	return buf.String()
}

const bareFlagsNote = `note: run "stackstead serve" (bare flags are deprecated)`

// Main is the process entry point: it wires signals, runs the command tree
// and maps the result to an exit code. Errors print as `stackstead: <message>`.
func Main(ctx context.Context, args []string, env *Env) int {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(args) > 0 && strings.HasPrefix(args[0], "-") && !isHelpFlag(args[0]) {
		fmt.Fprintln(env.Stderr, bareFlagsNote)
		args = append([]string{"serve"}, args...)
	}
	err := Run(ctx, rootCommand(), args, env)
	code := codeFor(err)
	if msg := errString(err); msg != "" && code != ExitOK && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(env.Stderr, "stackstead: %s\n", msg)
	}
	return code
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func isHelpFlag(s string) bool {
	return s == "-h" || s == "--help" || s == "-help"
}

// rootCommand is the command tree. Task 10 moves it to commands.go and adds
// the client and admin commands.
func rootCommand() *Command {
	return &Command{
		Name:  "stackstead",
		Short: "Stackstead: manage Docker Compose stacks",
		Sub:   []*Command{serveCommand(), versionCommand()},
	}
}
