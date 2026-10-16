package api

import (
	"context"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"github.com/rcss-labs/stackstead/internal/composefile"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/fsguard"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

// StacksAPI is the stack domain the HTTP layer needs; *stacks.Service
// satisfies it directly.
type StacksAPI interface {
	List(ctx context.Context) ([]stacks.Summary, error)
	Create(ctx context.Context, req stacks.CreateRequest) (stacks.Summary, error)
	Import(ctx context.Context, path string) (stacks.Summary, error)
	Get(ctx context.Context, name string) (stacks.Detail, error)
	Raw(ctx context.Context, name string) (content []byte, hash string, err error)
	SaveRaw(ctx context.Context, stack string, content []byte, hash string) (stacks.EditResult, error)
	UpdateService(ctx context.Context, stack, service string, cfg composefile.ServiceConfig, hash string) (stacks.EditResult, error)
	PreviewService(ctx context.Context, stack, service string, cfg composefile.ServiceConfig, hash string) (candidate []byte, err error)
	Act(ctx context.Context, stack, action string) (stacks.Operation, error)
	Remove(ctx context.Context, stack string, withVolumes bool) (stacks.Operation, error)
	Networks(ctx context.Context) ([]engine.Network, error)
}

// OperationsAPI is the operation registry; *stacks.Ops satisfies it directly.
type OperationsAPI interface {
	Recent(stack string) []stacks.Operation
	// All returns the retained operations of every stack, newest first.
	All() []stacks.Operation
	Get(id string) (stacks.Operation, error)
	Lines(id string) ([]string, error)
	// Subscribe has the contract documented on stacks.Ops.Subscribe.
	Subscribe(id string) (replay []string, first int, live <-chan string, done <-chan stacks.Operation, cancel func(), err error)
	Cancel(id string) error
}

// StatusReport is what a StatusFunc learns: Docker is "ok" or "unavailable",
// DockerVersion the engine version ("" when unknown), Compose the compose
// version or "unavailable".
type StatusReport struct{ Docker, DockerVersion, Compose string }

type StatusFunc func(ctx context.Context) StatusReport

// Compile-time proof that the production types fit.
var (
	_ StacksAPI     = (*stacks.Service)(nil)
	_ OperationsAPI = (*stacks.Ops)(nil)
)

const checksHeader = "X-Stackstead-Checks"

func (s *server) registerStackRoutes(mux *http.ServeMux) {
	route := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.requireSession(h)) }
	route("GET /api/stacks", s.handleListStacks)
	route("POST /api/stacks", s.handleCreateStack)
	route("POST /api/stacks/import", s.handleImportStack)
	route("GET /api/stacks/{name}", s.handleGetStack)
	route("GET /api/stacks/{name}/compose", s.handleGetCompose)
	route("PUT /api/stacks/{name}/compose", s.handlePutCompose)
	route("PUT /api/stacks/{name}/services/{service}", s.handleUpdateService)
	route("POST /api/stacks/{name}/services/{service}/preview", s.handlePreviewService)
	route("POST /api/stacks/{name}/actions/{action}", s.handleAction)
	route("DELETE /api/stacks/{name}", s.handleRemoveStack)
	route("GET /api/stacks/{name}/operations", s.handleStackOperations)
	route("GET /api/operations", s.handleListOperations)
	route("GET /api/operations/{id}", s.handleGetOperation)
	route("GET /api/operations/{id}/stream", s.handleStreamOperation)
	route("POST /api/operations/{id}/cancel", s.handleCancelOperation)
	route("GET /api/fs", s.handleFS)
	route("GET /api/status", s.handleStatus)
}

// markPartial tells the client the port/IP conflict checks ran without the
// engine and covered only ports declared by other stacks.
func markPartial(w http.ResponseWriter, partial bool) {
	if partial {
		w.Header().Set(checksHeader, "partial")
	}
}

func (s *server) handleListStacks(w http.ResponseWriter, r *http.Request) {
	list, err := s.opts.Stacks.List(r.Context())
	if err != nil {
		s.writeStackError(w, r, err)
		return
	}
	out := make([]summaryDTO, 0, len(list))
	for _, sum := range list {
		out = append(out, toSummary(sum))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) handleCreateStack(w http.ResponseWriter, r *http.Request) {
	var req createStackRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	sum, err := s.opts.Stacks.Create(r.Context(), stacks.CreateRequest{Name: req.Name, Services: []composefile.NamedService{{Name: req.Service, Config: serviceConfigToDomain(req.Config)}}, IndexedFields: false})
	if err != nil {
		s.writeStackError(w, r, err)
		return
	}
	markPartial(w, sum.ChecksPartial)
	writeJSON(w, http.StatusCreated, toSummary(sum))
}

func (s *server) handleImportStack(w http.ResponseWriter, r *http.Request) {
	var req importRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	sum, err := s.opts.Stacks.Import(r.Context(), req.Path)
	if err != nil {
		s.writeStackError(w, r, err)
		return
	}
	markPartial(w, sum.ChecksPartial)
	writeJSON(w, http.StatusCreated, toSummary(sum))
}

func (s *server) handleGetStack(w http.ResponseWriter, r *http.Request) {
	d, err := s.opts.Stacks.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		s.writeStackError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toDetail(d))
}

func (s *server) handleGetCompose(w http.ResponseWriter, r *http.Request) {
	content, hash, err := s.opts.Stacks.Raw(r.Context(), r.PathValue("name"))
	if err != nil {
		s.writeStackError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rawComposeBody{Content: string(content), Hash: hash})
}

func (s *server) handlePutCompose(w http.ResponseWriter, r *http.Request) {
	var req rawComposeBody
	if !decodeJSON(w, r, &req) {
		return
	}
	res, err := s.opts.Stacks.SaveRaw(r.Context(), r.PathValue("name"), []byte(req.Content), req.Hash)
	if err != nil {
		s.writeStackError(w, r, err)
		return
	}
	markPartial(w, res.ChecksPartial)
	writeJSON(w, http.StatusOK, hashResponse{Hash: res.Hash})
}

func (s *server) handleUpdateService(w http.ResponseWriter, r *http.Request) {
	var req updateServiceRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	res, err := s.opts.Stacks.UpdateService(r.Context(), r.PathValue("name"), r.PathValue("service"), serviceConfigToDomain(req.Config), req.Hash)
	if err != nil {
		s.writeStackError(w, r, err)
		return
	}
	markPartial(w, res.ChecksPartial)
	writeJSON(w, http.StatusOK, hashResponse{Hash: res.Hash})
}

// handlePreviewService returns the file a save of the same edit would write,
// without writing it, so the UI can show a diff before saving.
func (s *server) handlePreviewService(w http.ResponseWriter, r *http.Request) {
	var req updateServiceRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	candidate, err := s.opts.Stacks.PreviewService(r.Context(), r.PathValue("name"), r.PathValue("service"), serviceConfigToDomain(req.Config), req.Hash)
	if err != nil {
		s.writeStackError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, previewResponse{Content: string(candidate), Hash: req.Hash})
}

func (s *server) handleAction(w http.ResponseWriter, r *http.Request) {
	var empty struct{}
	if !decodeJSON(w, r, &empty) {
		return
	}
	op, err := s.opts.Stacks.Act(r.Context(), r.PathValue("name"), r.PathValue("action"))
	if err != nil {
		s.writeStackError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toOperation(op))
}

func (s *server) handleRemoveStack(w http.ResponseWriter, r *http.Request) {
	withVolumes := false
	if v := r.URL.Query().Get("volumes"); v != "" {
		parsed, err := strconv.ParseBool(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "volumes must be true or false")
			return
		}
		withVolumes = parsed
	}
	op, err := s.opts.Stacks.Remove(r.Context(), r.PathValue("name"), withVolumes)
	if err != nil {
		s.writeStackError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toOperation(op))
}

func (s *server) handleStackOperations(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, toOperations(s.opts.Operations.Recent(r.PathValue("name"))))
}

func (s *server) handleGetOperation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	op, err := s.opts.Operations.Get(id)
	if err != nil {
		s.writeStackError(w, r, err)
		return
	}
	lines, err := s.opts.Operations.Lines(id)
	if err != nil {
		s.writeStackError(w, r, err)
		return
	}
	if lines == nil {
		lines = []string{}
	}
	writeJSON(w, http.StatusOK, operationDetailDTO{Operation: toOperation(op), Lines: lines})
}

func (s *server) handleCancelOperation(w http.ResponseWriter, r *http.Request) {
	var empty struct{}
	if !decodeJSON(w, r, &empty) {
		return
	}
	id := r.PathValue("id")
	if err := s.opts.Operations.Cancel(id); err != nil {
		s.writeStackError(w, r, err)
		return
	}
	op, err := s.opts.Operations.Get(id)
	if err != nil {
		s.writeStackError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toOperation(op))
}

// handleFS lists a directory inside the allowed roots; with no path it lists
// the roots themselves so the browser has somewhere to start.
func (s *server) handleFS(w http.ResponseWriter, r *http.Request) {
	roots := s.opts.FSRoots
	path := r.URL.Query().Get("path")
	if path == "" {
		out := make([]fsEntryDTO, 0, len(roots))
		for _, root := range roots {
			out = append(out, fsEntryDTO{Name: root, IsDir: true})
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	if !filepath.IsAbs(path) {
		writeError(w, http.StatusBadRequest, "path must be absolute")
		return
	}
	entries, err := fsguard.List(roots, path)
	if err != nil {
		s.writeStackError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toFSEntries(entries))
}

func (s *server) handleStatus(w http.ResponseWriter, r *http.Request) {
	report := StatusReport{Docker: "unavailable", Compose: "unavailable"}
	if s.opts.Status != nil {
		report = s.opts.Status(r.Context())
	}
	flags := s.statusFlags()
	writeJSON(w, http.StatusOK, statusDTO{
		Docker: report.Docker, DockerVersion: report.DockerVersion, Compose: report.Compose,
		Events: flags.Events, MetricsDegraded: flags.MetricsDegraded, Version: s.opts.Version,
		MetricsIntervalSeconds:  int64(s.opts.MetricsInterval / time.Second),
		MetricsRetentionSeconds: int64(s.opts.MetricsRetention / time.Second),
	})
}
