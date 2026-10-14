package auth

import (
	"sync"
	"time"
)

// LoginLimiter blocks an IP once it has made maxFailures login attempts within
// window without a success. The window starts at the first attempt and is not
// extended by later ones.
type LoginLimiter struct {
	mu          sync.Mutex
	maxFailures int
	window      time.Duration
	now         func() time.Time
	failures    map[string]*failureWindow
}

type failureWindow struct {
	count int
	start time.Time
}

func NewLoginLimiter(maxFailures int, window time.Duration, now func() time.Time) *LoginLimiter {
	return &LoginLimiter{maxFailures: maxFailures, window: window, now: now, failures: map[string]*failureWindow{}}
}

// Attempt atomically checks and counts a login attempt for ip. It returns false
// when ip has already used up maxFailures attempts in the current window;
// otherwise it counts this attempt and returns true. Counting before the slow
// password check means concurrent requests cannot all slip past the limit.
func (l *LoginLimiter) Attempt(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	// Drop expired entries so a stream of distinct IPs cannot grow the map forever.
	for key, w := range l.failures {
		if now.Sub(w.start) >= l.window {
			delete(l.failures, key)
		}
	}
	w, ok := l.failures[ip]
	if !ok {
		l.failures[ip] = &failureWindow{count: 1, start: now}
		return true
	}
	if w.count >= l.maxFailures {
		return false
	}
	w.count++
	return true
}

// Reset forgets ip's attempts; call it after a successful login.
func (l *LoginLimiter) Reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, ip)
}
