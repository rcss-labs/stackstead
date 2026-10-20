package api

import (
	"context"
	"net/http"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

// dockerfileAPI is the optional Dockerfile surface of the stacks service. It
// is a separate interface so StacksAPI fakes need not implement it; the
// routes exist only when the configured service does.
type dockerfileAPI interface {
	Dockerfile(ctx context.Context, stack, service string) (stacks.DockerfileView, error)
	SaveDockerfile(ctx context.Context, stack, service string, content []byte, hash string) (string, error)
	ActService(ctx context.Context, stack, service, action string) (stacks.Operation, error)
}

var _ dockerfileAPI = (*stacks.Service)(nil)

func (s *server) registerDockerfileRoutes(mux *http.ServeMux) {
	df, ok := s.opts.Stacks.(dockerfileAPI)
	if !ok {
		return
	}
	route := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.requireSession(h)) }
	route("GET /api/stacks/{name}/services/{service}/dockerfile", func(w http.ResponseWriter, r *http.Request) {
		s.handleGetDockerfile(df, w, r)
	})
	route("PUT /api/stacks/{name}/services/{service}/dockerfile", func(w http.ResponseWriter, r *http.Request) {
		s.handlePutDockerfile(df, w, r)
	})
	route("POST /api/stacks/{name}/services/{service}/actions/{action}", func(w http.ResponseWriter, r *http.Request) {
		s.handleServiceAction(df, w, r)
	})
}

func (s *server) handleGetDockerfile(df dockerfileAPI, w http.ResponseWriter, r *http.Request) {
	v, err := df.Dockerfile(r.Context(), r.PathValue("name"), r.PathValue("service"))
	if err != nil {
		s.writeStackError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, apitypes.DockerfileView{Content: string(v.Content), Hash: v.Hash,
		Exists: v.Exists, Editable: v.Editable, Path: v.Path, Reason: v.Reason})
}

func (s *server) handlePutDockerfile(df dockerfileAPI, w http.ResponseWriter, r *http.Request) {
	var req apitypes.DockerfileContent
	if !decodeJSONLimit(w, r, &req, dockerfileBodyBytes) {
		return
	}
	hash, err := df.SaveDockerfile(r.Context(), r.PathValue("name"), r.PathValue("service"), []byte(req.Content), req.Hash)
	if err != nil {
		s.writeStackError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, hashResponse{Hash: hash})
}

func (s *server) handleServiceAction(df dockerfileAPI, w http.ResponseWriter, r *http.Request) {
	var empty struct{}
	if !decodeJSON(w, r, &empty) {
		return
	}
	op, err := df.ActService(r.Context(), r.PathValue("name"), r.PathValue("service"), r.PathValue("action"))
	if err != nil {
		s.writeStackError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toOperation(op))
}
