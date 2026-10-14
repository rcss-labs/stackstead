package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"syscall"
	"testing"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/client"
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

func TestExitCodeMappingForClientErrors(t *testing.T) {
	fields := []apitypes.FieldError{{Field: "name", Message: "bad"}}
	dial := &url.Error{Op: "Get", URL: "http://x", Err: &net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}}
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"401", &client.Error{Status: 401}, 3},
		{"403", &client.Error{Status: 403}, 3},
		{"404", &client.Error{Status: 404}, 4},
		{"409", &client.Error{Status: 409}, 5},
		{"400 with fields", &client.Error{Status: 400, Fields: fields}, 6},
		{"422", &client.Error{Status: 422}, 6},
		{"400 plain", &client.Error{Status: 400}, 1},
		{"429", &client.Error{Status: 429}, 8},
		{"503", &client.Error{Status: 503}, 7},
		{"other 4xx", &client.Error{Status: 418}, 1},
		{"500", &client.Error{Status: 500}, 1},
		{"wrapped 404", fmt.Errorf("x: %w", &client.Error{Status: 404}), 4},
		{"no session", client.ErrNoSession, 3},
		{"wrapped no session", fmt.Errorf("x: %w", client.ErrNoSession), 3},
		{"connection refused", fmt.Errorf("connect to host: %w", dial), 7},
		{"bare ECONNREFUSED", syscall.ECONNREFUSED, 7},
		{"dns", fmt.Errorf("connect: %w", &net.DNSError{Err: "no such host", Name: "x"}), 7},
		{"exit error wins", &ExitError{Code: 2, Err: &client.Error{Status: 404}}, 2},
	}
	for _, c := range cases {
		if got := codeFor(c.err); got != c.want {
			t.Errorf("%s: codeFor = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestCleanTextStripsControlCharacters(t *testing.T) {
	if got, want := cleanText("a\x1b[31mb\rc\x07d\ne\tf\x7fg\u009bh"), "a[31mbcd\ne\tfgh"; got != want {
		t.Errorf("cleanText = %q, want %q", got, want)
	}
}
