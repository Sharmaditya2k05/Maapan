package cache

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// TestServerCompat checks that the Redis-protocol server at TEST_REDIS_URL
// supports every command Taraju relies on (GETDEL for refresh-token
// rotation, EXPIRE NX for rate-limit windows, pipelines, INCR, PTTL).
// It skips when TEST_REDIS_URL is unset.
func TestServerCompat(t *testing.T) {
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("TEST_REDIS_URL not set")
	}
	ctx := context.Background()
	c, err := Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Set(ctx, "compat:k", "v", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	v, err := c.GetDel(ctx, "compat:k").Result()
	if err != nil || v != "v" {
		t.Fatalf("GETDEL: %q %v", v, err)
	}
	if _, err := c.Get(ctx, "compat:k").Result(); err != redis.Nil {
		t.Fatalf("key must be gone after GETDEL: %v", err)
	}
	for i := 0; i < 3; i++ {
		ok, _, err := Allow(ctx, c, "compat:rl", 2, time.Minute)
		if err != nil {
			t.Fatalf("Allow: %v", err)
		}
		if (i < 2) != ok {
			t.Fatalf("hit %d allowed=%v", i+1, ok)
		}
	}
	_ = c.Del(ctx, "compat:rl").Err()
}
