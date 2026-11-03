package admin

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	maxLoginFailures = 5
	loginBanDuration = 24 * time.Hour
)

// loginThrottle tracks failed login attempts per client IP and bans an IP for
// a fixed window once the failure threshold is reached. It is intentionally
// in-memory so that the login endpoints stay available even when the database
// is unreachable; a restart simply clears outstanding bans.
type loginThrottle struct {
	mu      sync.Mutex
	entries map[string]loginEntry
}

type loginEntry struct {
	failures    int
	lastAttempt time.Time
	bannedUntil time.Time
}

func newLoginThrottle() *loginThrottle {
	return &loginThrottle{entries: make(map[string]loginEntry)}
}

// allow reports whether the IP may attempt a login right now.
func (t *loginThrottle) allow(ip string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	entry, ok := t.entries[ip]
	if !ok {
		return true
	}
	if entry.bannedUntil.After(time.Now()) {
		return false
	}
	// Ban has expired, drop the stale entry.
	delete(t.entries, ip)
	return true
}

// recordFailure records a failed attempt and reports whether the IP is now
// banned as a result.
func (t *loginThrottle) recordFailure(ip string) bool {
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()

	entry := t.entries[ip]
	entry.failures++
	entry.lastAttempt = now
	if entry.failures >= maxLoginFailures {
		entry.bannedUntil = now.Add(loginBanDuration)
		entry.failures = 0
	}
	t.entries[ip] = entry
	t.cleanupLocked(now)
	return entry.bannedUntil.After(now)
}

// recordSuccess clears any tracked failures for the IP.
func (t *loginThrottle) recordSuccess(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.entries, ip)
}

func (t *loginThrottle) cleanupLocked(now time.Time) {
	if len(t.entries) < 1024 {
		return
	}
	for ip, entry := range t.entries {
		if entry.lastAttempt.Before(now.Add(-loginBanDuration)) {
			delete(t.entries, ip)
		}
	}
}

// clientIP returns the best-effort client IP, honouring the common reverse
// proxy headers before falling back to the connection's remote address.
func clientIP(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		if ip := strings.TrimSpace(parts[0]); ip != "" {
			return ip
		}
	}
	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
		return realIP
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
