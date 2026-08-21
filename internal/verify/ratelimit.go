package verify

import (
	"sync"
	"time"
)

type IPRateLimiter struct {
	mu      sync.Mutex
	rate    float64
	burst   float64
	buckets map[string]*rlBucket
	maxKeys int
}

type rlBucket struct {
	tokens float64
	last   time.Time
}

func NewIPRateLimiter(ratePerSec float64, burst int) *IPRateLimiter {
	if ratePerSec <= 0 {
		ratePerSec = 1
	}
	if burst < 1 {
		burst = 1
	}
	return &IPRateLimiter{
		rate:    ratePerSec,
		burst:   float64(burst),
		buckets: make(map[string]*rlBucket),
		maxKeys: 65536,
	}
}

func (l *IPRateLimiter) Allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	if len(l.buckets) >= l.maxKeys {
		l.evictLocked(now)
	}

	b, ok := l.buckets[key]
	if !ok {
		b = &rlBucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}

	elapsed := now.Sub(b.last).Seconds()
	b.tokens += elapsed * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now

	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

func (l *IPRateLimiter) evictLocked(now time.Time) {
	horizon := time.Duration(l.burst/l.rate*float64(time.Second)) + time.Minute
	for k, b := range l.buckets {
		if now.Sub(b.last) > horizon {
			delete(l.buckets, k)
		}
	}
	if len(l.buckets) >= l.maxKeys {
		l.buckets = make(map[string]*rlBucket)
	}
}
