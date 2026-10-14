package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/client"
)

func settingsCommand() *Command {
	return &Command{
		Name:  "settings",
		Short: "Server settings: the metrics token and the account password",
		Sub: []*Command{
			{
				Name:  "metrics-token",
				Short: "Manage the bearer token that protects /metrics",
				Sub: []*Command{
					{
						Name: "status", Short: "Show whether a metrics token is configured", Usage: "settings metrics-token status",
						Run: func(ctx context.Context, env *Env, g *globalFlags, _ []string) error {
							return withAPI(env, g, func(c *client.Client, _ string, _ Context) error {
								st, err := c.MetricsTokenStatus(ctx)
								if err != nil {
									return err
								}
								if g.Output == "json" {
									return newPrinter(env, g).JSONValue(st)
								}
								fmt.Fprintf(env.Stdout, "configured: %t\n", st.Configured)
								return nil
							})
						},
					},
					{
						Name: "rotate", Short: "Create a new metrics token (the old one stops working)", Usage: "settings metrics-token rotate",
						Long: "The token is printed once, on stdout.",
						Run: func(ctx context.Context, env *Env, g *globalFlags, _ []string) error {
							return withAPI(env, g, func(c *client.Client, _ string, _ Context) error {
								tok, err := c.CreateMetricsToken(ctx)
								if err != nil {
									return err
								}
								fmt.Fprintln(env.Stderr, "note: this token is shown only once; the old token (if any) stopped working")
								if g.Output == "json" {
									return newPrinter(env, g).JSONValue(apitypes.MetricsToken{Token: tok.Token})
								}
								fmt.Fprintln(env.Stdout, tok.Token)
								return nil
							})
						},
					},
					{
						Name: "clear", Short: "Remove the metrics token", Usage: "settings metrics-token clear",
						Run: func(ctx context.Context, env *Env, g *globalFlags, _ []string) error {
							return withAPI(env, g, func(c *client.Client, _ string, _ Context) error {
								if err := c.DeleteMetricsToken(ctx); err != nil {
									return err
								}
								fmt.Fprintln(env.Stderr, "metrics token cleared")
								return nil
							})
						},
					},
				},
			},
			passwordCommand(),
		},
	}
}

func passwordCommand() *Command {
	var passwordStdin bool
	return &Command{
		Name:  "password",
		Short: "Change the account password and sign out other sessions",
		Usage: "settings password [--password-stdin]",
		Long:  "Prompts for the current and the new password. With --password-stdin the first line of stdin is the current password and the second the new one.",
		Flags: func(fs *flag.FlagSet) {
			fs.BoolVar(&passwordStdin, "password-stdin", false, "read the current and the new password from two lines of stdin")
		},
		Run: func(ctx context.Context, env *Env, g *globalFlags, _ []string) error {
			return withAPI(env, g, func(c *client.Client, _ string, _ Context) error {
				src := secretSource{Stdin: passwordStdin}
				current, err := readSecret(ctx, env, src, "Current password: ", false)
				if err != nil {
					return err
				}
				next, err := readSecret(ctx, env, src, "New password: ", true)
				if err != nil {
					return err
				}
				err = c.ChangePassword(ctx, current, next)
				var ce *client.Error
				if errors.As(err, &ce) {
					switch ce.Status {
					case http.StatusForbidden:
						return exitf(ExitAuth, "current password is incorrect")
					case http.StatusBadRequest:
						if ee, ok := apiFailure(err).(*ExitError); ok {
							ee.Code = ExitValidation
							return ee
						}
					}
				}
				if err != nil {
					return err
				}
				if g.Output == "json" {
					return newPrinter(env, g).JSONValue(map[string]bool{"changed": true})
				}
				fmt.Fprintln(env.Stdout, "password changed; other sessions were signed out")
				return nil
			})
		},
	}
}
