package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/client"
)

// maxComposeBytes bounds a compose file read from a file or stdin.
const maxComposeBytes = 8 << 20

func joinClean(items []string, sep string) string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = oneLine(s)
	}
	return strings.Join(out, sep)
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// conflictError turns a 409 into an ExitConflict error with the given
// message, keeping the original reachable for errors.Is.
func conflictError(err error, msg string) error {
	var ce *client.Error
	if errors.As(err, &ce) && ce.Status == http.StatusConflict {
		return &ExitError{Code: ExitConflict, Err: &cleanError{msg: msg, err: err}}
	}
	return err
}

// busyError says which stack is busy on a 409, using the server's own
// message when it sent one.
func busyError(err error, name string) error {
	var ce *client.Error
	if errors.As(err, &ce) && ce.Status == http.StatusConflict {
		msg := ce.Message
		if msg == "" || msg == "HTTP 409" {
			msg = fmt.Sprintf("stack %s is busy: another operation is running", name)
		}
		return conflictError(err, oneLine(msg))
	}
	return err
}

// startAndFollow prints the started operation (--detach) or streams it to
// completion.
func startAndFollow(ctx context.Context, env *Env, g *globalFlags, c *client.Client, op apitypes.Operation, detach bool) error {
	p := newPrinter(env, g)
	if detach {
		if p.JSON {
			return p.JSONValue(op)
		}
		fmt.Fprintf(env.Stdout, "operation %s started\n", oneLine(op.ID))
		return nil
	}
	return streamOperation(ctx, env, p, c, op)
}

var lifecycleShort = map[string]string{
	"up":      "Start a stack (compose up)",
	"down":    "Stop and remove a stack's containers (compose down)",
	"restart": "Restart a stack's containers (compose restart)",
	"pull":    "Pull images for a stack (compose pull)",
	"build":   "Build images for a stack (compose build)",
}

func lifecycleCommand(action string) *Command {
	var detach bool
	return &Command{
		Name: action, Short: lifecycleShort[action] + " and follow the operation", MinArgs: 1, MaxArgs: 1,
		Usage: "stacks " + action + " <name> [--detach]",
		Long:  "Streams the operation's output and exits 9 if it did not succeed; --detach prints the operation id and returns at once.",
		Flags: func(fs *flag.FlagSet) {
			fs.BoolVar(&detach, "detach", false, "print the operation id and return without waiting")
		},
		Run: func(ctx context.Context, env *Env, g *globalFlags, args []string) error {
			name := args[0]
			return withAPI(env, g, func(c *client.Client, _ string, _ Context) error {
				op, err := c.Act(ctx, name, action)
				if err != nil {
					return busyError(err, name)
				}
				return startAndFollow(ctx, env, g, c, op, detach)
			})
		},
	}
}

func removeCommand() *Command {
	var detach, volumes, yes bool
	return &Command{
		Name: "rm", Short: "Remove a stack (compose down; its files stay on disk)", MinArgs: 1, MaxArgs: 1,
		Usage: "stacks rm <name> [--volumes] [--yes] [--detach]",
		Flags: func(fs *flag.FlagSet) {
			fs.BoolVar(&volumes, "volumes", false, "also delete the stack's volumes")
			fs.BoolVar(&yes, "yes", false, "do not ask for confirmation")
			fs.BoolVar(&detach, "detach", false, "print the operation id and return without waiting")
		},
		Run: func(ctx context.Context, env *Env, g *globalFlags, args []string) error {
			name := args[0]
			what := "compose down"
			if volumes {
				what += ", and delete its volumes"
			}
			// Inside withAPI so a logged-out user gets exit 3 before being asked.
			return withAPI(env, g, func(c *client.Client, _ string, _ Context) error {
				ok, err := confirm(env, fmt.Sprintf("remove stack %s (%s); files stay on disk?", name, what), yes)
				if err != nil {
					return err
				}
				if !ok {
					fmt.Fprintln(env.Stderr, "cancelled")
					return nil
				}
				op, err := c.Remove(ctx, name, volumes)
				if err != nil {
					return busyError(err, name)
				}
				return startAndFollow(ctx, env, g, c, op, detach)
			})
		},
	}
}

func stacksLsCommand() *Command {
	return &Command{
		Name: "ls", Short: "List stacks", Usage: "stacks ls",
		Run: func(ctx context.Context, env *Env, g *globalFlags, _ []string) error {
			return withAPI(env, g, func(c *client.Client, _ string, _ Context) error {
				stacks, err := c.Stacks(ctx)
				if err != nil {
					return err
				}
				p := newPrinter(env, g)
				if p.JSON {
					if stacks == nil {
						stacks = []apitypes.StackSummary{}
					}
					return p.JSONValue(stacks)
				}
				if len(stacks) == 0 {
					fmt.Fprintln(env.Stderr, "no stacks")
				}
				rows := make([][]string, 0, len(stacks))
				for _, s := range stacks {
					rows = append(rows, []string{
						oneLine(s.Name), p.Status(oneLine(s.Status)), fmt.Sprint(s.ServiceCount), fmt.Sprint(s.Running),
						oneLine(s.Origin), oneLine(s.Path),
					})
				}
				p.Table([]string{"name", "status", "services", "running", "origin", "path"}, rows)
				return nil
			})
		},
	}
}

func stacksShowCommand() *Command {
	return &Command{
		Name: "show", Short: "Show a stack's services and containers", Usage: "stacks show <name>", MinArgs: 1, MaxArgs: 1,
		Run: func(ctx context.Context, env *Env, g *globalFlags, args []string) error {
			return withAPI(env, g, func(c *client.Client, _ string, _ Context) error {
				d, err := c.Stack(ctx, args[0])
				if err != nil {
					return err
				}
				p := newPrinter(env, g)
				if p.JSON {
					return p.JSONValue(d)
				}
				fmt.Fprintf(env.Stdout, "Name:     %s\nProject:  %s\nOrigin:   %s\nPath:     %s\nStatus:   %s\nHash:     %s\n",
					oneLine(d.Name), oneLine(d.Project), oneLine(d.Origin), oneLine(d.Path), p.Status(oneLine(d.Status)), oneLine(d.Hash))
				if d.ParseError != "" {
					fmt.Fprintf(env.Stdout, "%s\n", p.paint("31", "parse error: "+strings.ReplaceAll(cleanText(d.ParseError), "\n", "\n  ")))
				}
				if len(d.Services) > 0 {
					fmt.Fprintln(env.Stdout)
					rows := make([][]string, 0, len(d.Services))
					for _, s := range d.Services {
						source := oneLine(s.Config.Image)
						if s.Config.Build != nil {
							source = "build: " + oneLine(s.Config.Build.Context)
						}
						var limits []string
						if s.Config.Limits.CPUs != "" {
							limits = append(limits, "cpus="+oneLine(s.Config.Limits.CPUs))
						}
						if s.Config.Limits.Memory != "" {
							limits = append(limits, "mem="+oneLine(s.Config.Limits.Memory))
						}
						rows = append(rows, []string{
							oneLine(s.Name), dashIfEmpty(source), dashIfEmpty(joinClean(s.Config.Ports, ",")),
							dashIfEmpty(strings.Join(limits, " ")), dashIfEmpty(joinClean(s.ReadOnly, ",")),
						})
					}
					p.Table([]string{"service", "image/build", "ports", "limits", "read-only"}, rows)
				}
				var crows [][]string
				for _, s := range d.Services {
					for _, ct := range s.Containers {
						crows = append(crows, []string{oneLine(ct.Name), oneLine(ct.Service), p.Status(oneLine(ct.State)), oneLine(ct.Status)})
					}
				}
				if len(crows) > 0 {
					fmt.Fprintln(env.Stdout)
					p.Table([]string{"container", "service", "state", "status"}, crows)
				}
				return nil
			})
		},
	}
}

func stacksImportCommand() *Command {
	return &Command{
		Name: "import", Short: "Import an existing compose file as a stack", Usage: "stacks import <path>", MinArgs: 1, MaxArgs: 1,
		Long: "The path is a compose file on the server host (e.g. /opt/stacks/web/compose.yaml), inside its allowed roots.",
		Run: func(ctx context.Context, env *Env, g *globalFlags, args []string) error {
			return withAPI(env, g, func(c *client.Client, _ string, _ Context) error {
				res, err := c.Import(ctx, args[0])
				if err != nil {
					return err
				}
				if res.ChecksPartial {
					fmt.Fprintln(env.Stderr, "note: port checks were partial (Docker unavailable)")
				}
				for _, w := range res.Data.Warnings {
					fmt.Fprintf(env.Stderr, "warning: %s\n", oneLine(w))
				}
				p := newPrinter(env, g)
				if p.JSON {
					return p.JSONValue(res.Data)
				}
				fmt.Fprintf(env.Stdout, "imported stack %s (project %s) from %s; status %s\n",
					oneLine(res.Data.Name), oneLine(res.Data.Project), oneLine(res.Data.Path), p.Status(oneLine(res.Data.Status)))
				return nil
			})
		},
	}
}

func readComposeInput(env *Env, file string) (string, error) {
	var r io.Reader
	if file == "-" {
		r = env.Stdin
	} else {
		f, err := os.Open(file)
		if err != nil {
			return "", exitf(ExitUsage, "read %s: %v", file, err)
		}
		defer f.Close()
		r = f
	}
	data, err := io.ReadAll(io.LimitReader(r, maxComposeBytes+1))
	if err != nil {
		return "", exitf(ExitGeneral, "read compose file: %v", err)
	}
	if len(data) > maxComposeBytes {
		return "", exitf(ExitUsage, "compose file is larger than %d MiB", maxComposeBytes>>20)
	}
	return string(data), nil
}

const staleComposeMessage = "compose file changed on the server: run stacks compose get and retry"

func composeCommand() *Command {
	var file, hash string
	return &Command{
		Name: "compose", Short: "Read and replace a stack's compose file",
		Sub: []*Command{
			{
				Name: "get", Short: "Print a stack's compose file", Usage: "stacks compose get <name>", MinArgs: 1, MaxArgs: 1,
				Long: "-o json prints the content together with the hash that compose put needs.",
				Run: func(ctx context.Context, env *Env, g *globalFlags, args []string) error {
					return withAPI(env, g, func(c *client.Client, _ string, _ Context) error {
						raw, err := c.Compose(ctx, args[0])
						if err != nil {
							return err
						}
						if p := newPrinter(env, g); p.JSON {
							return p.JSONValue(raw)
						}
						_, err = io.WriteString(env.Stdout, raw.Content)
						return err
					})
				},
			},
			composeEditCommand(),
			{
				Name: "put", Short: "Replace a stack's compose file", MinArgs: 1, MaxArgs: 1,
				Usage: "stacks compose put <name> -f FILE --hash H",
				Long:  "FILE of - reads stdin. H is the hash from compose get; the save is refused (exit 5) if the file changed since.",
				Flags: func(fs *flag.FlagSet) {
					fs.StringVar(&file, "f", "", "file with the new compose content, or - for stdin")
					fs.StringVar(&hash, "hash", "", "hash of the version being replaced (from compose get)")
				},
				Run: func(ctx context.Context, env *Env, g *globalFlags, args []string) error {
					if file == "" || hash == "" {
						return exitf(ExitUsage, "both -f FILE and --hash H are required")
					}
					content, err := readComposeInput(env, file)
					if err != nil {
						return err
					}
					return withAPI(env, g, func(c *client.Client, _ string, _ Context) error {
						res, err := c.SaveCompose(ctx, args[0], content, hash)
						if err != nil {
							return conflictError(err, staleComposeMessage)
						}
						if res.ChecksPartial {
							fmt.Fprintln(env.Stderr, "note: port checks were partial (Docker unavailable)")
						}
						if p := newPrinter(env, g); p.JSON {
							return p.JSONValue(res.Data)
						}
						fmt.Fprintln(env.Stdout, oneLine(res.Data.Hash))
						return nil
					})
				},
			},
		},
	}
}

var unsafeFileChars = regexp.MustCompile(`[^A-Za-z0-9._-]`)

func composeEditCommand() *Command {
	return &Command{
		Name: "edit", Short: "Edit a stack's compose file in $VISUAL or $EDITOR", Usage: "stacks compose edit <name>", MinArgs: 1, MaxArgs: 1,
		Long: "$VISUAL or $EDITOR is split on whitespace and run without a shell (\"code --wait\" works, quoting and pipes do not).\n" +
			"The file is saved with the hash it was fetched with, so a concurrent change is refused (exit 5). When the save is\n" +
			"refused (exit 5 or 6), the editor fails or the command is interrupted, the temporary file is kept and its path is printed.",
		Run: func(ctx context.Context, env *Env, g *globalFlags, args []string) error {
			name := args[0]
			editor := env.Getenv("VISUAL")
			if editor == "" {
				editor = env.Getenv("EDITOR")
			}
			fields := strings.Fields(editor)
			if len(fields) == 0 {
				return exitf(ExitUsage, "no editor: set $VISUAL or $EDITOR (or use stacks compose get/put)")
			}
			if !env.IsTerminal(stdinFd(env)) {
				return exitf(ExitUsage, "compose edit needs a terminal; use stacks compose get/put in scripts")
			}
			return withAPI(env, g, func(c *client.Client, _ string, _ Context) error {
				raw, err := c.Compose(ctx, name)
				if err != nil {
					return err
				}
				tmp, err := os.CreateTemp(env.Getenv("TMPDIR"), "stackstead-"+unsafeFileChars.ReplaceAllString(name, "_")+"-*.yaml")
				if err != nil {
					return exitf(ExitGeneral, "create temporary file: %v", err)
				}
				path := tmp.Name()
				_, werr := tmp.WriteString(raw.Content)
				if cerr := tmp.Close(); werr == nil {
					werr = cerr
				}
				if werr != nil {
					os.Remove(path)
					return exitf(ExitGeneral, "write %s: %v", path, werr)
				}

				// exec.Command, not CommandContext: on Ctrl-C the terminal already
				// signals the editor, and killing it would skip its terminal restore.
				cmd := exec.Command(fields[0], append(fields[1:], path)...)
				cmd.Stdin, cmd.Stdout, cmd.Stderr = env.Stdin, env.Stdout, env.Stderr
				runErr := cmd.Run()
				if ctx.Err() != nil {
					fmt.Fprintf(env.Stderr, "note: your edit is kept in %s\n", path)
					return ctx.Err()
				}
				if runErr != nil {
					return exitf(ExitGeneral, "editor %s failed: %v (your edit is kept in %s)", fields[0], runErr, path)
				}
				edited, err := os.ReadFile(path)
				if err != nil {
					return exitf(ExitGeneral, "read %s: %v", path, err)
				}
				if string(edited) == raw.Content {
					os.Remove(path)
					fmt.Fprintln(env.Stderr, "no changes")
					return nil
				}

				res, err := c.SaveCompose(ctx, name, string(edited), raw.Hash)
				if err != nil {
					kept := fmt.Sprintf("your edit is kept in %s", path)
					var ce *client.Error
					switch {
					case errors.As(err, &ce) && ce.Status == http.StatusConflict:
						return conflictError(err, staleComposeMessage+" ("+kept+")")
					case errors.As(err, &ce) && ce.Status == http.StatusUnauthorized:
						fmt.Fprintf(env.Stderr, "note: %s\n", kept)
						return err
					}
					wrapped := apiFailure(err)
					var ee *ExitError
					if errors.As(wrapped, &ee) {
						return &ExitError{Code: ee.Code, Err: &cleanError{msg: ee.Error() + " (" + kept + ")", err: err}}
					}
					fmt.Fprintf(env.Stderr, "note: %s\n", kept)
					return err
				}
				os.Remove(path)
				if res.ChecksPartial {
					fmt.Fprintln(env.Stderr, "note: port checks were partial (Docker unavailable)")
				}
				if p := newPrinter(env, g); p.JSON {
					return p.JSONValue(res.Data)
				}
				fmt.Fprintf(env.Stdout, "saved compose file of %s (hash %s)\n", oneLine(name), oneLine(res.Data.Hash))
				return nil
			})
		},
	}
}

func stacksCommand() *Command {
	sub := []*Command{stacksLsCommand(), stacksShowCommand()}
	for _, a := range apitypes.Actions {
		sub = append(sub, lifecycleCommand(a))
	}
	sub = append(sub, removeCommand(), stacksImportCommand(), composeCommand())
	return &Command{Name: "stacks", Short: "List, inspect and operate stacks", Sub: sub}
}
