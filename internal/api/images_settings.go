package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/registry"
	"github.com/rcss-labs/stackstead/internal/secret"
)

// RegistriesAPI is the registry store the settings routes need;
// *registry.Service satisfies it directly. Every method returns views without
// secret values.
type RegistriesAPI interface {
	List(ctx context.Context) ([]registry.Registry, error)
	Get(ctx context.Context, id string) (registry.Registry, error)
	Create(ctx context.Context, user string, req registry.Request) (registry.Registry, error)
	Update(ctx context.Context, user, id string, req registry.Request) (registry.Registry, error)
	Delete(ctx context.Context, user, id string) error
	Test(ctx context.Context, id string) (registry.TestResult, error)
}

var _ RegistriesAPI = (*registry.Service)(nil)

const codeRegistryUnconfigured = "registry_unconfigured"

func (s *server) registerRegistryRoutes(mux *http.ServeMux) {
	route := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.requireSession(h)) }
	route("GET /api/settings/registries", s.handleListRegistries)
	route("POST /api/settings/registries", s.handleCreateRegistry)
	route("PUT /api/settings/registries/{id}", s.handleUpdateRegistry)
	route("DELETE /api/settings/registries/{id}", s.handleDeleteRegistry)
	route("POST /api/settings/registries/{id}/test", s.handleTestRegistry)
	route("GET /api/images/registries", s.handleImageRegistries)
}

// toRegistryView builds the API view. It copies the non-secret ECR access key
// id and only the "is set" state of every secret; r must come from
// registry.Service, never from the store.
func toRegistryView(r registry.Registry) apitypes.RegistryView {
	v := apitypes.RegistryView{
		ID: r.ID, Name: r.Name, Kind: string(r.Kind), Host: r.Host,
		LastTestAt: r.LastTestAt, LastTestOK: r.LastTestOK, LastTestMessage: r.LastTestMessage,
	}
	switch r.Kind {
	case registry.KindDockerHub:
		v.Username = r.Hub.Username
		v.Configured = r.Hub.Username != "" && r.Hub.TokenSet
	case registry.KindECR:
		v.Region, v.Mode, v.Profile, v.AccountID = r.ECR.Region, string(r.ECR.Mode), r.ECR.Profile, r.ECR.AccountID
		switch r.ECR.Mode {
		case registry.ECRStatic:
			v.AccessKeyID = r.ECR.AccessKeyID
			v.Configured = r.ECR.Region != "" && r.ECR.AccessKeyID != "" && r.ECR.SecretKeySet
		case registry.ECRProfile:
			v.Configured = r.ECR.Profile != ""
		case registry.ECRRole:
			v.Configured = r.ECR.Region != ""
		}
	}
	return v
}

func toRegistryViews(list []registry.Registry) []apitypes.RegistryView {
	out := make([]apitypes.RegistryView, 0, len(list))
	for _, r := range list {
		out = append(out, toRegistryView(r))
	}
	return out
}

func toRegistryRequest(r apitypes.RegistryRequest) registry.Request {
	req := registry.Request{Name: r.Name, Kind: registry.Kind(r.Kind)}
	if r.Hub != nil {
		req.Hub = &registry.Hub{Username: r.Hub.Username, Token: secret.Secret(r.Hub.Token)}
	}
	if r.ECR != nil {
		req.ECR = &registry.ECR{
			Region: r.ECR.Region, Mode: registry.ECRMode(r.ECR.Mode), AccessKeyID: r.ECR.AccessKeyID,
			SecretAccessKey: secret.Secret(r.ECR.SecretAccessKey), SessionToken: secret.Secret(r.ECR.SessionToken),
			Profile: r.ECR.Profile,
		}
	}
	return req
}

// registryFieldError turns a registry.ErrInvalid message ("invalid registry:
// name must be ...") into a field error; the field is the leading word when it
// names a request field.
func registryFieldError(err error) apitypes.FieldError {
	msg := strings.TrimPrefix(err.Error(), registry.ErrInvalid.Error()+": ")
	field := ""
	if word, _, ok := strings.Cut(msg, " "); ok {
		switch word {
		case "name", "kind", "region", "accountId", "secretAccessKey", "accessKeyId":
			field = word
		}
	}
	return apitypes.FieldError{Field: field, Message: msg}
}

func (s *server) writeRegistryError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, registry.ErrInvalid):
		writeJSON(w, http.StatusBadRequest, fieldErrorBody{Error: err.Error(), Fields: []apitypes.FieldError{registryFieldError(err)}})
	case errors.Is(err, registry.ErrNameTaken):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, registry.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, registry.ErrUnconfigured):
		writeCodedError(w, http.StatusConflict, err.Error(), codeRegistryUnconfigured)
	default:
		s.internalError(w, r, err)
	}
}

func (s *server) handleListRegistries(w http.ResponseWriter, r *http.Request) {
	list, err := s.opts.Registries.List(r.Context())
	if err != nil {
		s.writeRegistryError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toRegistryViews(list))
}

func (s *server) handleCreateRegistry(w http.ResponseWriter, r *http.Request) {
	var req apitypes.RegistryRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	created, err := s.opts.Registries.Create(r.Context(), userFrom(r.Context()).Username, toRegistryRequest(req))
	if err != nil {
		s.writeRegistryError(w, r, err)
		return
	}
	view, err := s.opts.Registries.Get(r.Context(), created.ID)
	if err != nil {
		s.writeRegistryError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toRegistryView(view))
}

func (s *server) handleUpdateRegistry(w http.ResponseWriter, r *http.Request) {
	var req apitypes.RegistryRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	updated, err := s.opts.Registries.Update(r.Context(), userFrom(r.Context()).Username, r.PathValue("id"), toRegistryRequest(req))
	if err != nil {
		s.writeRegistryError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toRegistryView(updated))
}

func (s *server) handleDeleteRegistry(w http.ResponseWriter, r *http.Request) {
	if err := s.opts.Registries.Delete(r.Context(), userFrom(r.Context()).Username, r.PathValue("id")); err != nil {
		s.writeRegistryError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTestRegistry answers 200 with ok=false for a failed connection (the
// scrubbed reason is in message); only a missing registry, missing settings or
// an internal fault use an error status.
func (s *server) handleTestRegistry(w http.ResponseWriter, r *http.Request) {
	res, err := s.opts.Registries.Test(r.Context(), r.PathValue("id"))
	if err != nil {
		if res.OK {
			// The provider test passed but saving the result failed; the
			// connection itself works, so report that and log the save error.
			s.opts.Logger.Warn("record registry test failed", "registry", r.PathValue("id"), "err", err)
			writeJSON(w, http.StatusOK, apitypes.RegistryTest{OK: true, Host: res.Host, Message: res.Message})
			return
		}
		if res.Message != "" && !errors.Is(err, registry.ErrUnconfigured) {
			writeJSON(w, http.StatusOK, apitypes.RegistryTest{OK: false, Host: res.Host, Message: res.Message})
			return
		}
		s.writeRegistryError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, apitypes.RegistryTest{OK: res.OK, Host: res.Host, Message: res.Message})
}

// handleImageRegistries lists registries for the push-target picker. It is
// reachable with a peer credential, so it answers id, name, kind and host only.
func (s *server) handleImageRegistries(w http.ResponseWriter, r *http.Request) {
	list, err := s.opts.Registries.List(r.Context())
	if err != nil {
		s.writeRegistryError(w, r, err)
		return
	}
	out := make([]apitypes.RegistryOption, 0, len(list))
	for _, reg := range list {
		out = append(out, apitypes.RegistryOption{ID: reg.ID, Name: reg.Name, Kind: string(reg.Kind), Host: reg.Host})
	}
	writeJSON(w, http.StatusOK, out)
}
