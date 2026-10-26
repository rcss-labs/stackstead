package api

import "testing"

// Whole-host restore is only for the machine itself: a connected server must
// never reach it, so none of its routes may be forwarded.
func TestSetupRestoreRoutesNotForwardable(t *testing.T) {
	for _, path := range []string{
		"/api/setup/restore/upload", "/api/setup/restore/inspect",
		"/api/setup/restore/start", "/api/setup/restore/progress",
		"/api//setup/restore/start", "/api/stacks/../setup/restore/upload",
	} {
		if !isPeerDenied(path) {
			t.Errorf("%s is open to peer credentials", path)
		}
	}
}
