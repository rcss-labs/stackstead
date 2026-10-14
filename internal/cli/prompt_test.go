package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/term"
)

func exitCodeOf(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	return codeFor(err)
}

func TestReadSecretStdinTrimsOneNewlineAndRejectsEmpty(t *testing.T) {
	for in, want := range map[string]string{"s3cret\n": "s3cret", "s3cret\r\n": "s3cret", "s3cret": "s3cret", "a\n\n": "a"} {
		e := newTestEnv(t, nil)
		e.Stdin = strings.NewReader(in)
		got, err := readSecret(context.Background(), e.Env, secretSource{Stdin: true}, "Password: ", false)
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "\n", "\r\n"} {
		e := newTestEnv(t, nil)
		e.Stdin = strings.NewReader(in)
		_, err := readSecret(context.Background(), e.Env, secretSource{Stdin: true}, "Password: ", false)
		if c := exitCodeOf(t, err); c != ExitValidation && c != ExitUsage {
			t.Errorf("%q: code %d", in, c)
		}
	}
}

func TestReadSecretFile(t *testing.T) {
	e := newTestEnv(t, nil)
	p := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(p, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readSecret(context.Background(), e.Env, secretSource{File: p}, "", false)
	if err != nil || got != "from-file" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := readSecret(context.Background(), e.Env, secretSource{File: t.TempDir()}, "", false); err == nil {
		t.Error("directory must be rejected")
	}
	if _, err := readSecret(context.Background(), e.Env, secretSource{File: filepath.Join(t.TempDir(), "missing")}, "", false); err == nil {
		t.Error("missing file must fail")
	}
}

func TestReadSecretErrorsDoNotLeakSecret(t *testing.T) {
	const secret = "TOPSECRET-VALUE"
	e := newTestEnv(t, nil)
	e.IsTerminal = func(uintptr) bool { return true }
	stubPasswords(t, secret, secret+"x")
	_, err := readSecret(context.Background(), e.Env, secretSource{}, "Password: ", true)
	if err == nil {
		t.Fatal("mismatch must fail")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(e.Err.String(), secret) || strings.Contains(e.Out.String(), secret) {
		t.Error("secret leaked into an error or output")
	}
	d := t.TempDir()
	if _, err := readSecret(context.Background(), e.Env, secretSource{File: d}, "", false); err == nil || !strings.Contains(err.Error(), d) {
		t.Errorf("password-file errors must name the path: %v", err)
	}
	missing := filepath.Join(d, "missing")
	if _, err := readSecret(context.Background(), e.Env, secretSource{File: missing}, "", false); err == nil || !strings.Contains(err.Error(), missing) {
		t.Errorf("password-file errors must name the path: %v", err)
	}
}

func TestReadSecretStdinSequentialReads(t *testing.T) {
	e := newTestEnv(t, nil)
	e.Stdin = strings.NewReader("old\nnew\n")
	for _, want := range []string{"old", "new"} {
		got, err := readSecret(context.Background(), e.Env, secretSource{Stdin: true}, "", false)
		if err != nil || got != want {
			t.Fatalf("got %q, %v; want %q", got, err, want)
		}
	}
}

type endless struct{}

func (endless) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	return len(p), nil
}

func TestReadSecretInputIsBounded(t *testing.T) {
	e := newTestEnv(t, nil)
	e.Stdin = endless{}
	if _, err := readSecret(context.Background(), e.Env, secretSource{Stdin: true}, "", false); err == nil {
		t.Fatal("unbounded stdin must fail")
	}
	p := filepath.Join(t.TempDir(), "big")
	if err := os.WriteFile(p, []byte(strings.Repeat("a", 200<<10)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSecret(context.Background(), e.Env, secretSource{File: p}, "", false); err == nil {
		t.Fatal("huge file must fail")
	}
}

func TestReadSecretCtrlCAtHiddenPrompt(t *testing.T) {
	oldRead, oldGet, oldRestore := readPassword, getTerminalState, restoreTerminal
	t.Cleanup(func() { readPassword, getTerminalState, restoreTerminal = oldRead, oldGet, oldRestore })
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	readPassword = func(int) ([]byte, error) { <-release; return []byte("typed"), nil }
	getTerminalState = func(int) (*term.State, error) { return &term.State{}, nil }
	restored := make(chan struct{}, 1)
	restoreTerminal = func(int, *term.State) error { restored <- struct{}{}; return nil }

	e := newTestEnv(t, nil)
	e.IsTerminal = func(uintptr) bool { return true }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := readSecret(ctx, e.Env, secretSource{}, "Password: ", false)
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || codeFor(err) != ExitInterrupted {
			t.Fatalf("err %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("readSecret did not return after cancel")
	}
	select {
	case <-restored:
	default:
		t.Error("terminal state not restored")
	}
}

func TestReadSecretDiscardsInputWhenCancelledDuringRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	old := readPassword
	t.Cleanup(func() { readPassword = old })
	readPassword = func(int) ([]byte, error) { cancel(); return []byte("typed"), nil }
	e := newTestEnv(t, nil)
	e.IsTerminal = func(uintptr) bool { return true }
	got, err := readSecret(ctx, e.Env, secretSource{}, "Password: ", false)
	if !errors.Is(err, context.Canceled) || got != "" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestReadSecretNonTTYWithoutSourceIsUsageError(t *testing.T) {
	e := newTestEnv(t, nil)
	_, err := readSecret(context.Background(), e.Env, secretSource{}, "Password: ", false)
	if c := exitCodeOf(t, err); c != ExitUsage {
		t.Fatalf("code %d", c)
	}
	if !strings.Contains(err.Error(), "--password-stdin or --password-file") {
		t.Errorf("message %q", err)
	}
}

func stubPasswords(t *testing.T, answers ...string) {
	t.Helper()
	old := readPassword
	t.Cleanup(func() { readPassword = old })
	i := 0
	readPassword = func(int) ([]byte, error) {
		a := answers[i]
		i++
		return []byte(a), nil
	}
}

func TestReadSecretTTYPromptsOnStderr(t *testing.T) {
	stubPasswords(t, "hunter2", "hunter2")
	e := newTestEnv(t, nil)
	e.IsTerminal = func(uintptr) bool { return true }
	got, err := readSecret(context.Background(), e.Env, secretSource{}, "Password: ", true)
	if err != nil || got != "hunter2" {
		t.Fatalf("got %q, %v", got, err)
	}
	if !strings.Contains(e.Err.String(), "Password: ") || e.Out.String() != "" {
		t.Errorf("stderr %q stdout %q", e.Err.String(), e.Out.String())
	}
	if strings.Contains(e.Err.String(), "hunter2") {
		t.Error("secret echoed")
	}
}

func TestReadSecretConfirmMismatchIsValidationError(t *testing.T) {
	stubPasswords(t, "one", "two")
	e := newTestEnv(t, nil)
	e.IsTerminal = func(uintptr) bool { return true }
	_, err := readSecret(context.Background(), e.Env, secretSource{}, "Password: ", true)
	if c := exitCodeOf(t, err); c != ExitValidation {
		t.Fatalf("code %d", c)
	}
	if !strings.Contains(err.Error(), "passwords do not match") || strings.Contains(err.Error(), "one") && strings.Contains(err.Error(), "two") {
		t.Errorf("message %q", err)
	}
}

func TestReadSecretTTYEmptyRejected(t *testing.T) {
	stubPasswords(t, "")
	e := newTestEnv(t, nil)
	e.IsTerminal = func(uintptr) bool { return true }
	if _, err := readSecret(context.Background(), e.Env, secretSource{}, "Password: ", false); err == nil {
		t.Fatal("empty password must be rejected")
	}
}

func TestConfirm(t *testing.T) {
	e := newTestEnv(t, nil)
	if ok, err := confirm(e.Env, "Delete?", true); !ok || err != nil {
		t.Fatalf("yes: %v %v", ok, err)
	}
	for in, want := range map[string]bool{"y\n": true, "Y\n": true, "yes\n": true, "n\n": false, "\n": false, "": false} {
		e := newTestEnv(t, nil)
		e.IsTerminal = func(uintptr) bool { return true }
		e.Stdin = strings.NewReader(in)
		ok, err := confirm(e.Env, "Delete?", false)
		if err != nil || ok != want {
			t.Errorf("%q: %v %v", in, ok, err)
		}
		if !strings.Contains(e.Err.String(), "Delete? [y/N] ") {
			t.Errorf("prompt %q", e.Err.String())
		}
	}
	_, err := confirm(newTestEnv(t, nil).Env, "Delete?", false)
	if c := exitCodeOf(t, err); c != ExitUsage || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("non-tty: %v", err)
	}
}
