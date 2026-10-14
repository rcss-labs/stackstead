package cli

import (
	"errors"
	"flag"
	"io"
	"slices"
	"strings"
	"testing"
)

func TestParseInterspersed(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantPos  []string
		wantF    bool
		wantTail int
	}{
		{"flag then positional", []string{"-f", "web"}, []string{"web"}, true, 0},
		{"positional then flag", []string{"web", "-f"}, []string{"web"}, true, 0},
		{"flag with value between", []string{"web", "--tail", "5", "-f"}, []string{"web"}, true, 5},
		{"double dash ends flags", []string{"--", "-notaflag"}, []string{"-notaflag"}, false, 0},
		{"flag before double dash", []string{"-f", "--", "-x", "-f"}, []string{"-x", "-f"}, true, 0},
		{"two positionals", []string{"web", "extra"}, []string{"web", "extra"}, false, 0},
		{"lone dash is positional", []string{"-", "-f"}, []string{"-"}, true, 0},
		{"no args", nil, nil, false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fs := flag.NewFlagSet("t", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			f := fs.Bool("f", false, "")
			tail := fs.Int("tail", 0, "")
			pos, err := parseInterspersed(fs, c.args)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(pos, c.wantPos) {
				t.Errorf("positional = %q, want %q", pos, c.wantPos)
			}
			if *f != c.wantF || *tail != c.wantTail {
				t.Errorf("f=%v tail=%d, want f=%v tail=%d", *f, *tail, c.wantF, c.wantTail)
			}
		})
	}
}

func TestParseUnknownFlagIsUsageErrorNamingTheFlag(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	_, err := parseInterspersed(fs, []string{"web", "--bogus"})
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != ExitUsage {
		t.Fatalf("err = %v, want ExitError with code %d", err, ExitUsage)
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("message %q does not name the flag", err)
	}
}

func TestParseHelpReturnsErrHelp(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	_, err := parseInterspersed(fs, []string{"web", "-h"})
	if !errors.Is(err, flag.ErrHelp) {
		t.Errorf("err = %v, want flag.ErrHelp", err)
	}
}
