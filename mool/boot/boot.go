// Package boot wires the pieces every Maapan Go service needs so main.go stays short:
//
//	app, err := boot.New(ctx, boot.Options{Name: "aadesh", DefaultPort: "7004", DB: true, Migrations: migrations.FS, Outbox: true})
//	mux := http.NewServeMux()
//	... register routes ...
//	app.Run(ctx, mux)
package boot

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/cache"
	"github.com/maapan/mool/config"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/events"
	"github.com/maapan/mool/httpx"
	"github.com/maapan/mool/logx"
)

// Options select what the service needs.
type Options struct {
	Name        string
	DefaultPort string
	DB          bool  // connect Postgres (DATABASE_URL)
	Redis       bool  // connect Redis (REDIS_URL)
	Migrations  fs.FS // embedded *.up.sql, applied when AUTO_MIGRATE=true
	Outbox      bool  // start the outbox relay (service publishes events)
}

// App holds the shared dependencies.
type App struct {
	Cfg      config.Base
	Log      *slog.Logger
	DB       *pgxpool.Pool
	Redis    *redis.Client
	Verifier *auth.Verifier
	Pub      events.Publisher
	Relay    *events.Relay
	checks   map[string]httpx.Checker
}

// New loads config, connects dependencies and runs migrations.
func New(ctx context.Context, o Options) (*App, error) {
	cfg := config.LoadBase(o.Name, o.DefaultPort)
	log := logx.New(o.Name, cfg.LogLevel)
	if err := cfg.Validate(o.DB); err != nil {
		return nil, err
	}
	a := &App{
		Cfg: cfg, Log: log,
		Verifier: auth.NewVerifier(cfg.JWTSecret, cfg.ServiceSecret),
		Pub:      events.NewPublisher(cfg, log),
		checks:   map[string]httpx.Checker{},
	}
	if o.DB {
		pool, err := db.Connect(ctx, cfg.DatabaseURL)
		if err != nil {
			return nil, err
		}
		a.DB = pool
		a.checks["postgres"] = func(ctx context.Context) error { return pool.Ping(ctx) }
		if o.Migrations != nil && cfg.AutoMigrate {
			if err := db.Migrate(ctx, pool, o.Migrations, log); err != nil {
				return nil, err
			}
		}
		if o.Outbox {
			a.Relay = events.NewRelay(pool, a.Pub, log)
		}
	}
	if o.Redis {
		rc, err := cache.Connect(ctx, cfg.RedisURL)
		if err != nil {
			return nil, err
		}
		a.Redis = rc
		a.checks["redis"] = func(ctx context.Context) error { return rc.Ping(ctx).Err() }
	}
	return a, nil
}

// Run registers health routes, starts the outbox relay and serves until ctx ends.
func (a *App) Run(ctx context.Context, mux *http.ServeMux) error {
	httpx.Health(mux, a.checks)
	if a.Relay != nil {
		go a.Relay.Run(ctx, 5*time.Second)
	}
	defer a.Close()
	return httpx.Serve(ctx, a.Log, a.Cfg.Port, httpx.Standard(a.Log, a.Cfg.CORSOrigins, mux))
}

// Close releases connections.
func (a *App) Close() {
	if a.DB != nil {
		a.DB.Close()
	}
	if a.Redis != nil {
		_ = a.Redis.Close()
	}
}

// TrustIAMForEvents is true on Cloud Run, where /events is protected by IAM
// (the Pub/Sub push service account) instead of the shared secret.
func (a *App) TrustIAMForEvents() bool { return config.Bool("EVENTS_TRUST_IAM", false) }
