package stacks

import (
	"context"

	"github.com/rcss-labs/stackstead/internal/composefile"
	"github.com/rcss-labs/stackstead/internal/fsguard"
)

// HasBackup reports whether the stack has a compose backup (compose.yaml.bak)
// that RestoreBackup could use. Any problem reading it counts as no backup.
func (s *Service) HasBackup(ctx context.Context, stack string) bool {
	e, err := s.reg.Get(ctx, stack)
	if err != nil {
		return false
	}
	path, err := fsguard.Resolve(s.roots, e.ComposePath)
	return err == nil && composefile.HasBackup(path)
}

// RestoreBackup replaces the stack's compose file with its .bak and keeps the
// replaced file as compose.yaml.broken. hash is the version the caller saw;
// composefile.ErrConflict means it is stale and composefile.ErrNoBackup that
// there is nothing to restore. It returns the hash of the restored file.
//
// It runs under the stack lock like every other compose write. The current
// file is deliberately not parsed: restoring is how a broken file is replaced.
func (s *Service) RestoreBackup(ctx context.Context, stack, hash string) (string, error) {
	e, unlock, err := s.lockedEntry(ctx, stack)
	if err != nil {
		return "", err
	}
	defer unlock()
	path, err := fsguard.Resolve(s.roots, e.ComposePath)
	if err != nil {
		return "", err
	}
	if err := composefile.RestoreBackup(path, hash); err != nil {
		return "", err
	}
	_, restored, err := loadFile(e)
	return restored, err
}
