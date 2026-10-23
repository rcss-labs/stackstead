package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/imageref"
	"github.com/rcss-labs/stackstead/internal/images"
	"github.com/rcss-labs/stackstead/internal/peers"
	"github.com/rcss-labs/stackstead/internal/registry"
	"github.com/rcss-labs/stackstead/internal/secret"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

// ImagesAPI is the image library the HTTP layer needs; *images.Service
// satisfies it directly.
type ImagesAPI interface {
	Create(ctx context.Context, user string, req images.CreateRequest) (images.Image, error)
	Get(ctx context.Context, slug string) (images.Image, error)
	Update(ctx context.Context, user, slug string, req images.UpdateRequest) (images.Image, error)
	Delete(ctx context.Context, user, slug string) error
	Library(ctx context.Context) (images.Library, error)
	Detail(ctx context.Context, slug string) (images.Detail, error)
	Files(ctx context.Context, slug string) (images.Manifest, int, error)
	ReadFile(ctx context.Context, slug, path string) ([]byte, error)
	Dockerfile(ctx context.Context, slug string) ([]byte, int, error)
	RevisionDockerfile(ctx context.Context, slug string, n int) ([]byte, error)
	SaveDockerfile(ctx context.Context, user, slug string, content []byte, revision int) (images.Revision, error)
	Upload(ctx context.Context, user, slug string, files []images.UploadFile) (images.Revision, error)
	Rename(ctx context.Context, user, slug, from, to string) (images.Revision, error)
	DeleteFile(ctx context.Context, user, slug, path string) (images.Revision, error)
	Revisions(ctx context.Context, slug string) ([]images.Revision, error)
	Restore(ctx context.Context, user, slug string, n int) (images.Revision, error)
	Build(ctx context.Context, user, slug string, req images.BuildRequest) (images.Version, stacks.Operation, error)
	VersionDetail(ctx context.Context, slug, versionID string) (images.VersionDetail, error)
	DeleteVersion(ctx context.Context, user, slug, versionID string) error
	AddPushTarget(ctx context.Context, user, slug string, req images.PushTargetRequest) (images.PushTarget, error)
	UpdatePushTarget(ctx context.Context, user, slug, id string, req images.PushTargetRequest) (images.PushTarget, error)
	DeletePushTarget(ctx context.Context, user, slug, id string) error
	Push(ctx context.Context, user, slug, tag, targetID string) (stacks.Operation, error)
	Fetch(ctx context.Context, user string, ref imageref.Ref) (stacks.Operation, bool, error)
	Resolve(ctx context.Context, refs []string) (map[string]images.ResolvedRef, error)
	OwnImages(ctx context.Context) ([]images.LibraryEntry, error)
	OwnUsage(ctx context.Context) ([]imageref.Usage, error)
	OpenVersionTar(ctx context.Context, slug, tag string) (io.ReadCloser, string, error)
	Limits() images.Limits
	LocalServer(ctx context.Context) (images.ServerInfo, error)
	NewUploadSpool(ctx context.Context, slug string) (string, error)
	GitHubStatus(ctx context.Context) (images.GitHubStatus, error)
	DefaultRetention(ctx context.Context) (images.Retention, error)
	SetDefaultRetention(ctx context.Context, r images.Retention) error
	SetGitHubToken(ctx context.Context, user string, token secret.Secret) error
	TestGitHub(ctx context.Context) (string, error)
	GitHubHead(ctx context.Context, slug string) (images.GitHubHead, error)
}

var _ ImagesAPI = (*images.Service)(nil)

// Error codes of the image routes.
const (
	codeImageInUse         = "image_in_use"
	codeBuildRunning       = "build_running"
	codeTagTaken           = "tag_taken"
	codeRevisionConflict   = "revision_conflict"
	codeImageNotBuilt      = "image_not_built"
	codeArchMismatch       = "arch_mismatch"
	codeImageTooLarge      = "image_too_large"
	codeGitHubUnconfigured = "github_unconfigured"
	codeGitHubUnauthorized = "github_unauthorized"
	codeGitHubRateLimited  = "github_rate_limited"
	codeGitHubUnreachable  = "github_unreachable"
	maxUploadFiles         = 10000
	uploadEnvelopeBytes    = maxUploadFiles * (4096 + 512) // worst-case multipart framing: a longest path (images.maxPathBytes) plus boundary and headers per file
	dockerfileBodyMaxJSON  = dockerfileBodyBytes
)

// imageInUseBody is the 409 body of a delete refused because the image is running.
type imageInUseBody struct {
	Error  string                `json:"error"`
	Code   string                `json:"code"`
	UsedBy []apitypes.LibraryUse `json:"usedBy"`
	// Unreachable names the servers that could not be asked whether the image is in use.
	Unreachable []apitypes.LibraryServer `json:"unreachable"`
}

func (s *server) registerImageRoutes(mux *http.ServeMux) {
	route := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.requireSession(h)) }
	route("GET /api/images", s.handleListImages)
	route("POST /api/images", s.handleCreateImage)
	route("GET /api/images/{slug}", s.handleGetImage)
	route("PUT /api/images/{slug}", s.handleUpdateImage)
	route("DELETE /api/images/{slug}", s.handleDeleteImage)
	route("GET /api/images/{slug}/files", s.handleImageFiles)
	route("GET /api/images/{slug}/files/{path...}", s.handleImageFile)
	route("POST /api/images/{slug}/files", s.handleUploadImageFiles)
	route("POST /api/images/{slug}/files/rename", s.handleRenameImageFile)
	route("DELETE /api/images/{slug}/files/{path...}", s.handleDeleteImageFile)
	route("GET /api/images/{slug}/dockerfile", s.handleGetImageDockerfile)
	route("PUT /api/images/{slug}/dockerfile", s.handlePutImageDockerfile)
	route("GET /api/images/{slug}/revisions", s.handleImageRevisions)
	route("GET /api/images/{slug}/revisions/{n}", s.handleImageRevision)
	route("POST /api/images/{slug}/revisions/{n}/restore", s.handleRestoreImageRevision)
	route("POST /api/images/{slug}/builds", s.handleBuildImage)
	route("GET /api/images/{slug}/versions/{id}", s.handleGetImageVersion)
	route("DELETE /api/images/{slug}/versions/{id}", s.handleDeleteImageVersion)
	route("POST /api/images/{slug}/push-targets", s.handleAddPushTarget)
	route("PUT /api/images/{slug}/push-targets/{id}", s.handleUpdatePushTarget)
	route("DELETE /api/images/{slug}/push-targets/{id}", s.handleDeletePushTarget)
	route("POST /api/images/{slug}/versions/{tag}/push", s.handlePushImageVersion)
	route("POST /api/images/fetch", s.handleFetchImage)
	route("GET /api/images/{slug}/github/head", s.handleGitHubHead)
	route("GET /api/settings/images", s.handleImageDefaults)
	route("PUT /api/settings/images", s.handleSetImageDefaults)
	route("GET /api/settings/github", s.handleGitHubSettings)
	route("PUT /api/settings/github", s.handleSetGitHubToken)
	route("POST /api/settings/github/test", s.handleTestGitHub)
}

// writeImagesError maps image-library errors to HTTP answers. Unknown errors
// are logged and answered with a generic 500. Request bodies are never echoed.
func (s *server) writeImagesError(w http.ResponseWriter, r *http.Request, err error) {
	var (
		validation *stacks.ValidationError
		inUse      *images.InUseError
		tooBig     *http.MaxBytesError
		badForm    *uploadFormatError
		peerErr    *peers.PeerError
		authErr    *registry.AuthError
	)
	switch {
	case errors.As(err, &validation):
		fields := make([]apitypes.FieldError, 0, len(validation.Fields))
		for _, f := range validation.Fields {
			fields = append(fields, apitypes.FieldError(f))
		}
		writeJSON(w, http.StatusBadRequest, fieldErrorBody{Error: validation.Error(), Fields: fields})
	case errors.As(err, &inUse):
		writeJSON(w, http.StatusConflict, imageInUseBody{Error: inUse.Error(), Code: codeImageInUse, UsedBy: toLibraryUses(inUse.UsedBy), Unreachable: toLibraryServers(inUse.Unreachable)})
	case errors.Is(err, registry.ErrInvalid), errors.Is(err, registry.ErrNameTaken), errors.Is(err, registry.ErrNotFound),
		errors.Is(err, registry.ErrUnconfigured):
		s.writeRegistryError(w, r, err)
	case errors.As(err, &authErr):
		writeCodedError(w, http.StatusBadGateway, authErr.Msg, codeRegistryAuthFailed)
	case errors.Is(err, images.ErrTargetNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, images.ErrImageNotFound), errors.Is(err, images.ErrVersionNotFound),
		errors.Is(err, images.ErrRevisionNotFound), errors.Is(err, fs.ErrNotExist):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, images.ErrGitHubUnconfigured):
		writeCodedError(w, http.StatusConflict, err.Error(), codeGitHubUnconfigured)
	case errors.Is(err, images.ErrGitHubUnauthorized):
		writeCodedError(w, http.StatusConflict, err.Error(), codeGitHubUnauthorized)
	case errors.Is(err, images.ErrGitHubRateLimited):
		writeCodedError(w, http.StatusTooManyRequests, err.Error(), codeGitHubRateLimited)
	case errors.Is(err, images.ErrGitHubUnreachable):
		writeCodedError(w, http.StatusBadGateway, err.Error(), codeGitHubUnreachable)
	case errors.Is(err, images.ErrImageExists):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, images.ErrTagTaken):
		writeCodedError(w, http.StatusConflict, err.Error(), codeTagTaken)
	case errors.Is(err, images.ErrBuildRunning), errors.Is(err, images.ErrImageBusy):
		writeCodedError(w, http.StatusConflict, err.Error(), codeBuildRunning)
	case errors.Is(err, images.ErrArchMismatch):
		writeCodedError(w, http.StatusConflict, err.Error(), codeArchMismatch)
	case errors.As(err, &peerErr):
		writeCodedError(w, http.StatusBadGateway, peerErr.Msg, peerErr.Code)
	case errors.Is(err, images.ErrRevisionConflict):
		writeCodedError(w, http.StatusConflict, err.Error(), codeRevisionConflict)
	case errors.Is(err, images.ErrImageNotBuilt):
		writeCodedError(w, http.StatusConflict, err.Error(), codeImageNotBuilt)
	case errors.Is(err, images.ErrFileTooLarge), errors.Is(err, images.ErrContextTooLarge), errors.As(err, &tooBig):
		writeCodedError(w, http.StatusRequestEntityTooLarge, err.Error(), codeImageTooLarge)
	case errors.As(err, &badForm):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, images.ErrBadPath), errors.Is(err, images.ErrBadDockerfile):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, stacks.ErrShutdown), errors.Is(err, engine.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	default:
		s.internalError(w, r, err)
	}
}

func (s *server) handleListImages(w http.ResponseWriter, r *http.Request) {
	lib, err := s.opts.Images.Library(r.Context())
	if err != nil {
		s.writeImagesError(w, r, err)
		return
	}
	out := toLibrary(lib)
	byID := s.registriesByID(r.Context())
	for i := range out.Images {
		namePushTargetsWith(byID, out.Images[i].PushTargets)
		nameImagePushesWith(byID, &out.Images[i], nil)
	}
	writeJSON(w, http.StatusOK, out)
}

// imageView is the LibraryImage answer for slug, with its versions.
func (s *server) imageView(ctx context.Context, slug string) (apitypes.LibraryImage, error) {
	d, err := s.opts.Images.Detail(ctx, slug)
	if err != nil {
		return apitypes.LibraryImage{}, err
	}
	owner, err := s.opts.Images.LocalServer(ctx)
	if err != nil {
		return apitypes.LibraryImage{}, err
	}
	view := toLibraryImageDetail(d, owner).LibraryImage
	s.namePushTargets(ctx, view.PushTargets)
	s.nameImagePushes(ctx, &view, nil)
	return view, nil
}

func (s *server) handleCreateImage(w http.ResponseWriter, r *http.Request) {
	var req apitypes.CreateImageRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	img, err := s.opts.Images.Create(r.Context(), userFrom(r.Context()).Username, toCreateRequest(req))
	if err != nil {
		s.writeImagesError(w, r, err)
		return
	}
	view, err := s.imageView(r.Context(), img.Slug)
	if err != nil {
		s.writeImagesError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

func (s *server) handleGetImage(w http.ResponseWriter, r *http.Request) {
	d, err := s.opts.Images.Detail(r.Context(), r.PathValue("slug"))
	if err != nil {
		s.writeImagesError(w, r, err)
		return
	}
	owner, err := s.opts.Images.LocalServer(r.Context())
	if err != nil {
		s.writeImagesError(w, r, err)
		return
	}
	detail := toLibraryImageDetail(d, owner)
	s.namePushTargets(r.Context(), detail.PushTargets)
	s.nameImagePushes(r.Context(), &detail.LibraryImage, detail.Versions)
	writeJSON(w, http.StatusOK, detail)
}

func (s *server) handleUpdateImage(w http.ResponseWriter, r *http.Request) {
	var req apitypes.UpdateImageRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	slug := r.PathValue("slug")
	if _, err := s.opts.Images.Update(r.Context(), userFrom(r.Context()).Username, slug, toUpdateRequest(req)); err != nil {
		s.writeImagesError(w, r, err)
		return
	}
	view, err := s.imageView(r.Context(), slug)
	if err != nil {
		s.writeImagesError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *server) handleDeleteImage(w http.ResponseWriter, r *http.Request) {
	if err := s.opts.Images.Delete(r.Context(), userFrom(r.Context()).Username, r.PathValue("slug")); err != nil {
		s.writeImagesError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleImageFiles(w http.ResponseWriter, r *http.Request) {
	m, rev, err := s.opts.Images.Files(r.Context(), r.PathValue("slug"))
	if err != nil {
		s.writeImagesError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toImageFiles(m.Files(), rev))
}

func (s *server) handleImageFile(w http.ResponseWriter, r *http.Request) {
	slug, path := r.PathValue("slug"), r.PathValue("path")
	b, err := s.opts.Images.ReadFile(r.Context(), slug, path)
	if err != nil {
		s.writeImagesError(w, r, err)
		return
	}
	content, err := textContent(b)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "this file is not text")
		return
	}
	_, rev, err := s.opts.Images.Files(r.Context(), slug)
	if err != nil {
		s.writeImagesError(w, r, err)
		return
	}
	clean, err := images.ValidatePath(path)
	if err != nil {
		s.writeImagesError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, apitypes.ImageFileContent{Path: clean, Content: content, Revision: rev})
}

// revisionAnswer converts one revision the service just wrote, so the answer
// carries DockerfileChanged against its predecessor.
func (s *server) revisionAnswer(ctx context.Context, slug string, rev images.Revision) (apitypes.ImageRevision, error) {
	img, err := s.opts.Images.Get(ctx, slug)
	if err != nil {
		return apitypes.ImageRevision{}, err
	}
	all, err := s.opts.Images.Revisions(ctx, slug)
	if err != nil {
		return apitypes.ImageRevision{}, err
	}
	for _, dto := range toRevisionDTOs(all, img.DockerfilePath) {
		if dto.Number == rev.Number {
			return dto, nil
		}
	}
	return apitypes.ImageRevision{Number: rev.Number, CreatedAt: rev.CreatedAt, Summary: rev.Summary}, nil
}

func (s *server) writeRevision(w http.ResponseWriter, r *http.Request, status int, slug string, rev images.Revision, err error) {
	if err != nil {
		s.writeImagesError(w, r, err)
		return
	}
	dto, err := s.revisionAnswer(r.Context(), slug, rev)
	if err != nil {
		s.writeImagesError(w, r, err)
		return
	}
	writeJSON(w, status, dto)
}

func (s *server) handleRenameImageFile(w http.ResponseWriter, r *http.Request) {
	var req apitypes.RenameFileRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	slug := r.PathValue("slug")
	rev, err := s.opts.Images.Rename(r.Context(), userFrom(r.Context()).Username, slug, req.From, req.To)
	s.writeRevision(w, r, http.StatusOK, slug, rev, err)
}

func (s *server) handleDeleteImageFile(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	rev, err := s.opts.Images.DeleteFile(r.Context(), userFrom(r.Context()).Username, slug, r.PathValue("path"))
	s.writeRevision(w, r, http.StatusOK, slug, rev, err)
}

func (s *server) handleGetImageDockerfile(w http.ResponseWriter, r *http.Request) {
	content, rev, err := s.opts.Images.Dockerfile(r.Context(), r.PathValue("slug"))
	if err != nil {
		s.writeImagesError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, apitypes.ImageDockerfile{Content: string(content), Revision: rev})
}

func (s *server) handlePutImageDockerfile(w http.ResponseWriter, r *http.Request) {
	var req apitypes.ImageDockerfile
	if !decodeJSONLimit(w, r, &req, dockerfileBodyMaxJSON) {
		return
	}
	slug := r.PathValue("slug")
	rev, err := s.opts.Images.SaveDockerfile(r.Context(), userFrom(r.Context()).Username, slug, []byte(req.Content), req.Revision)
	s.writeRevision(w, r, http.StatusOK, slug, rev, err)
}

func (s *server) handleImageRevisions(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	img, err := s.opts.Images.Get(r.Context(), slug)
	if err != nil {
		s.writeImagesError(w, r, err)
		return
	}
	revs, err := s.opts.Images.Revisions(r.Context(), slug)
	if err != nil {
		s.writeImagesError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toRevisionDTOs(revs, img.DockerfilePath))
}

func (s *server) handleImageRevision(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	n, err := parseRevisionNumber(r.PathValue("n"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	img, err := s.opts.Images.Get(r.Context(), slug)
	if err != nil {
		s.writeImagesError(w, r, err)
		return
	}
	revs, err := s.opts.Images.Revisions(r.Context(), slug)
	if err != nil {
		s.writeImagesError(w, r, err)
		return
	}
	dtos := toRevisionDTOs(revs, img.DockerfilePath)
	for i, dto := range dtos {
		if dto.Number != n {
			continue
		}
		dockerfile, err := s.opts.Images.RevisionDockerfile(r.Context(), slug, n)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			s.writeImagesError(w, r, err)
			return
		}
		previous := []byte{}
		if i+1 < len(revs) {
			previous, err = s.opts.Images.RevisionDockerfile(r.Context(), slug, revs[i+1].Number)
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				s.writeImagesError(w, r, err)
				return
			}
		}
		writeJSON(w, http.StatusOK, apitypes.ImageRevisionDetail{ImageRevision: dto, Dockerfile: string(dockerfile),
			Previous: string(previous), Files: revisionFiles(revs[i])})
		return
	}
	s.writeImagesError(w, r, images.ErrRevisionNotFound)
}

func (s *server) handleRestoreImageRevision(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	n, err := parseRevisionNumber(r.PathValue("n"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rev, err := s.opts.Images.Restore(r.Context(), userFrom(r.Context()).Username, slug, n)
	s.writeRevision(w, r, http.StatusOK, slug, rev, err)
}

func (s *server) handleBuildImage(w http.ResponseWriter, r *http.Request) {
	var req apitypes.BuildRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	slug := r.PathValue("slug")
	v, op, err := s.opts.Images.Build(r.Context(), userFrom(r.Context()).Username, slug,
		images.BuildRequest{Tag: req.Tag, BuildArgs: req.BuildArgs, SaveArgs: req.SaveArgs, Ref: req.Ref})
	if err != nil {
		s.writeImagesError(w, r, err)
		return
	}
	owner, err := s.opts.Images.LocalServer(r.Context())
	if err != nil {
		s.writeImagesError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, apitypes.BuildStarted{
		Version:   toLibraryVersion(v, ownerIDOf(owner), slug, nil, nil),
		Operation: toOperation(op),
	})
}

func (s *server) handleGetImageVersion(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	d, err := s.opts.Images.VersionDetail(r.Context(), slug, r.PathValue("id"))
	if err != nil {
		s.writeImagesError(w, r, err)
		return
	}
	owner, err := s.opts.Images.LocalServer(r.Context())
	if err != nil {
		s.writeImagesError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toVersionDetail(d, owner, slug))
}

func (s *server) handleDeleteImageVersion(w http.ResponseWriter, r *http.Request) {
	err := s.opts.Images.DeleteVersion(r.Context(), userFrom(r.Context()).Username, r.PathValue("slug"), r.PathValue("id"))
	if err != nil {
		s.writeImagesError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- multipart upload ----

// handleUploadImageFiles reads parts named f/<path>. Each part is spooled to a
// temporary file while its size is checked against the per-file and context
// caps, so no file is held in memory and an oversized body stops early.
func (s *server) handleUploadImageFiles(w http.ResponseWriter, r *http.Request) {
	lim := s.opts.Images.Limits()
	r.Body = http.MaxBytesReader(w, r.Body, lim.MaxContextBytes+uploadEnvelopeBytes)
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "expected a multipart/form-data body")
		return
	}
	slug := r.PathValue("slug")
	spool, err := s.opts.Images.NewUploadSpool(r.Context(), slug)
	if err != nil {
		s.writeImagesError(w, r, err)
		return
	}
	defer os.RemoveAll(spool)

	files, err := spoolUploadParts(mr, spool, lim)
	// The service may stop reading early; release any descriptor still open.
	defer func() {
		for _, f := range files {
			if c, ok := f.Content.(io.Closer); ok {
				c.Close()
			}
		}
	}()
	if err != nil {
		s.writeImagesError(w, r, err)
		return
	}
	if len(files) == 0 {
		s.writeImagesError(w, r, &stacks.ValidationError{Fields: []stacks.FieldError{{Field: "files", Message: "no files were uploaded"}}})
		return
	}
	rev, err := s.opts.Images.Upload(r.Context(), userFrom(r.Context()).Username, slug, files)
	s.writeRevision(w, r, http.StatusCreated, slug, rev, err)
}

func spoolUploadParts(mr *multipart.Reader, dir string, lim images.Limits) ([]images.UploadFile, error) {
	var (
		files []images.UploadFile
		total int64
	)
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return files, nil
		}
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				return nil, err
			}
			return nil, &uploadFormatError{err}
		}
		name, ok := strings.CutPrefix(part.FormName(), "f/")
		if !ok {
			part.Close()
			return nil, &stacks.ValidationError{Fields: []stacks.FieldError{{Field: "files", Message: "every part must be named f/<path>"}}}
		}
		if _, err := images.ValidatePath(name); err != nil {
			part.Close()
			return nil, err
		}
		if len(files) >= maxUploadFiles {
			part.Close()
			return nil, &stacks.ValidationError{Fields: []stacks.FieldError{{Field: "files", Message: fmt.Sprintf("at most %d files per upload", maxUploadFiles)}}}
		}
		spooled := filepath.Join(dir, strconv.Itoa(len(files)))
		n, err := spoolPart(part, spooled, min(lim.MaxFileBytes, lim.MaxContextBytes-total))
		part.Close()
		switch {
		case errors.Is(err, errPartTooLarge) && lim.MaxContextBytes-total < lim.MaxFileBytes:
			return nil, fmt.Errorf("%w: %q", images.ErrContextTooLarge, name)
		case errors.Is(err, errPartTooLarge):
			return nil, fmt.Errorf("%w: %q", images.ErrFileTooLarge, name)
		case err != nil:
			return nil, err
		}
		total += n
		files = append(files, images.UploadFile{Path: name, Content: &lazyFile{path: spooled}})
	}
}

// uploadFormatError is a multipart body the parser rejected.
type uploadFormatError struct{ err error }

func (e *uploadFormatError) Error() string { return "malformed multipart body: " + e.err.Error() }
func (e *uploadFormatError) Unwrap() error { return e.err }

var errPartTooLarge = errors.New("part is too large")

// spoolPart copies part to path, failing once it holds more than limit bytes.
func spoolPart(part io.Reader, path string, limit int64) (int64, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, io.LimitReader(part, max(limit, 0)+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, err
	}
	if n > limit {
		return 0, errPartTooLarge
	}
	return n, nil
}

// lazyFile opens its spool file on first read and closes it at EOF, so a
// large upload does not hold one descriptor per part.
type lazyFile struct {
	path string
	f    *os.File
}

func (l *lazyFile) Close() error {
	if l.f == nil {
		return nil
	}
	return l.f.Close()
}

func (l *lazyFile) Read(p []byte) (int, error) {
	if l.f == nil {
		f, err := os.Open(l.path)
		if err != nil {
			return 0, err
		}
		l.f = f
	}
	n, err := l.f.Read(p)
	if err != nil {
		l.f.Close()
	}
	return n, err
}

func ownerIDOf(s images.ServerInfo) string { return imageref.OwnerID(s.ID) }

// pushTargetView is the PushTarget answer with the registry's name and kind;
// a registry that cannot be read leaves them empty.
func (s *server) pushTargetView(ctx context.Context, t images.PushTarget) apitypes.PushTarget {
	view := toPushTargets([]images.PushTarget{t})[0]
	if s.opts.Registries != nil {
		if reg, err := s.opts.Registries.Get(ctx, t.RegistryID); err == nil {
			view.RegistryName, view.Kind = reg.Name, string(reg.Kind)
		}
	}
	return view
}

// registriesByID indexes the registry list by id; nil when registries are
// unavailable or cannot be read.
func (s *server) registriesByID(ctx context.Context) map[string]registry.Registry {
	if s.opts.Registries == nil {
		return nil
	}
	list, err := s.opts.Registries.List(ctx)
	if err != nil {
		return nil
	}
	byID := make(map[string]registry.Registry, len(list))
	for _, reg := range list {
		byID[reg.ID] = reg
	}
	return byID
}

// namePushes replaces the registry id in each push state with its name. A
// version reached twice (detail list and latest pointer) is harmless: a name
// is not an id, so the second lookup leaves it alone.
func namePushes(names map[string]string, versions ...*apitypes.LibraryVersion) {
	for _, v := range versions {
		if v == nil {
			continue
		}
		for i := range v.Pushes {
			if n, ok := names[v.Pushes[i].Registry]; ok {
				v.Pushes[i].Registry = n
			}
		}
	}
}

func (s *server) nameImagePushes(ctx context.Context, img *apitypes.LibraryImage, versions []apitypes.LibraryVersion) {
	nameImagePushesWith(s.registriesByID(ctx), img, versions)
}

// nameImagePushesWith names the pushes of img and versions from an already read registry index.
func nameImagePushesWith(byID map[string]registry.Registry, img *apitypes.LibraryImage, versions []apitypes.LibraryVersion) {
	if len(byID) == 0 {
		return
	}
	names := make(map[string]string, len(byID))
	for id, reg := range byID {
		names[id] = reg.Name
	}
	for i := range versions {
		namePushes(names, &versions[i])
	}
	namePushes(names, img.LatestVersion, img.LastBuild)
}

// namePushTargets fills registryName and kind in place from the registry list.
func (s *server) namePushTargets(ctx context.Context, targets []apitypes.PushTarget) {
	if len(targets) == 0 {
		return
	}
	namePushTargetsWith(s.registriesByID(ctx), targets)
}

// namePushTargetsWith fills registryName and kind from an already read registry index.
func namePushTargetsWith(byID map[string]registry.Registry, targets []apitypes.PushTarget) {
	for i := range targets {
		if reg, ok := byID[targets[i].RegistryID]; ok {
			targets[i].RegistryName, targets[i].Kind = reg.Name, string(reg.Kind)
		}
	}
}

func (s *server) handleAddPushTarget(w http.ResponseWriter, r *http.Request) {
	var req apitypes.PushTargetRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	t, err := s.opts.Images.AddPushTarget(r.Context(), userFrom(r.Context()).Username, r.PathValue("slug"), images.PushTargetRequest(req))
	if err != nil {
		s.writeImagesError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.pushTargetView(r.Context(), t))
}

func (s *server) handleUpdatePushTarget(w http.ResponseWriter, r *http.Request) {
	var req apitypes.PushTargetRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	t, err := s.opts.Images.UpdatePushTarget(r.Context(), userFrom(r.Context()).Username, r.PathValue("slug"), r.PathValue("id"), images.PushTargetRequest(req))
	if err != nil {
		s.writeImagesError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.pushTargetView(r.Context(), t))
}

func (s *server) handleDeletePushTarget(w http.ResponseWriter, r *http.Request) {
	if err := s.opts.Images.DeletePushTarget(r.Context(), userFrom(r.Context()).Username, r.PathValue("slug"), r.PathValue("id")); err != nil {
		s.writeImagesError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handlePushImageVersion starts a push of the version with this tag to one
// target; the answer is the running operation.
func (s *server) handlePushImageVersion(w http.ResponseWriter, r *http.Request) {
	var req apitypes.PushRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	op, err := s.opts.Images.Push(r.Context(), userFrom(r.Context()).Username, r.PathValue("slug"), r.PathValue("tag"), req.Target)
	if err != nil {
		s.writeImagesError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toOperation(op))
}

// handleFetchImage copies a peer-owned version here. 204 means there is
// nothing to do (this server owns it, or it is already present); 202 answers
// the running operation.
func (s *server) handleFetchImage(w http.ResponseWriter, r *http.Request) {
	var req apitypes.FetchImageRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ref, ok := imageref.Parse(req.Ref)
	if !ok {
		s.writeImagesError(w, r, &stacks.ValidationError{Fields: []stacks.FieldError{{Field: "ref", Message: "not a library image reference"}}})
		return
	}
	op, started, err := s.opts.Images.Fetch(r.Context(), userFrom(r.Context()).Username, ref)
	if err != nil {
		s.writeImagesError(w, r, err)
		return
	}
	if !started {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusAccepted, toOperation(op))
}
