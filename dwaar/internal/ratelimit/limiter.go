// Package ratelimit provides fixed-window rate limiting backed by Redis
// (Memorystore on Cloud Run) or process memory (local development).
package ratelimit

import (
	"context"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/maapan/mool/cache"
)

// Limiter decides whether one more hit for key is allowed within window.
type Limiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (ok bool, retryAfter time.Duration, err error)
}

// Redis limits across all gateway instances.
type Redis struct{ Client *redis.Client }

// Allow implements Limiter.
func (r *Redis) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, time.Duration, error) {
	return cache.Allow(ctx, r.Client, "dwaar:rl:"+key, limit, window)
}

// Memory limits within one process. Good enough for local runs and tests.
type Memory struct {
	mu      sync.Mutex
	windows map[string]*memWindow
	now     func() time.Time
	sweeps  int
}

type memWindow struct {
	count int
	reset time.Time
}

// NewMemory builds an in-memory limiter.
func NewMemory() *Memory {
	return &Memory{windows: map[string]*memWindow{}, now: time.Now}
}

// Allow implements Limiter.
func (m *Memory) Allow(_ context.Context, key string, limit int, window time.Duration) (bool, time.Duration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	w, ok := m.windows[key]
	if !ok || !now.Before(w.reset) {
		w = &memWindow{reset: now.Add(window)}
		m.windows[key] = w
	}
	w.count++
	m.sweeps++
	if m.sweeps >= 10000 {
		m.sweeps = 0
		for k, v := range m.windows {
			if !now.Before(v.reset) {
				delete(m.windows, k)
			}
		}
	}
	return w.count <= limit, w.reset.Sub(now), nil
}

// Fallback uses Primary (Redis) and switches to Secondary (process memory)
// whenever Primary errors, so a Redis outage never removes the limits. While
// Redis is down every instance limits on its own, so the effective limit is
// up to N times the configured one with N instances — far better than none.
type Fallback struct {
	Primary   Limiter
	Secondary Limiter
	// OnFallback, when set, is called once each time the primary fails after
	// having worked (and once at the first failure).
	OnFallback func(err error)

	mu       sync.Mutex
	degraded bool
}

// NewFallback builds a Redis-first limiter with an in-memory fallback.
func NewFallback(primary Limiter, onFallback func(error)) *Fallback {
	return &Fallback{Primary: primary, Secondary: NewMemory(), OnFallback: onFallback}
}

// Allow implements Limiter. It never returns an error from the primary.
func (f *Fallback) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, time.Duration, error) {
	ok, retry, err := f.Primary.Allow(ctx, key, limit, window)
	f.mu.Lock()
	was := f.degraded
	f.degraded = err != nil
	f.mu.Unlock()
	if err == nil {
		return ok, retry, nil
	}
	if !was && f.OnFallback != nil {
		f.OnFallback(err)
	}
	return f.Secondary.Allow(ctx, key, limit, window)
}

// Degraded reports whether the last decision came from the fallback.
func (f *Fallback) Degraded() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.degraded
}

// Mode describes a limiter for /api/_status: "redis", "memory" or
// "memory-fallback" (Redis configured but failing).
func Mode(l Limiter) string {
	switch v := l.(type) {
	case *Fallback:
		if v.Degraded() {
			return "memory-fallback"
		}
		return "redis"
	case *Redis:
		return "redis"
	case *Memory:
		return "memory"
	case nil:
		return "none"
	}
	return "custom"
}
