package verify

import (
	"testing"
	"time"
)

func TestRateLimiterBurstThenReject(t *testing.T) {
	l := NewIPRateLimiter(0.001, 3)
	for i := 0; i < 3; i++ {
		if !l.Allow("1.2.3.4") {
			t.Fatalf("request %d should be allowed within burst", i+1)
		}
	}
	if l.Allow("1.2.3.4") {
		t.Fatal("request beyond burst should be rejected")
	}
}

func TestRateLimiterRefillsOverTime(t *testing.T) {
	l := NewIPRateLimiter(50, 1)
	if !l.Allow("a") {
		t.Fatal("first request should pass")
	}
	if l.Allow("a") {
		t.Fatal("second immediate request should be rejected")
	}
	time.Sleep(30 * time.Millisecond)
	if !l.Allow("a") {
		t.Fatal("request after refill window should pass")
	}
}

func TestRateLimiterKeysAreIndependent(t *testing.T) {
	l := NewIPRateLimiter(0.001, 1)
	if !l.Allow("ip-a") {
		t.Fatal("first key should pass")
	}
	if !l.Allow("ip-b") {
		t.Fatal("second key should be independent")
	}
	if l.Allow("ip-a") {
		t.Fatal("exhausted key should be rejected")
	}
}
