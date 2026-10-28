package backup

import (
	"context"
	"path/filepath"
)

// uploadName is the archive the setup screen uploads into the restore folder.
const uploadName = "upload.tar.gz"

// UploadPath is where an uploaded whole-host archive is stored:
// <dataDir>/restore/upload.tar.gz.
func UploadPath(dataDir string) string { return filepath.Join(restoreDir(dataDir), uploadName) }

// HostRestorer runs whole-host restores against one environment for the setup
// API.
type HostRestorer struct{ Env HostEnv }

// UploadPath is where this host keeps an uploaded archive.
func (h HostRestorer) UploadPath() string { return UploadPath(h.Env.DataDir) }

// Inspect is InspectHost with this restorer's environment.
func (h HostRestorer) Inspect(ctx context.Context, src HostSource) (HostSummary, error) {
	return InspectHost(ctx, src, h.Env)
}

// Restore is RestoreHost with this restorer's environment. out receives the
// progress lines, and accepted is called once the confirmed conflicts still
// match and before the host changes; either may be nil.
func (h HostRestorer) Restore(ctx context.Context, src HostSource, opts HostRestoreOptions, out func(string), accepted func()) (HostReport, error) {
	env := h.Env
	env.Out = out
	env.Accepted = accepted
	return RestoreHost(ctx, src, env, opts)
}
