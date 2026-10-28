package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/backup"
	"github.com/rcss-labs/stackstead/internal/secret"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

// HostRestoreAPI is the whole-host restore engine; backup.HostRestorer
// satisfies it.
type HostRestoreAPI interface {
	// UploadPath is where an uploaded archive is stored.
	UploadPath() string
	Inspect(ctx context.Context, src backup.HostSource) (backup.HostSummary, error)
	// Restore stages the restore. It calls accepted once the archive and the
	// confirmed conflicts check out, before the host changes.
	Restore(ctx context.Context, src backup.HostSource, opts backup.HostRestoreOptions, out func(string), accepted func()) (backup.HostReport, error)
}

// RestoreOperations runs the restore as an operation; *stacks.Ops satisfies it.
type RestoreOperations interface {
	operationSubscriber
	StartTimeout(key, action string, timeout time.Duration, fn stacks.OpFunc) (stacks.Operation, error)
	Get(id string) (stacks.Operation, error)
	Wait(id string)
	Cancel(id string) error
}

var (
	_ HostRestoreAPI    = backup.HostRestorer{}
	_ RestoreOperations = (*stacks.Ops)(nil)
)

const (
	// restoreOpKey and restoreOpAction name the restore in the operations list.
	restoreOpKey    = "host:restore"
	restoreOpAction = "restore"

	// uploadPartialSuffix names an upload while it is written; the restore
	// engine leaves that file alone when it clears its staging folder.
	uploadPartialSuffix = ".partial"

	// setupTokenHeader carries the setup token where the body cannot: the
	// upload body is the archive itself.
	setupTokenHeader = "X-Stackstead-Setup-Token"

	codeRestoreRunning   = "restore_running"
	codeUploadRunning    = "upload_running"
	codeRestorePending   = "restore_pending"
	codeConflictsChanged = "conflicts_changed"
	codeArchiveNewer     = "archive_newer"
	codeArchiveDamaged   = "archive_damaged"

	// Refusals that are about the archive's protection, not its structure.
	codeArchiveTampered    = "archive_tampered"
	codeWrongPassphrase    = "wrong_passphrase"
	codePassphraseNeeded   = "passphrase_required"
	codeUnencryptedArchive = "unencrypted_archive"
)

// protectionCode maps the typed refusals about an archive's authenticity to
// their error code. They are checked before ErrArchiveDamaged, which wraps them.
func protectionCodeOf(err error) string {
	code, _ := protectionCode(err)
	return code
}

func protectionCode(err error) (string, bool) {
	switch {
	case errors.Is(err, backup.ErrArchiveTampered):
		return codeArchiveTampered, true
	case errors.Is(err, backup.ErrWrongPassphrase):
		return codeWrongPassphrase, true
	case errors.Is(err, backup.ErrPassphraseRequired):
		return codePassphraseNeeded, true
	case errors.Is(err, backup.ErrUnencryptedArchive):
		return codeUnencryptedArchive, true
	}
	return "", false
}

// restoreState is the single whole-host restore: the operation running it
// and the token that authorises its progress stream.
type restoreState struct {
	mu        sync.Mutex
	token     string
	opID      string
	uploading bool
}

func (s *server) registerSetupRestoreRoutes(mux *http.ServeMux) {
	// No requireSession: there is no account yet. Each handler checks the
	// setup gate, and progress checks the restore token.
	mux.HandleFunc("POST /api/setup/restore/upload", s.handleRestoreUpload)
	mux.HandleFunc("POST /api/setup/restore/inspect", s.handleRestoreInspect)
	mux.HandleFunc("POST /api/setup/restore/start", s.handleRestoreStart)
	mux.HandleFunc("GET /api/setup/restore/progress", s.handleRestoreProgress)
}

// setupGate answers 403 outside the setup gate and 409 once an admin exists.
// It reports whether the request may go on.
func (s *server) setupGate(w http.ResponseWriter, r *http.Request, bodyToken string) bool {
	token := bodyToken
	if token == "" {
		token = r.Header.Get(setupTokenHeader)
	}
	if !s.setupAllowed(r, token) {
		writeError(w, http.StatusForbidden, "setup is only allowed from this machine or with the setup token from the service log")
		return false
	}
	has, err := s.opts.Auth.HasAdmin(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return false
	}
	if has {
		writeError(w, http.StatusConflict, "setup is already complete")
		return false
	}
	return true
}

// restoreActiveLocked reports whether a restore is running or staged and
// waiting for the restart. Callers hold s.restore.mu.
func (s *server) restoreActiveLocked() bool {
	if s.restore.opID == "" {
		return false
	}
	op, err := s.opts.RestoreOps.Get(s.restore.opID)
	return err == nil && (op.State == stacks.OpRunning || op.State == stacks.OpSucceeded)
}

// beginUpload claims the single upload slot, refusing while another upload
// or a restore is under way. The caller must call endUpload.
func (s *server) beginUpload(w http.ResponseWriter) bool {
	s.restore.mu.Lock()
	defer s.restore.mu.Unlock()
	switch {
	case s.restore.uploading:
		writeCodedError(w, http.StatusConflict, "another upload is in progress", codeUploadRunning)
		return false
	case s.restoreActiveLocked():
		writeCodedError(w, http.StatusConflict, "a restore is in progress", codeRestoreRunning)
		return false
	}
	s.restore.uploading = true
	return true
}

func (s *server) endUpload() {
	s.restore.mu.Lock()
	s.restore.uploading = false
	s.restore.mu.Unlock()
}

// uploadMediaTypes are the accepted archive types. None is a CORS "simple"
// type, so another site cannot make a browser on this machine post one.
var uploadMediaTypes = map[string]bool{"application/gzip": true, "application/x-gzip": true, "application/octet-stream": true}

// handleRestoreUpload streams the body to the upload path, replacing an
// earlier upload. There is no size cap and the body is never held in memory.
func (s *server) handleRestoreUpload(w http.ResponseWriter, r *http.Request) {
	if !s.setupGate(w, r, "") {
		return
	}
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); !uploadMediaTypes[mt] {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/gzip or application/octet-stream")
		return
	}
	if !s.beginUpload(w) {
		return
	}
	defer s.endUpload()
	// The server's read timeout suits API calls, not an archive of many gigabytes.
	if err := http.NewResponseController(w).SetReadDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		s.internalError(w, r, err)
		return
	}
	// Written under a temporary name and renamed into place, so the upload
	// path only ever holds a complete archive.
	path := s.opts.HostRestore.UploadPath()
	partial := path + uploadPartialSuffix
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		s.internalError(w, r, err)
		return
	}
	// Only a crashed earlier upload can have left this behind: the slot is ours.
	if err := os.Remove(partial); err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.internalError(w, r, err)
		return
	}
	f, err := os.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	_, copyErr := io.Copy(f, r.Body)
	if closeErr := f.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		_ = os.Remove(partial)
		writeError(w, http.StatusBadRequest, "upload failed: "+copyErr.Error())
		return
	}
	// Setup may have been completed the normal way while this was uploading.
	if has, err := s.opts.Auth.HasAdmin(r.Context()); err != nil || has {
		_ = os.Remove(partial)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		writeError(w, http.StatusConflict, "setup is already complete")
		return
	}
	if err := os.Rename(partial, path); err != nil {
		_ = os.Remove(partial)
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, apitypes.HostRestoreUploaded{Source: apitypes.HostRestoreSource{Kind: backup.SourceUpload}})
}

// hostSource turns the wire source into the engine's. An upload always means
// this server's upload file, whatever path the client sends.
func (s *server) hostSource(src apitypes.HostRestoreSource, passphrase string) (backup.HostSource, string) {
	hs, msg := s.hostSourceOf(src)
	hs.Passphrase = secret.Secret(passphrase)
	return hs, msg
}

func (s *server) hostSourceOf(src apitypes.HostRestoreSource) (backup.HostSource, string) {
	switch src.Kind {
	case backup.SourceUpload:
		return backup.HostSource{Kind: backup.SourceUpload, Path: s.opts.HostRestore.UploadPath()}, ""
	case backup.SourcePath:
		if !filepath.IsAbs(src.Path) {
			return backup.HostSource{}, "source path must be absolute"
		}
		return backup.HostSource{Kind: backup.SourcePath, Path: filepath.Clean(src.Path)}, ""
	case backup.SourceS3:
		if src.S3 == nil || src.S3.Bucket == "" || src.S3.Key == "" {
			return backup.HostSource{}, "an s3 source needs a bucket and a key"
		}
		return backup.HostSource{Kind: backup.SourceS3, Key: src.S3.Key, S3: backup.S3Dest{
			Enabled: true, Bucket: src.S3.Bucket, Region: src.S3.Region, Endpoint: src.S3.Endpoint,
			PathStyle: src.S3.PathStyle, AccessKeyID: src.S3.AccessKeyID, SecretKey: secret.Secret(src.S3.SecretKey),
		}}, ""
	default:
		return backup.HostSource{}, "source kind must be s3, path or upload"
	}
}

// writeRestoreError answers an inspection or a restore that was refused
// before it changed anything.
func writeRestoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, backup.ErrConflictsChanged):
		writeCodedError(w, http.StatusConflict, err.Error(), codeConflictsChanged)
	case errors.Is(err, backup.ErrRestorePending):
		writeCodedError(w, http.StatusConflict, err.Error(), codeRestorePending)
	case errors.Is(err, backup.ErrArchiveNewer):
		writeCodedError(w, http.StatusUnprocessableEntity, err.Error(), codeArchiveNewer)
	case protectionCodeOf(err) != "":
		writeCodedError(w, http.StatusUnprocessableEntity, err.Error(), protectionCodeOf(err))
	case errors.Is(err, backup.ErrArchiveDamaged), errors.Is(err, backup.ErrManifestMismatch), errors.Is(err, backup.ErrUnsafePath):
		writeCodedError(w, http.StatusUnprocessableEntity, err.Error(), codeArchiveDamaged)
	default:
		// Mostly an unreadable source: a wrong path, bucket or key.
		writeError(w, http.StatusBadRequest, err.Error())
	}
}

func (s *server) handleRestoreInspect(w http.ResponseWriter, r *http.Request) {
	var req apitypes.HostRestoreInspectRequest
	if !decodeJSON(w, r, &req) || !s.setupGate(w, r, req.SetupToken) {
		return
	}
	src, msg := s.hostSource(req.Source, req.Passphrase)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	sum, err := s.opts.HostRestore.Inspect(r.Context(), src)
	if err != nil {
		writeRestoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toHostRestoreSummary(sum))
}

// handleRestoreStart runs the restore as an operation. It answers once the
// restore is accepted (202) or refused before changing anything, so a stale
// confirmation still gets its 409. A staged restore restarts the server.
func (s *server) handleRestoreStart(w http.ResponseWriter, r *http.Request) {
	var req apitypes.HostRestoreStartRequest
	if !decodeJSON(w, r, &req) || !s.setupGate(w, r, req.SetupToken) {
		return
	}
	src, msg := s.hostSource(req.Source, req.Passphrase)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	accepted := make(chan struct{})
	var acceptOnce sync.Once
	early := make(chan error, 1)
	run := func(ctx context.Context, out func(string)) error {
		_, err := s.opts.HostRestore.Restore(ctx, src, backup.HostRestoreOptions{
			StartStacks: req.StartStacks, AllowUnencrypted: req.AllowUnencrypted, ConfirmedDigest: req.ConfirmedDigest,
		}, out,
			func() { acceptOnce.Do(func() { close(accepted) }) })
		if err == nil && src.Kind == backup.SourceUpload {
			// The archive is staged; the upload is no longer needed.
			if rmErr := os.Remove(src.Path); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
				s.opts.Logger.Warn("remove restore upload", "err", rmErr)
			}
		}
		early <- err
		return err
	}

	s.restore.mu.Lock()
	if src.Kind == backup.SourceUpload && s.restore.uploading {
		s.restore.mu.Unlock()
		writeCodedError(w, http.StatusConflict, "the archive is still being uploaded", codeUploadRunning)
		return
	}
	if s.restoreActiveLocked() {
		s.restore.mu.Unlock()
		writeCodedError(w, http.StatusConflict, "a restore is already in progress", codeRestoreRunning)
		return
	}
	// A restore may take hours; it has no operation timeout.
	op, err := s.opts.RestoreOps.StartTimeout(restoreOpKey, restoreOpAction, 0, run)
	if err != nil {
		s.restore.mu.Unlock()
		if errors.Is(err, stacks.ErrBusy) {
			writeCodedError(w, http.StatusConflict, "a restore is already in progress", codeRestoreRunning)
			return
		}
		s.internalError(w, r, err)
		return
	}
	token := rand.Text()
	s.restore.token, s.restore.opID = token, op.ID
	s.restore.mu.Unlock()
	go s.restartAfterRestore(op.ID)

	switch outcome, err := awaitAcceptance(r.Context().Done(), accepted, early); outcome {
	case restoreRefused:
		writeRestoreError(w, err)
		return
	case restoreClientGone:
		// Nobody would get the restore token: do not restore unseen.
		_ = s.opts.RestoreOps.Cancel(op.ID)
		return
	}
	writeJSON(w, http.StatusAccepted, apitypes.HostRestoreStarted{RestoreToken: token, Operation: apitypes.Operation(toOperation(op))})
}

// acceptance is how waiting for a restore to be accepted ended.
type acceptance int

const (
	restoreAccepted acceptance = iota
	restoreRefused
	restoreClientGone
)

// awaitAcceptance waits until the restore is accepted, refused before it
// changed anything (the error says why), or the client has gone.
func awaitAcceptance(clientGone, accepted <-chan struct{}, early <-chan error) (acceptance, error) {
	select {
	case <-accepted:
	case err := <-early:
		// Accepted and then failed: the operation reports it, not this answer.
		if err != nil && !isClosed(accepted) {
			return restoreRefused, err
		}
	case <-clientGone:
		// Both may be ready at once; an accepted restore is already changing
		// the host and must not be cancelled.
		if !isClosed(accepted) {
			return restoreClientGone, nil
		}
	}
	return restoreAccepted, nil
}

func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// restartAfterRestore restarts the server once the restore operation has
// staged everything, so the new process installs the restored database.
func (s *server) restartAfterRestore(opID string) {
	s.opts.RestoreOps.Wait(opID)
	op, err := s.opts.RestoreOps.Get(opID)
	if err != nil || op.State != stacks.OpSucceeded {
		return
	}
	if s.opts.Restart == nil {
		s.opts.Logger.Warn("host restore staged; restart Stackstead to finish it")
		return
	}
	s.opts.Restart()
}

// handleRestoreProgress streams the restore operation. The restore token,
// not the setup gate, authorises it: the gate closes as the restore installs
// the old admin, and the browser must keep following the progress.
func (s *server) handleRestoreProgress(w http.ResponseWriter, r *http.Request) {
	given := r.Header.Get("X-Restore-Token") // not the URL: proxies log query strings
	s.restore.mu.Lock()
	token, opID := s.restore.token, s.restore.opID
	s.restore.mu.Unlock()
	if token == "" || subtle.ConstantTimeCompare([]byte(given), []byte(token)) != 1 {
		writeError(w, http.StatusForbidden, "a valid restore token is required")
		return
	}
	s.streamOperation(w, r, s.opts.RestoreOps, opID)
}

func toHostRestoreSummary(sum backup.HostSummary) apitypes.HostRestoreSummary {
	out := apitypes.HostRestoreSummary{
		ConflictsDigest: sum.ConflictsDigest, Encrypted: sum.Encrypted, KeyID: sum.KeyID,
		Conflicts: make([]apitypes.HostRestoreConflict, 0, len(sum.Conflicts)),
		Manifest:  toBackupManifest(sum.Manifest),
	}
	for _, c := range sum.Conflicts {
		out.Conflicts = append(out.Conflicts, apitypes.HostRestoreConflict(c))
	}
	return out
}

// removeRestoreUpload drops an uploaded archive nobody will restore: setup
// was completed the normal way.
// An upload still being written is left to its handler, which discards it
// once it sees the admin.
func (s *server) removeRestoreUpload() {
	if s.opts.HostRestore == nil {
		return
	}
	s.restore.mu.Lock()
	defer s.restore.mu.Unlock()
	if s.restore.uploading {
		return
	}
	path := s.opts.HostRestore.UploadPath()
	for _, p := range []string{path, path + uploadPartialSuffix} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			s.opts.Logger.Warn("remove unused restore upload", "err", err)
		}
	}
}
