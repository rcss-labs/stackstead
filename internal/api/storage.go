package api

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/stacks"
	"github.com/rcss-labs/stackstead/internal/storage"
)

func (s *server) registerStorageRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/storage", s.requireSession(s.handleStorageOverview))
	mux.Handle("GET /api/storage/images", s.requireSession(s.handleStorageImages))
	mux.Handle("GET /api/storage/images/{id}", s.requireSession(s.handleStorageImage))
	mux.Handle("GET /api/storage/volumes", s.requireSession(s.handleStorageVolumes))
	mux.Handle("GET /api/storage/volumes/{name}", s.requireSession(s.handleStorageVolume))
	mux.Handle("GET /api/storage/build-cache", s.requireSession(s.handleStorageBuildCache))
	mux.Handle("POST /api/storage/refresh", s.requireSession(s.handleStorageRefresh))
	mux.Handle("POST /api/storage/plans", s.requireSession(s.handleStorageCreatePlan))
	mux.Handle("GET /api/storage/plans/{id}", s.requireSession(s.handleStoragePlan))
	mux.Handle("POST /api/storage/plans/{id}/confirm", s.requireSession(s.handleStorageConfirm))
}
func (s *server) storageInventory(w http.ResponseWriter, r *http.Request) (storage.Inventory, bool) {
	inv, err := s.opts.Storage.Inventory(r.Context())
	if err != nil {
		s.writeStorageError(w, r, err)
		return storage.Inventory{}, false
	}
	return inv, true
}
func (s *server) handleStorageOverview(w http.ResponseWriter, r *http.Request) {
	inv, ok := s.storageInventory(w, r)
	if !ok {
		return
	}
	out := apitypes.StorageOverview{Images: storageTotals(inv.Totals[engine.DFImages]), Containers: storageTotals(inv.Totals[engine.DFContainers]), Volumes: storageTotals(inv.Totals[engine.DFVolumes]), BuildCache: storageTotals(inv.Totals[engine.DFBuildCache]), TopContainers: []apitypes.StorageTopContainer{}, DeclarationsUnknown: nonNull(inv.DeclarationsUnknown)}
	for _, c := range inv.TopContainers {
		out.TopContainers = append(out.TopContainers, apitypes.StorageTopContainer{Name: c.Name, Stack: c.Stack, SizeRw: c.SizeRw})
	}
	writeJSON(w, http.StatusOK, out)
}
func (s *server) handleStorageImages(w http.ResponseWriter, r *http.Request) {
	inv, ok := s.storageInventory(w, r)
	if !ok {
		return
	}
	out := apitypes.StorageImages{Images: make([]apitypes.StorageImage, 0, len(inv.Images)), Totals: storageTotals(inv.Totals[engine.DFImages])}
	for _, i := range inv.Images {
		out.Images = append(out.Images, storageImage(i))
	}
	writeJSON(w, http.StatusOK, out)
}
func (s *server) handleStorageVolumes(w http.ResponseWriter, r *http.Request) {
	inv, ok := s.storageInventory(w, r)
	if !ok {
		return
	}
	out := apitypes.StorageVolumes{Volumes: make([]apitypes.StorageVolume, 0, len(inv.Volumes)), Totals: storageTotals(inv.Totals[engine.DFVolumes])}
	for _, v := range inv.Volumes {
		out.Volumes = append(out.Volumes, storageVolume(v))
	}
	writeJSON(w, http.StatusOK, out)
}
func (s *server) handleStorageBuildCache(w http.ResponseWriter, r *http.Request) {
	inv, ok := s.storageInventory(w, r)
	if !ok {
		return
	}
	out := apitypes.StorageBuildCache{Records: make([]apitypes.StorageBuildRecord, 0, len(inv.BuildCache)), Totals: storageTotals(inv.Totals[engine.DFBuildCache])}
	for _, b := range inv.BuildCache {
		out.Records = append(out.Records, apitypes.StorageBuildRecord{ID: b.ID, Type: b.Type, Description: b.Description, Size: b.Size, InUse: b.InUse, Shared: b.Shared, CreatedAt: b.CreatedAt, LastUsedAt: b.LastUsedAt, UsageCount: b.UsageCount})
	}
	writeJSON(w, http.StatusOK, out)
}
func (s *server) handleStorageImage(w http.ResponseWriter, r *http.Request) {
	d, err := s.opts.Storage.ImageDetails(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeStorageError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, apitypes.StorageImageDetail{StorageImage: storageImage(d.Image), Architecture: d.Architecture, OS: d.OS, Layers: d.Layers, Labels: nonNullMap(d.Labels)})
}
func (s *server) handleStorageVolume(w http.ResponseWriter, r *http.Request) {
	d, err := s.opts.Storage.VolumeDetails(r.Context(), r.PathValue("name"))
	if err != nil {
		s.writeStorageError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, apitypes.StorageVolumeDetail{StorageVolume: storageVolume(d.Volume), Mountpoint: d.Mountpoint})
}
func (s *server) handleStorageRefresh(w http.ResponseWriter, r *http.Request) {
	var body struct{}
	if !decodeJSON(w, r, &body) {
		return
	}
	if wait := s.opts.Storage.Refresh(); wait > 0 {
		seconds := int(math.Ceil(wait.Seconds()))
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		writeJSON(w, http.StatusTooManyRequests, apitypes.StorageRateLimit{Error: "storage refresh rate limited", RetryAfter: seconds})
		return
	}
	started := s.opts.Now()
	// Invalidation alone does not start work. Warm the shared slots now.
	s.opts.Storage.DFReadings()
	writeJSON(w, http.StatusAccepted, apitypes.StorageRefresh{StartedAt: started})
}
func (s *server) handleStorageCreatePlan(w http.ResponseWriter, r *http.Request) {
	var body apitypes.StoragePlanRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	p, err := s.opts.Storage.CreatePlan(r.Context(), storage.PlanRequest{Kind: body.Kind, Selection: body.Selection, Ref: body.Ref, Name: body.Name})
	if err != nil {
		s.writeStorageError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, storagePlan(p))
}
func (s *server) handleStoragePlan(w http.ResponseWriter, r *http.Request) {
	p, err := s.opts.Storage.Plan(r.PathValue("id"))
	if err != nil {
		s.writeStorageError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, storagePlan(p))
}
func (s *server) handleStorageConfirm(w http.ResponseWriter, r *http.Request) {
	var body apitypes.StorageConfirmRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	op, err := s.opts.Storage.ConfirmPlan(r.Context(), r.PathValue("id"), body.Confirm, userFrom(r.Context()).Username)
	if err != nil {
		s.writeStorageError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toOperation(op))
}
func (s *server) writeStorageError(w http.ResponseWriter, r *http.Request, err error) {
	var field interface{ ValidationField() string }
	switch {
	case errors.Is(err, storage.ErrValidation), errors.Is(err, storage.ErrWrongPhrase):
		fields := []apitypes.FieldError{}
		if errors.As(err, &field) {
			fields = append(fields, apitypes.FieldError{Field: field.ValidationField(), Message: err.Error()})
		}
		writeJSON(w, http.StatusBadRequest, fieldErrorBody{Error: err.Error(), Fields: fields})
	case errors.Is(err, storage.ErrPlanNotFound), errors.Is(err, engine.ErrImageNotFound), errors.Is(err, engine.ErrVolumeNotFound), errors.Is(err, engine.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, storage.ErrConflict), errors.Is(err, stacks.ErrBusy):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, storage.ErrUnavailable), errors.Is(err, engine.ErrUnavailable), errors.Is(err, stacks.ErrShutdown), errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusServiceUnavailable, "storage unavailable: "+err.Error())
	case r.Context().Err() != nil:
	default:
		s.internalError(w, r, err)
	}
}
func nonNull[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}
func nonNullMap(v map[string]string) map[string]string {
	if v == nil {
		return map[string]string{}
	}
	return v
}
func storageTotals(t storage.KindTotals) apitypes.StorageKindTotals {
	return apitypes.StorageKindTotals{Count: t.Count, Size: t.Size, Reclaimable: t.Reclaimable, SizesAt: t.SizesAt, Refreshing: t.Refreshing, Error: t.Error}
}
func storageConsumers(in []storage.Consumer) []apitypes.StorageConsumer {
	out := make([]apitypes.StorageConsumer, 0, len(in))
	for _, c := range in {
		out = append(out, apitypes.StorageConsumer{ContainerID: c.ContainerID, Name: c.Name, State: c.State, Project: c.Project, Service: c.Service, Stack: c.Stack})
	}
	return out
}
func storageDeclarations(in []storage.Declaration) []apitypes.StorageDeclaration {
	out := make([]apitypes.StorageDeclaration, 0, len(in))
	for _, d := range in {
		out = append(out, apitypes.StorageDeclaration{Stack: d.Stack, Service: d.Service, Ref: d.Ref, StackState: d.StackState})
	}
	return out
}
func storageImage(i storage.Image) apitypes.StorageImage {
	return apitypes.StorageImage{ID: i.ID, Tags: nonNull(i.Tags), Digests: nonNull(i.Digests), Created: i.Created, Size: i.Size, SharedSize: i.SharedSize, Containers: storageConsumers(i.Containers), DeclaredBy: storageDeclarations(i.DeclaredBy), Dangling: i.Dangling, Status: i.Status}
}
func storageVolume(v storage.Volume) apitypes.StorageVolume {
	out := apitypes.StorageVolume{Name: v.Name, Driver: v.Driver, Scope: v.Scope, Project: v.Project, CreatedAt: v.CreatedAt, Labels: nonNullMap(v.Labels), Size: v.Size, RefCount: v.RefCount, SizesPending: v.SizesPending, Containers: storageConsumers(v.Containers), DeclaredBy: storageDeclarations(v.DeclaredBy), Status: v.Status}
	if v.Protection != nil {
		out.Protection = &apitypes.StorageProtection{Reason: v.Protection.Reason, Stack: v.Protection.Stack}
	}
	return out
}
func storagePlan(p storage.Plan) apitypes.StoragePlan {
	out := apitypes.StoragePlan{ID: p.ID, Kind: p.Kind, Mode: p.Mode, CreatedAt: p.CreatedAt, ExpiresAt: p.ExpiresAt, Items: make([]apitypes.StoragePlanItem, 0, len(p.Items)), Protected: make([]apitypes.StorageProtectedItem, 0, len(p.Protected)), Warnings: nonNull(p.Warnings), Reclaim: apitypes.StorageReclaim{Min: p.Reclaim.Min, Max: p.Reclaim.Max, Exact: p.Reclaim.Exact}, ConfirmPhrase: p.ConfirmPhrase}
	for _, i := range p.Items {
		out.Items = append(out.Items, apitypes.StoragePlanItem{Kind: i.Kind, ID: i.ID, Ref: i.Ref, Bytes: i.Bytes})
	}
	for _, i := range p.Protected {
		out.Protected = append(out.Protected, apitypes.StorageProtectedItem{Kind: i.Kind, ID: i.ID, Ref: i.Ref, Reason: i.Reason, Stack: i.Stack, StackState: i.StackState})
	}
	return out
}
