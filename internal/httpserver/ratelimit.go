package httpserver

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// verifyRateLimit bounds POST /v1/verify per client IP. #36 calls per-IP
// throttling on this endpoint a hard requirement: with no auth and no build
// pipeline yet to naturally bound volume, an unthrottled endpoint lets a
// single caller grow contract_verification_sources without limit.
const (
	verifyRateLimitPerMinute = 10
	verifyRateLimitWindow    = time.Minute
)

// ipRateLimiter is a simple fixed-window per-key counter. It is intentionally
// not a token bucket: the endpoint it protects is low-volume by design (a
// human submitting contract source), so a coarse per-minute cap is enough to
// stop unbounded growth without the complexity of smoothing bursts.
type ipRateLimiter struct {
	mu        sync.Mutex
	limit     int
	window    time.Duration
	counters  map[string]*windowCounter
	lastSweep time.Time
}

type windowCounter struct {
	count      int
	windowEnds time.Time
}

func newIPRateLimiter(limit int, window time.Duration) *ipRateLimiter {
	return &ipRateLimiter{
		limit:    limit,
		window:   window,
		counters: make(map[string]*windowCounter),
	}
}

// allow reports whether another request from key is permitted in the current
// window, incrementing the count as a side effect.
func (l *ipRateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	l.sweepExpiredLocked(now)

	c, ok := l.counters[key]
	if !ok || now.After(c.windowEnds) {
		c = &windowCounter{count: 0, windowEnds: now.Add(l.window)}
		l.counters[key] = c
	}
	if c.count >= l.limit {
		return false
	}
	c.count++
	return true
}

// sweepExpiredLocked drops entries whose window has already closed, run at
// most once per window so counters does not grow without bound over the
// life of a long-running process. Callers must hold l.mu.
func (l *ipRateLimiter) sweepExpiredLocked(now time.Time) {
	if now.Sub(l.lastSweep) < l.window {
		return
	}
	l.lastSweep = now
	for key, c := range l.counters {
		if now.After(c.windowEnds) {
			delete(l.counters, key)
		}
	}
}

// clientIP extracts the request's source IP for rate-limiting purposes,
// stripping the port from RemoteAddr. It does not trust proxy headers
// (X-Forwarded-For), which are trivially spoofable by the same caller the
// limiter is meant to throttle unless a trusted reverse proxy sanitizes them
// upstream.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// rateLimitMiddleware wraps next with a per-IP rate limit, returning HTTP 429
// once a client exceeds it within the current window.
func rateLimitMiddleware(limiter *ipRateLimiter, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !limiter.allow(clientIP(r)) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = writeRateLimitError(w)
			return
		}
		next(w, r)
	}
}

func writeRateLimitError(w http.ResponseWriter) error {
	_, err := w.Write([]byte(`{"error":"rate limit exceeded, try again later"}`))
	return err
}
