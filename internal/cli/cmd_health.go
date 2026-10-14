package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/config"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/store"
)

const healthProbeTimeout = 3 * time.Second

// healthCheck is one row of the health report.
type healthCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

type healthReport struct {
	OK     bool          `json:"ok"`
	Checks []healthCheck `json:"checks"`
}

func healthCommand() *Command {
	var lf localFlags
	return &Command{
		Name: "health", Short: "Check config, database, server, Docker and compose", Usage: "health [--config P] [-o json]",
		Long:  "Exit 0 when everything is fine, 7 when the server, Docker or compose is unreachable, 1 for a config or database problem.",
		Flags: lf.register,
		Run: func(ctx context.Context, env *Env, g *globalFlags, _ []string) error {
			report, localProblem := runHealth(ctx, env, lf)
			p := newPrinter(env, g)
			if p.JSON {
				if err := p.JSONValue(report); err != nil {
					return err
				}
			} else {
				rows := make([][]string, len(report.Checks))
				for i, c := range report.Checks {
					result := "ok"
					switch {
					case !c.OK:
						result = p.paint("31", "fail") + ": " + c.Detail
					case c.Detail != "":
						result = "ok (" + c.Detail + ")"
						if strings.HasPrefix(c.Detail, "skipped") {
							result = c.Detail
						}
					}
					rows[i] = []string{c.Name, result}
				}
				p.Table([]string{"check", "result"}, rows)
			}
			switch {
			case report.OK:
				return nil
			case localProblem:
				return &ExitError{Code: ExitGeneral}
			default:
				return &ExitError{Code: ExitUnavailable}
			}
		},
	}
}

// runHealth runs every check; localProblem is true when config or database failed.
func runHealth(ctx context.Context, env *Env, lf localFlags) (report healthReport, localProblem bool) {
	add := func(name string, err error, detail string) bool {
		c := healthCheck{Name: name, OK: err == nil, Detail: detail}
		if err != nil {
			c.Detail = err.Error()
		}
		report.Checks = append(report.Checks, c)
		return err == nil
	}
	cfg, path, err := loadLocalConfig(env, lf)
	if !add("config", err, configDetail(path)) {
		for _, name := range []string{"database", "server", "docker", "compose"} {
			report.Checks = append(report.Checks, healthCheck{Name: name, Detail: "skipped: the config is invalid"})
		}
		return report, true
	}

	dbDetail, dbErr := checkDatabase(ctx, env, cfg)
	add("database", dbErr, dbDetail)
	if dbErr != nil {
		localProblem = true
	}

	serverOK := add("server", checkServer(ctx, cfg), "")
	dockerOK := add("docker", engine.New(cfg.DockerSocket, healthProbeTimeout).Ping(ctx), "")
	composeVersion, composeErr := composeCheck(ctx, cfg)
	composeOK := add("compose", composeErr, composeVersion)

	report.OK = !localProblem && serverOK && dockerOK && composeOK
	return report, localProblem
}

func configDetail(path string) string {
	if path == "" {
		return "defaults and environment"
	}
	return path
}

func checkDatabase(ctx context.Context, env *Env, cfg config.Config) (string, error) {
	if uid, foreign, err := foreignOwner(cfg); err != nil {
		return "", err
	} else if foreign {
		return "skipped: run as " + ownerName(uid), nil
	}
	db, err := openLocalDB(env, cfg, store.OpenOptions{ReadOnly: true})
	if err != nil {
		return "", err
	}
	defer db.Close()
	problems, err := store.QuickCheck(ctx, db)
	if err != nil {
		return "", err
	}
	if len(problems) > 0 {
		return "", fmt.Errorf("quick_check: %s", strings.Join(problems, "; "))
	}
	version, err := store.SchemaVersion(ctx, db)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("schema %d", version), nil
}

func checkServer(ctx context.Context, cfg config.Config) error {
	host, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen %q: %w", cfg.Listen, err)
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	ctx, cancel := context.WithTimeout(ctx, healthProbeTimeout)
	defer cancel()
	url := "http://" + net.JoinHostPort(host, port) + "/api/health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: healthProbeTimeout}).Do(req)
	if err != nil {
		return fmt.Errorf("server not reachable at %s: %w", cfg.Listen, err)
	}
	defer resp.Body.Close()
	var h apitypes.Health
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&h) != nil || !h.OK {
		return fmt.Errorf("unexpected answer from %s (HTTP %d)", url, resp.StatusCode)
	}
	return nil
}

func composeCheck(ctx context.Context, cfg config.Config) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, healthProbeTimeout)
	defer cancel()
	return buildRunner(cfg).Version(ctx)
}
