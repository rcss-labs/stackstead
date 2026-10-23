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
	Resolve(ctx context.Context, refs []string) (map[string]images.ResolvedRef, error)
	Limits() images.Limits
	LocalServer(ctx context.Context) (images.ServerInfo, error)
	NewUploadSpool(ctx context.Context, slug string) (string, error)
}

var _ ImagesAPI = (*images.Service)(nil)

// Error codes of the image routes.
const (
	codeImageInUse        = "image_in_use"
	codeBuildRunning      = "build_running"
	codeTagTaken          = "tag_taken"
	codeRevisionConflict  = "revision_conflict"
	codeImageNotBuilt     = "image_not_built"
	codeImageTooLarge     = "image_too_large"
	maxUploadFiles        = 10000
	uploadEnvelopeBytes   = 1 << 20 // multipart framing allowed on top of the context cap
	dockerfileBodyMaxJSON = dockerfileBodyBytes
)

// imageInUseBody is the 409 body of a delete refused because the image is running.
type imageInUseBody struct {
	Error  string                `json:"error"`
	Code   string                `json:"code"`
	UsedBy []apitypes.LibraryUse `json:"usedBy"`
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
}

// writeImagesError maps image-library errors to HTTP answers. Unknown errors
// are logged and answered with a generic 500. Request bodies are never echoed.
func (s *server) writeImagesError(w http.ResponseWriter, r *http.Request, err error) {
	var (
		validation *stacks.ValidationError
		inUse      *images.InUseError
		tooBig     *http.MaxBytesError
		badForm    *uploadFormatError
	)
	switch {
	case errors.As(err, &validation):
		fields := make([]apitypes.FieldError, 0, len(validation.Fields))
		for _, f := range validation.Fields {
			fields = append(fields, apitypes.FieldError(f))
		}
		writeJSON(w, http.StatusBadRequest, fieldErrorBody{Error: validation.Error(), Fields: fields})
	case errors.As(err, &inUse):
		writeJSON(w, http.StatusConflict, imageInUseBody{Error: inUse.Error(), Code: codeImageInUse, UsedBy: toLibraryUses(inUse.UsedBy)})
	case errors.Is(err, images.ErrImageNotFound), errors.Is(err, images.ErrVersionNotFound),
		errors.Is(err, images.ErrRevisionNotFound), errors.Is(err, fs.ErrNotExist):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, images.ErrImageExists):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, images.ErrTagTaken):
		writeCodedError(w, http.StatusConflict, err.Error(), codeTagTaken)
	case errors.Is(err, images.ErrBuildRunning):
		writeCodedError(w, http.StatusConflict, err.Error(), codeBuildRunning)
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
	writeJSON(w, http.StatusOK, toLibrary(lib))
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
	return toLibraryImageDetail(d, owner).LibraryImage, nil
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
	writeJSON(w, http.StatusOK, toLibraryImageDetail(d, owner))
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
