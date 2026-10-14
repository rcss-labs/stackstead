package auth

import (
	"sync"
	"time"
)

// LoginLimiter blocks an IP once it reaches maxFailures failed logins within
// window. The window starts at the first failure and is not extended by later ones.
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

func (l *LoginLimiter) Allowed(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	w, ok := l.failures[ip]
	if !ok {
		return true
	}
	if l.now().Sub(w.start) >= l.window {
		delete(l.failures, ip)
		return true
	}
	return w.count < l.maxFailures
}

func (l *LoginLimiter) RecordFailure(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	// Drop expired entries so a stream of distinct IPs cannot grow the map forever.
	for key, w := range l.failures {
		if now.Sub(w.start) >= l.window {
			delete(l.failures, key)
		}
	}
	if w, ok := l.failures[ip]; ok {
		w.count++
		return
	}
	l.failures[ip] = &failureWindow{count: 1, start: now}
}

func (l *LoginLimiter) Reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, ip)
}
