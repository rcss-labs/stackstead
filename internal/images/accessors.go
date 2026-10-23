package images

import (
	"context"
	"os"
	"path/filepath"
)

// Limits returns the upload caps this service enforces, so the HTTP layer can
// stop a multipart body early instead of buffering it.
func (s *Service) Limits() Limits { return s.limits }

// LocalServer describes this server as the owner of its images.
func (s *Service) LocalServer(ctx context.Context) (ServerInfo, error) {
	info, _, err := s.localServer(ctx)
	return info, err
}

// NewUploadSpool creates a private scratch directory inside the image's own
// directory for spooling an upload, so it lives on the data volume and the
// startup sweep of tmp-* removes leftovers from a crash. It fails with
// ErrImageNotFound before any upload bytes are read. The caller removes it.
func (s *Service) NewUploadSpool(ctx context.Context, slug string) (string, error) {
	if _, err := s.store.Image(ctx, slug); err != nil {
		return "", err
	}
	imageDir := filepath.Join(s.dir, slug)
	if err := os.MkdirAll(imageDir, 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(imageDir, "tmp-upload-")
}
