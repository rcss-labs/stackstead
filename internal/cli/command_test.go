package cli

import (
	"context"
	"errors"
	"flag"
	"slices"
	"strings"
	"testing"
	"time"
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

func stubServe(t *testing.T) *[]string {
	t.Helper()
	called := new([]string)
	orig := serveRun
	serveRun = func(_ context.Context, _ *Env, args []string) error {
		*called = append([]string{"called"}, args...)
		return nil
	}
	t.Cleanup(func() { serveRun = orig })
	return called
}

func TestGlobalFlagsBeforeSubcommandDoNotTakeCompatPath(t *testing.T) {
	for _, args := range [][]string{{"-o", "json", "version"}, {"--no-color", "version"}} {
		called := stubServe(t)
		te := newTestEnv(t, nil)
		if code := Main(context.Background(), args, te.Env); code != 0 {
			t.Fatalf("%v: code %d, stderr %s", args, code, te.Err.String())
		}
		if len(*called) != 0 {
			t.Errorf("%v: serve was called", args)
		}
		if !strings.Contains(te.Out.String(), "AGPL-3.0") {
			t.Errorf("%v: version did not run: %q", args, te.Out.String())
		}
	}
}

func TestConfigFlagWithSubcommandIsUsageErrorNotServe(t *testing.T) {
	called := stubServe(t)
	te := newTestEnv(t, nil)
	if code := Main(context.Background(), []string{"--config", "x", "version"}, te.Env); code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
	if len(*called) != 0 {
		t.Error("serve was called")
	}
}

func TestConfigEqualsFormStillServes(t *testing.T) {
	called := stubServe(t)
	te := newTestEnv(t, nil)
	if code := Main(context.Background(), []string{"--config=x"}, te.Env); code != 0 || len(*called) == 0 {
		t.Errorf("code=%d called=%v", code, *called)
	}
}

func TestNestedHelpResolvesFromCurrentCommand(t *testing.T) {
	var s seen
	te := newTestEnv(t, nil)
	if err := Run(context.Background(), testTree(&s), []string{"stacks", "help", "up"}, te.Env); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(te.Out.String(), "stacks up <name> [--detach]") {
		t.Errorf("help output: %q", te.Out.String())
	}
}

func TestServeHelpOmitsGlobalFlags(t *testing.T) {
	te := newTestEnv(t, nil)
	if code := Main(context.Background(), []string{"serve", "--help"}, te.Env); code != 0 {
		t.Fatalf("code %d", code)
	}
	if strings.Contains(te.Out.String(), "Global flags") || !strings.Contains(te.Out.String(), "-config") {
		t.Errorf("serve help: %q", te.Out.String())
	}
}

func TestServeBadFlagExits2AndPrintsOnce(t *testing.T) {
	te := newTestEnv(t, nil)
	if code := Main(context.Background(), []string{"serve", "--bogus"}, te.Env); code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
	if n := strings.Count(te.Err.String(), "bogus"); n != 1 {
		t.Errorf("flag named %d times in %q, want once", n, te.Err.String())
	}
}

func TestStreamOptionsUseTheSharedBackoffSeam(t *testing.T) {
	orig := streamBackoff
	t.Cleanup(func() { streamBackoff = orig })
	streamBackoff.Min, streamBackoff.Max = 3*time.Millisecond, 4*time.Millisecond
	te := newTestEnv(t, nil)

	opts := streamOptions(te.Env, func() string { return "id-1" }, false)
	if opts.Backoff.Min != 3*time.Millisecond || opts.Backoff.Max != 4*time.Millisecond {
		t.Errorf("backoff = %+v, want the streamBackoff seam", opts.Backoff)
	}
	if opts.LastEventID == nil || opts.LastEventID() != "id-1" {
		t.Error("LastEventID was not passed through")
	}
	if opts.OnReconnect == nil {
		t.Fatal("OnReconnect is nil")
	}
	opts.OnReconnect(2, errors.New("boom\x1b[2J"))
	if got := te.Err.String(); !strings.Contains(got, "reconnecting (attempt 2)") || strings.Contains(got, "\x1b") {
		t.Errorf("reconnect note = %q", got)
	}

	quiet := newTestEnv(t, nil)
	streamOptions(quiet.Env, nil, true).OnReconnect(1, nil)
	if quiet.Err.String() != "" {
		t.Errorf("quiet (JSON) mode printed %q", quiet.Err.String())
	}
}
