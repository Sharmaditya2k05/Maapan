package gstin

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/taraju/mool/cache"
	"github.com/taraju/mool/db"
)

// Service verifies GSTINs with caching.
type Service struct {
	DB       *pgxpool.Pool
	Redis    *redis.Client // optional
	Provider Provider
	TTL      time.Duration
	Log      *slog.Logger
}

// Verify never fails on a provider outage: it falls back to the checksum result
// so party creation and billing are never blocked by the GST portal.
func (s *Service) Verify(ctx context.Context, raw string) (Result, error) {
	base := checksumResult(raw)
	if !base.Valid || s.Provider.Name() == "checksum_only" {
		return base, nil
	}
	g := base.GSTIN
	key := "kar:gstin:" + g

	if s.Redis != nil {
		var r Result
		if ok, err := cache.GetJSON(ctx, s.Redis, key, &r); err == nil && ok {
			r.Source = "cache"
			return r, nil
		}
	}
	var payload []byte
	err := s.DB.QueryRow(ctx, `SELECT result FROM gstin_cache WHERE gstin=$1 AND fetched_at > now() - make_interval(secs => $2)`,
		g, s.TTL.Seconds()).Scan(&payload)
	if err == nil {
		var r Result
		if json.Unmarshal(payload, &r) == nil {
			r.Source = "cache"
			s.toRedis(ctx, key, r)
			return r, nil
		}
	} else if !db.IsNotFound(err) {
		s.Log.Warn("gstin cache read", "error", err.Error())
	}

	r, err := s.Provider.Lookup(ctx, g)
	if err != nil {
		s.Log.Warn("gstin live lookup failed, using checksum", "gstin", g, "error", err.Error())
		base.Message = "live lookup unavailable; checksum verified only"
		return base, nil
	}
	b, _ := json.Marshal(r)
	if _, err := s.DB.Exec(ctx, `INSERT INTO gstin_cache (gstin, result, source, fetched_at) VALUES ($1,$2,$3,now())
		ON CONFLICT (gstin) DO UPDATE SET result=EXCLUDED.result, source=EXCLUDED.source, fetched_at=now()`,
		g, b, r.Source); err != nil {
		s.Log.Warn("gstin cache write", "error", err.Error())
	}
	s.toRedis(ctx, key, r)
	return r, nil
}

func (s *Service) toRedis(ctx context.Context, key string, r Result) {
	if s.Redis != nil {
		_ = cache.SetJSON(ctx, s.Redis, key, r, s.TTL)
	}
}
