// Package composecli is the only place Stackstead runs `docker compose`.
package composecli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
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
// merged stdout/stderr.
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

// ProjectName returns the project name compose derives for the file.
func (r *Runner) ProjectName(ctx context.Context, file string) (string, error) {
	stdout, _, err := r.capture(ctx, filepath.Dir(file), composeArgs(file, []string{"config", "--format", "json"}))
	if err != nil {
		return "", err
	}
	var doc struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
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
	if errors.As(err, &exitErr) && ctx.Err() == nil {
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
	if errors.As(err, &exitErr) && ctx.Err() == nil {
		tail := &tailBuffer{}
		splitLines(strings.NewReader(stderr.String()+stdout.String()), tail.add)
		return "", "", &ExitError{Code: exitErr.ExitCode(), Output: tail.String()}
	}
	return stdout.String(), stderr.String(), err
}

// exec runs the binary in its own process group. On cancellation the group
// gets SIGTERM, then SIGKILL after Grace; exec returns after everything has
// exited. Errors caused by ctx wrap ctx.Err().
func (r *Runner) exec(ctx context.Context, dir string, args []string, stdout, stderr io.Writer) error {
	grace := r.grace()
	cmd := exec.Command(r.binary(), args...)
	cmd.Dir = dir
	if len(r.Env) > 0 {
		cmd.Env = append(cmd.Environ(), r.Env...)
	}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	isolate(cmd)

	if err := cmd.Start(); err != nil {
		return err
	}

	var killTimer *time.Timer
	var mu sync.Mutex
	stop := make(chan struct{})
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-ctx.Done():
			signalGroup(cmd, syscall.SIGTERM)
			mu.Lock()
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
	mu.Unlock()

	if ctxErr := ctx.Err(); ctxErr != nil {
		// Sweep stragglers that outlived the leader (e.g. ignored SIGTERM).
		signalGroup(cmd, syscall.SIGKILL)
		if waitErr != nil {
			return fmt.Errorf("docker compose %s: %w", firstArg(args), ctxErr)
		}
		return nil
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

// splitLines emits lines split on \n, \r and \r\n. Lines longer than
// maxLineBytes are emitted in chunks of that size. Empty lines from \r\n or
// consecutive \r are dropped, but blank \n lines are kept.
func splitLines(rd io.Reader, emit func(string)) {
	buf := make([]byte, 32*1024)
	line := make([]byte, 0, 4096)
	prevCR := false
	for {
		n, err := rd.Read(buf)
		for _, b := range buf[:n] {
			switch b {
			case '\r':
				if len(line) > 0 {
					emit(string(line))
					line = line[:0]
				}
				prevCR = true
				continue
			case '\n':
				if prevCR {
					prevCR = false
					continue
				}
				emit(string(line))
				line = line[:0]
				continue
			}
			prevCR = false
			line = append(line, b)
			if len(line) >= maxLineBytes {
				emit(string(line))
				line = line[:0]
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
