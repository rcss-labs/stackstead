package images

import "context"

// Limits returns the upload caps this service enforces, so the HTTP layer can
// stop a multipart body early instead of buffering it.
func (s *Service) Limits() Limits { return s.limits }

// LocalServer describes this server as the owner of its images.
func (s *Service) LocalServer(ctx context.Context) (ServerInfo, error) {
	info, _, err := s.localServer(ctx)
	return info, err
}
