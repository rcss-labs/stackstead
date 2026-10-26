package backup

import (
	"context"
	"io"

	"github.com/rcss-labs/stackstead/internal/engine"
)

// Engine is the part of the Docker engine the backup code uses; satisfied by
// *engine.Client and by test fakes.
type Engine interface {
	CreateContainer(ctx context.Context, spec engine.HelperSpec) (string, error)
	StartContainer(ctx context.Context, id string) error
	WaitContainer(ctx context.Context, id string) (int, error)
	RemoveContainer(ctx context.Context, id string, force bool) error
	GetArchive(ctx context.Context, id, path string) (io.ReadCloser, error)
	PutArchive(ctx context.Context, id, path string, tar io.Reader) error
	PullImage(ctx context.Context, ref string, out func(string)) error
	InspectImage(ctx context.Context, id string) (engine.ImageInspect, error)
	SaveImage(ctx context.Context, ref string) (io.ReadCloser, error)
	LoadImage(ctx context.Context, tar io.Reader) error
	Networks(ctx context.Context) ([]engine.Network, error)
	CreateNetwork(ctx context.Context, spec engine.NetworkSpec) (string, error)
	InspectVolume(ctx context.Context, name string) (engine.VolumeInspect, error)
	CreateVolume(ctx context.Context, name string, labels map[string]string) error
	InspectContainer(ctx context.Context, id string) (engine.ContainerInspect, error)
	Containers(ctx context.Context) ([]engine.Container, error)
}

var _ Engine = (*engine.Client)(nil)
