package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash/fnv"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/client"
)

// liveStreamBackoff is the reconnect backoff of logs, metrics --live and
// events; tests shrink it.
var liveStreamBackoff = struct{ Min, Max time.Duration }{time.Second, 30 * time.Second}

// liveStreamOptions builds the stream options shared by every streaming
// command: the backoff, an optional resume id, and a reconnect note on stderr.
func liveStreamOptions(env *Env, lastID func() string) client.StreamOptions {
	opts := client.StreamOptions{LastEventID: lastID}
	opts.Backoff.Min, opts.Backoff.Max = liveStreamBackoff.Min, liveStreamBackoff.Max
	opts.OnReconnect = func(attempt int, err error) {
		if err != nil {
			fmt.Fprintf(env.Stderr, "stream interrupted (%s); reconnecting (attempt %d)\n", cleanText(err.Error()), attempt)
			return
		}
		fmt.Fprintf(env.Stderr, "stream interrupted; reconnecting (attempt %d)\n", attempt)
	}
	return opts
}

// interrupted reports whether the command ended because the user pressed
// Ctrl-C (or the process was terminated): exit 130 with nothing to print.
func interrupted(ctx context.Context, err error) error {
	if err != nil && ctx.Err() != nil && errors.Is(err, ctx.Err()) {
		return &ExitError{Code: ExitInterrupted}
	}
	return err
}

// logCursors remembers the id of the last log line seen per container. The
// server resumes each container from its own cursor, so a reconnect must
// send the latest id of every container, not just the last one overall.
type logCursors struct {
	mu  sync.Mutex
	ids map[string]string
}

// Set records id ("<container12>:<unix ns>:<ordinal>"); malformed ids are ignored.
func (l *logCursors) Set(id string) {
	container, _, ok := strings.Cut(id, ":")
	if !ok || container == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ids == nil {
		l.ids = map[string]string{}
	}
	l.ids[container] = id
}

// String is the Last-Event-ID value: all cursors, comma-separated.
func (l *logCursors) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	ids := make([]string, 0, len(l.ids))
	for _, id := range l.ids {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return strings.Join(ids, ",")
}

// Terminal safety of log text. Container output is untrusted: an escape
// sequence in it could retitle the window, rewrite the screen, or (on some
// terminals) inject input. So by default every escape sequence and control
// character except TAB is dropped, which also drops colour codes; --raw
// opts out for people who want the colours and trust their stacks. JSON
// output is never touched (JSON escapes control characters itself).
var escapeSequence = regexp.MustCompile(
	`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)?` + // OSC
		`|\x1b[PX^_][^\x1b]*(?:\x1b\\)?` + // DCS, SOS, PM, APC
		`|\x1b\[[0-?]*[ -/]*[@-~]?` + // CSI
		`|\x{9b}[0-?]*[ -/]*[@-~]?` + // 8-bit CSI
		`|\x1b[ -/]*[0-~]?`) // other two- and three-byte escapes

func sanitizeLogText(s string) string {
	s = escapeSequence.ReplaceAllString(s, "")
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\t' {
			return -1
		}
		return r
	}, s)
}

var servicePalette = []string{"36", "33", "35", "34", "32", "96", "93", "95"}

func serviceColour(service string) string {
	h := fnv.New32a()
	h.Write([]byte(service))
	return servicePalette[int(h.Sum32())%len(servicePalette)]
}

func logsCommand() *Command {
	var follow, timestamps, raw bool
	var tail, since, service string
	return &Command{
		Name: "logs", Short: "Show container logs of a stack",
		Usage: "logs <stack> [-f] [--tail N|all] [--since X] [--service S] [--timestamps] [--raw]",
		Long: "Lines are prefixed with their service. Escape sequences and control characters in the\n" +
			"output are removed so a container cannot drive your terminal; --raw prints the text unchanged.\n" +
			"With -f the stream follows new lines and resumes without gaps or repeats after a reconnect.",
		MinArgs: 1, MaxArgs: 1,
		Flags: func(fs *flag.FlagSet) {
			fs.BoolVar(&follow, "f", false, "follow new log lines")
			fs.BoolVar(&follow, "follow", false, "follow new log lines")
			fs.StringVar(&tail, "tail", "", "lines to show per container: a number or all (server default when unset)")
			fs.StringVar(&since, "since", "", "only logs since an RFC3339 time, a Unix timestamp or a duration such as 10m")
			fs.StringVar(&service, "service", "", "only this service")
			fs.BoolVar(&timestamps, "timestamps", false, "prefix each line with its time")
			fs.BoolVar(&raw, "raw", false, "do not strip escape sequences and control characters from the log text")
		},
		Run: func(ctx context.Context, env *Env, g *globalFlags, args []string) error {
			if tail != "" && tail != "all" {
				if n, err := strconv.Atoi(tail); err != nil || n < 0 {
					return exitf(ExitUsage, "invalid --tail %q: use a number >= 0 or all", tail)
				}
			}
			p := newPrinter(env, g)
			q := client.LogsQuery{Follow: follow, Tail: tail, Since: since, Service: service}
			return withAPI(env, g, func(c *client.Client, _ string, _ Context) error {
				var cursors logCursors
				opts := liveStreamOptions(env, cursors.String)
				err := c.StreamLogs(ctx, args[0], q, opts, func(ev client.Event) error {
					return handleLogEvent(env, p, &cursors, ev, timestamps, raw)
				})
				return interrupted(ctx, err)
			})
		},
	}
}

func handleLogEvent(env *Env, p *Printer, cursors *logCursors, ev client.Event, timestamps, raw bool) error {
	switch ev.Name {
	case "log":
		cursors.Set(ev.ID)
		if p.JSON {
			return p.NDJSON("log", ev.ID, eventData(ev))
		}
		var le apitypes.LogEvent
		if err := json.Unmarshal(ev.Data, &le); err != nil {
			fmt.Fprintf(env.Stderr, "warning: skipped a malformed log event: %v\n", err)
			return nil
		}
		text := le.Text
		if !raw {
			text = sanitizeLogText(text)
		}
		prefix := p.paint(serviceColour(le.Service), "["+cleanText(le.Service)+"]")
		if timestamps {
			prefix = le.Time.UTC().Format(time.RFC3339Nano) + " " + prefix
		}
		line := prefix + " " + text
		if le.Stream == "stderr" {
			line = prefix + " " + p.paint("2;31", text)
		}
		fmt.Fprintln(p.Out, line)
	case "error":
		if p.JSON {
			return p.NDJSON("error", ev.ID, eventData(ev))
		}
		var le apitypes.LogError
		if err := json.Unmarshal(ev.Data, &le); err != nil {
			fmt.Fprintf(env.Stderr, "warning: skipped a malformed log error event: %v\n", err)
			return nil
		}
		fmt.Fprintf(env.Stderr, "log stream error for %s: %s\n", cleanText(le.Container), sanitizeLogText(le.Error))
	case "end":
		if p.JSON {
			if err := p.NDJSON("end", ev.ID, eventData(ev)); err != nil {
				return err
			}
		}
		return client.ErrStreamDone
	}
	return nil
}

// eventData is the event payload as JSON, `{}` when the event had none.
func eventData(ev client.Event) json.RawMessage {
	if !json.Valid(ev.Data) {
		return json.RawMessage(`{}`)
	}
	return ev.Data
}
