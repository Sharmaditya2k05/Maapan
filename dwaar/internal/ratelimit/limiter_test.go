package ratelimit

import (
	"context"
	"errors"
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

type failing struct{ calls int }

func (f *failing) Allow(context.Context, string, int, time.Duration) (bool, time.Duration, error) {
	f.calls++
	return true, 0, errors.New("redis down")
}

func TestFallbackKeepsLimitingWhenRedisFails(t *testing.T) {
	notified := 0
	p := &failing{}
	f := NewFallback(p, func(error) { notified++ })
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if ok, _, err := f.Allow(ctx, "ip:1", 2, time.Minute); !ok || err != nil {
			t.Fatalf("hit %d: ok=%v err=%v", i+1, ok, err)
		}
	}
	if ok, _, err := f.Allow(ctx, "ip:1", 2, time.Minute); ok || err != nil {
		t.Fatalf("3rd hit must be blocked by the memory fallback: ok=%v err=%v", ok, err)
	}
	if notified != 1 || p.calls != 3 || Mode(f) != "memory-fallback" {
		t.Fatalf("notified=%d calls=%d mode=%s", notified, p.calls, Mode(f))
	}
}
