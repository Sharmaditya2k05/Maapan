-- soochna phase 1: e-mail channel, documents, dead letters, consent/DND,
-- quiet hours and the per-tenant sender/template registry.

-- ---------------------------------------------------------------- messages
-- The recipient is a phone (+91XXXXXXXXXX) for sms/whatsapp, an address for email.
ALTER TABLE messages RENAME COLUMN to_phone TO recipient;

ALTER TABLE messages DROP CONSTRAINT IF EXISTS messages_channel_check;
ALTER TABLE messages ADD CONSTRAINT messages_channel_check CHECK (channel IN ('sms','whatsapp','email'));

-- scheduled  = held for quiet hours, sent by the retry job at not_before
-- dead       = gave up after MaxAttempts (row in dead_letters, replayable)
-- suppressed = never sent: the recipient opted out / is on the DND list
ALTER TABLE messages DROP CONSTRAINT IF EXISTS messages_status_check;
ALTER TABLE messages ADD CONSTRAINT messages_status_check
    CHECK (status IN ('queued','scheduled','sent','delivered','read','failed','dead','suppressed'));

ALTER TABLE messages
    ADD COLUMN category        TEXT NOT NULL DEFAULT 'transactional'
                               CHECK (category IN ('otp','alert','transactional','promotional')),
    ADD COLUMN subject         TEXT,             -- email only
    ADD COLUMN media_filename  TEXT,             -- display name of media_url
    ADD COLUMN next_attempt_at TIMESTAMPTZ;      -- failed: retry after; scheduled: send at

UPDATE messages SET category = CASE
    WHEN template = 'otp' THEN 'otp'
    WHEN template IN ('stock_low','batch_expiring','import_done','weekly_summary') THEN 'alert'
    ELSE 'transactional' END;

DROP INDEX IF EXISTS messages_failed;
CREATE INDEX messages_due ON messages (next_attempt_at) WHERE status IN ('failed','scheduled');
CREATE INDEX messages_recipient ON messages (tenant_id, recipient, created_at DESC);

-- ------------------------------------------------------------ dead letters
CREATE TABLE dead_letters (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    message_id   UUID NOT NULL UNIQUE REFERENCES messages(id) ON DELETE CASCADE,
    tenant_id    UUID,
    channel      TEXT NOT NULL,
    template     TEXT NOT NULL,
    recipient    TEXT NOT NULL,
    attempts     INT NOT NULL,
    last_error   TEXT,
    reason       TEXT NOT NULL,                 -- max_attempts | expired | not_configured
    dead_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    replay_count INT NOT NULL DEFAULT 0,
    replayed_at  TIMESTAMPTZ,
    replayed_by  TEXT
);
CREATE INDEX dead_letters_open ON dead_letters (tenant_id, dead_at DESC) WHERE replayed_at IS NULL;

-- ------------------------------------------------------------- consent/DND
-- scope 'global' = applies to every tenant (STOP sent to the shared WhatsApp
-- number / SMS header, national DND import); otherwise a tenant id (the store
-- marked this customer "do not message").
-- channel: sms | whatsapp | email | all
-- status:  opted_out | opted_in | dnd (blocks promotional only)
CREATE TABLE consents (
    scope      TEXT NOT NULL,
    address    TEXT NOT NULL,                   -- +91XXXXXXXXXX or lower-case e-mail
    channel    TEXT NOT NULL CHECK (channel IN ('sms','whatsapp','email','all')),
    status     TEXT NOT NULL CHECK (status IN ('opted_out','opted_in','dnd')),
    source     TEXT NOT NULL,                   -- keyword | api | dnd_import | admin
    note       TEXT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (scope, address, channel)
);
CREATE INDEX consents_address ON consents (address);

-- Every opt-out / opt-in, for audit (TRAI / Meta may ask when consent changed).
CREATE TABLE consent_log (
    id         BIGSERIAL PRIMARY KEY,
    scope      TEXT NOT NULL,
    address    TEXT NOT NULL,
    channel    TEXT NOT NULL,
    status     TEXT NOT NULL,
    source     TEXT NOT NULL,
    detail     TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX consent_log_address ON consent_log (address, created_at DESC);

-- -------------------------------------------- per-tenant sender registry
CREATE TABLE tenant_senders (
    tenant_id         UUID PRIMARY KEY,
    sms_sender_id     TEXT,          -- DLT header (6 letters), linked to the tenant's templates on MSG91
    whatsapp_source   TEXT,          -- the store's own WhatsApp business number (+91…)
    whatsapp_app_name TEXT,          -- Gupshup app for that number
    email_from        TEXT,          -- verified sender address on the e-mail provider
    email_from_name   TEXT,
    email_reply_to    TEXT,
    payment_reminders BOOLEAN NOT NULL DEFAULT false,
    reminder_days     INT[] NOT NULL DEFAULT '{0,7,15,30}',  -- days after due date
    reminder_channels TEXT[] NOT NULL DEFAULT '{whatsapp,email}',
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by        TEXT
);

-- Provider template ids per tenant (a store's own DLT / Meta-approved templates).
CREATE TABLE tenant_templates (
    tenant_id   UUID NOT NULL,
    template    TEXT NOT NULL,
    channel     TEXT NOT NULL CHECK (channel IN ('sms','whatsapp')),
    provider_template_id TEXT NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, template, channel)
);

-- --------------------------------------------------------- job bookkeeping
CREATE TABLE job_runs (
    id          BIGSERIAL PRIMARY KEY,
    job         TEXT NOT NULL,
    started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    result      JSONB
);
