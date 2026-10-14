// Command stackstead is the Stackstead server and command-line tool.
package main

import (
	"context"
	"os"

	"github.com/rcss-labs/stackstead/internal/cli"
)

func main() {
	os.Exit(cli.Main(context.Background(), os.Args[1:], cli.OSEnv()))
}
