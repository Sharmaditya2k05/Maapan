// Package store holds buddhi's Postgres access for tenant AI settings, token
// usage and the monthly budget guard.
package store

import (
	"context"
	"time"

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

// Check returns an API error if the tenant may not use AI right now.
func (g *Guard) Check(ctx context.Context, tenantID string) error {
	s, err := g.Settings(ctx, tenantID)
	if err != nil {
		return err
	}
	if g.RequireFlag && !s.AIEnabled {
		return httpx.Forbidden("AI features are not enabled for this business yet (pilot flag ai_enabled)")
	}
	budget := g.Budget(s)
	if budget <= 0 {
		return nil
	}
	used, err := g.MonthUsage(ctx, tenantID)
	if err != nil {
		return err
	}
	if used >= budget {
		return httpx.TooMany("monthly AI token budget used up (%d of %d tokens); it resets on the 1st", used, budget)
	}
	return nil
}

// Record logs one feature call's usage. Failed calls are recorded too (they still cost tokens).
func (g *Guard) Record(ctx context.Context, tenantID, userID, feature string, u llm.Usage, ok bool) error {
	if u.Total() == 0 && u.Model == "" {
		return nil
	}
	_, err := g.DB.Exec(ctx, `INSERT INTO ai_usage (tenant_id, user_id, feature, model, input_tokens, output_tokens,
		cache_read_tokens, cache_write_tokens, ok) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		tenantID, userID, feature, u.Model, u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens, ok)
	return err
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
