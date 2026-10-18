package ratelimit

import (
	"context"
	"testing"
	"time"
)

func TestMemoryLimiter(t *testing.T) {
	now := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	m := NewMemory()
	m.now = func() time.Time { return now }
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if ok, _, _ := m.Allow(ctx, "ip:1", 3, time.Minute); !ok {
			t.Fatalf("hit %d should be allowed", i+1)
		}
	}
	ok, retry, _ := m.Allow(ctx, "ip:1", 3, time.Minute)
	if ok || retry != time.Minute {
		t.Fatalf("4th hit must be blocked with 1m retry, got ok=%v retry=%v", ok, retry)
	}
	if ok, _, _ := m.Allow(ctx, "ip:2", 3, time.Minute); !ok {
		t.Fatal("other keys are independent")
	}
	now = now.Add(time.Minute)
	if ok, _, _ := m.Allow(ctx, "ip:1", 3, time.Minute); !ok {
		t.Fatal("new window must reset the count")
	}
}
