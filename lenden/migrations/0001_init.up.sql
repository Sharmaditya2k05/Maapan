-- lenden: payment requests (UPI QR / links), payments, refunds, webhook log

CREATE TABLE IF NOT EXISTS outbox (
    id           TEXT PRIMARY KEY,
    event_type   TEXT NOT NULL,
    payload      JSONB NOT NULL,
    attempts     INT NOT NULL DEFAULT 0,
    last_error   TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS outbox_pending_idx ON outbox (created_at) WHERE published_at IS NULL;

-- What the counter asked the customer to pay (one QR or link each).
CREATE TABLE payment_requests (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        UUID NOT NULL,
    store_id         UUID,
    invoice_id       UUID,
    party_id         UUID,
    party_phone      TEXT,
    amount_paise     BIGINT NOT NULL CHECK (amount_paise > 0),
    purpose          TEXT NOT NULL CHECK (purpose IN ('invoice','on_account')),
    kind             TEXT NOT NULL CHECK (kind IN ('upi_qr','payment_link')),
    gateway          TEXT NOT NULL,
    gateway_ref      TEXT,
    upi_qr_image_url TEXT,
    upi_intent       TEXT,
    short_url        TEXT,
    status           TEXT NOT NULL DEFAULT 'created'
                     CHECK (status IN ('created','paid','expired','cancelled','failed')),
    paid_paise       BIGINT NOT NULL DEFAULT 0,
    last_error       TEXT,
    expires_at       TIMESTAMPTZ NOT NULL,
    created_by       TEXT NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX payment_requests_gateway_ref ON payment_requests (gateway, gateway_ref) WHERE gateway_ref IS NOT NULL;
CREATE INDEX payment_requests_pending ON payment_requests (created_at) WHERE status = 'created';
CREATE INDEX payment_requests_tenant ON payment_requests (tenant_id, created_at DESC);

-- Money actually received (gateway captures and manual receipts).
CREATE TABLE payments (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      UUID NOT NULL,
    store_id       UUID,
    request_id     UUID REFERENCES payment_requests(id),
    invoice_id     UUID,
    party_id       UUID,
    party_phone    TEXT,
    amount_paise   BIGINT NOT NULL CHECK (amount_paise > 0),
    refunded_paise BIGINT NOT NULL DEFAULT 0,
    method         TEXT NOT NULL,              -- upi | card | netbanking | wallet | cash | cheque | bank
    gateway        TEXT NOT NULL,              -- razorpay | mock | manual
    gateway_ref    TEXT,                       -- pay_xxx, or cheque/UTR number for manual
    status         TEXT NOT NULL DEFAULT 'captured'
                   CHECK (status IN ('captured','partially_refunded','refunded')),
    notes          TEXT,
    captured_at    TIMESTAMPTZ NOT NULL,
    created_by     TEXT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- One row per provider payment: the second of payment.captured / qr_code.credited is a no-op.
CREATE UNIQUE INDEX payments_gateway_ref ON payments (gateway, gateway_ref) WHERE gateway <> 'manual';
CREATE INDEX payments_tenant ON payments (tenant_id, captured_at DESC);
CREATE INDEX payments_invoice ON payments (tenant_id, invoice_id);

CREATE TABLE refunds (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID NOT NULL,
    payment_id   UUID NOT NULL REFERENCES payments(id),
    amount_paise BIGINT NOT NULL CHECK (amount_paise > 0),
    gateway_ref  TEXT,
    status       TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processed','failed')),
    reason       TEXT NOT NULL,
    created_by   TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    processed_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX refunds_gateway_ref ON refunds (gateway_ref) WHERE gateway_ref IS NOT NULL;
CREATE INDEX refunds_payment ON refunds (tenant_id, payment_id);

-- Every webhook we accepted, keyed by X-Razorpay-Event-Id (idempotency + audit).
CREATE TABLE webhook_events (
    event_id    TEXT PRIMARY KEY,
    gateway     TEXT NOT NULL,
    event_type  TEXT NOT NULL,
    payload     JSONB NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
