package main

import (
	"crypto/sha256"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// buckets idle this long are dropped, so one-off clients do not pile up
const idleBucketTTL = 10 * time.Minute

// RateLimiter is a token bucket per key, in memory. With several gateway
// instances each one limits on its own; a shared store (Redis) would be the
// next step if that matters.
type RateLimiter struct {
	rate    float64 // tokens per second
	burst   float64
	now     func() time.Time
	mu      sync.Mutex
	buckets map[[32]byte]*bucket
	calls   int
}

type bucket struct {
	tokens float64
	last   time.Time
}

func NewRateLimiter(rate float64, burst int, now func() time.Time) *RateLimiter {
	return &RateLimiter{
		rate:    rate,
		burst:   float64(burst),
		now:     now,
		buckets: make(map[[32]byte]*bucket),
	}
}

// Allow takes one token for key; when there is none it returns how long
// until the next one.
func (l *RateLimiter) Allow(key string) (bool, time.Duration) {
	// keys are hashed: the map must not keep API keys in memory as plain text
	id := sha256.Sum256([]byte(key))
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	l.calls++
	if l.calls%1000 == 0 {
		for k, b := range l.buckets {
			if now.Sub(b.last) > idleBucketTTL {
				delete(l.buckets, k)
			}
		}
	}

	b, ok := l.buckets[id]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[id] = b
	}
	b.tokens = math.Min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now

	if b.tokens < 1 {
		wait := time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
		return false, wait
	}
	b.tokens--
	return true, 0
}

func (l *RateLimiter) Middleware(keyOf func(*http.Request) string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok, wait := l.Allow(keyOf(r))
		if !ok {
			seconds := int(math.Ceil(wait.Seconds()))
			w.Header().Set("Retry-After", strconv.Itoa(max(seconds, 1)))
			writeJSONError(w, http.StatusTooManyRequests, "rate_limited",
				"Too many requests for this API key. Retry later.")
			return
		}
		next.ServeHTTP(w, r)
	})
}
