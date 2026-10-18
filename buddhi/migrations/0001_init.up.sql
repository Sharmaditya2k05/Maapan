-- buddhi: AI usage & budgets, KPI rollups, forecasts, anomalies

CREATE TABLE IF NOT EXISTS processed_events (
    event_id     TEXT PRIMARY KEY,
    event_type   TEXT NOT NULL,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Per-tenant feature flag + budget override. AI features are off until enabled.
CREATE TABLE tenant_settings (
    tenant_id            UUID PRIMARY KEY,
    ai_enabled           BOOLEAN NOT NULL DEFAULT false,
    monthly_token_budget BIGINT,                 -- NULL = use AI_MONTHLY_TOKEN_BUDGET
    owner_phone          TEXT,                   -- cached for the weekly WhatsApp summary
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE ai_usage (
    id                 BIGSERIAL PRIMARY KEY,
    tenant_id          UUID NOT NULL,
    user_id            TEXT NOT NULL,
    feature            TEXT NOT NULL,            -- scan | ask | weekly_summary
    model              TEXT NOT NULL,
    input_tokens       BIGINT NOT NULL DEFAULT 0,
    output_tokens      BIGINT NOT NULL DEFAULT 0,
    cache_read_tokens  BIGINT NOT NULL DEFAULT 0,
    cache_write_tokens BIGINT NOT NULL DEFAULT 0,
    ok                 BOOLEAN NOT NULL DEFAULT true,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX ai_usage_tenant_month ON ai_usage (tenant_id, created_at);

-- Daily sales rollup per store (source of truth; Redis is the hot cache).
CREATE TABLE sales_daily (
    tenant_id         UUID NOT NULL,
    store_id          UUID NOT NULL,
    day               DATE NOT NULL,
    sales_paise       BIGINT NOT NULL DEFAULT 0, -- grand totals of sale invoices
    taxable_paise     BIGINT NOT NULL DEFAULT 0,
    tax_paise         BIGINT NOT NULL DEFAULT 0,
    bills             INT    NOT NULL DEFAULT 0,
    cancelled         INT    NOT NULL DEFAULT 0,
    margin_paise      BIGINT NOT NULL DEFAULT 0, -- only over lines whose cost is known
    margin_base_paise BIGINT NOT NULL DEFAULT 0, -- taxable value of those lines
    PRIMARY KEY (tenant_id, store_id, day)
);

CREATE TABLE item_sales_daily (
    tenant_id   UUID NOT NULL,
    store_id    UUID NOT NULL,
    item_key    TEXT NOT NULL,                   -- item_id, or "name:<lower name>" for ad-hoc lines
    day         DATE NOT NULL,
    item_id     UUID,
    name        TEXT NOT NULL,
    qty         NUMERIC(14,3) NOT NULL DEFAULT 0,
    sales_paise BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (tenant_id, store_id, item_key, day)
);
CREATE INDEX item_sales_daily_day ON item_sales_daily (tenant_id, store_id, day);

CREATE TABLE payments_daily (
    tenant_id    UUID NOT NULL,
    store_id     TEXT NOT NULL,                  -- "unassigned" when a gateway payment has no store
    day          DATE NOT NULL,
    mode         TEXT NOT NULL,
    amount_paise BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (tenant_id, store_id, day, mode)
);

-- Latest purchase cost per item (from purchase bills), for gross margin.
CREATE TABLE item_costs (
    tenant_id       UUID NOT NULL,
    item_id         UUID NOT NULL,
    unit_cost_paise BIGINT NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, item_id)
);

-- One row per invoice we have seen; lets cancellations reverse exactly what was added.
CREATE TABLE invoice_facts (
    tenant_id         UUID NOT NULL,
    invoice_id        UUID NOT NULL,
    store_id          UUID NOT NULL,
    doc_type          TEXT NOT NULL,
    number            TEXT NOT NULL,
    day               DATE NOT NULL,
    created_by        TEXT NOT NULL,
    party_id          TEXT,
    party_gstin       TEXT,
    grand_total_paise BIGINT NOT NULL,
    taxable_paise     BIGINT NOT NULL,
    tax_paise         BIGINT NOT NULL,
    discount_paise    BIGINT NOT NULL DEFAULT 0,
    gross_paise       BIGINT NOT NULL DEFAULT 0,
    margin_paise      BIGINT NOT NULL DEFAULT 0,
    margin_base_paise BIGINT NOT NULL DEFAULT 0,
    cancelled         BOOLEAN NOT NULL DEFAULT false,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, invoice_id)
);
CREATE INDEX invoice_facts_cashier ON invoice_facts (tenant_id, created_by, day);

-- Running discount statistics per cashier (Welford), for the 3-sigma rule.
CREATE TABLE cashier_discount_stats (
    tenant_id  UUID NOT NULL,
    cashier_id TEXT NOT NULL,
    n          BIGINT NOT NULL DEFAULT 0,
    mean       DOUBLE PRECISION NOT NULL DEFAULT 0,
    m2         DOUBLE PRECISION NOT NULL DEFAULT 0,
    PRIMARY KEY (tenant_id, cashier_id)
);

-- Which GST rates each HSN has been billed at.
CREATE TABLE hsn_rates (
    tenant_id   UUID NOT NULL,
    hsn         TEXT NOT NULL,
    tax_rate_bp INT NOT NULL,
    uses        BIGINT NOT NULL DEFAULT 0,
    last_number TEXT,
    last_seen   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, hsn, tax_rate_bp)
);

CREATE TABLE anomalies (
    id          BIGSERIAL PRIMARY KEY,
    tenant_id   UUID NOT NULL,
    kind        TEXT NOT NULL,                   -- unusual_discount | high_cancellations | missing_gstin | invalid_gstin | hsn_rate_mismatch
    severity    TEXT NOT NULL,                   -- info | warning | critical
    store_id    TEXT,
    subject     TEXT NOT NULL,                   -- cashier id, invoice number or HSN
    message     TEXT NOT NULL,
    details     JSONB NOT NULL DEFAULT '{}',
    dedupe_key  TEXT NOT NULL,
    resolved    BOOLEAN NOT NULL DEFAULT false,
    detected_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, kind, dedupe_key)
);
CREATE INDEX anomalies_recent ON anomalies (tenant_id, detected_at DESC);

-- Latest stock.low signals from bhandar (fed into re-order suggestions).
CREATE TABLE stock_alerts (
    tenant_id     UUID NOT NULL,
    store_id      UUID NOT NULL,
    item_id       UUID NOT NULL,
    item_name     TEXT NOT NULL,
    on_hand       NUMERIC(14,3) NOT NULL,
    reorder_level NUMERIC(14,3) NOT NULL,
    reorder_qty   NUMERIC(14,3) NOT NULL,
    supplier_id   TEXT,
    alerted_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, store_id, item_id)
);

CREATE TABLE forecasts (
    tenant_id     UUID NOT NULL,
    store_id      UUID NOT NULL,
    item_key      TEXT NOT NULL,
    item_id       UUID,
    name          TEXT NOT NULL,
    daily_level   DOUBLE PRECISION NOT NULL,     -- de-seasonalised units/day
    dow_factors   DOUBLE PRECISION[] NOT NULL,   -- Sunday..Saturday multipliers
    next_7d       DOUBLE PRECISION NOT NULL,
    history_days  INT NOT NULL,
    computed_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, store_id, item_key)
);

CREATE TABLE weekly_summaries (
    tenant_id  UUID NOT NULL,
    week_start DATE NOT NULL,
    text       TEXT NOT NULL,
    sent       BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, week_start)
);
