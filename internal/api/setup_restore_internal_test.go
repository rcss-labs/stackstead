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

// A client that leaves just as the restore is accepted must not cancel it:
// the restore is already changing the host and has to run to its end.
func TestAwaitAcceptancePrefersAcceptedOverClientGone(t *testing.T) {
	for range 200 {
		accepted := make(chan struct{})
		close(accepted)
		clientGone := make(chan struct{})
		close(clientGone)
		if got, _ := awaitAcceptance(clientGone, accepted, make(chan error)); got != restoreAccepted {
			t.Fatalf("outcome = %v, want accepted", got)
		}
	}
}

func TestAwaitAcceptanceReportsClientGoneBeforeAcceptance(t *testing.T) {
	clientGone := make(chan struct{})
	close(clientGone)
	if got, _ := awaitAcceptance(clientGone, make(chan struct{}), make(chan error)); got != restoreClientGone {
		t.Fatalf("outcome = %v, want client gone", got)
	}
}
