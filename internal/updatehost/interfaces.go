package updatehost

import (
	"context"
	"github.com/rcss-labs/stackstead/internal/update"
)

type PackageAdapter interface {
	Validate(context.Context, update.Plan, update.VerifiedArtifact) error
	Install(context.Context, update.Plan, update.VerifiedArtifact) error
	Inspect(context.Context, update.Plan) (update.Identity, error)
}
type Supervisor interface {
	Probe(context.Context, update.Instance) (update.ServiceState, error)
	Stop(context.Context, update.Instance) error
	Start(context.Context, update.Instance, update.ServiceState) error
}
type CommandRunner interface {
	Run(ctx context.Context, uid, gid uint32, path string, args []string, stdin []byte) (stdout []byte, stderr []byte, err error)
}
