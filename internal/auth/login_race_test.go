package auth

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// loginRacingRecovery logs in with the old password while recoverFn runs in
// the middle of password verification, as a concurrent password recovery
// committing between the hash lookup and the session insert would.
func loginRacingRecovery(t *testing.T, svc *Service, recoverFn func(ctx context.Context)) (string, error) {
	t.Helper()
	ctx := context.Background()
	orig := deriveKey
	t.Cleanup(func() { deriveKey = orig })
	var fired atomic.Bool
	deriveKey = func(password, salt []byte, iterations, memory uint32, threads uint8, keyLen uint32) []byte {
		if string(password) == goodPassword {
			if fired.CompareAndSwap(false, true) {
				recoverFn(ctx)
			}
		}
		return orig(password, salt, iterations, memory, threads, keyLen)
	}
	return svc.Login(ctx, "admin", goodPassword)
}

func TestLoginDoesNotIssueSessionAfterConcurrentPasswordChange(t *testing.T) {
	svc, db, _ := newTestService(t)
	ctx := context.Background()
	if err := svc.CreateAdmin(ctx, "admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	keep, err := svc.Login(ctx, "admin", goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	user, err := svc.Authenticate(ctx, keep)
	if err != nil {
		t.Fatal(err)
	}
	token, err := loginRacingRecovery(t, svc, func(ctx context.Context) {
		if err := svc.ChangePassword(ctx, user.ID, goodPassword, newPassword, keep); err != nil {
			t.Errorf("ChangePassword: %v", err)
		}
	})
	if !errors.Is(err, ErrInvalidCredentials) || token != "" {
		t.Fatalf("Login = %q, %v; want no token and ErrInvalidCredentials", token, err)
	}
	if n := countRows(t, db, "sessions"); n != 1 {
		t.Errorf("sessions = %d, want only the retained current-owner session", n)
	}
	if _, err := svc.Authenticate(ctx, keep); err != nil {
		t.Errorf("retained session rejected: %v", err)
	}
	if _, err := svc.Login(ctx, "admin", newPassword); err != nil {
		t.Errorf("login with new password: %v", err)
	}
}

func TestLoginDoesNotIssueSessionAfterConcurrentPasswordReset(t *testing.T) {
	svc, db, _ := newTestService(t)
	ctx := context.Background()
	if err := svc.CreateAdmin(ctx, "admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	token, err := loginRacingRecovery(t, svc, func(ctx context.Context) {
		if _, err := svc.ResetPassword(ctx, "admin", newPassword); err != nil {
			t.Errorf("ResetPassword: %v", err)
		}
	})
	if !errors.Is(err, ErrInvalidCredentials) || token != "" {
		t.Fatalf("Login = %q, %v; want no token and ErrInvalidCredentials", token, err)
	}
	if n := countRows(t, db, "sessions"); n != 0 {
		t.Errorf("sessions = %d, want 0", n)
	}
	if _, err := svc.Login(ctx, "admin", newPassword); err != nil {
		t.Errorf("login with new password: %v", err)
	}
}
