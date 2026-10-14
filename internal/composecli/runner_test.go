package composecli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeDocker behaves according to FAKE_MODE so tests never need real Docker.
const fakeDocker = `#!/bin/sh
case "$FAKE_MODE" in
lines)
  echo one
  echo two >&2
  echo three
  ;;
fail)
  echo "starting"
  echo "no such image: nope" >&2
  exit 3
  ;;
args)
  echo "ARGS: $*"
  echo "PWD: $(pwd -P)"
  ;;
progress)
  printf 'a 10%%\ra 50%%\ra 100%%\ndone\n'
  ;;
longline)
  head -c 150000 /dev/zero | tr '\0' x
  echo
  echo tail
  ;;
project)
  echo '{"name":"lab","services":{}}'
  echo "warning: noisy" >&2
  ;;
version)
  echo "2.29.1"
  ;;
noplugin)
  echo "docker: 'compose' is not a docker command." >&2
  exit 1
  ;;
child)
  sleep 60 &
  echo $! > "$FAKE_PIDFILE.child"
  echo $$ > "$FAKE_PIDFILE.parent"
  echo ready
  wait
  ;;
ignoreterm)
  trap '' TERM
  sleep 60 &
  echo $! > "$FAKE_PIDFILE.child"
  echo $$ > "$FAKE_PIDFILE.parent"
  echo ready
  wait
  ;;
termexit0)
  trap 'exit 0' TERM
  sleep 60 &
  echo $! > "$FAKE_PIDFILE.child"
  echo $$ > "$FAKE_PIDFILE.parent"
  echo ready
  wait
  ;;
marker)
  touch "$FAKE_MARKER"
  ;;
linger)
  sleep 3 &
  echo $! > "$FAKE_PIDFILE.child"
  echo written
  exit 0
  ;;
esac
`

func newRunner(t *testing.T, mode string, extra ...string) (*Runner, string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "docker")
	if err := os.WriteFile(bin, []byte(fakeDocker), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &Runner{
		Binary: bin,
		Grace:  500 * time.Millisecond,
		Env:    append([]string{"FAKE_MODE=" + mode}, extra...),
	}
	file := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(file, []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return r, file
}

func collect() (func(string), func() []string) {
	var lines []string
	return func(l string) { lines = append(lines, l) }, func() []string { return lines }
}

func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestRunStreamsLinesInOrder(t *testing.T) {
	r, file := newRunner(t, "lines")
	out, got := collect()
	if err := r.Run(ctxT(t), file, []string{"up"}, out); err != nil {
		t.Fatal(err)
	}
	want := []string{"one", "two", "three"}
	if strings.Join(got(), "|") != strings.Join(want, "|") {
		t.Fatalf("got %q want %q", got(), want)
	}
}

func TestRunSplitsCarriageReturnsAndCapsLongLines(t *testing.T) {
	r, file := newRunner(t, "progress")
	out, got := collect()
	if err := r.Run(ctxT(t), file, nil, out); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got(), "|") != "a 10%|a 50%|a 100%|done" {
		t.Fatalf("got %q", got())
	}

	r, file = newRunner(t, "longline")
	out, got = collect()
	if err := r.Run(ctxT(t), file, nil, out); err != nil {
		t.Fatal(err)
	}
	lines := got()
	if len(lines) < 3 || lines[len(lines)-1] != "tail" {
		t.Fatalf("unexpected lines (%d)", len(lines))
	}
	total := 0
	for _, l := range lines[:len(lines)-1] {
		if len(l) > 64*1024 {
			t.Fatalf("line of %d bytes exceeds cap", len(l))
		}
		total += len(l)
	}
	if total != 150000 {
		t.Fatalf("lost bytes: %d", total)
	}
}

func TestRunNonZeroExitReturnsExitErrorWithOutput(t *testing.T) {
	r, file := newRunner(t, "fail")
	out, got := collect()
	err := r.Run(ctxT(t), file, []string{"up"}, out)
	var ee *ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("want *ExitError, got %T %v", err, err)
	}
	if ee.Code != 3 || ee.ExitCode() != 3 {
		t.Fatalf("code = %d", ee.Code)
	}
	if !strings.Contains(ee.Output, "no such image: nope") || !strings.Contains(ee.Output, "starting") {
		t.Fatalf("output = %q", ee.Output)
	}
	if len(got()) != 2 {
		t.Fatalf("callback lines = %q", got())
	}
}

// killGroupOnCleanup kills the fake's process group (it leads its own group)
// but only while that group still exists, so a reaped and reused pid is
// never signalled.
func killGroupOnCleanup(t *testing.T, pgid int) {
	t.Helper()
	t.Cleanup(func() {
		if syscall.Kill(-pgid, 0) == nil {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
	})
}

func pidAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func readPID(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func waitDead(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for pidAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("process %d still alive", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestCancelKillsProcessGroup(t *testing.T) {
	for _, mode := range []string{"child", "ignoreterm"} {
		t.Run(mode, func(t *testing.T) {
			pidfile := filepath.Join(t.TempDir(), "pid")
			r, file := newRunner(t, mode, "FAKE_PIDFILE="+pidfile)
			ctx, cancel := context.WithCancel(ctxT(t))
			defer cancel()
			ready := make(chan struct{})
			var once = make(chan struct{}, 1)
			out := func(l string) {
				if l == "ready" {
					select {
					case once <- struct{}{}:
						close(ready)
					default:
					}
				}
			}
			done := make(chan error, 1)
			go func() { done <- r.Run(ctx, file, []string{"up"}, out) }()
			select {
			case <-ready:
			case err := <-done:
				t.Fatalf("exited early: %v", err)
			case <-time.After(10 * time.Second):
				t.Fatal("fake never became ready")
			}
			parent, child := readPID(t, pidfile+".parent"), readPID(t, pidfile+".child")
			killGroupOnCleanup(t, parent)
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("want context.Canceled, got %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("Run did not return after cancel")
			}
			// Run returns only after exit, so both must already be gone.
			waitDead(t, parent)
			waitDead(t, child)
		})
	}
}

func TestTimeoutReturnsDeadlineExceeded(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "pid")
	r, file := newRunner(t, "child", "FAKE_PIDFILE="+pidfile)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ready := false
	err := r.Run(ctx, file, nil, func(l string) { ready = ready || l == "ready" })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if !ready {
		t.Fatal("deadline fired before the fake was ready")
	}
	killGroupOnCleanup(t, readPID(t, pidfile+".parent"))
	waitDead(t, readPID(t, pidfile+".child"))
}

func TestCancelNeverReportsSuccess(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "pid")
	r, file := newRunner(t, "termexit0", "FAKE_PIDFILE="+pidfile)
	ctx, cancel := context.WithCancel(ctxT(t))
	defer cancel()
	err := r.Run(ctx, file, nil, func(l string) {
		if l == "ready" {
			cancel()
		}
	})
	killGroupOnCleanup(t, readPID(t, pidfile+".parent"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("process exiting 0 after SIGTERM must still report cancel, got %v", err)
	}
}

func TestRunWithCancelledContextDoesNotStart(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	r, file := newRunner(t, "marker", "FAKE_MARKER="+marker)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := r.Run(ctx, file, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("docker was invoked despite cancelled context")
	}
}

func TestRunDoesNotWaitForLingeringGrandchild(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "pid")
	r, file := newRunner(t, "linger", "FAKE_PIDFILE="+pidfile)
	out, got := collect()
	start := time.Now()
	if err := r.Run(ctxT(t), file, nil, out); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Run blocked %v on a lingering grandchild", d)
	}
	if strings.Join(got(), "|") != "written" {
		t.Fatalf("lines = %q", got())
	}
	waitDead(t, readPID(t, pidfile+".child"))
}

func TestRunMissingBinaryIsComposeUnavailable(t *testing.T) {
	r := &Runner{Binary: filepath.Join(t.TempDir(), "nope")}
	file := filepath.Join(t.TempDir(), "compose.yaml")
	if err := r.Run(ctxT(t), file, []string{"up"}, nil); !errors.Is(err, ErrComposeUnavailable) {
		t.Fatalf("got %v", err)
	}
}

func TestMissingPluginRunIsExitError(t *testing.T) {
	r, file := newRunner(t, "noplugin")
	var ee *ExitError
	if err := r.Run(ctxT(t), file, []string{"up"}, nil); !errors.As(err, &ee) {
		t.Fatalf("got %v", err)
	}
}

func TestSplitLines(t *testing.T) {
	cap64 := strings.Repeat("x", 64*1024)
	cases := []struct {
		name, in string
		want     []string
	}{
		{"exact cap then newline has no empty line", cap64 + "\n" + "z\n", []string{cap64, "z"}},
		{"exact cap then crlf", cap64 + "\r\nz", []string{cap64, "z"}},
		{"crlf keeps blank line", "a\r\n\r\nb", []string{"a", "", "b"}},
		{"lf keeps blank line", "b\n\nc", []string{"b", "", "c"}},
		{"bare cr is a break", "a\rb\r", []string{"a", "b"}},
		{"cap does not split a rune", strings.Repeat("x", 64*1024-1) + "é" + "tail",
			[]string{strings.Repeat("x", 64*1024-1), "étail"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			splitLines(strings.NewReader(c.in), func(l string) { got = append(got, l) })
			if len(got) != len(c.want) {
				t.Fatalf("got %d lines, want %d", len(got), len(c.want))
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("line %d differs (len %d vs %d)", i, len(got[i]), len(c.want[i]))
				}
			}
		})
	}
}

func TestRunArgsAndWorkingDir(t *testing.T) {
	r, file := newRunner(t, "args")
	out, got := collect()
	if err := r.Run(ctxT(t), file, []string{"up", "-d"}, out); err != nil {
		t.Fatal(err)
	}
	lines := got()
	if len(lines) != 2 {
		t.Fatalf("lines = %q", lines)
	}
	if lines[0] != "ARGS: compose -f "+file+" up -d" {
		t.Fatalf("args line = %q", lines[0])
	}
	if strings.Contains(lines[0], " -p") {
		t.Fatal("must never pass -p")
	}
	wantDir, _ := filepath.EvalSymlinks(filepath.Dir(file))
	if lines[1] != "PWD: "+wantDir {
		t.Fatalf("pwd line = %q want dir %q", lines[1], wantDir)
	}
}

func TestProjectNameMatchesCompose(t *testing.T) {
	r, file := newRunner(t, "project")
	name, err := r.ProjectName(ctxT(t), file)
	if err != nil {
		t.Fatal(err)
	}
	if name != "lab" {
		t.Fatalf("name = %q", name)
	}
}

func TestVersion(t *testing.T) {
	r, _ := newRunner(t, "version")
	v, err := r.Version(ctxT(t))
	if err != nil || v != "2.29.1" {
		t.Fatalf("v=%q err=%v", v, err)
	}
}

func TestVersionReportsUnavailable(t *testing.T) {
	r := &Runner{Binary: filepath.Join(t.TempDir(), "does-not-exist")}
	if _, err := r.Version(ctxT(t)); !errors.Is(err, ErrComposeUnavailable) {
		t.Fatalf("missing binary: got %v", err)
	}
	r, _ = newRunner(t, "noplugin")
	if _, err := r.Version(ctxT(t)); !errors.Is(err, ErrComposeUnavailable) {
		t.Fatalf("missing plugin: got %v", err)
	}
}

func TestConfigErrorCarriesMessage(t *testing.T) {
	r, file := newRunner(t, "fail")
	err := r.Config(ctxT(t), file)
	var ee *ExitError
	if !errors.As(err, &ee) || !strings.Contains(ee.Output, "no such image: nope") {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), "no such image: nope") {
		t.Fatalf("Error() should carry the message: %q", err.Error())
	}
	r, file = newRunner(t, "lines")
	if err := r.Config(ctxT(t), file); err != nil {
		t.Fatal(err)
	}
}
