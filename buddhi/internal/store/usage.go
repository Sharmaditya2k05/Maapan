// Package store holds buddhi's Postgres access for tenant AI settings, token
// usage and the monthly budget guard.
package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maapan/buddhi/internal/llm"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/httpx"
)

// IST is India Standard Time; budgets reset on the 1st of the month IST.
var IST = time.FixedZone("IST", 5*3600+1800)

// Settings is a tenant's AI configuration.
type Settings struct {
	TenantID           string    `json:"tenant_id"`
	AIEnabled          bool      `json:"ai_enabled"`
	MonthlyTokenBudget *int64    `json:"monthly_token_budget,omitempty"`
	OwnerPhone         *string   `json:"owner_phone,omitempty"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// Guard enforces the feature flag and monthly token budget, and records usage.
type Guard struct {
	DB            *pgxpool.Pool
	DefaultBudget int64 // AI_MONTHLY_TOKEN_BUDGET; 0 = unlimited
	RequireFlag   bool  // AI_REQUIRE_TENANT_FLAG; pilots only see AI when ai_enabled
	Now           func() time.Time
}

// Settings returns the tenant's settings (zero value if never saved).
func (g *Guard) Settings(ctx context.Context, tenantID string) (Settings, error) {
	s := Settings{TenantID: tenantID}
	err := g.DB.QueryRow(ctx, `SELECT ai_enabled, monthly_token_budget, owner_phone, updated_at
		FROM tenant_settings WHERE tenant_id=$1`, tenantID).Scan(&s.AIEnabled, &s.MonthlyTokenBudget, &s.OwnerPhone, &s.UpdatedAt)
	if db.IsNotFound(err) {
		return s, nil
	}
	return s, err
}

// SaveSettings upserts the tenant's settings.
func (g *Guard) SaveSettings(ctx context.Context, s Settings) (Settings, error) {
	err := g.DB.QueryRow(ctx, `INSERT INTO tenant_settings (tenant_id, ai_enabled, monthly_token_budget, owner_phone)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (tenant_id) DO UPDATE SET ai_enabled=$2, monthly_token_budget=$3,
			owner_phone=COALESCE($4, tenant_settings.owner_phone), updated_at=now()
		RETURNING updated_at, owner_phone`, s.TenantID, s.AIEnabled, s.MonthlyTokenBudget, s.OwnerPhone).Scan(&s.UpdatedAt, &s.OwnerPhone)
	return s, err
}

// MonthStart is the first instant of the current IST month.
func (g *Guard) MonthStart() time.Time {
	n := g.Now().In(IST)
	return time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, IST)
}

// MonthUsage returns tokens used by the tenant this month.
func (g *Guard) MonthUsage(ctx context.Context, tenantID string) (int64, error) {
	var used int64
	err := g.DB.QueryRow(ctx, `SELECT COALESCE(sum(input_tokens+output_tokens+cache_read_tokens+cache_write_tokens),0)::bigint
		FROM ai_usage WHERE tenant_id=$1 AND created_at >= $2`, tenantID, g.MonthStart()).Scan(&used)
	return used, err
}

// Budget returns the effective monthly budget (0 = unlimited).
func (g *Guard) Budget(s Settings) int64 {
	if s.MonthlyTokenBudget != nil {
		return *s.MonthlyTokenBudget
	}
	return g.DefaultBudget
}

// CheckFlag enforces only the pilot flag. Non-LLM AI features (GST insights,
// re-order suggestions, item matching) use it: they cost no tokens.
func (g *Guard) CheckFlag(ctx context.Context, tenantID string) error {
	s, err := g.Settings(ctx, tenantID)
	if err != nil {
		return err
	}
	return g.flag(s)
}

func (g *Guard) flag(s Settings) error {
	if g.RequireFlag && !s.AIEnabled {
		return httpx.Forbidden("AI features are not enabled for this business yet (pilot flag ai_enabled)")
	}
	return nil
}

// Token estimates reserved before each kind of call. A call that ends up
// cheaper frees the difference when it settles; the ask agent loop also stops
// calling tools once it has spent its reservation (llm.ChatRequest.TokenBudget).
const (
	EstimateScan   int64 = 40_000 // one image/PDF + ≤16k output
	EstimateAsk    int64 = 80_000 // up to 6 tool turns re-sending the context
	EstimateWeekly int64 = 8_000
)

// ReservationTTL bounds how long a crashed call's reservation keeps counting.
const ReservationTTL = 15 * time.Minute

// Reservation holds tokens for one AI call until Settle.
type Reservation struct {
	ID       int64 // 0 = nothing held (unlimited budget)
	TenantID string
	Feature  string
	Tokens   int64
}

// Reserve checks the pilot flag and holds estimate tokens against the monthly
// budget before an AI call. Concurrent calls serialise per tenant on an
// advisory lock, so used + in-flight reservations can never pass the budget.
func (g *Guard) Reserve(ctx context.Context, tenantID, feature string, estimate int64) (*Reservation, error) {
	s, err := g.Settings(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if err := g.flag(s); err != nil {
		return nil, err
	}
	r := &Reservation{TenantID: tenantID, Feature: feature, Tokens: max(estimate, 0)}
	budget := g.Budget(s)
	if budget <= 0 {
		return r, nil
	}
	err = db.WithTx(ctx, g.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('buddhi:ai-budget:' || $1::text, 0))`, tenantID); err != nil {
			return err
		}
		now := g.Now()
		if _, err := tx.Exec(ctx, `DELETE FROM ai_reservations WHERE tenant_id=$1 AND expires_at <= $2`, tenantID, now); err != nil {
			return err
		}
		var used, held int64
		if err := tx.QueryRow(ctx, `SELECT
				COALESCE((SELECT sum(input_tokens+output_tokens+cache_read_tokens+cache_write_tokens)
					FROM ai_usage WHERE tenant_id=$1 AND created_at >= $2),0)::bigint,
				COALESCE((SELECT sum(tokens) FROM ai_reservations WHERE tenant_id=$1 AND expires_at > $3),0)::bigint`,
			tenantID, g.MonthStart(), now).Scan(&used, &held); err != nil {
			return err
		}
		if used >= budget {
			return httpx.TooMany("monthly AI token budget used up (%d of %d tokens); it resets on the 1st", used, budget)
		}
		if left := budget - used - held; r.Tokens > left {
			return httpx.TooMany("not enough AI token budget left for this request (about %d needed, %d free of %d this month; %d held by requests in progress); it resets on the 1st",
				r.Tokens, max(left, 0), budget, held).WithDetails(map[string]any{
				"needed": r.Tokens, "free": max(left, 0), "used": used, "in_progress": held, "budget": budget})
		}
		return tx.QueryRow(ctx, `INSERT INTO ai_reservations (tenant_id, feature, tokens, expires_at) VALUES ($1,$2,$3,$4) RETURNING id`,
			tenantID, feature, r.Tokens, now.Add(ReservationTTL)).Scan(&r.ID)
	})
	if err != nil {
		return nil, err
	}
	return r, nil
}

// Settle logs the call's real usage and releases its reservation in one
// transaction. Failed calls are logged too (they still cost tokens). A nil
// reservation only records.
func (g *Guard) Settle(ctx context.Context, r *Reservation, userID string, u llm.Usage, ok bool) error {
	if r == nil {
		return nil
	}
	return db.WithTx(ctx, g.DB, func(tx pgx.Tx) error {
		if r.ID != 0 {
			if _, err := tx.Exec(ctx, `DELETE FROM ai_reservations WHERE id=$1`, r.ID); err != nil {
				return err
			}
		}
		return record(ctx, tx, r.TenantID, userID, r.Feature, u, ok)
	})
}

// Record logs one feature call's usage without a reservation.
func (g *Guard) Record(ctx context.Context, tenantID, userID, feature string, u llm.Usage, ok bool) error {
	return record(ctx, g.DB, tenantID, userID, feature, u, ok)
}

func record(ctx context.Context, q db.Querier, tenantID, userID, feature string, u llm.Usage, ok bool) error {
	if u.Total() == 0 && u.Model == "" {
		return nil
	}
	_, err := q.Exec(ctx, `INSERT INTO ai_usage (tenant_id, user_id, feature, model, input_tokens, output_tokens,
		cache_read_tokens, cache_write_tokens, ok) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		tenantID, userID, feature, u.Model, u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens, ok)
	return err
}

// Held returns tokens currently reserved by calls in progress.
func (g *Guard) Held(ctx context.Context, tenantID string) (int64, error) {
	var held int64
	err := g.DB.QueryRow(ctx, `SELECT COALESCE(sum(tokens),0)::bigint FROM ai_reservations WHERE tenant_id=$1 AND expires_at > $2`,
		tenantID, g.Now()).Scan(&held)
	return held, err
}

// FeatureUsage is one row of the usage report.
type FeatureUsage struct {
	Feature      string `json:"feature"`
	Model        string `json:"model"`
	Calls        int64  `json:"calls"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	CacheRead    int64  `json:"cache_read_tokens"`
	CacheWrite   int64  `json:"cache_write_tokens"`
}

// Report summarises this month's usage by feature and model.
func (g *Guard) Report(ctx context.Context, tenantID string) ([]FeatureUsage, error) {
	rows, err := g.DB.Query(ctx, `SELECT feature, model, count(*), sum(input_tokens)::bigint, sum(output_tokens)::bigint,
		sum(cache_read_tokens)::bigint, sum(cache_write_tokens)::bigint
		FROM ai_usage WHERE tenant_id=$1 AND created_at >= $2 GROUP BY feature, model ORDER BY feature, model`,
		tenantID, g.MonthStart())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FeatureUsage{}
	for rows.Next() {
		var f FeatureUsage
		if err := rows.Scan(&f.Feature, &f.Model, &f.Calls, &f.InputTokens, &f.OutputTokens, &f.CacheRead, &f.CacheWrite); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// EnabledTenants lists tenants with AI turned on (for scheduled jobs).
func (g *Guard) EnabledTenants(ctx context.Context) ([]Settings, error) {
	rows, err := g.DB.Query(ctx, `SELECT tenant_id, ai_enabled, monthly_token_budget, owner_phone, updated_at
		FROM tenant_settings WHERE ai_enabled ORDER BY tenant_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Settings
	for rows.Next() {
		var s Settings
		if err := rows.Scan(&s.TenantID, &s.AIEnabled, &s.MonthlyTokenBudget, &s.OwnerPhone, &s.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
