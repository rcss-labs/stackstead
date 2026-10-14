package stacks

import "context"

// RevertProject exposes the failed-save revert to tests.
func (s *Service) RevertProject(ctx context.Context, e Entry, saveErr error) error {
	return s.revertProject(ctx, e, saveErr)
}
