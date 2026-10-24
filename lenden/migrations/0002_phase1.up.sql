-- lenden phase 1: refund states, manual-receipt idempotency, short/excess captures,
-- partial requests, settlements and per-tenant gateway credentials. Additive only.

-- Refunds: 'unknown' = the gateway call failed in a way that may still have executed
-- (timeout, transport error, 5xx); the webhook or reconcile settles it. source tells
-- whether Taraju initiated the refund or the gateway dashboard did.
ALTER TABLE refunds DROP CONSTRAINT IF EXISTS refunds_status_check;
ALTER TABLE refunds ADD CONSTRAINT refunds_status_check
    CHECK (status IN ('pending','unknown','processed','failed'));
ALTER TABLE refunds ADD COLUMN IF NOT EXISTS source     TEXT NOT NULL DEFAULT 'taraju'; -- taraju | gateway
ALTER TABLE refunds ADD COLUMN IF NOT EXISTS last_error TEXT;
ALTER TABLE refunds ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();
CREATE INDEX IF NOT EXISTS refunds_open ON refunds (created_at) WHERE status IN ('pending','unknown');

-- Manual receipts: a replayed idempotency key returns the existing payment.
ALTER TABLE payments ADD COLUMN IF NOT EXISTS idempotency_key TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS payments_idempotency
    ON payments (tenant_id, idempotency_key) WHERE idempotency_key IS NOT NULL;

-- Captured vs requested: short_paise = requested − captured, excess_paise = captured − requested
-- (whichever is positive, else 0), measured against what was still outstanding on the request.
ALTER TABLE payments ADD COLUMN IF NOT EXISTS short_paise  BIGINT NOT NULL DEFAULT 0 CHECK (short_paise >= 0);
ALTER TABLE payments ADD COLUMN IF NOT EXISTS excess_paise BIGINT NOT NULL DEFAULT 0 CHECK (excess_paise >= 0);

-- A short capture leaves the request 'partial' (another capture may complete it).
ALTER TABLE payment_requests DROP CONSTRAINT IF EXISTS payment_requests_status_check;
ALTER TABLE payment_requests ADD CONSTRAINT payment_requests_status_check
    CHECK (status IN ('created','partial','paid','expired','cancelled','failed'));
DROP INDEX IF EXISTS payment_requests_pending;
CREATE INDEX payment_requests_pending ON payment_requests (created_at) WHERE status IN ('created','partial');

-- Gateway payouts (settlement.processed): gross − refunds = net + fee + fee_tax.
-- tenant_id is NULL when the webhook was verified with the platform-wide secret and the
-- body references no tenant (shared gateway account); such rows are kept for audit only.
CREATE TABLE settlements (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID,
    gateway       TEXT NOT NULL,
    gateway_ref   TEXT NOT NULL,
    utr           TEXT,
    gross_paise   BIGINT NOT NULL DEFAULT 0,
    fee_paise     BIGINT NOT NULL DEFAULT 0,
    fee_tax_paise BIGINT NOT NULL DEFAULT 0,
    refunds_paise BIGINT NOT NULL DEFAULT 0,
    net_paise     BIGINT NOT NULL DEFAULT 0,
    settled_at    TIMESTAMPTZ NOT NULL,
    payload       JSONB NOT NULL DEFAULT '{}',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (gateway, gateway_ref)
);
CREATE INDEX settlements_tenant ON settlements (tenant_id, settled_at DESC);

-- Per-tenant gateway keys (MATURITY-PLAN D5): secrets are AES-256-GCM sealed with CREDENTIALS_KEY.
CREATE TABLE gateway_credentials (
    tenant_id          UUID NOT NULL,
    gateway            TEXT NOT NULL,
    key_id             TEXT NOT NULL,
    key_secret_enc     BYTEA NOT NULL,
    webhook_secret_enc BYTEA,
    env                TEXT NOT NULL DEFAULT '',
    updated_by         TEXT NOT NULL DEFAULT '',
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, gateway)
);
