-- soochna: outbound SMS/WhatsApp log with delivery status

CREATE TABLE IF NOT EXISTS processed_events (
    event_id     TEXT PRIMARY KEY,
    event_type   TEXT NOT NULL,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE messages (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- NULL only for pre-login messages (OTP), which belong to no business yet.
    tenant_id       UUID,
    -- tenant_id as text, or 'global' — makes idempotency keys unique per tenant.
    scope           TEXT NOT NULL,
    channel         TEXT NOT NULL CHECK (channel IN ('sms','whatsapp')),
    to_phone        TEXT NOT NULL,
    template        TEXT NOT NULL,
    params          JSONB NOT NULL DEFAULT '{}',   -- OTP codes are redacted
    body            TEXT NOT NULL,                  -- OTP codes are redacted
    media_url       TEXT,
    status          TEXT NOT NULL DEFAULT 'queued'
                    CHECK (status IN ('queued','sent','delivered','read','failed')),
    provider        TEXT,
    provider_ref    TEXT,
    error           TEXT,
    attempts        INT NOT NULL DEFAULT 0,
    idempotency_key TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    sent_at         TIMESTAMPTZ,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (scope, idempotency_key)
);
CREATE INDEX messages_tenant ON messages (tenant_id, created_at DESC);
CREATE INDEX messages_provider_ref ON messages (provider, provider_ref);
CREATE INDEX messages_failed ON messages (created_at) WHERE status = 'failed';
