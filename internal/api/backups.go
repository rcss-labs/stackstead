package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/backup"
	"github.com/rcss-labs/stackstead/internal/secret"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

// BackupAPI is the backup feature the HTTP layer needs; *backup.Service
// satisfies it directly.
type BackupAPI interface {
	Config(ctx context.Context) (backup.Config, error)
	SaveConfig(ctx context.Context, cfg backup.Config) error
	ClearS3Secret(ctx context.Context) error
	TestS3(ctx context.Context, cfg backup.S3Dest) error
	Selections(ctx context.Context) ([]backup.StackSelectionView, error)
	SetSelection(ctx context.Context, sel backup.StackSelection) error
	Start(ctx context.Context, trigger, user string) (stacks.Operation, error)
	Runs(ctx context.Context, limit int) ([]backup.Run, error)
	Archives(ctx context.Context) ([]backup.ArchiveRef, error)
	NextRun(ctx context.Context) (time.Time, bool)
	PlanRestore(ctx context.Context, req backup.StackRestoreRequest) (backup.StackRestorePlan, error)
	Restore(ctx context.Context, req backup.StackRestoreRequest, user string) (stacks.Operation, error)
	ArchiveManifest(ctx context.Context, dest, name string) (backup.Manifest, error)
}

var _ BackupAPI = (*backup.Service)(nil)

const (
	codeBackupRunning = "backup_running"
	defaultRunsLimit  = 20
	maxRunsLimit      = 100
	manualTrigger     = "manual"
)

func (s *server) registerBackupRoutes(mux *http.ServeMux) {
	route := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.requireSession(h)) }
	route("GET /api/backups/config", s.handleBackupConfig)
	route("PUT /api/backups/config", s.handleBackupSaveConfig)
	route("DELETE /api/backups/config/s3-secret", s.handleBackupClearSecret)
	route("POST /api/backups/s3/test", s.handleBackupTestS3)
	route("GET /api/backups/stacks", s.handleBackupStacks)
	route("PUT /api/backups/stacks/{name}", s.handleBackupSetStack)
	route("POST /api/backups/run", s.handleBackupRun)
	route("GET /api/backups/runs", s.handleBackupRuns)
	route("GET /api/backups/archives", s.handleBackupArchives)
	s.registerBackupRestoreRoutes(mux)
}

func (s *server) backupConfigDTO(ctx context.Context, cfg backup.Config) apitypes.BackupConfig {
	dto := apitypes.BackupConfig{
		Schedule: apitypes.BackupSchedule{
			Enabled: cfg.Schedule.Enabled, Frequency: cfg.Schedule.Frequency,
			Weekday: int(cfg.Schedule.Weekday), Time: cfg.Schedule.Time,
		},
		Local: apitypes.BackupLocal{Enabled: cfg.Local.Enabled, Path: cfg.Local.Path, KeepLast: cfg.Local.KeepLast},
		S3: apitypes.BackupS3{
			Enabled: cfg.S3.Enabled, Bucket: cfg.S3.Bucket, Prefix: cfg.S3.Prefix, Region: cfg.S3.Region,
			Endpoint: cfg.S3.Endpoint, PathStyle: cfg.S3.PathStyle, AccessKeyID: cfg.S3.AccessKeyID,
			SecretKeySet: cfg.S3.SecretKey != "",
		},
	}
	if next, ok := s.opts.Backups.NextRun(ctx); ok {
		dto.NextRun = &next
	}
	return dto
}

func backupConfigFromDTO(dto apitypes.BackupConfig) backup.Config {
	return backup.Config{
		Schedule: backup.Schedule{
			Enabled: dto.Schedule.Enabled, Frequency: dto.Schedule.Frequency,
			Weekday: time.Weekday(dto.Schedule.Weekday), Time: dto.Schedule.Time,
		},
		Local: backup.LocalDest{Enabled: dto.Local.Enabled, Path: dto.Local.Path, KeepLast: dto.Local.KeepLast},
		S3:    backupS3FromDTO(dto.S3),
	}
}

func backupS3FromDTO(dto apitypes.BackupS3) backup.S3Dest {
	return backup.S3Dest{
		Enabled: dto.Enabled, Bucket: dto.Bucket, Prefix: dto.Prefix, Region: dto.Region,
		Endpoint: dto.Endpoint, PathStyle: dto.PathStyle, AccessKeyID: dto.AccessKeyID,
		SecretKey: secret.Secret(dto.SecretKey),
	}
}

func (s *server) handleBackupConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.opts.Backups.Config(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.backupConfigDTO(r.Context(), cfg))
}

func (s *server) handleBackupSaveConfig(w http.ResponseWriter, r *http.Request) {
	var req apitypes.BackupConfig
	if !decodeJSON(w, r, &req) {
		return
	}
	cfg := backupConfigFromDTO(req)
	if err := cfg.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.opts.Backups.SaveConfig(r.Context(), cfg); err != nil {
		s.internalError(w, r, err)
		return
	}
	saved, err := s.opts.Backups.Config(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.backupConfigDTO(r.Context(), saved))
}

func (s *server) handleBackupClearSecret(w http.ResponseWriter, r *http.Request) {
	if err := s.opts.Backups.ClearS3Secret(r.Context()); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleBackupTestS3(w http.ResponseWriter, r *http.Request) {
	var req apitypes.BackupS3
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Bucket == "" {
		writeError(w, http.StatusBadRequest, "s3 bucket is required")
		return
	}
	if err := s.opts.Backups.TestS3(r.Context(), backupS3FromDTO(req)); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleBackupStacks(w http.ResponseWriter, r *http.Request) {
	views, err := s.opts.Backups.Selections(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]apitypes.BackupStack, 0, len(views))
	for _, v := range views {
		row := apitypes.BackupStack{
			Stack: v.Stack, Volumes: v.Volumes, Images: v.Images, StopDuring: v.StopDuring,
			Mounts: make([]apitypes.BackupMount, 0, len(v.Mounts)),
		}
		for _, m := range v.Mounts {
			row.Mounts = append(row.Mounts, apitypes.BackupMount{Kind: m.Kind, Name: m.Name, Target: m.Target, Bytes: m.Bytes})
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) handleBackupSetStack(w http.ResponseWriter, r *http.Request) {
	var req apitypes.BackupStackSettings
	if !decodeJSON(w, r, &req) {
		return
	}
	sel := backup.StackSelection{Stack: r.PathValue("name"), Volumes: req.Volumes, Images: req.Images, StopDuring: req.StopDuring}
	if err := s.opts.Backups.SetSelection(r.Context(), sel); err != nil {
		if errors.Is(err, backup.ErrUnknownStack) {
			writeError(w, http.StatusNotFound, "stack not found")
			return
		}
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleBackupRun(w http.ResponseWriter, r *http.Request) {
	var empty struct{}
	if !decodeJSON(w, r, &empty) {
		return
	}
	op, err := s.opts.Backups.Start(r.Context(), manualTrigger, userFrom(r.Context()).Username)
	switch {
	case errors.Is(err, stacks.ErrBusy):
		writeCodedError(w, http.StatusConflict, "a backup is already running", codeBackupRunning)
	case err != nil:
		s.internalError(w, r, err)
	default:
		writeJSON(w, http.StatusAccepted, apitypes.BackupRunStarted{Operation: apitypes.Operation(toOperation(op))})
	}
}

func (s *server) handleBackupRuns(w http.ResponseWriter, r *http.Request) {
	limit := defaultRunsLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxRunsLimit {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and "+strconv.Itoa(maxRunsLimit))
			return
		}
		limit = n
	}
	runs, err := s.opts.Backups.Runs(r.Context(), limit)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]apitypes.BackupRun, 0, len(runs))
	for _, run := range runs {
		out = append(out, toBackupRun(run))
	}
	writeJSON(w, http.StatusOK, out)
}

func toBackupRun(run backup.Run) apitypes.BackupRun {
	dto := apitypes.BackupRun{
		ID: run.ID, StartedAt: run.StartedAt, Trigger: run.Trigger, Status: run.Status,
		Archive: run.Archive, Size: run.Size, Error: run.Error,
		Destinations: append([]string{}, run.Destinations...),
		Items:        make([]apitypes.BackupItem, 0, len(run.Items)),
	}
	if !run.EndedAt.IsZero() {
		ended := run.EndedAt
		dto.EndedAt = &ended
	}
	for _, it := range run.Items {
		dto.Items = append(dto.Items, apitypes.BackupItem(it))
	}
	return dto
}

func (s *server) handleBackupArchives(w http.ResponseWriter, r *http.Request) {
	refs, err := s.opts.Backups.Archives(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]apitypes.BackupArchive, 0, len(refs))
	for _, ref := range refs {
		out = append(out, apitypes.BackupArchive(ref))
	}
	writeJSON(w, http.StatusOK, out)
}
