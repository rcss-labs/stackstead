package cli

import (
	"context"
	"errors"
	"flag"
	"net/http"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/client"
)

func backupCommand() *Command {
	return &Command{Name: "backup", Short: "Run backups and list archives", Sub: []*Command{backupRunCommand(), backupListCommand()}}
}

func backupRunCommand() *Command {
	var detach bool
	return &Command{Name: "run", Short: "Run a backup now and follow the operation", Usage: "backup run [--detach]",
		Long: "Streams the backup's output and exits 9 when the run failed or was partial (some items failed); --detach prints the operation id and returns at once.",
		Flags: func(fs *flag.FlagSet) {
			fs.BoolVar(&detach, "detach", false, "print the operation id and return without waiting")
		},
		Run: func(ctx context.Context, env *Env, g *globalFlags, _ []string) error {
			return withAPI(env, g, func(c *client.Client, _ string, _ Context) error {
				op, err := c.BackupRun(ctx)
				if err != nil {
					return backupConflict(err)
				}
				if err := startAndFollow(ctx, env, g, c, op, detach); err != nil || detach {
					return err
				}
				// A partial run still ends its operation as succeeded; the run record tells.
				runs, err := c.BackupRuns(ctx, 1)
				if err != nil || len(runs) == 0 || runs[0].Status != "partial" {
					return err
				}
				failed := 0
				for _, it := range runs[0].Items {
					if it.Status == "failed" {
						failed++
					}
				}
				return exitf(ExitOperationFailed, "backup partial: %d item(s) failed", failed)
			})
		}}
}

func backupConflict(err error) error {
	var ce *client.Error
	if errors.As(err, &ce) && ce.Status == http.StatusConflict {
		return conflictError(err, oneLine(ce.Message))
	}
	return err
}

func backupListCommand() *Command {
	return &Command{Name: "list", Short: "List backup archives", Usage: "backup list [-o table|json]",
		Run: func(ctx context.Context, env *Env, g *globalFlags, _ []string) error {
			return withAPI(env, g, func(c *client.Client, _ string, _ Context) error {
				archives, err := c.BackupArchives(ctx)
				if err != nil {
					return err
				}
				p := newPrinter(env, g)
				if p.JSON {
					if archives == nil {
						archives = []apitypes.BackupArchive{}
					}
					return p.JSONValue(archives)
				}
				rows := make([][]string, 0, len(archives))
				for _, a := range archives {
					rows = append(rows, []string{dashIfEmpty(oneLine(a.HostSlug)), relativeTime(a.CreatedAt, env.Now()),
						formatBytes(uint64(max(a.Size, 0))), oneLine(a.Dest)})
				}
				p.Table([]string{"host", "time", "size", "destination"}, rows)
				return nil
			})
		}}
}
