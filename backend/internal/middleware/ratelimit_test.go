package middleware

import (
	"testing"
	"time"
)

func TestSlidingWindowLimiter_AllowsUpToLimit(t *testing.T) {
	lim := NewSlidingWindowLimiter(3, time.Second)
	for i := 0; i < 3; i++ {
		if !lim.Allow("k") {
			t.Errorf("call %d unexpectedly denied", i+1)
		}
	}
	if lim.Allow("k") {
		t.Error("4th call should be denied")
	}
}

func TestSlidingWindowLimiter_KeysAreIndependent(t *testing.T) {
	lim := NewSlidingWindowLimiter(1, time.Second)
	if !lim.Allow("a") {
		t.Fatal("first call to a denied")
	}
	if !lim.Allow("b") {
		t.Fatal("first call to b denied")
	}
	if lim.Allow("a") {
		t.Error("second call to a should be denied")
	}
}

func TestSlidingWindowLimiter_RecoversAfterWindow(t *testing.T) {
	lim := NewSlidingWindowLimiter(1, 50*time.Millisecond)
	if !lim.Allow("k") {
		t.Fatal("first call denied")
	}
	if lim.Allow("k") {
		t.Fatal("second call within window should be denied")
	}
	time.Sleep(80 * time.Millisecond)
	if !lim.Allow("k") {
		t.Error("call after window should be allowed again")
	}
}

func TestSlidingWindowLimiter_NilOrZeroLimitIsNoop(t *testing.T) {
	var nilLim *SlidingWindowLimiter
	for i := 0; i < 100; i++ {
		if !nilLim.Allow("k") {
			t.Fatal("nil limiter should always allow")
		}
	}
	zero := NewSlidingWindowLimiter(0, time.Second)
	for i := 0; i < 100; i++ {
		if !zero.Allow("k") {
			t.Fatal("zero limit should always allow")
		}
	}
}

func TestSlidingWindowLimiter_CleanupDropsStaleBuckets(t *testing.T) {
	lim := NewSlidingWindowLimiter(5, 30*time.Millisecond)
	for _, k := range []string{"a", "b", "c"} {
		if !lim.Allow(k) {
			t.Fatalf("first call to %s denied", k)
		}
	}
	if got := len(lim.hits); got != 3 {
		t.Fatalf("expected 3 buckets, got %d", got)
	}

	time.Sleep(60 * time.Millisecond)
	lim.Cleanup()
	if got := len(lim.hits); got != 0 {
		t.Errorf("expected all buckets reclaimed, got %d", got)
	}

	// after cleanup the limiter still works
	if !lim.Allow("a") {
		t.Error("post-cleanup call should be allowed")
	}
}
