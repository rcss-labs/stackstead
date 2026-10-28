package backup

import (
	"context"
	"io"
	"time"
)

// Destination names for ArchiveRef.Dest and Destination.Name.
const (
	destLocal = "local"
	destS3    = "s3"
)

// Destination is a place archives are written to and restored from.
type Destination interface {
	// Name is "local" or "s3".
	Name() string
	// Create starts writing an archive; Close publishes it.
	Create(ctx context.Context, archive string) (io.WriteCloser, error)
	PutSidecar(ctx context.Context, archive string, manifest []byte) error
	List(ctx context.Context) ([]ArchiveRef, error)
	Open(ctx context.Context, archive string) (io.ReadCloser, error)
	ReadSidecar(ctx context.Context, archive string) (Manifest, error)
	// Abort discards an in-progress Create.
	Abort(ctx context.Context, archive string) error
}

// ArchiveRef identifies one stored archive.
type ArchiveRef struct {
	Dest      string
	Name      string
	HostSlug  string
	CreatedAt time.Time
	Size      int64
	// Encrypted is read from the name; the content decides when it is opened.
	Encrypted bool
}
