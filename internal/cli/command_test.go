package cli

import (
	"context"
	"flag"
	"slices"
	"strings"
	"testing"
)

// testTree builds stackstead > stacks > {up <name> [--detach], ls}; got
// receives what the leaf command saw.
type seen struct {
	g      globalFlags
	args   []string
	detach bool
	ran    string
}

func testTree(s *seen) *Command {
	up := &Command{
		Name: "up", Short: "Start a stack", Usage: "stacks up <name> [--detach]",
		MinArgs: 1, MaxArgs: 1,
		Flags: func(fs *flag.FlagSet) { fs.BoolVar(&s.detach, "detach", false, "do not wait") },
		Run: func(_ context.Context, _ *Env, g *globalFlags, args []string) error {
			s.ran, s.g, s.args = "up", *g, args
			return nil
		},
	}
	ls := &Command{
		Name: "ls", Short: "List stacks", Usage: "stacks ls", MaxArgs: 0,
		Run: func(_ context.Context, _ *Env, g *globalFlags, args []string) error {
			s.ran, s.g, s.args = "ls", *g, args
			return nil
		},
	}
	return &Command{Name: "stackstead", Sub: []*Command{
		{Name: "stacks", Short: "Manage stacks", Sub: []*Command{up, ls}},
	}}
}

func TestRunResolvesNestedCommand(t *testing.T) {
	var s seen
	te := newTestEnv(t, nil)
	if err := Run(context.Background(), testTree(&s), []string{"stacks", "up", "web"}, te.Env); err != nil {
		t.Fatal(err)
	}
	if s.ran != "up" || !slices.Equal(s.args, []string{"web"}) {
		t.Errorf("ran=%q args=%q", s.ran, s.args)
	}
}

func TestRunInterspersedFlagsReachTheCommand(t *testing.T) {
	var s seen
	te := newTestEnv(t, nil)
	if err := Run(context.Background(), testTree(&s), []string{"stacks", "up", "web", "--detach"}, te.Env); err != nil {
		t.Fatal(err)
	}
	if !s.detach || !slices.Equal(s.args, []string{"web"}) {
		t.Errorf("detach=%v args=%q", s.detach, s.args)
	}
}

func TestHelpListsSubcommandsWithShort(t *testing.T) {
	var s seen
	te := newTestEnv(t, nil)
	if err := Run(context.Background(), testTree(&s), []string{"help", "stacks"}, te.Env); err != nil {
		t.Fatal(err)
	}
	out := te.Out.String()
	for _, want := range []string{"up", "Start a stack", "ls", "List stacks"} {
		if !strings.Contains(out, want) {
			t.Errorf("help output lacks %q:\n%s", want, out)
		}
	}
	if s.ran != "" {
		t.Errorf("help ran command %q", s.ran)
	}
}

func TestDashHelpPrintsLeafUsageAndFlags(t *testing.T) {
	var s seen
	te := newTestEnv(t, nil)
	if err := Run(context.Background(), testTree(&s), []string{"stacks", "up", "--help"}, te.Env); err != nil {
		t.Fatalf("--help returned %v, want nil (exit 0)", err)
	}
	out := te.Out.String()
	for _, want := range []string{"stacks up <name> [--detach]", "-detach", "-context", "-o"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage lacks %q:\n%s", want, out)
		}
	}
	if s.ran != "" {
		t.Errorf("--help ran the command")
	}
}

func TestGlobalFlagBeforeAndAfterSubcommand(t *testing.T) {
	for _, args := range [][]string{
		{"--context", "prod", "stacks", "ls"},
		{"stacks", "--context", "prod", "ls"},
		{"stacks", "ls", "--context", "prod"},
		{"stacks", "ls", "--context=prod"},
	} {
		var s seen
		te := newTestEnv(t, nil)
		if err := Run(context.Background(), testTree(&s), args, te.Env); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if s.g.Context != "prod" {
			t.Errorf("%v: Context = %q, want prod", args, s.g.Context)
		}
	}
}

func TestContextEnvSeedsAndFlagOverrides(t *testing.T) {
	var s seen
	te := newTestEnv(t, map[string]string{"STACKSTEAD_CONTEXT": "fromenv"})
	if err := Run(context.Background(), testTree(&s), []string{"stacks", "ls"}, te.Env); err != nil {
		t.Fatal(err)
	}
	if s.g.Context != "fromenv" {
		t.Errorf("Context = %q, want fromenv", s.g.Context)
	}
	if err := Run(context.Background(), testTree(&s), []string{"stacks", "ls", "--context", "flag"}, te.Env); err != nil {
		t.Fatal(err)
	}
	if s.g.Context != "flag" {
		t.Errorf("Context = %q, want flag", s.g.Context)
	}
}

func TestGlobalDefaults(t *testing.T) {
	var s seen
	te := newTestEnv(t, nil)
	if err := Run(context.Background(), testTree(&s), []string{"stacks", "ls"}, te.Env); err != nil {
		t.Fatal(err)
	}
	if s.g.Output != "table" || s.g.NoColor || s.g.Timeout <= 0 {
		t.Errorf("defaults = %+v", s.g)
	}
}

func TestInvalidOutputFormatIsUsageError(t *testing.T) {
	var s seen
	te := newTestEnv(t, nil)
	err := Run(context.Background(), testTree(&s), []string{"stacks", "ls", "-o", "yaml"}, te.Env)
	if codeFor(err) != ExitUsage || !strings.Contains(err.Error(), "yaml") {
		t.Errorf("err = %v (code %d)", err, codeFor(err))
	}
}

func TestMinArgsViolationExits2WithUsageLine(t *testing.T) {
	var s seen
	te := newTestEnv(t, nil)
	err := Run(context.Background(), testTree(&s), []string{"stacks", "up"}, te.Env)
	if codeFor(err) != ExitUsage {
		t.Fatalf("code = %d (err %v), want 2", codeFor(err), err)
	}
	if !strings.Contains(err.Error(), "stacks up <name> [--detach]") {
		t.Errorf("error lacks the usage line: %q", err)
	}
	if s.ran != "" {
		t.Error("command ran despite missing argument")
	}
}

func TestMaxArgsViolationExits2(t *testing.T) {
	var s seen
	te := newTestEnv(t, nil)
	err := Run(context.Background(), testTree(&s), []string{"stacks", "ls", "extra"}, te.Env)
	if codeFor(err) != ExitUsage {
		t.Errorf("code = %d (err %v), want 2", codeFor(err), err)
	}
}

func TestUnknownSubcommandExits2AndNamesIt(t *testing.T) {
	var s seen
	te := newTestEnv(t, nil)
	err := Run(context.Background(), testTree(&s), []string{"stacks", "frobnicate"}, te.Env)
	if codeFor(err) != ExitUsage || !strings.Contains(err.Error(), "frobnicate") {
		t.Errorf("err = %v (code %d)", err, codeFor(err))
	}
}

func TestBareInvocationPrintsHelpExit2(t *testing.T) {
	var s seen
	te := newTestEnv(t, nil)
	err := Run(context.Background(), testTree(&s), nil, te.Env)
	if codeFor(err) != ExitUsage {
		t.Errorf("code = %d, want 2", codeFor(err))
	}
	if !strings.Contains(te.Err.String(), "stacks") || te.Out.String() != "" {
		t.Errorf("usage should go to stderr only; stderr=%q stdout=%q", te.Err.String(), te.Out.String())
	}
}

func TestBareFlagsRunServe(t *testing.T) {
	var gotArgs []string
	orig := serveRun
	serveRun = func(_ context.Context, _ *Env, args []string) error { gotArgs = args; return nil }
	t.Cleanup(func() { serveRun = orig })

	te := newTestEnv(t, nil)
	code := Main(context.Background(), []string{"--config", "x"}, te.Env)
	if code != 0 {
		t.Fatalf("code = %d, stderr: %s", code, te.Err.String())
	}
	if !slices.Equal(gotArgs, []string{"--config", "x"}) {
		t.Errorf("serve args = %q", gotArgs)
	}
	note := `note: run "stackstead serve" (bare flags are deprecated)`
	if strings.Count(te.Err.String(), note) != 1 {
		t.Errorf("stderr lacks exactly one deprecation note: %q", te.Err.String())
	}
}

func TestServeSubcommandPassesRawArgsWithoutNote(t *testing.T) {
	var gotArgs []string
	orig := serveRun
	serveRun = func(_ context.Context, _ *Env, args []string) error { gotArgs = args; return nil }
	t.Cleanup(func() { serveRun = orig })

	te := newTestEnv(t, nil)
	if code := Main(context.Background(), []string{"serve", "--config", "x"}, te.Env); code != 0 {
		t.Fatalf("code = %d, stderr: %s", code, te.Err.String())
	}
	if !slices.Equal(gotArgs, []string{"--config", "x"}) {
		t.Errorf("serve args = %q", gotArgs)
	}
	if strings.Contains(te.Err.String(), "deprecated") {
		t.Errorf("unexpected deprecation note: %q", te.Err.String())
	}
}

func TestMainBareInvocationExits2(t *testing.T) {
	te := newTestEnv(t, nil)
	if code := Main(context.Background(), nil, te.Env); code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
	if !strings.Contains(te.Err.String(), "serve") || !strings.Contains(te.Err.String(), "version") {
		t.Errorf("root usage lacks commands: %q", te.Err.String())
	}
}

func TestMainPrintsErrorsWithoutStackTraceAndMapsCode(t *testing.T) {
	te := newTestEnv(t, nil)
	if code := Main(context.Background(), []string{"nope"}, te.Env); code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
	if !strings.HasPrefix(te.Err.String(), "stackstead: ") {
		t.Errorf("stderr = %q, want a `stackstead: ` prefix", te.Err.String())
	}
}

func TestMainHelpExitsZero(t *testing.T) {
	te := newTestEnv(t, nil)
	if code := Main(context.Background(), []string{"--help"}, te.Env); code != 0 {
		t.Errorf("code = %d, want 0", code)
	}
	if !strings.Contains(te.Out.String(), "serve") {
		t.Errorf("stdout lacks usage: %q", te.Out.String())
	}
}

func TestMainCancelledCommandExits130Silently(t *testing.T) {
	orig := serveRun
	serveRun = func(ctx context.Context, _ *Env, _ []string) error { return context.Canceled }
	t.Cleanup(func() { serveRun = orig })
	te := newTestEnv(t, nil)
	if code := Main(context.Background(), []string{"serve"}, te.Env); code != 130 {
		t.Errorf("code = %d, want 130", code)
	}
	if te.Err.String() != "" {
		t.Errorf("stderr = %q, want nothing", te.Err.String())
	}
}
