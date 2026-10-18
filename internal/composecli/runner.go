// Package composecli is the only place Stackstead runs `docker compose`.
package composecli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	defaultBinary = "docker"
	defaultGrace  = 10 * time.Second
	maxLineBytes  = 64 * 1024
	tailLines     = 200
)

// ErrComposeUnavailable means the docker binary or its compose plugin is missing.
var ErrComposeUnavailable = errors.New("docker compose is not available")

// ExitError reports a non-zero exit. Output holds the last ~200 lines of
// merged stdout/stderr for Run, or stderr alone for captured commands.
type ExitError struct {
	Code   int
	Output string
}

func (e *ExitError) Error() string {
	msg := strings.TrimSpace(e.Output)
	if msg == "" {
		return fmt.Sprintf("docker compose exited with code %d", e.Code)
	}
	return fmt.Sprintf("docker compose exited with code %d: %s", e.Code, msg)
}

// ExitCode lets other packages read the code through an interface.
func (e *ExitError) ExitCode() int { return e.Code }

// Runner invokes `docker compose`. The zero value is usable.
type Runner struct {
	Binary string        // default "docker"
	Grace  time.Duration // SIGTERM to SIGKILL delay, default 10s
	Env    []string      // extra environment entries, appended to os.Environ
}

func (r *Runner) binary() string {
	if r.Binary == "" {
		return defaultBinary
	}
	return r.Binary
}

func (r *Runner) grace() time.Duration {
	if r.Grace <= 0 {
		return defaultGrace
	}
	return r.Grace
}

// Version returns `docker compose version --short`.
func (r *Runner) Version(ctx context.Context) (string, error) {
	stdout, _, err := r.capture(ctx, "", []string{"compose", "version", "--short"})
	if err != nil {
		var ee *ExitError
		if errors.Is(err, exec.ErrNotFound) || errors.As(err, &ee) || isNotExist(err) {
			return "", fmt.Errorf("%w: %v", ErrComposeUnavailable, err)
		}
		return "", err
	}
	return strings.TrimSpace(stdout), nil
}

// Config validates the file with `docker compose config --quiet`.
func (r *Runner) Config(ctx context.Context, file string) error {
	return r.Run(ctx, file, []string{"config", "--quiet"}, nil)
}

// ConfigJSON returns the resolved Compose configuration as JSON. On failure,
// errors include only stderr and never the potentially secret-bearing stdout.
func (r *Runner) ConfigJSON(ctx context.Context, file string) ([]byte, error) {
	stdout, _, err := r.capture(ctx, filepath.Dir(file), composeArgs(file, []string{"config", "--format", "json"}))
	if err != nil {
		return nil, err
	}
	return []byte(stdout), nil
}

// ProjectName returns the project name compose derives for the file.
func (r *Runner) ProjectName(ctx context.Context, file string) (string, error) {
	stdout, err := r.ConfigJSON(ctx, file)
	if err != nil {
		return "", err
	}
	var doc struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(stdout, &doc); err != nil {
		return "", fmt.Errorf("parse compose config: %w", err)
	}
	if doc.Name == "" {
		return "", errors.New("compose config has no project name")
	}
	return doc.Name, nil
}

// Run executes `docker compose -f file args...` in the file's directory and
// calls out once per output line (stdout and stderr merged, in order). It
// returns only after the process has exited and all output was delivered.
func (r *Runner) Run(ctx context.Context, file string, args []string, out func(line string)) error {
	pr, pw := io.Pipe()
	tail := &tailBuffer{}
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		splitLines(pr, func(line string) {
			tail.add(line)
			if out != nil {
				out(line)
			}
		})
		_, _ = io.Copy(io.Discard, pr) // never block the writer if the splitter stops
	}()

	err := r.exec(ctx, filepath.Dir(file), composeArgs(file, args), pw, pw)
	_ = pw.Close()
	<-readerDone

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return &ExitError{Code: exitErr.ExitCode(), Output: tail.String()}
	}
	return err
}

func composeArgs(file string, args []string) []string {
	return append([]string{"compose", "-f", file}, args...)
}

// capture runs a command collecting stdout and stderr separately, so
// warnings on stderr cannot corrupt machine-readable stdout.
func (r *Runner) capture(ctx context.Context, dir string, args []string) (string, string, error) {
	var stdout, stderr bytes.Buffer
	err := r.exec(ctx, dir, args, &stdout, &stderr)
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		tail := &tailBuffer{}
		splitLines(strings.NewReader(stderr.String()), tail.add)
		return "", "", &ExitError{Code: exitErr.ExitCode(), Output: tail.String()}
	}
	return stdout.String(), stderr.String(), err
}

// exec runs the binary in its own process group. On cancellation the group
// gets SIGTERM, then SIGKILL after Grace; exec returns after everything has
// exited. Errors caused by ctx wrap ctx.Err().
func (r *Runner) exec(ctx context.Context, dir string, args []string, stdout, stderr io.Writer) error {
	grace := r.grace()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("docker compose %s: %w", firstArg(args), ctxErr)
	}
	// With SysProcAttr set, os/exec skips its own Dir check and the child's
	// chdir failure would look like a missing binary, so check it here.
	if dir != "" {
		if _, err := os.Stat(dir); err != nil {
			return fmt.Errorf("compose file directory: %w", err)
		}
	}
	cmd := exec.Command(r.binary(), args...)
	cmd.Dir = dir
	if len(r.Env) > 0 {
		cmd.Env = append(cmd.Environ(), r.Env...)
	}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	// A leader that exits while a grandchild still holds the output pipe must
	// not block us forever.
	cmd.WaitDelay = grace
	isolate(cmd)

	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || isMissingBinary(err, r.binary()) {
			return fmt.Errorf("%w: %v", ErrComposeUnavailable, err)
		}
		return err
	}

	var killTimer *time.Timer
	var mu sync.Mutex
	var signalled bool
	stop := make(chan struct{})
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-ctx.Done():
			signalGroup(cmd, syscall.SIGTERM)
			mu.Lock()
			signalled = true
			killTimer = time.AfterFunc(grace, func() { signalGroup(cmd, syscall.SIGKILL) })
			mu.Unlock()
		case <-stop:
		}
	}()

	waitErr := cmd.Wait()
	close(stop)
	<-watcherDone
	mu.Lock()
	if killTimer != nil {
		killTimer.Stop()
	}
	wasSignalled := signalled
	mu.Unlock()

	// Always sweep stragglers that outlived the leader, whatever the outcome.
	signalGroup(cmd, syscall.SIGKILL)
	lingering := errors.Is(waitErr, exec.ErrWaitDelay)
	if wasSignalled {
		// A cancelled operation is never a success, whatever the exit status.
		return fmt.Errorf("docker compose %s: %w", firstArg(args), ctx.Err())
	}
	if lingering {
		return nil // leader exited 0; only a grandchild held the pipe
	}
	return waitErr
}

func firstArg(args []string) string {
	for i, a := range args {
		if a == "-f" {
			continue
		}
		if i > 0 && args[i-1] == "-f" {
			continue
		}
		if a != "compose" {
			return a
		}
	}
	return ""
}

func isNotExist(err error) bool {
	return errors.Is(err, syscall.ENOENT)
}

// isMissingBinary reports a failed start because the binary itself does not
// exist (as opposed to a missing working directory).
func isMissingBinary(err error, binary string) bool {
	var pe *os.PathError
	return errors.As(err, &pe) && pe.Path == binary && isNotExist(pe.Err)
}

// tailBuffer keeps the last tailLines lines.
type tailBuffer struct {
	lines []string
}

func (t *tailBuffer) add(line string) {
	t.lines = append(t.lines, line)
	if len(t.lines) > 2*tailLines {
		t.lines = append([]string(nil), t.lines[len(t.lines)-tailLines:]...)
	}
}

func (t *tailBuffer) String() string {
	l := t.lines
	if len(l) > tailLines {
		l = l[len(l)-tailLines:]
	}
	return strings.Join(l, "\n")
}

// splitLines emits lines split on \n, \r and \r\n (the pair is one break).
// Lines longer than maxLineBytes are emitted in chunks of that size, never
// cutting a UTF-8 rune. A bare \r only ends non-empty lines (progress bars
// redraw in place); \n and \r\n keep blank lines.
func splitLines(rd io.Reader, emit func(string)) {
	buf := make([]byte, 32*1024)
	line := make([]byte, 0, 4096)
	prevCR, crEmitted, justCapped := false, false, false
	for {
		n, err := rd.Read(buf)
		for _, b := range buf[:n] {
			switch b {
			case '\r':
				if !prevCR { // a run of \r before \n is one break
					crEmitted = len(line) > 0 || justCapped
				}
				if len(line) > 0 {
					emit(string(line))
					line = line[:0]
				}
				prevCR, justCapped = true, false
				continue
			case '\n':
				switch {
				case prevCR:
					if !crEmitted {
						emit("")
					}
				case len(line) == 0 && justCapped:
				default:
					emit(string(line))
					line = line[:0]
				}
				prevCR, justCapped = false, false
				continue
			}
			prevCR, justCapped = false, false
			line = append(line, b)
			if len(line) >= maxLineBytes {
				cut := runeBoundary(line)
				emit(string(line[:cut]))
				line = append(line[:0], line[cut:]...)
				justCapped = len(line) == 0
			}
		}
		if err != nil {
			if len(line) > 0 {
				emit(string(line))
			}
			return
		}
	}
}

// runeBoundary returns where to cut b so a trailing incomplete rune moves to
// the next chunk.
func runeBoundary(b []byte) int {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:]) {
				return i
			}
			break
		}
	}
	return len(b)
}
