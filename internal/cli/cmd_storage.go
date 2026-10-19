package cli

import (
	"context"
	"flag"
	"fmt"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/client"
)

func storageCommand() *Command {
	var preview, yes, detach bool
	return &Command{Name: "storage", Short: "Show disk usage and clean unused Docker storage", Sub: []*Command{
		{Name: "df", Short: "Show Docker storage usage", Usage: "storage df", Run: func(ctx context.Context, env *Env, g *globalFlags, _ []string) error {
			return withAPI(env, g, func(c *client.Client, _ string, _ Context) error {
				overview, err := c.StorageOverview(ctx)
				if err != nil {
					return err
				}
				p := newPrinter(env, g)
				if p.JSON {
					return p.JSONValue(overview)
				}
				rows := make([][]string, 0, 4)
				for _, kind := range []struct {
					name   string
					totals apitypes.StorageKindTotals
				}{{"images", overview.Images}, {"containers", overview.Containers}, {"volumes", overview.Volumes}, {"build-cache", overview.BuildCache}} {
					age := "—"
					if kind.totals.SizesAt != nil {
						age = relativeTime(*kind.totals.SizesAt, env.Now())
					}
					if kind.totals.Refreshing {
						age += " refreshing"
					}
					rows = append(rows, []string{kind.name, fmt.Sprint(kind.totals.Count), storageBytes(kind.totals.Size), storageBytes(kind.totals.Reclaimable), age})
				}
				p.Table([]string{"kind", "count", "size", "reclaimable", "as of"}, rows)
				return nil
			})
		}},
		{Name: "prune", Short: "Preview and confirm cleanup of unused storage", Usage: "storage prune <images|volumes|build-cache> [--preview] [--yes] [--detach]", MinArgs: 1, MaxArgs: 1,
			Flags: func(fs *flag.FlagSet) {
				fs.BoolVar(&preview, "preview", false, "print the cleanup plan without confirming")
				fs.BoolVar(&yes, "yes", false, "confirm the plan without prompting")
				fs.BoolVar(&detach, "detach", false, "print the operation id and return without waiting")
			},
			Run: func(ctx context.Context, env *Env, g *globalFlags, args []string) error {
				selection := "unused"
				switch args[0] {
				case "images":
					selection = "dangling"
				case "volumes", "build-cache":
				default:
					return exitf(ExitUsage, "unknown storage kind %q: use images, volumes or build-cache", args[0])
				}
				return withAPI(env, g, func(c *client.Client, _ string, _ Context) error {
					plan, err := c.CreateStoragePlan(ctx, apitypes.StoragePlanRequest{Kind: args[0], Selection: selection})
					if err != nil {
						return err
					}
					p := newPrinter(env, g)
					printStoragePlan(p, plan)
					if len(plan.Items) == 0 {
						fmt.Fprintln(env.Stdout, "nothing to clean")
						return nil
					}
					if preview {
						return nil
					}
					if err := confirmTyped(env, plan.ConfirmPhrase, yes); err != nil {
						return err
					}
					op, err := c.ConfirmStoragePlan(ctx, plan.ID, plan.ConfirmPhrase)
					if err != nil {
						return err
					}
					return startAndFollow(ctx, env, g, c, op, detach)
				})
			},
		},
	}}
}

func storageBytes(n *int64) string {
	if n == nil || *n < 0 {
		return "—"
	}
	return formatBytes(uint64(*n))
}

func printStoragePlan(p *Printer, plan apitypes.StoragePlan) {
	out := p.Out
	rows := make([][]string, 0, len(plan.Items))
	for _, item := range plan.Items {
		ref := item.Ref
		if ref == "" {
			ref = item.ID
		}
		rows = append(rows, []string{oneLine(item.Kind), oneLine(ref), storageBytes(item.Bytes)})
	}
	p.Table([]string{"kind", "item", "size"}, rows)
	if len(plan.Protected) > 0 {
		fmt.Fprintln(out, "Protected:")
		rows = make([][]string, 0, len(plan.Protected))
		for _, item := range plan.Protected {
			ref := item.Ref
			if ref == "" {
				ref = item.ID
			}
			rows = append(rows, []string{oneLine(ref), oneLine(item.Reason), oneLine(item.Stack), oneLine(item.StackState)})
		}
		p.Table([]string{"item", "reason", "stack", "state"}, rows)
	}
	for _, warning := range plan.Warnings {
		fmt.Fprintf(out, "Warning: %s\n", oneLine(warning))
	}
	reclaim := storageBytes(plan.Reclaim.Min)
	if plan.Reclaim.Max != nil && (plan.Reclaim.Min == nil || *plan.Reclaim.Min != *plan.Reclaim.Max) {
		reclaim += " – " + storageBytes(plan.Reclaim.Max)
	}
	if !plan.Reclaim.Exact || plan.Mode == "all-unused" {
		reclaim += " (not exact)"
	}
	fmt.Fprintf(out, "Reclaim: %s\n", reclaim)
}
