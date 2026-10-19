package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/history"
)

const (
	defaultHealthSpan = time.Hour
	minHealthSpan     = time.Minute
	maxHealthSpan     = 24 * time.Hour
)

// HistoryAPI is the per-service state timeline; *history.Store satisfies it.
type HistoryAPI interface {
	Timeline(ctx context.Context, project string, from, to time.Time) (history.Timeline, error)
}

// parseHealthSpan reads the span query value: empty means the default,
// otherwise a Go duration within [minHealthSpan, maxHealthSpan].
func parseHealthSpan(v string) (time.Duration, error) {
	if v == "" {
		return defaultHealthSpan, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < minHealthSpan || d > maxHealthSpan {
		return 0, errors.New("span must be a duration between 1m and 24h")
	}
	return d, nil
}

// handleHealthHistory returns the stack's per-service state runs over the
// last span, ending at the server's now truncated to the second.
func (s *server) handleHealthHistory(w http.ResponseWriter, r *http.Request) {
	span, err := parseHealthSpan(r.URL.Query().Get("span"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	detail, err := s.opts.Stacks.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		s.writeStackError(w, r, err)
		return
	}
	to := s.opts.Now().Truncate(time.Second)
	from := to.Add(-span)
	out := apitypes.HealthHistory{From: from.Unix(), To: to.Unix(), Services: []apitypes.ServiceHealth{}}
	// An empty project would match nothing meaningful; skip the query.
	if detail.Project != "" {
		tl, err := s.opts.History.Timeline(r.Context(), detail.Project, from, to)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		for _, svc := range tl.Services {
			runs := make([]apitypes.HealthRun, 0, len(svc.Runs))
			for _, run := range svc.Runs {
				runs = append(runs, apitypes.HealthRun{State: run.State, From: run.From.Unix(), To: run.To.Unix()})
			}
			out.Services = append(out.Services, apitypes.ServiceHealth{Service: svc.Service, Restarts: svc.Restarts, Runs: runs})
		}
	}
	writeJSON(w, http.StatusOK, out)
}
