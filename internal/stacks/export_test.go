package stacks

import (
	"context"
	"time"
)

// RevertProject exposes the failed-save revert to tests.
func (s *Service) RevertProject(ctx context.Context, e Entry, saveErr error) error {
	return s.revertProject(ctx, e, saveErr)
}

// SetAfterClaim installs a hook that runs after an edit claims a new project
// and before it saves the file.
func (s *Service) SetAfterClaim(fn func()) { s.afterClaim = fn }

// SetBeforeComposeSave installs a hook that runs just before an edit saves the
// compose file.
func SetBeforeComposeSave(s *Service, fn func()) { s.beforeComposeSave = fn }

// SetProbeTimeout shortens the version probe's own timeout for tests.
func SetProbeTimeout(c Compose, d time.Duration) { c.(*versionCache).probeTimeout = d }

// Exposed so the confinement rules can be tested without a registry.
var (
	DockerfileTarget      = dockerfileTarget
	WriteDockerfile       = writeDockerfile
	WriteDockerfileInRoot = writeDockerfileInRoot
)

// WriteTrust is the trusted users and groups of the import writability check.
type WriteTrust struct{ UIDs, GIDs []uint32 }

// UntrustedWriters runs the import writability check against an explicit trust set.
func UntrustedWriters(path string, t WriteTrust) []string {
	trust := writeTrust{uids: map[uint32]bool{}, gids: map[uint32]bool{}}
	for _, u := range t.UIDs {
		trust.uids[u] = true
	}
	for _, g := range t.GIDs {
		trust.gids[g] = true
	}
	return untrustedWriters(path, trust)
}
