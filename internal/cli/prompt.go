package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// readPassword is the hidden-input seam; tests replace it.
var readPassword = term.ReadPassword

// secretSource says where a secret comes from when it is not typed at the
// terminal. Secrets are never accepted as flag values (they would land in
// shell history and the process list).
type secretSource struct {
	Stdin bool
	File  string
	// AllowFile says the command also has a --password-file flag, so the
	// non-terminal error may suggest it.
	AllowFile bool
}

func stdinFd(env *Env) uintptr {
	if f, ok := env.Stdin.(*os.File); ok {
		return f.Fd()
	}
	return os.Stdin.Fd()
}

// maxSecretBytes bounds every secret read so a stray file or endless stream
// cannot exhaust memory.
const maxSecretBytes = 64 << 10

// readLine reads one line byte by byte (no buffering, so a following read
// from the same stream sees the rest), stripping "\n" or "\r\n". It fails
// when the limit is hit before a newline.
func readLine(r io.Reader) (string, error) {
	var line []byte
	one := make([]byte, 1)
	for len(line) <= maxSecretBytes {
		n, err := r.Read(one)
		if n == 1 {
			if one[0] == '\n' {
				return strings.TrimSuffix(string(line), "\r"), nil
			}
			line = append(line, one[0])
		}
		if errors.Is(err, io.EOF) {
			return strings.TrimSuffix(string(line), "\r"), nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", errors.New("input is too long")
}

// readHidden reads a password without echo. Ctrl-C arrives as a cancelled
// ctx (the signal handler swallows SIGINT, so the blocked read would
// otherwise wait for Enter): the terminal is restored and ctx.Err returned.
func readHidden(ctx context.Context, env *Env, prompt string) (string, error) {
	fd := int(stdinFd(env))
	fmt.Fprint(env.Stderr, prompt)
	state, stateErr := getTerminalState(fd)
	type result struct {
		b   []byte
		err error
	}
	read := readPassword
	ch := make(chan result, 1)
	go func() {
		b, err := read(fd)
		ch <- result{b, err}
	}()
	select {
	case <-ctx.Done():
		if stateErr == nil {
			_ = restoreTerminal(fd, state)
		}
		fmt.Fprintln(env.Stderr)
		return "", ctx.Err()
	case r := <-ch:
		fmt.Fprintln(env.Stderr)
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if r.err != nil {
			return "", exitf(ExitGeneral, "read password: %v", r.err)
		}
		return string(r.b), nil
	}
}

// readSecret returns a secret from stdin, a file or an interactive prompt.
// Error messages never contain the secret.
func readSecret(ctx context.Context, env *Env, src secretSource, prompt string, confirmSecret bool) (string, error) {
	var secret string
	switch {
	case src.Stdin:
		line, err := readLine(env.Stdin)
		if err != nil {
			return "", exitf(ExitUsage, "read secret from stdin: %v", err)
		}
		secret = line
	case src.File != "":
		fi, err := os.Stat(src.File)
		if err != nil {
			return "", exitf(ExitUsage, "password file: %v", err)
		}
		if fi.IsDir() {
			return "", exitf(ExitUsage, "password file %s is a directory", src.File)
		}
		f, err := os.Open(src.File)
		if err != nil {
			return "", exitf(ExitUsage, "password file: %v", err)
		}
		defer f.Close()
		line, err := readLine(f)
		if err != nil {
			return "", exitf(ExitUsage, "password file %s: %v", src.File, err)
		}
		secret = line
	case env.IsTerminal(stdinFd(env)):
		var err error
		if secret, err = readHidden(ctx, env, prompt); err != nil {
			return "", err
		}
		if secret != "" && confirmSecret {
			again, err := readHidden(ctx, env, "Confirm "+strings.ToLower(prompt[:min(1, len(prompt))])+prompt[min(1, len(prompt)):])
			if err != nil {
				return "", err
			}
			if again != secret {
				return "", exitf(ExitValidation, "passwords do not match")
			}
		}
	default:
		hint := "--password-stdin"
		if src.AllowFile {
			hint = "--password-stdin or --password-file"
		}
		return "", exitf(ExitUsage, "stdin is not a terminal: use %s", hint)
	}
	if secret == "" {
		return "", exitf(ExitValidation, "the password is empty")
	}
	return secret, nil
}

// confirm asks a yes/no question on stderr. yes skips the question; without
// a terminal and without yes it refuses rather than guessing.
func confirm(env *Env, question string, yes bool) (bool, error) {
	if yes {
		return true, nil
	}
	if !env.IsTerminal(stdinFd(env)) {
		return false, exitf(ExitUsage, "refusing to continue without confirmation: use --yes")
	}
	fmt.Fprintf(env.Stderr, "%s [y/N] ", question)
	line, err := readLine(env.Stdin)
	if err != nil {
		return false, exitf(ExitGeneral, "read answer: %v", err)
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}

var (
	getTerminalState = term.GetState
	restoreTerminal  = term.Restore
)
