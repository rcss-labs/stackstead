package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/maintenance"
)

func TestProbationExpiredSessionReadDoesNotWrite(t *testing.T) {
	svc, db, clock := newTestService(t)
	ctx := context.Background()
	if err := svc.CreateAdmin(ctx, "admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	token, err := svc.Login(ctx, "admin", goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	gate := maintenance.New()
	WithMaintenance(gate)(svc)
	if err := gate.Freeze(); err != nil {
		t.Fatal(err)
	}
	clock.Advance(SessionTTL + time.Second)
	if _, err := svc.Authenticate(ctx, token); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("Authenticate expired = %v", err)
	}
	if n := countRows(t, db, "sessions"); n != 1 {
		t.Fatalf("probation deleted expired session; rows=%d", n)
	}
	gate.Thaw()
	if _, err := svc.Authenticate(ctx, token); !errors.Is(err, ErrInvalidSession) {
		t.Fatal(err)
	}
	if n := countRows(t, db, "sessions"); n != 0 {
		t.Fatalf("after release expired cleanup rows=%d", n)
	}
}
