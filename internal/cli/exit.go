package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"strings"
	"syscall"
	"unicode"

	"github.com/rcss-labs/stackstead/internal/client"
)

// Exit codes (design spec §4).
const (
	ExitOK              = 0
	ExitGeneral         = 1 // also the code for any unclassified error
	ExitUsage           = 2
	ExitAuth            = 3
	ExitNotFound        = 4
	ExitConflict        = 5
	ExitValidation      = 6
	ExitUnavailable     = 7
	ExitRateLimited     = 8
	ExitOperationFailed = 9
	ExitInterrupted     = 130
)

// ExitError carries a process exit code. An ExitError with a nil Err has an
// empty message: the command already printed what the user needs to see.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string {
	if e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e *ExitError) Unwrap() error { return e.Err }

func exitf(code int, format string, a ...any) error {
	return &ExitError{Code: code, Err: fmt.Errorf(format, a...)}
}

// codeFor maps an error to the process exit code.
func codeFor(err error) int {
	var ee *ExitError
	switch {
	case err == nil:
		return ExitOK
	case errors.As(err, &ee):
		return ee.Code
	case errors.Is(err, client.ErrNoSession):
		return ExitAuth
	case clientErrorCode(err) != 0:
		return clientErrorCode(err)
	case isUnreachable(err):
		return ExitUnavailable
	case errors.Is(err, context.Canceled):
		return ExitInterrupted
	case errors.Is(err, flag.ErrHelp):
		return ExitOK
	default:
		return ExitGeneral
	}
}

// clientErrorCode maps an API error response to an exit code; 0 means err
// holds no *client.Error.
func clientErrorCode(err error) int {
	var ce *client.Error
	if !errors.As(err, &ce) {
		return 0
	}
	switch s := ce.Status; {
	case s == 401 || s == 403:
		return ExitAuth
	case s == 404:
		return ExitNotFound
	case s == 409:
		return ExitConflict
	case s == 422 || (s == 400 && len(ce.Fields) > 0):
		return ExitValidation
	case s == 429:
		return ExitRateLimited
	case s == 503:
		return ExitUnavailable
	}
	return ExitGeneral
}

// isUnreachable reports a refused connection or a failed DNS lookup.
func isUnreachable(err error) bool {
	var dns *net.DNSError
	return errors.Is(err, syscall.ECONNREFUSED) || errors.As(err, &dns)
}

// cleanText drops control characters (ESC, CR, BEL, DEL, C1) from text that
// came from a server so it cannot rewrite the user's terminal. Newlines and
// tabs stay.
func cleanText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
}

// cleanError carries sanitised server text while keeping the original error
// reachable for errors.Is/As (client.ErrNotFound and friends).
type cleanError struct {
	msg string
	err error
}

func (e *cleanError) Error() string { return e.msg }
func (e *cleanError) Unwrap() error { return e.err }
