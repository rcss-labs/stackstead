package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
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
	case errors.Is(err, context.Canceled):
		return ExitInterrupted
	case errors.Is(err, flag.ErrHelp):
		return ExitOK
	default:
		return ExitGeneral
	}
}
