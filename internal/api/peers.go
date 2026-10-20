package api

import (
	"errors"
	"net/http"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/peers"
)

// registerPeerManagementRoutes adds the Settings routes for connected
// servers. They are session-only: /api/peers is on the peer denylist.
func (s *server) registerPeerManagementRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/peers", s.requireSession(s.handleListPeers))
	mux.Handle("POST /api/peers", s.requireSession(s.handleAddPeer))
	mux.Handle("DELETE /api/peers/{id}", s.requireSession(s.handleRemovePeer))
	mux.Handle("POST /api/peers/probe", s.requireSession(s.handleProbePeer))
	mux.Handle("POST /api/peers/tokens", s.requireSession(s.handleCreatePeerToken))
	mux.Handle("GET /api/peers/self", s.requireSession(s.handleGetSelf))
	mux.Handle("PUT /api/peers/self", s.requireSession(s.handleSetSelf))
}

// writePeersError maps peers errors to responses. It never answers 401: the
// web app treats any 401 outside login as a lost session and signs out.
func (s *server) writePeersError(w http.ResponseWriter, r *http.Request, err error) {
	var (
		validation *peers.ValidationError
		pe         *peers.PeerError
	)
	switch {
	case errors.As(err, &validation):
		writeJSON(w, http.StatusBadRequest, fieldErrorBody{Error: validation.Error(), Fields: validation.Fields})
	case errors.Is(err, peers.ErrPeerNotFound):
		writeCodedError(w, http.StatusNotFound, "server not found", peers.CodeUnknown)
	case errors.Is(err, peers.ErrAlreadyPaired):
		writeError(w, http.StatusConflict, "this server is already connected")
	case errors.As(err, &pe):
		status := http.StatusBadGateway
		if pe.Code == peers.CodeUnknown {
			status = http.StatusNotFound
		}
		writeCodedError(w, status, pe.Msg, pe.Code)
	default:
		s.internalError(w, r, err)
	}
}

func (s *server) handleListPeers(w http.ResponseWriter, r *http.Request) {
	views, err := s.opts.Peers.Views(r.Context())
	if err != nil {
		s.writePeersError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *server) handleAddPeer(w http.ResponseWriter, r *http.Request) {
	var req apitypes.AddPeerRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	view, err := s.opts.Peers.AddServer(r.Context(), req)
	if err != nil {
		s.writePeersError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

func (s *server) handleRemovePeer(w http.ResponseWriter, r *http.Request) {
	if err := s.opts.Peers.Remove(r.Context(), r.PathValue("id")); err != nil {
		s.writePeersError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleProbePeer(w http.ResponseWriter, r *http.Request) {
	var req apitypes.PeerProbeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	probe, err := s.opts.Peers.ProbeAddress(r.Context(), req.URL)
	if err != nil {
		s.writePeersError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, probe)
}

func (s *server) handleCreatePeerToken(w http.ResponseWriter, r *http.Request) {
	token, err := s.opts.Peers.CreateConnectionToken(r.Context())
	if err != nil {
		s.writePeersError(w, r, err)
		return
	}
	// The plaintext token exists only in this response.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, token)
}

func (s *server) handleGetSelf(w http.ResponseWriter, r *http.Request) {
	self, err := s.opts.Peers.Self(r.Context(), r)
	if err != nil {
		s.writePeersError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, self)
}

func (s *server) handleSetSelf(w http.ResponseWriter, r *http.Request) {
	var req apitypes.ServerSelfUpdate
	if !decodeJSON(w, r, &req) {
		return
	}
	self, err := s.opts.Peers.SetSelf(r.Context(), req)
	if err != nil {
		s.writePeersError(w, r, err)
		return
	}
	// SetSelf has no request to derive a suggestion from; reuse the GET logic.
	if full, err := s.opts.Peers.Self(r.Context(), r); err == nil {
		self.SuggestedURL = full.SuggestedURL
	}
	writeJSON(w, http.StatusOK, self)
}
