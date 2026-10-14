package client

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
)

// ErrNoSession is returned, without sending a request, by methods that need
// a session when the Client has none.
var ErrNoSession = errors.New("no session for this context")

// statusSentinel lets callers write errors.Is(err, client.ErrNotFound)
// against an *Error without comparing status codes.
type statusSentinel int

func (s statusSentinel) Error() string { return fmt.Sprintf("HTTP %d", int(s)) }

const (
	ErrUnauthorized    = statusSentinel(http.StatusUnauthorized)
	ErrForbidden       = statusSentinel(http.StatusForbidden)
	ErrNotFound        = statusSentinel(http.StatusNotFound)
	ErrConflict        = statusSentinel(http.StatusConflict)
	ErrTooManyRequests = statusSentinel(http.StatusTooManyRequests)
	ErrUnavailable     = statusSentinel(http.StatusServiceUnavailable)
)

// Error is a non-2xx API response.
type Error struct {
	Status  int
	Message string
	Fields  []apitypes.FieldError
	// RetryAfter is the parsed Retry-After header of a 429; zero otherwise.
	RetryAfter time.Duration
}

func (e *Error) Error() string { return e.Message }

// Is matches the status sentinels (ErrUnauthorized, ErrNotFound, ...).
func (e *Error) Is(target error) bool {
	s, ok := target.(statusSentinel)
	return ok && int(s) == e.Status
}

// wrapNetwork drops the request URL from a transport error (it adds nothing
// the host does not) while keeping the cause for errors.Is/As.
func wrapNetwork(host string, err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return fmt.Errorf("connect to %s: %w", host, err)
}
