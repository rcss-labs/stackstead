package cli

import (
	"bufio"
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
}

func stdinFd(env *Env) uintptr {
	if f, ok := env.Stdin.(*os.File); ok {
		return f.Fd()
	}
	return os.Stdin.Fd()
}

// trimLine strips one trailing "\n" or "\r\n".
func trimLine(s string) string {
	s = strings.TrimSuffix(s, "\n")
	return strings.TrimSuffix(s, "\r")
}

// readSecret returns a secret from stdin, a file or an interactive prompt.
// Error messages never contain the secret.
func readSecret(env *Env, src secretSource, prompt string, confirmSecret bool) (string, error) {
	var secret string
	switch {
	case src.Stdin:
		line, err := bufio.NewReader(env.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", exitf(ExitGeneral, "read secret from stdin: %v", err)
		}
		secret = trimLine(line)
	case src.File != "":
		fi, err := os.Stat(src.File)
		if err != nil {
			return "", exitf(ExitUsage, "password file: %v", errors.Unwrap(err))
		}
		if fi.IsDir() {
			return "", exitf(ExitUsage, "password file %s is a directory", src.File)
		}
		b, err := os.ReadFile(src.File)
		if err != nil {
			return "", exitf(ExitUsage, "password file: %v", errors.Unwrap(err))
		}
		first, _, _ := strings.Cut(string(b), "\n")
		secret = trimLine(first + "\n")
	case env.IsTerminal(stdinFd(env)):
		fd := int(stdinFd(env))
		fmt.Fprint(env.Stderr, prompt)
		b, err := readPassword(fd)
		fmt.Fprintln(env.Stderr)
		if err != nil {
			return "", exitf(ExitGeneral, "read password: %v", err)
		}
		secret = string(b)
		if secret != "" && confirmSecret {
			fmt.Fprint(env.Stderr, "Confirm "+strings.ToLower(prompt[:min(1, len(prompt))])+prompt[min(1, len(prompt)):])
			again, err := readPassword(fd)
			fmt.Fprintln(env.Stderr)
			if err != nil {
				return "", exitf(ExitGeneral, "read password: %v", err)
			}
			if string(again) != secret {
				return "", exitf(ExitValidation, "passwords do not match")
			}
		}
	default:
		return "", exitf(ExitUsage, "stdin is not a terminal: use --password-stdin or --password-file")
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
	line, err := bufio.NewReader(env.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, exitf(ExitGeneral, "read answer: %v", err)
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}
