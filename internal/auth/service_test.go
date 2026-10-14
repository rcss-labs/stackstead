package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/store"
)

const goodPassword = "correct horse battery"

// fakeClock is defined in clock_test.go.

func newTestService(t *testing.T) (*Service, *sql.DB, *fakeClock) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	clock := &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	svc, err := NewService(db, clock.Now)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc, db, clock
}

func countRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCreateAdminThenLogin(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	has, err := svc.HasAdmin(ctx)
	if err != nil || has {
		t.Fatalf("HasAdmin before setup = %v, %v; want false, nil", has, err)
	}
	if err := svc.CreateAdmin(ctx, "admin", goodPassword); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	if has, _ := svc.HasAdmin(ctx); !has {
		t.Fatal("HasAdmin after setup = false")
	}
	token, err := svc.Login(ctx, "admin", goodPassword)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	user, err := svc.Authenticate(ctx, token)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if user.Username != "admin" {
		t.Errorf("Username = %q, want admin", user.Username)
	}
}

func TestCreateAdminOnlyOnce(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()
	if err := svc.CreateAdmin(ctx, "admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	if err := svc.CreateAdmin(ctx, "other", goodPassword); !errors.Is(err, ErrAdminExists) {
		t.Errorf("second CreateAdmin err = %v, want ErrAdminExists", err)
	}
}

func TestConcurrentCreateAdminCreatesExactlyOne(t *testing.T) {
	svc, db, _ := newTestService(t)
	const attempts = 8
	errs := make(chan error, attempts)
	var wg sync.WaitGroup
	for i := range attempts {
		wg.Go(func() {
			errs <- svc.CreateAdmin(context.Background(), fmt.Sprintf("admin%d", i), goodPassword)
		})
	}
	wg.Wait()
	close(errs)

	created := 0
	for err := range errs {
		switch {
		case err == nil:
			created++
		case errors.Is(err, ErrAdminExists):
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if created != 1 {
		t.Errorf("created %d admins, want exactly 1", created)
	}
	if n := countRows(t, db, "users"); n != 1 {
		t.Errorf("users table has %d rows, want 1", n)
	}
}

func TestCreateAdminValidatesInput(t *testing.T) {
	tests := []struct {
		name, username, password string
		want                     error
	}{
		{"empty username", "", goodPassword, ErrInvalidUsername},
		{"username with space", "ad min", goodPassword, ErrInvalidUsername},
		{"username too long", strings.Repeat("a", 65), goodPassword, ErrInvalidUsername},
		{"short password", "admin", "short", ErrWeakPassword},
		{"11 chars", "admin", "abcdefghijk", ErrWeakPassword},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _, _ := newTestService(t)
			if err := svc.CreateAdmin(context.Background(), tt.username, tt.password); !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestStoredPasswordIsArgon2idHash(t *testing.T) {
	svc, db, _ := newTestService(t)
	if err := svc.CreateAdmin(context.Background(), "admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := db.QueryRow(`SELECT password_hash FROM users`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, "$argon2id$v=19$") || strings.Contains(stored, goodPassword) {
		t.Errorf("stored hash %q is not an argon2id PHC string", stored)
	}
}

func TestLoginFailuresAreIndistinguishable(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()
	if err := svc.CreateAdmin(ctx, "admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Login(ctx, "admin", "wrong password!"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("wrong password err = %v, want ErrInvalidCredentials", err)
	}
	if _, err := svc.Login(ctx, "nobody", goodPassword); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("unknown user err = %v, want ErrInvalidCredentials", err)
	}
}

func TestHashingConcurrencyIsBounded(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()
	if err := svc.CreateAdmin(ctx, "admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	var current, peak atomic.Int32
	orig := deriveKey
	t.Cleanup(func() { deriveKey = orig })
	deriveKey = func(password, salt []byte, iterations, memory uint32, threads uint8, keyLen uint32) []byte {
		n := current.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		defer current.Add(-1)
		time.Sleep(20 * time.Millisecond)
		return orig(password, salt, iterations, memory, threads, keyLen)
	}

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			user := "admin"
			if i%2 == 1 {
				user = "nobody" // dummy-hash path must be bounded too
			}
			_, _ = svc.Login(ctx, user, goodPassword)
		}()
	}
	wg.Wait()
	if got := peak.Load(); got > 2 {
		t.Errorf("peak concurrent hashes = %d, want <= 2", got)
	}
}

func TestHashingWaitRespectsContext(t *testing.T) {
	svc, _, _ := newTestService(t)
	if err := svc.CreateAdmin(context.Background(), "admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	for range cap(svc.hashSlots) {
		svc.hashSlots <- struct{}{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := svc.Login(ctx, "admin", goodPassword); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
}

func TestSessionExpires(t *testing.T) {
	svc, db, clock := newTestService(t)
	ctx := context.Background()
	if err := svc.CreateAdmin(ctx, "admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	token, err := svc.Login(ctx, "admin", goodPassword)
	if err != nil {
		t.Fatal(err)
	}

	clock.Advance(SessionTTL - time.Minute)
	if _, err := svc.Authenticate(ctx, token); err != nil {
		t.Fatalf("Authenticate just before expiry: %v", err)
	}

	clock.Advance(time.Minute)
	if _, err := svc.Authenticate(ctx, token); !errors.Is(err, ErrInvalidSession) {
		t.Errorf("Authenticate at expiry err = %v, want ErrInvalidSession", err)
	}
	if n := countRows(t, db, "sessions"); n != 0 {
		t.Errorf("sessions = %d after expiry, want expired row removed", n)
	}
}

func TestLogoutInvalidatesSession(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()
	if err := svc.CreateAdmin(ctx, "admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	token, err := svc.Login(ctx, "admin", goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Logout(ctx, token); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, err := svc.Authenticate(ctx, token); !errors.Is(err, ErrInvalidSession) {
		t.Errorf("Authenticate after logout err = %v, want ErrInvalidSession", err)
	}
}

func TestAuthenticateRejectsUnknownToken(t *testing.T) {
	svc, _, _ := newTestService(t)
	for _, token := range []string{"", "not-a-real-token"} {
		if _, err := svc.Authenticate(context.Background(), token); !errors.Is(err, ErrInvalidSession) {
			t.Errorf("Authenticate(%q) err = %v, want ErrInvalidSession", token, err)
		}
	}
}

const newPassword = "another sturdy passphrase"

func TestChangePasswordKeepsCurrentSessionDropsOthers(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()
	if err := svc.CreateAdmin(ctx, "admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	tokenA, err := svc.Login(ctx, "admin", goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	tokenB, err := svc.Login(ctx, "admin", goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	user, err := svc.Authenticate(ctx, tokenA)
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.ChangePassword(ctx, user.ID, goodPassword, newPassword, tokenA); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	if _, err := svc.Authenticate(ctx, tokenA); err != nil {
		t.Errorf("current session after change: %v, want valid", err)
	}
	if _, err := svc.Authenticate(ctx, tokenB); !errors.Is(err, ErrInvalidSession) {
		t.Errorf("other session after change: %v, want ErrInvalidSession", err)
	}
	if _, err := svc.Login(ctx, "admin", goodPassword); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("login with old password: %v, want ErrInvalidCredentials", err)
	}
	if _, err := svc.Login(ctx, "admin", newPassword); err != nil {
		t.Errorf("login with new password: %v", err)
	}
}

func TestChangePasswordRejectsWithoutSideEffects(t *testing.T) {
	svc, db, _ := newTestService(t)
	ctx := context.Background()
	if err := svc.CreateAdmin(ctx, "admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	tokenA, _ := svc.Login(ctx, "admin", goodPassword)
	tokenB, _ := svc.Login(ctx, "admin", goodPassword)
	user, err := svc.Authenticate(ctx, tokenA)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name          string
		current, next string
		want          error
	}{
		{"wrong current", "not the password", newPassword, ErrInvalidCredentials},
		{"too short", goodPassword, "short", ErrWeakPassword},
		{"same password", goodPassword, goodPassword, ErrSamePassword},
	}
	for _, tc := range cases {
		if err := svc.ChangePassword(ctx, user.ID, tc.current, tc.next, tokenA); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	if _, err := svc.Login(ctx, "admin", goodPassword); err != nil {
		t.Errorf("old password no longer works: %v", err)
	}
	if _, err := svc.Authenticate(ctx, tokenB); err != nil {
		t.Errorf("other session was dropped: %v", err)
	}
	if n := countRows(t, db, "sessions"); n != 3 { // A, B and the login above
		t.Errorf("sessions = %d, want 3", n)
	}
}

// A concurrent change that lands between verifying the old password and
// writing the new hash must win: the late write may not overwrite it.
func TestChangePasswordLosesRaceAgainstConcurrentChange(t *testing.T) {
	svc, db, _ := newTestService(t)
	ctx := context.Background()
	if err := svc.CreateAdmin(ctx, "admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	tokenA, err := svc.Login(ctx, "admin", goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	user, err := svc.Authenticate(ctx, tokenA)
	if err != nil {
		t.Fatal(err)
	}
	const rivalHash = "$argon2id$v=19$m=1024,t=1,p=1$c2FsdHNhbHRzYWx0c2FsdA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	orig := deriveKey
	t.Cleanup(func() { deriveKey = orig })
	deriveKey = func(password, salt []byte, iterations, memory uint32, threads uint8, keyLen uint32) []byte {
		if string(password) == newPassword { // hashing the new password: the rival commits now
			if _, err := db.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, rivalHash, user.ID); err != nil {
				t.Error(err)
			}
		}
		return orig(password, salt, iterations, memory, threads, keyLen)
	}

	err = svc.ChangePassword(ctx, user.ID, goodPassword, newPassword, tokenA)
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("err = %v, want ErrInvalidCredentials", err)
	}
	var got string
	if err := db.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, user.ID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != rivalHash {
		t.Errorf("stored hash was overwritten by the late change: %q", got)
	}
}

func TestResetPasswordCreatesAdminWhenNone(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()
	created, err := svc.ResetPassword(ctx, "root", goodPassword)
	if err != nil || !created {
		t.Fatalf("ResetPassword = %v, %v; want created", created, err)
	}
	if _, err := svc.Login(ctx, "root", goodPassword); err != nil {
		t.Errorf("Login after create: %v", err)
	}
	if name, err := svc.AdminUsername(ctx); err != nil || name != "root" {
		t.Errorf("AdminUsername = %q, %v; want root", name, err)
	}
}

func TestAdminUsernameEmptyWhenNone(t *testing.T) {
	svc, _, _ := newTestService(t)
	if name, err := svc.AdminUsername(context.Background()); err != nil || name != "" {
		t.Errorf("AdminUsername = %q, %v; want empty", name, err)
	}
}

func TestResetPasswordReplacesHashAndRevokesSessions(t *testing.T) {
	svc, db, _ := newTestService(t)
	ctx := context.Background()
	if err := svc.CreateAdmin(ctx, "admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	tok1, err := svc.Login(ctx, "admin", goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	tok2, err := svc.Login(ctx, "admin", goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	created, err := svc.ResetPassword(ctx, "admin", "a brand new passphrase")
	if err != nil || created {
		t.Fatalf("ResetPassword = %v, %v; want created=false", created, err)
	}
	for _, tok := range []string{tok1, tok2} {
		if _, err := svc.Authenticate(ctx, tok); !errors.Is(err, ErrInvalidSession) {
			t.Errorf("old session err = %v, want ErrInvalidSession", err)
		}
	}
	if n := countRows(t, db, "sessions"); n != 0 {
		t.Errorf("sessions left = %d, want 0", n)
	}
	if _, err := svc.Login(ctx, "admin", goodPassword); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("old password err = %v, want ErrInvalidCredentials", err)
	}
	if _, err := svc.Login(ctx, "admin", "a brand new passphrase"); err != nil {
		t.Errorf("new password: %v", err)
	}
}

func TestResetPasswordUsernameMismatch(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()
	if err := svc.CreateAdmin(ctx, "admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResetPassword(ctx, "other", "a brand new passphrase"); !errors.Is(err, ErrUsernameMismatch) {
		t.Fatalf("err = %v, want ErrUsernameMismatch", err)
	}
	if _, err := svc.Login(ctx, "admin", goodPassword); err != nil {
		t.Errorf("original password must still work: %v", err)
	}
}

func TestResetPasswordWeakIsCheckedBeforeHashing(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()
	if err := svc.CreateAdmin(ctx, "admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	var hashes atomic.Int32
	orig := deriveKey
	t.Cleanup(func() { deriveKey = orig })
	deriveKey = func(password, salt []byte, iterations, memory uint32, threads uint8, keyLen uint32) []byte {
		hashes.Add(1)
		return orig(password, salt, iterations, memory, threads, keyLen)
	}
	if _, err := svc.ResetPassword(ctx, "admin", "short"); !errors.Is(err, ErrWeakPassword) {
		t.Errorf("weak err = %v, want ErrWeakPassword", err)
	}
	if _, err := svc.ResetPassword(ctx, "bad name", goodPassword); !errors.Is(err, ErrInvalidUsername) {
		t.Errorf("username err = %v, want ErrInvalidUsername", err)
	}
	if n := hashes.Load(); n != 0 {
		t.Errorf("hashed %d times before validation failed, want 0", n)
	}
}

func TestRevokeAllSessionsCount(t *testing.T) {
	svc, db, _ := newTestService(t)
	ctx := context.Background()
	if err := svc.CreateAdmin(ctx, "admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := svc.Login(ctx, "admin", goodPassword); err != nil {
			t.Fatal(err)
		}
	}
	n, err := svc.RevokeAllSessions(ctx)
	if err != nil || n != 3 {
		t.Fatalf("RevokeAllSessions = %d, %v; want 3", n, err)
	}
	if c := countRows(t, db, "sessions"); c != 0 {
		t.Errorf("sessions left = %d", c)
	}
}
