package service

import (
	"sync"
	"time"
)

const (
	defaultLoginMaxFailures = 10
	defaultLoginWindow      = time.Minute
	maxLimiterKeys          = 4096
)

type failureBucket struct {
	count   int
	resetAt time.Time
}

// loginRateLimiter enforces a bounded per-client failure window without background goroutines.
type loginRateLimiter struct {
	mu          sync.Mutex
	maxFailures int
	window      time.Duration
	maxKeys     int
	buckets     map[string]failureBucket
}

func newLoginRateLimiter(maxFailures int, window time.Duration) *loginRateLimiter {
	return &loginRateLimiter{
		maxFailures: maxFailures,
		window:      window,
		maxKeys:     maxLimiterKeys,
		buckets:     make(map[string]failureBucket),
	}
}

func (l *loginRateLimiter) check(key string, now time.Time) (time.Duration, bool) {
	if l == nil || key == "" {
		return 0, true
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	bucket, exists := l.buckets[key]
	if !exists || !now.Before(bucket.resetAt) {
		delete(l.buckets, key)
		return 0, true
	}
	if bucket.count < l.maxFailures {
		return 0, true
	}

	return bucket.resetAt.Sub(now), false
}

func (l *loginRateLimiter) recordFailure(key string, now time.Time) {
	if l == nil || key == "" {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	bucket, exists := l.buckets[key]
	if !exists || !now.Before(bucket.resetAt) {
		l.ensureCapacity(now)
		l.buckets[key] = failureBucket{count: 1, resetAt: now.Add(l.window)}
		return
	}

	bucket.count++
	l.buckets[key] = bucket
}

func (l *loginRateLimiter) reset(key string) {
	if l == nil || key == "" {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, key)
}

func (l *loginRateLimiter) ensureCapacity(now time.Time) {
	if len(l.buckets) < l.maxKeys {
		return
	}

	for key, bucket := range l.buckets {
		if !now.Before(bucket.resetAt) {
			delete(l.buckets, key)
		}
	}
	if len(l.buckets) < l.maxKeys {
		return
	}

	for key := range l.buckets {
		delete(l.buckets, key)
		return
	}
}
