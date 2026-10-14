package cli

import (
	"errors"
	"flag"
	"slices"
)

// parseInterspersed parses flags that may appear before, between or after
// positional arguments (`logs web -f` and `logs -f web` are the same). `--`
// ends flag parsing. A bad flag is an ExitUsage error; -h/--help returns
// flag.ErrHelp.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, err
			}
			return nil, exitf(ExitUsage, "%v", err)
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		// fs.Parse swallows a `--`; it is the argument right before rest.
		if consumed := len(args) - len(rest); consumed > 0 && args[consumed-1] == "--" {
			return append(positional, rest...), nil
		}
		positional = append(positional, rest[0])
		args = slices.Clone(rest[1:])
		if len(args) == 0 {
			return positional, nil
		}
	}
}
