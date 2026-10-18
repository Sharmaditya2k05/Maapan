// Package tokens manages opaque refresh tokens (Redis) and short-lived
// onboarding tokens.
//
// Refresh tokens rotate on every use. All tokens descending from one login form
// a family; presenting an already-rotated token means it was stolen (or the
// client misbehaved), so the whole family is revoked.
package tokens

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// RefreshTTL is how long a refresh token lives.
const RefreshTTL = 30 * 24 * time.Hour

var (
	// ErrInvalid means the token is unknown, expired or its family was revoked.
	ErrInvalid = errors.New("invalid refresh token")
	// ErrReuse means a rotated token was presented again; the family is now revoked.
	ErrReuse = errors.New("refresh token reuse detected")
)

// Session is what a refresh token resolves to.
type Session struct {
	UserID   string    `json:"uid"`
	TenantID string    `json:"tid"`
	StoreID  string    `json:"sid"`
	Family   string    `json:"fam"`
	IssuedAt time.Time `json:"iat"`
}

// KV is the subset of Redis the store needs (a fake implements it in tests).
type KV interface {
	Set(ctx context.Context, key, val string, ttl time.Duration) error
	GetDel(ctx context.Context, key string) (string, bool, error)
	Get(ctx context.Context, key string) (string, bool, error)
	Exists(ctx context.Context, key string) (bool, error)
	Del(ctx context.Context, keys ...string) error
}

// Store issues and rotates refresh tokens.
type Store struct {
	KV  KV
	Now func() time.Time
}

func tokenKey(hash string) string { return "pehchaan:rt:" + hash }
func usedKey(hash string) string  { return "pehchaan:rtu:" + hash }
func familyKey(fam string) string { return "pehchaan:rtf:" + fam }
func hashToken(tok string) string { s := sha256.Sum256([]byte(tok)); return hex.EncodeToString(s[:]) }
func newOpaque() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Issue creates a token for s. An empty Family starts a new family (new login).
func (st *Store) Issue(ctx context.Context, s Session) (string, error) {
	if s.Family == "" {
		s.Family = uuid.NewString()
		if err := st.KV.Set(ctx, familyKey(s.Family), s.UserID, RefreshTTL); err != nil {
			return "", err
		}
	}
	s.IssuedAt = st.Now().UTC()
	tok, err := newOpaque()
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(s)
	if err := st.KV.Set(ctx, tokenKey(hashToken(tok)), string(b), RefreshTTL); err != nil {
		return "", err
	}
	return tok, nil
}

// Rotate consumes tok and returns its session plus a replacement token in the same family.
func (st *Store) Rotate(ctx context.Context, tok string) (Session, string, error) {
	s, err := st.consume(ctx, tok)
	if err != nil {
		return Session{}, "", err
	}
	next, err := st.Issue(ctx, s)
	return s, next, err
}

func (st *Store) consume(ctx context.Context, tok string) (Session, error) {
	if tok == "" {
		return Session{}, ErrInvalid
	}
	h := hashToken(tok)
	raw, ok, err := st.KV.GetDel(ctx, tokenKey(h))
	if err != nil {
		return Session{}, err
	}
	if !ok {
		if fam, used, err := st.KV.Get(ctx, usedKey(h)); err == nil && used {
			_ = st.KV.Del(ctx, familyKey(fam))
			return Session{}, ErrReuse
		}
		return Session{}, ErrInvalid
	}
	var s Session
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return Session{}, ErrInvalid
	}
	alive, err := st.KV.Exists(ctx, familyKey(s.Family))
	if err != nil {
		return Session{}, err
	}
	if !alive {
		return Session{}, ErrInvalid
	}
	if err := st.KV.Set(ctx, usedKey(h), s.Family, RefreshTTL); err != nil {
		return Session{}, err
	}
	return s, nil
}

// Revoke ends the family tok belongs to (logout). Unknown tokens are ignored.
func (st *Store) Revoke(ctx context.Context, tok string) error {
	h := hashToken(tok)
	raw, ok, err := st.KV.GetDel(ctx, tokenKey(h))
	if err != nil || !ok {
		return err
	}
	var s Session
	if json.Unmarshal([]byte(raw), &s) == nil && s.Family != "" {
		return st.KV.Del(ctx, familyKey(s.Family))
	}
	return nil
}

// RedisKV adapts go-redis to KV.
type RedisKV struct{ C *redis.Client }

func (r RedisKV) Set(ctx context.Context, key, val string, ttl time.Duration) error {
	return r.C.Set(ctx, key, val, ttl).Err()
}

func (r RedisKV) GetDel(ctx context.Context, key string) (string, bool, error) {
	v, err := r.C.GetDel(ctx, key).Result()
	if err == redis.Nil {
		return "", false, nil
	}
	return v, err == nil, err
}

func (r RedisKV) Get(ctx context.Context, key string) (string, bool, error) {
	v, err := r.C.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", false, nil
	}
	return v, err == nil, err
}

func (r RedisKV) Exists(ctx context.Context, key string) (bool, error) {
	n, err := r.C.Exists(ctx, key).Result()
	return n > 0, err
}

func (r RedisKV) Del(ctx context.Context, keys ...string) error { return r.C.Del(ctx, keys...).Err() }
