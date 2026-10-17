package updatehost

import (
	"context"
	"errors"
	"github.com/rcss-labs/stackstead/internal/update"
	"io"
	"os"
)

func inspectNativePayload(ctx context.Context, req payloadRequest, f *os.File) (payloadInspection, error) {
	var proof payloadInspection
	switch req.Installer {
	case update.InstallerDeb:
		err := convertPayloadStream(ctx, f, "/usr/bin/dpkg-deb", []string{"--fsys-tarfile", "/dev/fd/3"}, 2<<30, func(r io.Reader) error {
			var e error
			proof, e = inventoryTar(ctx, r, req, defaultPayloadBounds, true)
			return e
		})
		return proof, err
	case update.InstallerRPM:
		facts, err := rpmMetadataFromArtifact(ctx, f)
		if err != nil {
			return proof, err
		}
		err = convertPayloadStream(ctx, f, "/usr/bin/rpm2cpio", []string{"/dev/fd/3"}, 2<<30, func(r io.Reader) error {
			pr, pw := io.Pipe()
			done := make(chan error, 1)
			go func() { e := newcToTar(r, pw); pw.CloseWithError(e); done <- e }()
			var e error
			proof, e = inventoryTarWithRPMMetadata(ctx, pr, req, defaultPayloadBounds, true, facts)
			pr.CloseWithError(e)
			return errors.Join(e, <-done)
		})
		return proof, err
	default:
		return proof, update.ErrUnavailable
	}
}
