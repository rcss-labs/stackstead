package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/client"
	"github.com/rcss-labs/stackstead/internal/client/execws"
)

const (
	stdinFD  = 0
	stdoutFD = 1
)

// resolveExecTarget picks the running container to exec into, either by
// stack and service (with --index choosing among replicas) or by --container
// (a name or an ID prefix).
func resolveExecTarget(rows []apitypes.ContainerRow, stack, service, container string, index int) (apitypes.ContainerRow, error) {
	if index < 0 {
		return apitypes.ContainerRow{}, exitf(ExitUsage, "invalid --index %d: replica numbers start at 1", index)
	}
	if container != "" {
		if stack != "" || service != "" {
			return apitypes.ContainerRow{}, exitf(ExitUsage, "use either <stack> <service> or --container, not both")
		}
		var match []apitypes.ContainerRow
		for _, r := range rows {
			if r.Name == container {
				match = []apitypes.ContainerRow{r}
				break
			}
			if strings.HasPrefix(r.ID, container) {
				match = append(match, r)
			}
		}
		switch len(match) {
		case 0:
			return apitypes.ContainerRow{}, exitf(ExitNotFound, "no container named or with id %q", singleLine(container))
		case 1:
			return runningOrConflict(match[0])
		}
		return apitypes.ContainerRow{}, exitf(ExitUsage, "container id prefix %q is ambiguous: %s", singleLine(container), rowNames(match))
	}
	if stack == "" || service == "" {
		return apitypes.ContainerRow{}, exitf(ExitUsage, "name a <stack> and <service>, or use --container")
	}

	var replicas []apitypes.ContainerRow
	for _, r := range rows {
		if r.Stack == stack && r.Service == service {
			replicas = append(replicas, r)
		}
	}
	if len(replicas) == 0 {
		return apitypes.ContainerRow{}, exitf(ExitNotFound, "no container for service %q in stack %q", singleLine(service), singleLine(stack))
	}
	slices.SortFunc(replicas, func(a, b apitypes.ContainerRow) int { return strings.Compare(a.Name, b.Name) })

	if index > 0 {
		// Replica numbers come from compose labels; a server that does not
		// send them leaves every Number at 0, so fall back to name order.
		hasNumbers := !slices.ContainsFunc(replicas, func(r apitypes.ContainerRow) bool { return r.Number == 0 })
		for i, r := range replicas {
			if (hasNumbers && r.Number == index) || (!hasNumbers && i+1 == index) {
				return runningOrConflict(r)
			}
		}
		return apitypes.ContainerRow{}, exitf(ExitNotFound, "service %q has no replica %d (%s)", singleLine(service), index, rowNames(replicas))
	}

	var running []apitypes.ContainerRow
	for _, r := range replicas {
		if r.State == "running" {
			running = append(running, r)
		}
	}
	switch len(running) {
	case 0:
		return runningOrConflict(replicas[0])
	case 1:
		return running[0], nil
	}
	return apitypes.ContainerRow{}, exitf(ExitUsage, "service %q has %d running replicas; choose one with --index N: %s",
		singleLine(service), len(running), rowNames(running))
}

func runningOrConflict(r apitypes.ContainerRow) (apitypes.ContainerRow, error) {
	if r.State != "running" {
		return apitypes.ContainerRow{}, exitf(ExitConflict, "container is not running: %s is %s", singleLine(r.Name), singleLine(r.State))
	}
	return r, nil
}

func rowNames(rows []apitypes.ContainerRow) string {
	names := make([]string, len(rows))
	for i, r := range rows {
		names[i] = singleLine(r.Name)
	}
	return strings.Join(names, ", ")
}

func execCommand() *Command {
	var container, user string
	var index int
	var noTTY bool
	return &Command{
		Name: "exec", Short: "Run a command or a shell inside a running container",
		Usage: "exec (<stack> <service> | --container <name|id>) [--index N] [--user U] [-T] [-- cmd args...]",
		Long: "Without a command the server starts a shell. A terminal is allocated when both stdin and stdout\n" +
			"are terminals (-T turns it off); stdin is then sent raw, so Ctrl-C goes to the remote program.\n" +
			"The remote command's exit code becomes this command's exit code, even where it collides with\n" +
			"the codes of stackstead itself.",
		MinArgs: 0, MaxArgs: -1,
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&container, "container", "", "container name or id prefix instead of <stack> <service>")
			fs.IntVar(&index, "index", 0, "replica number when the service has several (starting at 1)")
			fs.StringVar(&user, "user", "", "user to run as (container default when unset)")
			fs.BoolVar(&noTTY, "T", false, "do not allocate a terminal (for pipes and scripts)")
		},
		Run: func(ctx context.Context, env *Env, g *globalFlags, args []string) error {
			var stack, service string
			cmdArgs := args
			if container == "" {
				if len(args) >= 1 {
					stack = args[0]
				}
				if len(args) >= 2 {
					service = args[1]
				}
				cmdArgs = args[min(len(args), 2):]
			}
			return withAPI(env, g, func(c *client.Client, _ string, _ Context) error {
				rows, err := c.Containers(ctx)
				if err != nil {
					return err
				}
				target, err := resolveExecTarget(rows, stack, service, container, index)
				if err != nil {
					return err
				}
				tty := !noTTY && env.IsTerminal(stdinFD) && env.IsTerminal(stdoutFD)
				return runExec(ctx, env, c, target, execws.Options{Cmd: cmdArgs, User: user, TTY: tty})
			})
		},
	}
}

// runExec dials the session, wires the terminal and stdin, and turns the way
// the session ended into an error with the right exit code.
func runExec(ctx context.Context, env *Env, c *client.Client, target apitypes.ContainerRow, opts execws.Options) error {
	if opts.TTY && env.TermSize != nil {
		if cols, rows, err := env.TermSize(stdoutFD); err == nil {
			opts.Cols, opts.Rows = cols, rows
		}
	}
	sess, err := execws.Dial(ctx, c.Config(), target.ID, opts)
	if err != nil {
		if errors.Is(err, execws.ErrOldServer) {
			return &ExitError{Code: ExitUnavailable, Err: err}
		}
		return err
	}
	defer sess.Close()

	if opts.TTY && env.MakeRaw != nil {
		restore, err := env.MakeRaw(stdinFD)
		if err != nil {
			return fmt.Errorf("put the terminal in raw mode: %w", err)
		}
		defer restore() // runs on every return path and on panic
	}

	sessionCtx, stop := context.WithCancel(ctx)
	defer stop()
	if opts.TTY && env.Resizes != nil && env.TermSize != nil {
		go forwardResizes(sessionCtx, env, sess)
	}
	// The stdin goroutine is not waited for: a blocked terminal read cannot be
	// interrupted, and the process ends right after the command returns.
	go copyStdin(env.Stdin, sess, opts.TTY)

	end, err := sess.Run(env.Stdout, env.Stderr, func(seconds int) {
		fmt.Fprintf(env.Stderr, "\r\n[stackstead: idle session closes in %ds]\r\n", seconds)
	})
	if err != nil {
		return interrupted(ctx, err)
	}
	return endError(end)
}

func forwardResizes(ctx context.Context, env *Env, sess *execws.Session) {
	ch := env.Resizes(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ch:
			if !ok {
				return
			}
			if cols, rows, err := env.TermSize(stdoutFD); err == nil {
				_ = sess.Resize(cols, rows)
			}
		}
	}
}

// copyStdin sends local input to the command. Without a terminal, end of
// input is signalled with an eof frame; on a terminal EOF is just a byte
// the remote side interprets, so nothing is sent.
func copyStdin(in io.Reader, sess *execws.Session, tty bool) {
	buf := make([]byte, 32*1024)
	for {
		n, err := in.Read(buf)
		if n > 0 && sess.Stdin(buf[:n]) != nil {
			return
		}
		if err != nil {
			if !tty {
				_ = sess.CloseStdin()
			}
			return
		}
	}
}

// endError maps how the session ended to an exit code. A remote exit code is
// passed through unchanged and silently: the command's own output already
// said what there is to say.
func endError(end execws.End) error {
	if end.ExitCode != nil {
		if *end.ExitCode == 0 {
			return nil
		}
		return &ExitError{Code: *end.ExitCode}
	}
	switch {
	case end.Reason == "":
		return nil
	case end.Reason == "idle":
		return exitf(ExitUnavailable, "session closed after being idle")
	case end.Reason == "session_expired":
		return exitf(ExitAuth, "your session expired; log in again")
	case end.Reason == "shutdown":
		return exitf(ExitUnavailable, "the server is shutting down")
	case strings.HasPrefix(end.Reason, "error: "):
		return exitf(ExitGeneral, "%s", oneLine(strings.TrimPrefix(end.Reason, "error: ")))
	}
	return exitf(ExitGeneral, "session ended: %s", oneLine(end.Reason))
}
