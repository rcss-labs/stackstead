package api

import (
	"errors"
	"io/fs"
	"net/http"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/backup"
	"github.com/rcss-labs/stackstead/internal/secret"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

const (
	codeStackBusy      = "stack_busy"
	codeTargetOccupied = "target_occupied"
	codeProjectTaken   = "project_taken"
	codeFilesRequired  = "files_required"
	codeNothingToDo    = "nothing_to_restore"
	codeBadArchiveRef  = "bad_archive_ref"
)

func (s *server) registerBackupRestoreRoutes(mux *http.ServeMux) {
	route := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.requireSession(h)) }
	route("POST /api/backups/restore/plan", s.handleBackupRestorePlan)
	route("POST /api/backups/restore", s.handleBackupRestore)
	route("GET /api/backups/archives/{dest}/{name}/manifest", s.handleBackupArchiveManifest)
}

// validArchiveRef accepts only a known destination and an archive name
// Stackstead writes, with no path in it.
func validArchiveRef(dest, name string) bool {
	return (dest == "local" || dest == "s3") && backup.ValidArchiveName(name)
}

// decodeRestoreRequest reads and checks a restore body; it answers 400 itself.
func decodeRestoreRequest(w http.ResponseWriter, r *http.Request) (backup.StackRestoreRequest, bool) {
	var dto apitypes.BackupRestoreRequest
	if !decodeJSON(w, r, &dto) {
		return backup.StackRestoreRequest{}, false
	}
	if !validArchiveRef(dto.Archive.Dest, dto.Archive.Name) {
		writeCodedError(w, http.StatusBadRequest, "archive.dest must be local or s3 and archive.name an archive file name", codeBadArchiveRef)
		return backup.StackRestoreRequest{}, false
	}
	if dto.Stack == "" {
		writeError(w, http.StatusBadRequest, "stack is required")
		return backup.StackRestoreRequest{}, false
	}
	return backup.StackRestoreRequest{
		Archive: backup.ArchiveRef{Dest: dto.Archive.Dest, Name: dto.Archive.Name},
		Stack:   dto.Stack, NewName: dto.NewName,
		Files: dto.Files, Volumes: dto.Volumes, Images: dto.Images,
		VolumeKeys: dto.VolumeKeys,
		Passphrase: secret.Secret(dto.Passphrase), AllowUnencrypted: dto.AllowUnencrypted,
	}, true
}

// writeRestoreError maps restore errors to statuses; the rest go through the
// stack error mapping.
func (s *server) writeRestoreError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, backup.ErrArchiveNewer):
		writeCodedError(w, http.StatusBadRequest, err.Error(), codeArchiveNewer)
	case protectionCodeOf(err) != "":
		writeCodedError(w, http.StatusBadRequest, err.Error(), protectionCodeOf(err))
	case errors.Is(err, stacks.ErrBusy):
		writeCodedError(w, http.StatusConflict, "the stack is busy with another operation", codeStackBusy)
	case errors.Is(err, backup.ErrTargetOccupied):
		writeCodedError(w, http.StatusConflict, err.Error(), codeTargetOccupied)
	case errors.Is(err, stacks.ErrProjectTaken):
		writeCodedError(w, http.StatusConflict, err.Error(), codeProjectTaken)
	case errors.Is(err, backup.ErrBadArchiveRef):
		writeCodedError(w, http.StatusBadRequest, err.Error(), codeBadArchiveRef)
	case errors.Is(err, backup.ErrNothingToRestore):
		writeCodedError(w, http.StatusBadRequest, err.Error(), codeNothingToDo)
	case errors.Is(err, backup.ErrFilesRequired):
		writeCodedError(w, http.StatusBadRequest, err.Error(), codeFilesRequired)
	case errors.Is(err, backup.ErrArchiveDamaged), errors.Is(err, backup.ErrManifestMismatch):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, backup.ErrStackNotInArchive):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, fs.ErrNotExist):
		writeError(w, http.StatusNotFound, "archive not found")
	default:
		s.writeStackError(w, r, err)
	}
}

func (s *server) handleBackupRestorePlan(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeRestoreRequest(w, r)
	if !ok {
		return
	}
	plan, err := s.opts.Backups.PlanRestore(r.Context(), req)
	if err != nil {
		s.writeRestoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toRestorePlan(plan))
}

func (s *server) handleBackupRestore(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeRestoreRequest(w, r)
	if !ok {
		return
	}
	op, err := s.opts.Backups.Restore(r.Context(), req, userFrom(r.Context()).Username)
	if err != nil {
		s.writeRestoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, apitypes.BackupRestoreStarted{Operation: apitypes.Operation(toOperation(op))})
}

func (s *server) handleBackupArchiveManifest(w http.ResponseWriter, r *http.Request) {
	dest, name := r.PathValue("dest"), r.PathValue("name")
	if !validArchiveRef(dest, name) {
		writeCodedError(w, http.StatusBadRequest, "dest must be local or s3 and name an archive file name", codeBadArchiveRef)
		return
	}
	m, err := s.opts.Backups.ArchiveManifest(r.Context(), dest, name)
	if err != nil {
		s.writeRestoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toBackupManifest(m))
}

func toRestorePlan(p backup.StackRestorePlan) apitypes.BackupRestorePlan {
	dto := apitypes.BackupRestorePlan{
		Stack: p.Stack, TargetName: p.TargetName, TargetDir: p.TargetDir, ComposePath: p.ComposePath,
		Project: p.Project, Origin: string(p.Origin), Overwrites: p.Overwrites, CrossHost: p.CrossHost,
		FileDiffs:       make([]apitypes.BackupFileDiff, 0, len(p.FileDiffs)),
		Volumes:         make([]apitypes.BackupPlannedVolume, 0, len(p.Volumes)),
		Images:          append([]string{}, p.Images...),
		MissingNetworks: make([]apitypes.BackupRestoreNetwork, 0, len(p.MissingNetworks)),
		Warnings:        append([]string{}, p.Warnings...),
		Encrypted:       p.Encrypted, KeyID: p.KeyID,
	}
	for _, d := range p.FileDiffs {
		dto.FileDiffs = append(dto.FileDiffs, apitypes.BackupFileDiff(d))
	}
	for _, v := range p.Volumes {
		dto.Volumes = append(dto.Volumes, apitypes.BackupPlannedVolume(v))
	}
	for _, n := range p.MissingNetworks {
		dto.MissingNetworks = append(dto.MissingNetworks, apitypes.BackupRestoreNetwork{Name: n.Name, Driver: n.Driver})
	}
	return dto
}

func toBackupManifest(m backup.Manifest) apitypes.BackupManifest {
	dto := apitypes.BackupManifest{
		Format: m.Format, StacksteadVersion: m.StacksteadVersion, Host: m.Host, HostSlug: m.HostSlug,
		InstanceID: m.InstanceID, CreatedAt: m.CreatedAt, Status: m.Status, Networks: m.Networks,
		Stacks: make([]apitypes.BackupManifestStack, 0, len(m.Stacks)),
		Items:  make([]apitypes.BackupItem, 0, len(m.Items)),
	}
	if m.Encryption != nil {
		dto.Encrypted, dto.KeyID = true, m.Encryption.KeyID
	}
	for _, st := range m.Stacks {
		row := apitypes.BackupManifestStack{
			Name: st.Name, ComposePath: st.ComposePath, Origin: st.Origin, Project: st.Project,
			WasRunning: st.WasRunning, LiveCopy: st.LiveCopy,
			Networks: append([]string{}, st.Networks...),
			Volumes:  make([]apitypes.BackupManifestVolume, 0, len(st.Volumes)),
			Images:   append([]string{}, st.Images...),
		}
		for _, v := range st.Volumes {
			row.Volumes = append(row.Volumes, apitypes.BackupManifestVolume(v))
		}
		dto.Stacks = append(dto.Stacks, row)
	}
	for _, it := range m.Items {
		dto.Items = append(dto.Items, apitypes.BackupItem(it))
	}
	return dto
}
