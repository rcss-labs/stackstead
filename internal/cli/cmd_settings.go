package cli

import "flag"

func settingsCommand() *Command {
	c := stub("settings")
	mt := stub("metrics-token")
	mt.Sub = []*Command{stub("status"), stub("rotate"), stub("clear")}
	pw := stub("password")
	pw.Flags = func(fs *flag.FlagSet) { fs.Bool("password-stdin", false, "") }
	c.Sub = []*Command{mt, pw}
	return c
}
