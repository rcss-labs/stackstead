package api

import (
	"errors"
	"io/fs"
	"net/http"
	"strings"
	"syscall"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/composecli"
	"github.com/rcss-labs/stackstead/internal/composefile"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/fsguard"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

// fieldErrorBody differs from apitypes.ErrorBody only in always carrying
// "fields" (an empty list, not omitted), which is what the API has always sent.
type fieldErrorBody struct {
	Error  string                `json:"error"`
	Fields []apitypes.FieldError `json:"fields"`
}

// writeStackError maps domain errors to HTTP statuses. Unknown errors are
// logged and answered with a generic 500 so internals never leak.
func (s *server) writeStackError(w http.ResponseWriter, r *http.Request, err error) {
	var (
		dependents *composefile.DependentsError
		validation *stacks.ValidationError
		exit       *composecli.ExitError
		readOnly   *composefile.ReadOnlyFieldError
	)
	switch {
	case errors.As(err, &validation):
		fields := make([]apitypes.FieldError, 0, len(validation.Fields))
		for _, f := range validation.Fields {
			fields = append(fields, apitypes.FieldError(f))
		}
		writeJSON(w, http.StatusBadRequest, fieldErrorBody{Error: validation.Error(), Fields: fields})
	case errors.As(err, &exit):
		msg := strings.TrimSpace(exit.Output)
		if msg == "" {
			msg = exit.Error()
		}
		writeError(w, http.StatusUnprocessableEntity, msg)

	case errors.Is(err, stacks.ErrStackNotFound), errors.Is(err, stacks.ErrOperationNotFound),
		errors.Is(err, composefile.ErrServiceNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, stacks.ErrStackUnavailable):
		// A state conflict, not a missing resource: the registered stack's file is gone or broken.
		msg := "stack is unavailable"
		if detail := strings.TrimPrefix(err.Error(), stacks.ErrStackUnavailable.Error()); strings.TrimPrefix(detail, ": ") != "" {
			msg += ": " + strings.TrimPrefix(detail, ": ")
		}
		writeError(w, http.StatusConflict, msg)
	case errors.Is(err, composefile.ErrServiceExists), errors.Is(err, composefile.ErrLastService), errors.As(err, &dependents), errors.Is(err, stacks.ErrStackExists), errors.Is(err, stacks.ErrBusy), errors.Is(err, composefile.ErrConflict):
		writeError(w, http.StatusConflict, err.Error())

	case errors.Is(err, stacks.ErrShutdown), errors.Is(err, engine.ErrUnavailable),
		errors.Is(err, composecli.ErrComposeUnavailable):
		writeError(w, http.StatusServiceUnavailable, err.Error())

	case errors.Is(err, fsguard.ErrOutsideRoots):
		writeError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, fs.ErrNotExist):
		writeError(w, http.StatusNotFound, "no such file or directory")
	case errors.Is(err, fs.ErrPermission):
		writeError(w, http.StatusForbidden, "permission denied")
	case errors.Is(err, syscall.ENOTDIR):
		writeError(w, http.StatusBadRequest, "not a directory")
	case errors.Is(err, syscall.EINVAL):
		// Path resolution rejects names the kernel cannot take, e.g. a NUL byte.
		writeError(w, http.StatusBadRequest, "invalid path")

	case errors.Is(err, composefile.ErrEditRefused):
		writeError(w, http.StatusUnprocessableEntity, "this change needs the raw YAML editor")
	case errors.As(err, &readOnly), errors.Is(err, composefile.ErrInvalidConfig),
		errors.Is(err, composefile.ErrInvalidServiceName), errors.Is(err, composefile.ErrNotMapping),
		errors.Is(err, composefile.ErrMultipleDocuments):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	default:
		s.internalError(w, r, err)
	}
}
