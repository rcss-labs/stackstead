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

// SetProbeTimeout shortens the version probe's own timeout for tests.
func SetProbeTimeout(c Compose, d time.Duration) { c.(*versionCache).probeTimeout = d }

// Exposed so the confinement rules can be tested without a registry.
var (
	DockerfileTarget      = dockerfileTarget
	WriteDockerfile       = writeDockerfile
	WriteDockerfileInRoot = writeDockerfileInRoot
)
