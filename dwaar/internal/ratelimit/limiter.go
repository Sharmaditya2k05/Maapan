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
