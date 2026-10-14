package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/client"
)

const localTimeLayout = "2006-01-02 15:04:05"

// parseTimeArg reads an RFC3339 time or Unix seconds; with allowAgo also a
// duration meaning "that long ago" (`30m`, `2h`, `1d`).
func parseTimeArg(flagName, s string, now time.Time, allowAgo bool) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && n >= 0 {
		return time.Unix(n, 0), nil
	}
	if allowAgo {
		if d, err := parseAgo(s); err == nil && d > 0 {
			return now.Add(-d), nil
		}
	}
	forms := "an RFC3339 time or Unix seconds"
	if allowAgo {
		forms += ", or a duration such as 30m, 2h or 1d"
	}
	return time.Time{}, exitf(ExitUsage, "invalid --%s %q: use %s", flagName, s, forms)
}

// maxAgoDays bounds --from durations so the arithmetic cannot overflow.
const maxAgoDays = 3650

func parseAgo(s string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err == nil && (n < 0 || n > maxAgoDays) {
			err = errors.New("too far back")
		}
		return time.Duration(n) * 24 * time.Hour, err
	}
	d, err := time.ParseDuration(s)
	if err == nil && d > maxAgoDays*24*time.Hour {
		err = errors.New("too far back")
	}
	return d, err
}

func localTime(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return singleLine(s)
	}
	return t.Local().Format(localTimeLayout)
}

func dash(v *float64) string {
	if v == nil {
		return "-"
	}
	return formatRate(*v)
}

func metricsCommand() *Command {
	var from, to, service, step string
	var live bool
	return &Command{
		Name: "metrics", Short: "Show resource usage of a stack",
		Usage: "metrics <stack> [--from T] [--to T] [--step D] [--service S] [--live]",
		Long: "History covers the last hour unless --from/--to say otherwise. Per service, CPU and memory\n" +
			"are averages across its replicas and the network and block I/O rates are totals across them.\n" +
			"--live follows new samples, one row per container, with cumulative counters as totals.",
		MinArgs: 1, MaxArgs: 1,
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&from, "from", "", "start: RFC3339 time, Unix seconds, or how long ago (30m, 2h, 1d)")
			fs.StringVar(&to, "to", "", "end: RFC3339 time or Unix seconds")
			fs.StringVar(&step, "step", "", "bucket size, e.g. 30s or 5m")
			fs.StringVar(&service, "service", "", "only this service")
			fs.BoolVar(&live, "live", false, "follow new samples instead of showing history")
		},
		Run: func(ctx context.Context, env *Env, g *globalFlags, args []string) error {
			if live {
				if from != "" || to != "" || step != "" {
					return exitf(ExitUsage, "--live cannot be combined with --from, --to or --step")
				}
				return metricsLive(ctx, env, g, args[0])
			}
			q := client.MetricsQuery{Service: service}
			var err error
			if from != "" {
				if q.From, err = parseTimeArg("from", from, env.Now(), true); err != nil {
					return err
				}
			}
			if to != "" {
				if q.To, err = parseTimeArg("to", to, env.Now(), false); err != nil {
					return err
				}
			}
			if step != "" {
				if q.Step, err = time.ParseDuration(step); err != nil || q.Step <= 0 {
					return exitf(ExitUsage, "invalid --step %q: use a positive duration such as 30s or 5m", step)
				}
			}
			p := newPrinter(env, g)
			return withAPI(env, g, func(c *client.Client, _ string, _ Context) error {
				h, err := c.MetricsHistory(ctx, args[0], q)
				if err != nil {
					return err
				}
				if p.JSON {
					return p.JSONValue(h)
				}
				rows := make([][]string, 0, len(h.Points))
				for _, pt := range h.Points {
					rows = append(rows, []string{
						localTime(pt.Time), singleLine(pt.Service), formatPercent(pt.CPU), formatBytes(pt.Mem), formatBytes(pt.MemLimit),
						dash(pt.NetRx), dash(pt.NetTx), dash(pt.BlkRead), dash(pt.BlkWrite),
					})
				}
				p.Table([]string{"time", "service", "CPU (avg)", "MEM (avg)", "limit",
					"NET RX/s (total)", "NET TX/s (total)", "BLK R/s (total)", "BLK W/s (total)"}, rows)
				if len(rows) == 0 {
					fmt.Fprintln(env.Stderr, "no samples in this window")
				}
				return nil
			})
		},
	}
}

// liveRow prints one fixed-width row; a live feed has no final column
// widths, so cells are padded to a preset width and never truncated.
func liveRow(w io.Writer, cells ...string) {
	widths := []int{19, 12, 12, 7, 10, 10, 14, 14, 14, 14} // visible widths
	var b strings.Builder
	for i, c := range cells {
		b.WriteString(c)
		if i < len(cells)-1 {
			b.WriteString(strings.Repeat(" ", max(widths[i]-utf8.RuneCountInString(stripANSI(c)), 0)+2))
		}
	}
	fmt.Fprintln(w, b.String())
}

func metricsLive(ctx context.Context, env *Env, g *globalFlags, stack string) error {
	p := newPrinter(env, g)
	return withAPI(env, g, func(c *client.Client, _ string, _ Context) error {
		headerDone := false
		err := c.StreamMetricsLive(ctx, stack, streamOptions(env, nil, false), func(ev client.Event) error {
			if ev.Name != "sample" {
				return nil // "dropped": the client reconnects by itself
			}
			if p.JSON {
				return p.NDJSON("sample", ev.ID, eventData(ev))
			}
			var s apitypes.Sample
			if err := json.Unmarshal(ev.Data, &s); err != nil {
				fmt.Fprintf(env.Stderr, "warning: skipped a malformed sample: %v\n", err)
				return nil
			}
			if !headerDone {
				headerDone = true
				liveRow(p.Out, p.paint("2", "TIME"), p.paint("2", "SERVICE"), p.paint("2", "CONTAINER"), p.paint("2", "CPU"),
					p.paint("2", "MEM"), p.paint("2", "LIMIT"), p.paint("2", "NET RX (TOTAL)"), p.paint("2", "NET TX (TOTAL)"),
					p.paint("2", "BLK R (TOTAL)"), p.paint("2", "BLK W (TOTAL)"))
			}
			liveRow(p.Out, localTime(s.Time), singleLine(s.Service), singleLine(s.Container), formatPercent(s.CPU),
				formatBytes(s.Mem), formatBytes(s.MemLimit), formatBytes(s.NetRx), formatBytes(s.NetTx),
				formatBytes(s.BlkRead), formatBytes(s.BlkWrite))
			return nil
		})
		return interrupted(ctx, err)
	})
}

func eventsCommand() *Command {
	return &Command{
		Name: "events", Short: "Follow container changes until interrupted", Usage: "events",
		Long:    "Prints the connection state of the server's Docker event feed and every project whose containers changed.",
		MaxArgs: 0,
		Run: func(ctx context.Context, env *Env, g *globalFlags, args []string) error {
			p := newPrinter(env, g)
			return withAPI(env, g, func(c *client.Client, _ string, _ Context) error {
				err := c.StreamEvents(ctx, streamOptions(env, nil, false), func(ev client.Event) error {
					if p.JSON {
						if ev.Name == "status" || ev.Name == "change" {
							return p.NDJSON(ev.Name, ev.ID, eventData(ev))
						}
						return nil
					}
					stamp := env.Now().Local().Format(localTimeLayout)
					switch ev.Name {
					case "status":
						var s apitypes.StatusFlags
						if err := json.Unmarshal(ev.Data, &s); err != nil {
							return nil
						}
						metrics := "ok"
						if s.MetricsDegraded {
							metrics = "degraded"
						}
						fmt.Fprintf(p.Out, "%s  status events=%s metrics=%s\n", stamp, singleLine(s.Events), metrics)
					case "change":
						var ch apitypes.Change
						if err := json.Unmarshal(ev.Data, &ch); err != nil {
							return nil
						}
						fmt.Fprintf(p.Out, "%s  change %s seq=%d\n", stamp, singleLine(ch.Project), ch.Seq)
					}
					return nil
				})
				return interrupted(ctx, err)
			})
		},
	}
}
