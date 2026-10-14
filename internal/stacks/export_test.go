package stacks

import "context"

// RevertProject exposes the failed-save revert to tests.
func (s *Service) RevertProject(ctx context.Context, e Entry, saveErr error) error {
	return s.revertProject(ctx, e, saveErr)
}

// SetAfterClaim installs a hook that runs after an edit claims a new project
// and before it saves the file.
func (s *Service) SetAfterClaim(fn func()) { s.afterClaim = fn }
