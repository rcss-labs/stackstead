package auth

import (
	"container/list"
	"net/netip"
	"sync"
	"time"
)

// maxLimiterEntries caps how many clients the limiter tracks, so address
// rotation cannot grow it without bound.
const maxLimiterEntries = 10000

// LoginLimiter blocks a client once it has made maxAttempts login attempts
// within window without a success. The window starts at the first attempt and
// is not extended by later ones. IPv4 clients are tracked per address, IPv6
// clients per /64, since one subscriber normally controls a whole /64.
type LoginLimiter struct {
	mu          sync.Mutex
	maxAttempts int
	window      time.Duration
	now         func() time.Time
	attempts    map[string]*attemptWindow
	// byStart orders entries by window start, oldest first. Windows never
	// extend, so it is also expiry order and eviction only looks at the front.
	byStart *list.List
}

type attemptWindow struct {
	key   string
	count int
	start time.Time
	elem  *list.Element
}

func NewLoginLimiter(maxAttempts int, window time.Duration, now func() time.Time) *LoginLimiter {
	return &LoginLimiter{maxAttempts: maxAttempts, window: window, now: now, attempts: map[string]*attemptWindow{}, byStart: list.New()}
}

// limiterKey maps a client address to its bucket. Unparseable input is used
// as is: the entry cap still bounds it.
func limiterKey(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	addr = addr.Unmap().WithZone("")
	if addr.Is6() {
		return netip.PrefixFrom(addr, 64).Masked().String()
	}
	return addr.String()
}

// Attempt atomically checks and counts a login attempt for ip. It returns false
// when ip's bucket has already used up maxAttempts attempts in the current
// window; otherwise it counts this attempt and returns true. Counting before
// the slow password check means concurrent requests cannot all slip past the limit.
func (l *LoginLimiter) Attempt(ip string) bool {
	key := limiterKey(ip)
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	// Expired entries sit at the front; stop at the first live one.
	for front := l.byStart.Front(); front != nil; front = l.byStart.Front() {
		if w := front.Value.(*attemptWindow); now.Sub(w.start) < l.window {
			break
		}
		l.remove(front.Value.(*attemptWindow))
	}
	w, ok := l.attempts[key]
	if !ok {
		// At the cap the oldest window goes: it has the least time left.
		for len(l.attempts) >= maxLimiterEntries {
			l.remove(l.byStart.Front().Value.(*attemptWindow))
		}
		w = &attemptWindow{key: key, count: 1, start: now}
		w.elem = l.byStart.PushBack(w)
		l.attempts[key] = w
		return true
	}
	if w.count >= l.maxAttempts {
		return false
	}
	w.count++
	return true
}

// Reset forgets ip's bucket; call it after a successful login.
func (l *LoginLimiter) Reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if w, ok := l.attempts[limiterKey(ip)]; ok {
		l.remove(w)
	}
}

func (l *LoginLimiter) remove(w *attemptWindow) {
	l.byStart.Remove(w.elem)
	delete(l.attempts, w.key)
}
