package cli

import (
	"context"
	"flag"
)

func adminCommand() *Command {
	var lf localFlags
	var username, passwordFile string
	var passwordStdin bool
	stub := func(context.Context, *Env, *globalFlags, []string) error { return errNotImplemented }
	return &Command{
		Name: "admin", Short: "Admin account and token commands (run as the service user)",
		Sub: []*Command{
			{Name: "set-password", Short: "Create the admin or reset its password", Usage: "admin set-password [--username U] [--password-stdin|--password-file F] [--config P]",
				Flags: func(fs *flag.FlagSet) {
					lf.register(fs)
					fs.StringVar(&username, "username", "", "admin username")
					fs.BoolVar(&passwordStdin, "password-stdin", false, "read the password from stdin")
					fs.StringVar(&passwordFile, "password-file", "", "read the password from a file")
				}, Run: stub},
			{Name: "revoke-sessions", Short: "Sign out every session", Usage: "admin revoke-sessions [--config P]", Flags: lf.register, Run: stub},
			{Name: "metrics-token", Short: "Manage the /metrics bearer token",
				Sub: []*Command{
					{Name: "status", Short: "Show whether a token is set", Usage: "admin metrics-token status [--config P]", Flags: lf.register, Run: stub},
					{Name: "rotate", Short: "Create a new token", Usage: "admin metrics-token rotate [--config P]", Flags: lf.register, Run: stub},
					{Name: "clear", Short: "Disable /metrics", Usage: "admin metrics-token clear [--config P]", Flags: lf.register, Run: stub},
				}},
		},
	}
}
