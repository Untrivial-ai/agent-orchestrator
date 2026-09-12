package httpapi

import (
	"net"
	"net/http"
	"sync"
	"time"
)

type attemptWindow struct {
	start time.Time
	count int
}

type fixedWindowLimiter struct {
	mutex      sync.Mutex
	attempts   map[string]attemptWindow
	limit      int
	window     time.Duration
	maxEntries int
}

func newFixedWindowLimiter(
	limit int,
	window time.Duration,
	maxEntries int,
) *fixedWindowLimiter {
	return &fixedWindowLimiter{
		attempts:   make(map[string]attemptWindow),
		limit:      limit,
		window:     window,
		maxEntries: maxEntries,
	}
}

func (l *fixedWindowLimiter) allow(key string, now time.Time) bool {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	attempt := l.attempts[key]
	if attempt.start.IsZero() || now.Sub(attempt.start) >= l.window {
		attempt = attemptWindow{start: now}
	}
	if attempt.count >= l.limit {
		return false
	}
	attempt.count++
	if _, exists := l.attempts[key]; !exists && len(l.attempts) >= l.maxEntries {
		l.evict(now)
	}
	l.attempts[key] = attempt
	return true
}

func (l *fixedWindowLimiter) evict(now time.Time) {
	for key, attempt := range l.attempts {
		if now.Sub(attempt.start) >= l.window {
			delete(l.attempts, key)
		}
	}
	for len(l.attempts) >= l.maxEntries {
		for key := range l.attempts {
			delete(l.attempts, key)
			break
		}
	}
}

func localAuthRateLimitKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return r.URL.Path + "|" + host
}
