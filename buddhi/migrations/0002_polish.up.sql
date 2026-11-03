-- buddhi polish: returns in KPIs, Redis mirror versions, AI budget reservations,
-- GST 2B insight cache.

-- Sale returns (credit notes) reduce net sales; they are counted separately too.
ALTER TABLE sales_daily ADD COLUMN IF NOT EXISTS returns       INT    NOT NULL DEFAULT 0;
ALTER TABLE sales_daily ADD COLUMN IF NOT EXISTS returns_paise BIGINT NOT NULL DEFAULT 0;

-- One row per (tenant, store, day) and (tenant, 'all', day): bumped in the same
-- transaction as every KPI change. The Redis mirror carries the version it has
-- applied; the dashboard trusts Redis only when the versions agree.
CREATE TABLE IF NOT EXISTS kpi_versions (
    tenant_id UUID   NOT NULL,
    store_key TEXT   NOT NULL,                  -- store uuid, "unassigned" or "all"
    day       DATE   NOT NULL,
    seq       BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (tenant_id, store_key, day)
);

-- Tokens held for AI calls in flight, so concurrent calls cannot overshoot the
-- monthly budget. Settled (deleted) when the call's real usage is logged;
-- expired rows (crashed calls) stop counting after expires_at.
CREATE TABLE IF NOT EXISTS ai_reservations (
    id         BIGSERIAL PRIMARY KEY,
    tenant_id  UUID        NOT NULL,
    feature    TEXT        NOT NULL,
    tokens     BIGINT      NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ai_reservations_tenant ON ai_reservations (tenant_id, expires_at);

-- Latest GSTR-2B vs books insight per GSTIN and period (from kar's matching).
CREATE TABLE IF NOT EXISTS gst_insights (
    tenant_id   UUID        NOT NULL,
    gstin       TEXT        NOT NULL,
    period      TEXT        NOT NULL,           -- MMYYYY
    summary     JSONB       NOT NULL,
    computed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, gstin, period)
);
CREATE INDEX IF NOT EXISTS gst_insights_recent ON gst_insights (tenant_id, computed_at DESC);
