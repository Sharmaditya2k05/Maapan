package tokens

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/maapan/mool/auth"
)

type fakeKV map[string]string

func (f fakeKV) Set(_ context.Context, k, v string, _ time.Duration) error { f[k] = v; return nil }
func (f fakeKV) GetDel(_ context.Context, k string) (string, bool, error) {
	v, ok := f[k]
	delete(f, k)
	return v, ok, nil
}
func (f fakeKV) Get(_ context.Context, k string) (string, bool, error) {
	v, ok := f[k]
	return v, ok, nil
}
func (f fakeKV) Exists(_ context.Context, k string) (bool, error) { _, ok := f[k]; return ok, nil }
func (f fakeKV) Del(_ context.Context, ks ...string) error {
	for _, k := range ks {
		delete(f, k)
	}
	return nil
}

func TestRotationAndReuseDetection(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	st := &Store{KV: fakeKV{}, Now: func() time.Time { return now }}
	t1, err := st.Issue(ctx, Session{UserID: "u1", TenantID: "t1"})
	if err != nil {
		t.Fatal(err)
	}
	s, t2, err := st.Rotate(ctx, t1)
	if err != nil || s.UserID != "u1" || t2 == t1 {
		t.Fatalf("rotate: %v %+v", err, s)
	}
	// A second tab refreshing with t1 a few seconds later stays logged in,
	// in the same family.
	now = now.Add(5 * time.Second)
	s2, t3, err := st.Rotate(ctx, t1)
	if err != nil || s2.Family != s.Family || t3 == t2 {
		t.Fatalf("concurrent refresh inside the grace window must succeed: %v %+v", err, s2)
	}
	// Replaying t1 after the grace window is reuse: it fails and kills the
	// family, so the live tokens die too.
	now = now.Add(ReuseGrace)
	if _, _, err := st.Rotate(ctx, t1); !errors.Is(err, ErrReuse) {
		t.Fatalf("want ErrReuse, got %v", err)
	}
	if _, _, err := st.Rotate(ctx, t2); !errors.Is(err, ErrInvalid) {
		t.Fatalf("family should be revoked, got %v", err)
	}
	if _, _, err := st.Rotate(ctx, t3); !errors.Is(err, ErrInvalid) {
		t.Fatalf("family should be revoked, got %v", err)
	}
	if _, _, err := st.Rotate(ctx, "garbage"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("garbage: %v", err)
	}
}

func TestRevoke(t *testing.T) {
	ctx := context.Background()
	st := &Store{KV: fakeKV{}, Now: time.Now}
	t1, _ := st.Issue(ctx, Session{UserID: "u1"})
	_, t2, _ := st.Rotate(ctx, t1)
	if err := st.Revoke(ctx, t2); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Rotate(ctx, t2); err == nil {
		t.Fatal("revoked token must not rotate")
	}
}

func TestOnboardingTokenIsNotAnAccessToken(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	tok, err := IssueOnboarding(secret, "u1", "+919876543210", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	c, err := ParseOnboarding(secret, tok)
	if err != nil || c.Subject != "u1" {
		t.Fatalf("parse: %v", err)
	}
	if _, err := auth.Parse(secret, tok); err == nil {
		t.Fatal("onboarding token must be rejected as an access token")
	}
}
