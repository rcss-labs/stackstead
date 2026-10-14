package cli

import (
	"context"
	"flag"

	"github.com/rcss-labs/stackstead/internal/client"
)

func notImplemented() error { return exitf(ExitGeneral, "not implemented") }

func apiClient(env *Env, g *globalFlags) (c *client.Client, name string, cx Context, err error) {
	return nil, "", Context{}, notImplemented()
}

func onUnauthorized(env *Env, store *ContextStore, name string, err error) error { return err }

func stub(name string) *Command {
	return &Command{Name: name, MaxArgs: -1, Run: func(context.Context, *Env, *globalFlags, []string) error { return notImplemented() }}
}

func loginCommand() *Command {
	c := stub("login")
	c.Flags = func(fs *flag.FlagSet) {
		fs.String("username", "admin", "")
		fs.Bool("password-stdin", false, "")
		fs.String("ca-file", "", "")
		fs.Bool("insecure", false, "")
	}
	return c
}
func logoutCommand() *Command { return stub("logout") }
func contextCommand() *Command {
	c := stub("context")
	c.Sub = []*Command{stub("ls"), stub("current"), stub("use"), stub("rm")}
	return c
}
func whoamiCommand() *Command { return stub("whoami") }
func statusCommand() *Command { return stub("status") }
