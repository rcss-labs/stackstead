package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/auth"
	"github.com/rcss-labs/stackstead/internal/config"
	"github.com/rcss-labs/stackstead/internal/metrics"
	"github.com/rcss-labs/stackstead/internal/store"
)

func adminCommand() *Command {
	var lf localFlags
	var username, passwordFile string
	var passwordStdin bool
	run := func(fn func(ctx context.Context, env *Env, g *globalFlags, cfg config.Config) error) func(context.Context, *Env, *globalFlags, []string) error {
		return func(ctx context.Context, env *Env, g *globalFlags, _ []string) error {
			cfg, _, err := loadLocalConfig(env, lf)
			if err != nil {
				return err
			}
			return fn(ctx, env, g, cfg)
		}
	}
	return &Command{
		Name: "admin", Short: "Admin account and token commands (run as the service user)",
		Sub: []*Command{
			{Name: "set-password", Short: "Create the admin or reset its password", Usage: "admin set-password [--username U] (--password-stdin | --password-file F) [--config P]",
				Long: "Works while the server runs. Resetting signs out every session. The password is read from one line of stdin or from a file; an interactive prompt is not available yet.",
				Flags: func(fs *flag.FlagSet) {
					lf.register(fs)
					fs.StringVar(&username, "username", "", "admin username (default: the existing admin, else admin)")
					fs.BoolVar(&passwordStdin, "password-stdin", false, "read the password from one line of stdin")
					fs.StringVar(&passwordFile, "password-file", "", "read the password from the first line of a file")
				},
				Run: run(func(ctx context.Context, env *Env, _ *globalFlags, cfg config.Config) error {
					return setPassword(ctx, env, cfg, username, passwordStdin, passwordFile)
				})},
			{Name: "revoke-sessions", Short: "Sign out every session", Usage: "admin revoke-sessions [--config P]", Flags: lf.register,
				Run: run(func(ctx context.Context, env *Env, _ *globalFlags, cfg config.Config) error {
					db, err := openLocalDB(env, cfg, store.OpenOptions{})
					if err != nil {
						return err
					}
					defer db.Close()
					svc, err := auth.NewService(db, env.Now)
					if err != nil {
						return err
					}
					n, err := svc.RevokeAllSessions(ctx)
					if err != nil {
						return err
					}
					fmt.Fprintf(env.Stdout, "signed out %d session(s)\n", n)
					return nil
				})},
			{Name: "metrics-token", Short: "Manage the /metrics bearer token",
				Sub: []*Command{
					{Name: "status", Short: "Show whether a token is set", Usage: "admin metrics-token status [--config P]", Flags: lf.register,
						Run: run(func(ctx context.Context, env *Env, g *globalFlags, cfg config.Config) error {
							return withMetricsStore(env, cfg, func(ms *metrics.Store) error {
								hash, err := ms.TokenHash(ctx)
								if err != nil {
									return err
								}
								p := newPrinter(env, g)
								if p.JSON {
									return p.JSONValue(apitypes.MetricsTokenStatus{Configured: hash != ""})
								}
								if hash != "" {
									fmt.Fprintln(env.Stdout, "metrics token: configured")
								} else {
									fmt.Fprintln(env.Stdout, "metrics token: not configured")
								}
								return nil
							})
						})},
					{Name: "rotate", Short: "Create a new token (printed once)", Usage: "admin metrics-token rotate [--config P]", Flags: lf.register,
						Run: run(func(ctx context.Context, env *Env, g *globalFlags, cfg config.Config) error {
							return withMetricsStore(env, cfg, func(ms *metrics.Store) error {
								plain, hash, err := metrics.NewToken()
								if err != nil {
									return err
								}
								if err := ms.SetTokenHash(ctx, hash); err != nil {
									return err
								}
								p := newPrinter(env, g)
								if p.JSON {
									err = p.JSONValue(apitypes.MetricsToken{Token: plain})
								} else {
									_, err = fmt.Fprintln(env.Stdout, plain)
								}
								fmt.Fprintln(env.Stderr, "the server picks this up within 5 s; the old token stops working then")
								return err
							})
						})},
					{Name: "clear", Short: "Disable /metrics", Usage: "admin metrics-token clear [--config P]", Flags: lf.register,
						Run: run(func(ctx context.Context, env *Env, _ *globalFlags, cfg config.Config) error {
							return withMetricsStore(env, cfg, func(ms *metrics.Store) error {
								if err := ms.SetTokenHash(ctx, ""); err != nil {
									return err
								}
								fmt.Fprintln(env.Stdout, "metrics token cleared; /metrics is disabled (within 5 s on a running server)")
								return nil
							})
						})},
				}},
		},
	}
}

func withMetricsStore(env *Env, cfg config.Config, fn func(*metrics.Store) error) error {
	db, err := openLocalDB(env, cfg, store.OpenOptions{})
	if err != nil {
		return err
	}
	defer db.Close()
	return fn(metrics.NewStore(db, env.Now))
}

func setPassword(ctx context.Context, env *Env, cfg config.Config, username string, fromStdin bool, file string) error {
	switch {
	case fromStdin && file != "":
		return exitf(ExitUsage, "use only one of --password-stdin and --password-file")
	case !fromStdin && file == "":
		return exitf(ExitUsage, "give the password with --password-stdin or --password-file")
	}
	var src io.Reader = env.Stdin
	if file != "" {
		f, err := os.Open(file)
		if err != nil {
			return err
		}
		defer f.Close()
		src = f
	}
	password, err := readSecretLine(src)
	if err != nil {
		return err
	}

	db, err := openLocalDB(env, cfg, store.OpenOptions{})
	if err != nil {
		return err
	}
	defer db.Close()
	svc, err := auth.NewService(db, env.Now)
	if err != nil {
		return err
	}
	if username == "" {
		if username, err = svc.AdminUsername(ctx); err != nil {
			return err
		}
		if username == "" {
			username = "admin"
		}
	}
	created, err := svc.ResetPassword(ctx, username, password)
	switch {
	case errors.Is(err, auth.ErrWeakPassword), errors.Is(err, auth.ErrUsernameMismatch), errors.Is(err, auth.ErrInvalidUsername):
		return &ExitError{Code: ExitValidation, Err: err}
	case err != nil:
		return err
	case created:
		fmt.Fprintf(env.Stdout, "admin account %s created\n", username)
	default:
		fmt.Fprintf(env.Stdout, "password for %s updated; all sessions were signed out\n", username)
	}
	return nil
}

// readSecretLine reads the first line and strips its line ending.
func readSecretLine(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
