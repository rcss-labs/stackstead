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
