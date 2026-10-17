package updatehost

import (
	"context"
	"github.com/rcss-labs/stackstead/internal/update"
	"os"
)

func inspectNativePayload(ctx context.Context, r payloadRequest, f *os.File) (payloadInspection, error) {
	if r.Installer != update.InstallerPkg {
		return payloadInspection{}, update.ErrUnavailable
	}
	return inspectPKGPayload(ctx, r, f)
}
