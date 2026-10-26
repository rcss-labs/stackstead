package api

import "testing"

func TestBackupRoutesForwardable(t *testing.T) {
	for _, path := range []string{
		"/api/backups/config", "/api/backups/config/s3-secret", "/api/backups/s3/test", "/api/backups/stacks",
		"/api/backups/stacks/web", "/api/backups/run", "/api/backups/runs", "/api/backups/archives",
		"/api/backups/restore", "/api/backups/restore/plan", "/api/backups/archives/local/a.tar.gz/manifest",
	} {
		if isPeerDenied(path) {
			t.Errorf("%s is denied to peer credentials, so it cannot be forwarded", path)
		}
	}
}
