package api

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"

	"github.com/rcss-labs/stackstead/internal/actionlog"
	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

// Prefix filters accept partial kind components, never SQL wildcard syntax.
var actionKindPrefix = regexp.MustCompile(`^[a-z]+(?:\.[a-z]*)?$`)

func (s *server) handleActions(w http.ResponseWriter, r *http.Request) {
	q, queryErr := url.ParseQuery(r.URL.RawQuery)
	if queryErr != nil {
		writeError(w, http.StatusBadRequest, "invalid action query")
		return
	}
	kind, scope := q.Get("kind"), q.Get("scope")
	if kind != "" && !actionKindPrefix.MatchString(kind) {
		writeError(w, http.StatusBadRequest, "invalid action kind prefix")
		return
	}
	if scope != "" && scope != "host" {
		if err := stacks.ValidateName(scope); err != nil {
			writeError(w, http.StatusBadRequest, "invalid action scope")
			return
		}
	}
	limit := 50
	if q.Has("limit") {
		n, err := strconv.Atoi(q.Get("limit"))
		if err != nil || n < 1 || n > 500 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 500")
			return
		}
		limit = n
	}
	entries, err := s.opts.Actions.Recent(r.Context(), actionlog.Filter{KindPrefix: kind, Scope: scope, Limit: limit})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]apitypes.ActionEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, apitypes.ActionEntry{ID: e.ID, TS: e.TS, Username: e.Username, Kind: e.Kind, Scope: e.Scope, Target: e.Target, Detail: e.Detail, Bytes: e.Bytes, Result: e.Result, OperationID: e.OperationID})
	}
	writeJSON(w, http.StatusOK, out)
}
