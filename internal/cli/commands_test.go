package cli

import (
	"context"
	"flag"
	"strings"
	"testing"
)

// specTree is the command tree of the CLI design spec (section 4); every
// path must resolve in rootCommand().
var specTree = [][]string{
	{"serve"}, {"version"}, {"config", "init"}, {"config", "validate"}, {"config", "show"},
	{"db", "init"}, {"db", "check"}, {"db", "backup"}, {"db", "restore"},
	{"admin", "set-password"}, {"admin", "revoke-sessions"},
	{"admin", "metrics-token", "status"}, {"admin", "metrics-token", "rotate"}, {"admin", "metrics-token", "clear"},
	{"health"}, {"login"}, {"logout"},
	{"context", "ls"}, {"context", "current"}, {"context", "use"}, {"context", "rm"},
	{"whoami"}, {"status"},
	{"stacks", "ls"}, {"stacks", "show"}, {"stacks", "up"}, {"stacks", "down"}, {"stacks", "restart"},
	{"stacks", "pull"}, {"stacks", "build"}, {"stacks", "rm"}, {"stacks", "import"},
	{"stacks", "compose", "get"}, {"stacks", "compose", "edit"}, {"stacks", "compose", "put"},
	{"ops", "ls"}, {"ops", "show"}, {"ops", "stream"}, {"ops", "cancel"},
	{"logs"}, {"metrics"}, {"events"}, {"networks", "ls"},
	{"settings", "metrics-token", "status"}, {"settings", "metrics-token", "rotate"}, {"settings", "metrics-token", "clear"},
	{"settings", "password"},
}

func resolve(root *Command, path []string) *Command {
	cmd := root
	for _, name := range path {
		if cmd = cmd.find(name); cmd == nil {
			return nil
		}
	}
	return cmd
}

func TestRootResolvesEveryCommandOfTheSpecTree(t *testing.T) {
	root := rootCommand()
	for _, path := range specTree {
		cmd := resolve(root, path)
		if cmd == nil {
			t.Errorf("%s does not resolve", strings.Join(path, " "))
			continue
		}
		if cmd.Run == nil && len(cmd.Sub) == 0 {
			t.Errorf("%s has neither Run nor Sub", strings.Join(path, " "))
		}
	}
}

func TestNoTwoSubcommandsShareAName(t *testing.T) {
	var walk func(c *Command, path string)
	walk = func(c *Command, path string) {
		seen := map[string]bool{}
		for _, s := range c.Sub {
			if seen[s.Name] {
				t.Errorf("%s has two subcommands named %q", path, s.Name)
			}
			seen[s.Name] = true
			if s.Short == "" {
				t.Errorf("%s %s has no Short text", path, s.Name)
			}
			walk(s, path+" "+s.Name)
		}
	}
	walk(rootCommand(), "stackstead")
	if len(rootCommand().Sub) < 10 {
		t.Error("the root tree is missing commands")
	}
}

func TestRootHelpIsGroupedServerRemoteHostAdmin(t *testing.T) {
	te := newTestEnv(t, nil)
	if err := Run(context.Background(), rootCommand(), []string{"--help"}, te.Env); err != nil {
		t.Fatal(err)
	}
	out := te.Out.String()
	headings := []string{"Server:", "Remote:", "Host admin:"}
	last := -1
	for _, h := range headings {
		i := strings.Index(out, h)
		if i < 0 || i < last {
			t.Fatalf("heading %q missing or out of order in:\n%s", h, out)
		}
		last = i
	}
	section := func(from, to string) string {
		i := strings.Index(out, from)
		if to == "" {
			return out[i:]
		}
		return out[i:strings.Index(out, to)]
	}
	for _, c := range []struct{ heading, next, cmd string }{
		{"Server:", "Remote:", "serve"}, {"Server:", "Remote:", "version"},
		{"Remote:", "Host admin:", "stacks"}, {"Remote:", "Host admin:", "login"}, {"Remote:", "Host admin:", "logs"},
		{"Host admin:", "", "db"}, {"Host admin:", "", "admin"}, {"Host admin:", "", "health"}, {"Host admin:", "", "config"},
	} {
		if !strings.Contains(section(c.heading, c.next), "\n  "+c.cmd+" ") {
			t.Errorf("%q is not listed under %s:\n%s", c.cmd, c.heading, out)
		}
	}
}

// The Plan 7 installers rely on exactly these flags and outputs (Contract for
// Plan 7 in the Plan 6 document).
func TestPlan7ContractFlagsExist(t *testing.T) {
	root := rootCommand()
	for _, c := range []struct {
		path  []string
		flags []string
	}{
		{[]string{"serve"}, []string{"config"}},
		{[]string{"version"}, []string{"short"}},
		{[]string{"config", "validate"}, []string{"config"}},
		{[]string{"config", "init"}, []string{"path", "listen", "stacks-dir", "data-dir", "force"}},
		{[]string{"admin", "set-password"}, []string{"config", "username", "password-stdin", "password-file"}},
		{[]string{"admin", "metrics-token", "rotate"}, []string{"config"}},
		{[]string{"admin", "metrics-token", "clear"}, []string{"config"}},
		{[]string{"db", "init"}, []string{"config"}},
		{[]string{"db", "backup"}, []string{"config"}},
		{[]string{"db", "restore"}, []string{"config", "yes"}},
		{[]string{"health"}, []string{"config"}},
	} {
		cmd := resolve(root, c.path)
		if cmd == nil || cmd.Flags == nil {
			t.Errorf("%s: missing command or flags", strings.Join(c.path, " "))
			continue
		}
		fs := flag.NewFlagSet("x", flag.ContinueOnError)
		cmd.Flags(fs)
		for _, name := range c.flags {
			if fs.Lookup(name) == nil {
				t.Errorf("%s: flag --%s is part of the Plan 7 contract", strings.Join(c.path, " "), name)
			}
		}
	}
}

func TestPlan7ContractVersionShortIsBareAndJSONHasSevenKeys(t *testing.T) {
	te := newTestEnv(t, nil)
	if err := Run(context.Background(), rootCommand(), []string{"version", "--short"}, te.Env); err != nil {
		t.Fatal(err)
	}
	if got := te.Out.String(); !strings.HasSuffix(got, "\n") || strings.Count(got, "\n") != 1 || strings.Contains(got, " ") {
		t.Errorf("version --short = %q, want one bare token and a newline", got)
	}
	te = newTestEnv(t, nil)
	if err := Run(context.Background(), rootCommand(), []string{"version", "-o", "json"}, te.Env); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"version", "commit", "date", "go", "os", "arch", "license"} {
		if !strings.Contains(te.Out.String(), `"`+k+`"`) {
			t.Errorf("version -o json lacks %q: %s", k, te.Out.String())
		}
	}
}
