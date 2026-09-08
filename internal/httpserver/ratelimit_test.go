package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRateLimitMiddlewareBlocksExcessRequests(t *testing.T) {
	limiter := newIPRateLimiter(2, time.Minute)
	calls := 0
	h := rateLimitMiddleware(limiter, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/verify", nil)
	req.RemoteAddr = "203.0.113.5:12345"

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if calls != 2 {
		t.Fatalf("handler called %d times, want 2", calls)
	}
}

func TestRateLimitMiddlewareIsolatesByIP(t *testing.T) {
	limiter := newIPRateLimiter(1, time.Minute)
	h := rateLimitMiddleware(limiter, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req1 := httptest.NewRequest(http.MethodPost, "/v1/verify", nil)
	req1.RemoteAddr = "203.0.113.5:12345"
	req2 := httptest.NewRequest(http.MethodPost, "/v1/verify", nil)
	req2.RemoteAddr = "198.51.100.9:54321"

	rec1 := httptest.NewRecorder()
	h(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("ip1 status = %d, want 200", rec1.Code)
	}

	rec2 := httptest.NewRecorder()
	h(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("ip2 status = %d, want 200 (different IP should not be throttled)", rec2.Code)
	}
}
