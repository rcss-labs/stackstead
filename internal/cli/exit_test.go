package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"testing"
)

func TestExitCodeMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, 0},
		{"exit error", exitf(ExitNotFound, "no such stack"), 4},
		{"wrapped exit error", fmt.Errorf("outer: %w", exitf(ExitConflict, "busy")), 5},
		{"canceled", context.Canceled, 130},
		{"wrapped canceled", fmt.Errorf("stream: %w", context.Canceled), 130},
		{"help", flag.ErrHelp, 0},
		{"plain", errors.New("boom"), 1},
	}
	for _, c := range cases {
		if got := codeFor(c.err); got != c.want {
			t.Errorf("%s: codeFor = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestExitCodeConstantsMatchSpec(t *testing.T) {
	want := map[int]int{
		ExitOK: 0, ExitGeneral: 1, ExitUsage: 2, ExitAuth: 3, ExitNotFound: 4, ExitConflict: 5,
		ExitValidation: 6, ExitUnavailable: 7, ExitRateLimited: 8, ExitOperationFailed: 9, ExitInterrupted: 130,
	}
	for got, w := range want {
		if got != w {
			t.Errorf("constant = %d, want %d", got, w)
		}
	}
}

func TestExitErrorMessageAndUnwrap(t *testing.T) {
	inner := errors.New("inner")
	e := &ExitError{Code: 5, Err: inner}
	if e.Error() != "inner" || !errors.Is(e, inner) {
		t.Errorf("Error()=%q Is=%v", e.Error(), errors.Is(e, inner))
	}
	if (&ExitError{Code: 2}).Error() != "" {
		t.Error("ExitError without Err must have an empty message")
	}
}
