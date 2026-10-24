package gstin

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/taraju/kar/internal/gsp"
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
// so party creation and billing are never blocked by the GST portal. That
// fallback is reported as source "checksum_only" with portal_verified=false;
// it is never cached and never presented as a portal answer.
func (s *Service) Verify(ctx context.Context, raw string) (Result, error) {
	base := checksumResult(raw)
	if !base.Valid || s.Provider == nil || s.Provider.Name() == SourceChecksumOnly {
		return base, nil
	}
	g := base.GSTIN
	key := "kar:gstin:" + g

	if s.Redis != nil {
		var r Result
		if ok, err := cache.GetJSON(ctx, s.Redis, key, &r); err == nil && ok && r.PortalVerified {
			r.Source = SourceCache
			return r, nil
		}
	}
	if s.DB != nil {
		if r, ok := s.fromDB(ctx, g); ok {
			s.toRedis(ctx, key, r)
			return r, nil
		}
	}
	return s.live(ctx, base, key)
}

func (s *Service) fromDB(ctx context.Context, g string) (Result, bool) {
	var payload []byte
	err := s.DB.QueryRow(ctx, `SELECT result FROM gstin_cache WHERE gstin=$1 AND source=$3
		AND fetched_at > now() - make_interval(secs => $2)`, g, s.TTL.Seconds(), SourceGSP).Scan(&payload)
	if err == nil {
		var r Result
		if json.Unmarshal(payload, &r) == nil && r.PortalVerified {
			r.Source = SourceCache
			return r, true
		}
	} else if !db.IsNotFound(err) {
		s.log().Warn("gstin cache read", "error", err.Error())
	}
	return Result{}, false
}

func (s *Service) live(ctx context.Context, base Result, key string) (Result, error) {
	g := base.GSTIN
	r, err := s.Provider.Lookup(ctx, g)
	if err != nil {
		if gsp.IsKind(err, gsp.KindNotFound) {
			base.Valid = false
			base.B2BAllowed = false
			base.Message = "checksum is valid but the GST portal has no taxpayer with this GSTIN"
			return base, nil
		}
		s.log().Warn("gstin live lookup failed, answering from checksum only", "gstin", g, "error", err.Error())
		base.Message = "GST portal lookup unavailable: format and checksum valid, NOT verified on the portal"
		return base, nil
	}
	b, _ := json.Marshal(r)
	if s.DB == nil {
		s.toRedis(ctx, key, r)
		return r, nil
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO gstin_cache (gstin, result, source, fetched_at) VALUES ($1,$2,$3,now())
		ON CONFLICT (gstin) DO UPDATE SET result=EXCLUDED.result, source=EXCLUDED.source, fetched_at=now()`,
		g, b, r.Source); err != nil {
		s.log().Warn("gstin cache write", "error", err.Error())
	}
	s.toRedis(ctx, key, r)
	return r, nil
}

func (s *Service) toRedis(ctx context.Context, key string, r Result) {
	if s.Redis != nil {
		ttl := s.TTL
		if !r.B2BAllowed {
			ttl = s.TTL / 7 // re-check cancelled/suspended GSTINs sooner (revocation)
		}
		_ = cache.SetJSON(ctx, s.Redis, key, r, ttl)
	}
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}
