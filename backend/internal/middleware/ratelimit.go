package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// SlidingWindowLimiter is a tiny in-memory rate limiter keyed by an arbitrary
// string (typically IP or "ip|username"). It keeps the last `limit` hit
// timestamps per key and rejects when the oldest is still within `window`.
//
// It is intentionally process-local: this project assumes a single-binary
// deployment (no horizontal scaling). For multi-replica setups, swap this out
// for a Redis-backed limiter.
type SlidingWindowLimiter struct {
	mu     sync.Mutex
	hits   map[string][]time.Time
	limit  int
	window time.Duration
}

func NewSlidingWindowLimiter(limit int, window time.Duration) *SlidingWindowLimiter {
	return &SlidingWindowLimiter{
		hits:   make(map[string][]time.Time),
		limit:  limit,
		window: window,
	}
}

// Allow returns true if the call is allowed; false if the key has exhausted its budget.
func (l *SlidingWindowLimiter) Allow(key string) bool {
	if l == nil || l.limit <= 0 {
		return true
	}
	now := time.Now()
	cutoff := now.Add(-l.window)

	l.mu.Lock()
	defer l.mu.Unlock()

	bucket := l.hits[key]
	pruned := bucket[:0]
	for _, t := range bucket {
		if t.After(cutoff) {
			pruned = append(pruned, t)
		}
	}
	if len(pruned) >= l.limit {
		l.hits[key] = pruned
		return false
	}
	pruned = append(pruned, now)
	l.hits[key] = pruned
	return true
}

// Cleanup drops bucket entries whose hits have all aged out of the window.
// Caller should run this periodically (e.g. every 10 minutes) to prevent the
// map from growing unbounded across distinct keys (IPs) over the process'
// lifetime. Cheap: holds the lock for one pass over the (typically small) map.
func (l *SlidingWindowLimiter) Cleanup() {
	if l == nil {
		return
	}
	now := time.Now()
	cutoff := now.Add(-l.window)

	l.mu.Lock()
	defer l.mu.Unlock()

	for k, bucket := range l.hits {
		stillFresh := false
		for _, t := range bucket {
			if t.After(cutoff) {
				stillFresh = true
				break
			}
		}
		if !stillFresh {
			delete(l.hits, k)
		}
	}
}

// StartJanitor spawns a goroutine that periodically reclaims stale buckets.
// Returns a cancel function that stops the janitor. Safe to call multiple
// times (each returns its own cancel).
func (l *SlidingWindowLimiter) StartJanitor(every time.Duration) (stop func()) {
	if every <= 0 {
		every = 10 * time.Minute
	}
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				l.Cleanup()
			}
		}
	}()
	return func() { close(done) }
}

// LoginRateLimit returns a Gin middleware that rate-limits requests per client IP.
// On rejection it responds 429 with the project's standard JSON envelope.
func LoginRateLimit(lim *SlidingWindowLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := c.ClientIP()
		if !lim.Allow(key) {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"code": 429,
				"msg":  "too many attempts, please slow down",
			})
			return
		}
		c.Next()
	}
}
