// Package cache wraps Redis (Memorystore on Cloud Run) with the helpers Maapan
// needs: fixed-window rate limiting and small JSON caching.
package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Connect parses REDIS_URL (redis://[:password@]host:port/db) and pings.
func Connect(ctx context.Context, url string) (*redis.Client, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse REDIS_URL: %w", err)
	}
	c := redis.NewClient(opt)
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := c.Ping(pctx).Err(); err != nil {
		return nil, fmt.Errorf("redis ping: %w", err)
	}
	return c, nil
}

// Allow is a fixed-window limiter: at most limit hits per window for key.
// It returns whether the hit is allowed and how long until the window resets.
func Allow(ctx context.Context, c *redis.Client, key string, limit int, window time.Duration) (bool, time.Duration, error) {
	pipe := c.TxPipeline()
	incr := pipe.Incr(ctx, key)
	pipe.ExpireNX(ctx, key, window)
	ttl := pipe.PTTL(ctx, key)
	if _, err := pipe.Exec(ctx); err != nil {
		return true, 0, err // fail open: never block billing because Redis blinked
	}
	return incr.Val() <= int64(limit), ttl.Val(), nil
}

// GetJSON loads key into dst. ok=false when the key is missing.
func GetJSON(ctx context.Context, c *redis.Client, key string, dst any) (bool, error) {
	b, err := c.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(b, dst)
}

// SetJSON stores v under key for ttl.
func SetJSON(ctx context.Context, c *redis.Client, key string, v any, ttl time.Duration) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.Set(ctx, key, b, ttl).Err()
}
