package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/client"
)

// streamOperation streams one operation to completion, shared by the
// lifecycle commands and `ops stream`.
//
// Output lines are server-provided user data (compose output, which often
// carries colour and progress escapes). They are passed through cleanText, so
// ESC/CR/BEL and other control characters are dropped before they reach the
// terminal; colour from compose is lost, a hostile stack cannot rewrite the
// screen. NDJSON mode keeps the text verbatim: JSON escapes control characters.
//
// Human mode prints raw lines to Stdout, a note on Stderr per reconnect, and
// on success a one-line summary on Stderr; a failed operation returns
// ExitError{9, "<action> <stack> <state>: <error>"}, which Main prints, so
// the failure summary is that error line. JSON mode prints NDJSON
// {event, id, data}. The stream resumes from the highest line id seen (the
// client tracks it; the id-less "earlier lines dropped" note never moves it).
func streamOperation(ctx context.Context, env *Env, p *Printer, c *client.Client, op apitypes.Operation) error {
	var final *apitypes.Operation
	// The stream resumes from the highest line id, tracked by the client.
	opts := streamOptions(env, nil, p.JSON)

	err := c.StreamOperation(ctx, op.ID, opts, func(ev client.Event) error {
		switch ev.Name {
		case "line":
			var text string
			if err := json.Unmarshal(ev.Data, &text); err != nil {
				return fmt.Errorf("malformed line event: %w", err)
			}
			if p.JSON {
				return p.NDJSON("line", ev.ID, text)
			}
			fmt.Fprintln(env.Stdout, cleanText(text))
		case "done":
			var done apitypes.Operation
			if err := json.Unmarshal(ev.Data, &done); err != nil {
				return fmt.Errorf("malformed done event: %w", err)
			}
			final = &done
			if p.JSON {
				if err := p.NDJSON("done", "", done); err != nil {
					return err
				}
			}
			return client.ErrStreamDone
		case "dropped":
			if p.JSON {
				return p.NDJSON("dropped", "", struct{}{})
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintf(env.Stderr, "note: operation %s continues on the server; follow it with stackstead ops stream %s\n", oneLine(op.ID), oneLine(op.ID))
		}
		return err
	}
	if final == nil {
		return exitf(ExitGeneral, "the output stream of operation %s ended without a result", oneLine(op.ID))
	}
	desc := fmt.Sprintf("%s %s %s", oneLine(final.Action), operationStack(final.Stack), oneLine(final.State))
	if final.State != apitypes.OpSucceeded {
		if final.Error != "" {
			desc += ": " + oneLine(final.Error)
		}
		return &ExitError{Code: ExitOperationFailed, Err: errors.New(desc)}
	}
	if !p.JSON {
		took := ""
		if final.EndedAt != nil {
			took = " (" + formatDuration(final.EndedAt.Sub(final.StartedAt)) + ")"
		}
		fmt.Fprintln(env.Stderr, desc+took)
	}
	return nil
}

func operationDuration(op apitypes.Operation, now time.Time) string {
	end := now
	if op.EndedAt != nil {
		end = *op.EndedAt
	}
	return formatDuration(end.Sub(op.StartedAt))
}

func opsCommand() *Command {
	var running bool
	return &Command{
		Name: "ops", Short: "List, follow and cancel operations",
		Sub: []*Command{
			{
				Name: "ls", Short: "List recent operations", Usage: "ops ls [--running]",
				Flags: func(fs *flag.FlagSet) {
					fs.BoolVar(&running, "running", false, "only operations that are still running")
				},
				Run: func(ctx context.Context, env *Env, g *globalFlags, _ []string) error {
					return withAPI(env, g, func(c *client.Client, _ string, _ Context) error {
						state := ""
						if running {
							state = apitypes.OpRunning
						}
						ops, err := c.Operations(ctx, state)
						if err != nil {
							return err
						}
						p := newPrinter(env, g)
						if p.JSON {
							if ops == nil {
								ops = []apitypes.Operation{}
							}
							return p.JSONValue(ops)
						}
						now := env.Now()
						rows := make([][]string, 0, len(ops))
						for _, op := range ops {
							exit := "-"
							if op.ExitCode != nil {
								exit = fmt.Sprint(*op.ExitCode)
							}
							rows = append(rows, []string{
								oneLine(op.ID), operationStack(op.Stack), oneLine(op.Action), p.Status(oneLine(op.State)),
								relativeTime(op.StartedAt, now), operationDuration(op, now), exit,
							})
						}
						p.Table([]string{"id", "stack", "action", "state", "started", "duration", "exit"}, rows)
						return nil
					})
				},
			},
			{
				Name: "show", Short: "Show one operation with its output", Usage: "ops show <id>", MinArgs: 1, MaxArgs: 1,
				Run: func(ctx context.Context, env *Env, g *globalFlags, args []string) error {
					return withAPI(env, g, func(c *client.Client, _ string, _ Context) error {
						d, err := c.Operation(ctx, args[0])
						if err != nil {
							return err
						}
						p := newPrinter(env, g)
						if p.JSON {
							if d.Lines == nil {
								d.Lines = []string{}
							}
							return p.JSONValue(d)
						}
						now := env.Now()
						fmt.Fprintf(env.Stdout, "ID:        %s\nStack:     %s\nAction:    %s\nState:     %s\nStarted:   %s\nDuration:  %s\n",
							oneLine(d.ID), operationStack(d.Stack), oneLine(d.Action), p.Status(oneLine(d.State)),
							d.StartedAt.Local().Format(time.RFC3339), operationDuration(d.Operation, now))
						if d.ExitCode != nil {
							fmt.Fprintf(env.Stdout, "Exit code: %d\n", *d.ExitCode)
						}
						if d.Error != "" {
							fmt.Fprintf(env.Stdout, "Error:     %s\n", p.paint("31", oneLine(d.Error)))
						}
						if len(d.Lines) > 0 {
							fmt.Fprintln(env.Stdout)
						}
						for _, l := range d.Lines {
							fmt.Fprintln(env.Stdout, cleanText(l))
						}
						return nil
					})
				},
			},
			{
				Name: "stream", Short: "Follow an operation's output until it finishes", Usage: "ops stream <id>", MinArgs: 1, MaxArgs: 1,
				Long: "Exits 0 when the operation succeeded and 9 otherwise. A finished operation prints its output and exits at once.",
				Run: func(ctx context.Context, env *Env, g *globalFlags, args []string) error {
					return withAPI(env, g, func(c *client.Client, _ string, _ Context) error {
						d, err := c.Operation(ctx, args[0])
						if err != nil {
							return err
						}
						return streamOperation(ctx, env, newPrinter(env, g), c, d.Operation)
					})
				},
			},
			{
				Name: "cancel", Short: "Cancel a running operation", Usage: "ops cancel <id>", MinArgs: 1, MaxArgs: 1,
				Run: func(ctx context.Context, env *Env, g *globalFlags, args []string) error {
					return withAPI(env, g, func(c *client.Client, _ string, _ Context) error {
						op, err := c.Cancel(ctx, args[0])
						if err != nil {
							return err
						}
						p := newPrinter(env, g)
						if p.JSON {
							return p.JSONValue(op)
						}
						fmt.Fprintf(env.Stdout, "operation %s %s\n", oneLine(op.ID), p.Status(oneLine(op.State)))
						return nil
					})
				},
			},
		},
	}
}

func networksCommand() *Command {
	return &Command{
		Name: "networks", Short: "List Docker networks",
		Sub: []*Command{{
			Name: "ls", Short: "List Docker networks and their subnets", Usage: "networks ls",
			Run: func(ctx context.Context, env *Env, g *globalFlags, _ []string) error {
				return withAPI(env, g, func(c *client.Client, _ string, _ Context) error {
					nets, err := c.Networks(ctx)
					if err != nil {
						return err
					}
					p := newPrinter(env, g)
					if p.JSON {
						if nets == nil {
							nets = []apitypes.Network{}
						}
						return p.JSONValue(nets)
					}
					rows := make([][]string, 0, len(nets))
					for _, n := range nets {
						subnets := make([]string, len(n.Subnets))
						for i, s := range n.Subnets {
							subnets[i] = oneLine(s)
						}
						rows = append(rows, []string{oneLine(n.Name), oneLine(n.Driver), strings.Join(subnets, ",")})
					}
					p.Table([]string{"name", "driver", "subnets"}, rows)
					return nil
				})
			},
		}},
	}
}

func operationStack(stack string) string {
	if stack == "" {
		return "host"
	}
	return oneLine(stack)
}
