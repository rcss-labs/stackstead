package api

import (
	"context"
	"net/http"
	"net/url"

	"github.com/rcss-labs/stackstead/internal/apitypes"
)

// UpdateAPI is the update checker the routes read; *update.Checker satisfies it.
type UpdateAPI interface {
	Status() apitypes.UpdateStatus
	CheckNow(ctx context.Context) apitypes.UpdateStatus
}

const maxUpdateCommandBytes = 512

func (s *server) registerUpdateRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/settings/update", s.requireSession(s.handleUpdateStatus))
	mux.Handle("POST /api/settings/update/check", s.requireSession(s.handleUpdateCheck))
}

func (s *server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	writeUpdateStatus(w, s.opts.Updates.Status())
}

// handleUpdateCheck asks for a fresh check; the body must be a JSON object with
// no fields, like every other mutation. The checker rate-limits itself. The
// check outlives the request: a browser that gives up must not leave a
// cancelled attempt recorded as a failure for everyone.
func (s *server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if !decodeJSON(w, r, &struct{}{}) {
		return
	}
	writeUpdateStatus(w, s.opts.Updates.CheckNow(context.WithoutCancel(r.Context())))
}

func writeUpdateStatus(w http.ResponseWriter, st apitypes.UpdateStatus) {
	st = publishable(st)
	writeJSON(w, http.StatusOK, st)
}

// publishable blanks the two fields the browser renders as an instruction or a
// link when they are not safe to show: a command that is not printable ASCII of
// at most 512 bytes, and a notes URL that is not an https github.com URL.
func publishable(st apitypes.UpdateStatus) apitypes.UpdateStatus {
	if !printableASCII(st.Command, maxUpdateCommandBytes) {
		st.Command = ""
	}
	if !githubNotesURL(st.NotesURL) {
		st.NotesURL = ""
	}
	return st
}

func printableASCII(s string, max int) bool {
	if len(s) > max {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

func githubNotesURL(raw string) bool {
	if raw == "" {
		return true
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host == "github.com" && u.User == nil
}
