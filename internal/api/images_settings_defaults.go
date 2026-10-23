package api

import (
	"net/http"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/images"
)

func (s *server) imageDefaultsView(w http.ResponseWriter, r *http.Request) {
	ret, err := s.opts.Images.DefaultRetention(r.Context())
	if err != nil {
		s.writeImagesError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, apitypes.ImageDefaults{Retention: apitypes.Retention{KeepLast: ret.KeepLast, KeepDays: ret.KeepDays}})
}

func (s *server) handleImageDefaults(w http.ResponseWriter, r *http.Request) {
	s.imageDefaultsView(w, r)
}

// handleSetImageDefaults stores the retention given to new images; existing
// images keep their own policy.
func (s *server) handleSetImageDefaults(w http.ResponseWriter, r *http.Request) {
	var req apitypes.ImageDefaults
	if !decodeJSON(w, r, &req) {
		return
	}
	err := s.opts.Images.SetDefaultRetention(r.Context(), images.Retention{KeepLast: req.Retention.KeepLast, KeepDays: req.Retention.KeepDays})
	if err != nil {
		s.writeImagesError(w, r, err)
		return
	}
	s.imageDefaultsView(w, r)
}
