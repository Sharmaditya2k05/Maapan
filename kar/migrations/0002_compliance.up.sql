-- kar Phase 3 (track B): outbox, effective-dated HSN master, per-GSTIN portal
-- credentials, IRN / e-way bill lifecycle, GSP simulator state.

-- ---------- outbox (kar now publishes kar.einvoice.generated / kar.eway.generated) ----------
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
ALTER TABLE outbox ADD COLUMN IF NOT EXISTS leased_until TIMESTAMPTZ;
ALTER TABLE outbox ADD COLUMN IF NOT EXISTS delivered JSONB NOT NULL DEFAULT '[]';
ALTER TABLE outbox ADD COLUMN IF NOT EXISTS dead_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS outbox_dead_idx ON outbox (dead_at) WHERE dead_at IS NOT NULL;

-- ---------- HSN/SAC master, effective-dated ----------
-- Global reference data (no tenant_id), like gstin_cache. One row per code,
-- condition and start date; valid_to is inclusive, NULL = still in force.
CREATE TABLE hsn_rates (
    id                  BIGSERIAL PRIMARY KEY,
    code                TEXT NOT NULL CHECK (code ~ '^([0-9]{4}|[0-9]{6}|[0-9]{8})$'),
    description         TEXT NOT NULL,
    kind                TEXT NOT NULL CHECK (kind IN ('goods','service')),
    rate_bp             INT NOT NULL CHECK (rate_bp >= 0),
    cess_bp             INT NOT NULL DEFAULT 0 CHECK (cess_bp >= 0),
    cess_per_unit_paise BIGINT NOT NULL DEFAULT 0 CHECK (cess_per_unit_paise >= 0),
    valid_from          DATE NOT NULL,
    valid_to            DATE,
    condition           TEXT NOT NULL DEFAULT '',   -- e.g. 'sale value up to Rs 2,500 per piece'
    source              TEXT NOT NULL,              -- seed-unverified | import | cbic-…
    search              TSVECTOR GENERATED ALWAYS AS (to_tsvector('simple', description || ' ' || condition)) STORED,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (valid_to IS NULL OR valid_to >= valid_from),
    UNIQUE (code, valid_from, condition)
);
CREATE INDEX hsn_rates_search ON hsn_rates USING gin (search);
CREATE INDEX hsn_rates_code_prefix ON hsn_rates (code text_pattern_ops);
CREATE INDEX hsn_rates_validity ON hsn_rates (code, condition, valid_from DESC);

-- The 69 Phase-1 starter rows move over unchanged and unverified. No rate is
-- invented or corrected here: real data enters through POST /v1/hsn/import.
INSERT INTO hsn_rates (code, description, kind, rate_bp, cess_bp, valid_from, source)
SELECT code, description, kind, default_rate_bp, cess_rate_bp, DATE '2017-07-01', 'seed-unverified' FROM hsn_codes
ON CONFLICT (code, valid_from, condition) DO NOTHING;

-- hsn_codes stays readable for anything that still queries it, as the rates in force today.
DROP TABLE hsn_codes;
CREATE VIEW hsn_codes AS
SELECT DISTINCT ON (code) code, description, kind, rate_bp AS default_rate_bp, cess_bp AS cess_rate_bp
FROM hsn_rates
WHERE valid_from <= CURRENT_DATE AND (valid_to IS NULL OR valid_to >= CURRENT_DATE) AND condition = ''
ORDER BY code, valid_from DESC;

-- ---------- per-GSTIN portal API users (AES-256-GCM under CREDENTIALS_KEY) ----------
CREATE TABLE gsp_credentials (
    tenant_id             UUID NOT NULL,
    gstin                 TEXT NOT NULL,
    einvoice_username     TEXT NOT NULL DEFAULT '',
    einvoice_password_enc BYTEA,
    eway_username         TEXT NOT NULL DEFAULT '',
    eway_password_enc     BYTEA,
    updated_by            TEXT NOT NULL DEFAULT '',
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, gstin)
);

-- ---------- e-Invoice lifecycle ----------
ALTER TABLE einvoices
    ADD COLUMN store_id       UUID,
    ADD COLUMN gstin          TEXT NOT NULL DEFAULT '',
    ADD COLUMN doc_type       TEXT NOT NULL DEFAULT '',        -- INV | CRN | DBN
    ADD COLUMN doc_date       DATE,
    ADD COLUMN party_gstin    TEXT NOT NULL DEFAULT '',
    ADD COLUMN party_name     TEXT NOT NULL DEFAULT '',
    ADD COLUMN supply_type    TEXT NOT NULL DEFAULT '',        -- B2B | SEZWP | SEZWOP | EXPWP | EXPWOP | DEXP
    ADD COLUMN total_paise    BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN status         TEXT NOT NULL DEFAULT 'draft'
        CHECK (status IN ('draft','pending','generated','cancelled','failed')),
    ADD COLUMN ack_no         TEXT,
    ADD COLUMN ack_date       TIMESTAMPTZ,
    ADD COLUMN signed_invoice TEXT,
    ADD COLUMN signed_qr      TEXT,
    ADD COLUMN error_code     TEXT,
    ADD COLUMN error_message  TEXT,
    ADD COLUMN attempts       INT NOT NULL DEFAULT 0,
    ADD COLUMN provider       TEXT NOT NULL DEFAULT '',
    ADD COLUMN triggered_by   TEXT NOT NULL DEFAULT 'manual',  -- manual | event
    ADD COLUMN cancelled_at   TIMESTAMPTZ,
    ADD COLUMN cancel_reason  INT,
    ADD COLUMN cancel_remark  TEXT,
    ADD COLUMN updated_at     TIMESTAMPTZ NOT NULL DEFAULT now();
CREATE INDEX einvoices_list ON einvoices (tenant_id, status, doc_date DESC);
CREATE UNIQUE INDEX einvoices_irn ON einvoices (irn) WHERE irn IS NOT NULL;

-- ---------- e-Way bill lifecycle ----------
ALTER TABLE eway_bills
    ADD COLUMN store_id       UUID,
    ADD COLUMN gstin          TEXT NOT NULL DEFAULT '',
    ADD COLUMN doc_date       DATE,
    ADD COLUMN irn            TEXT,
    ADD COLUMN status         TEXT NOT NULL DEFAULT 'draft'
        CHECK (status IN ('draft','generated','cancelled','failed')),
    ADD COLUMN ewb_date       TIMESTAMPTZ,
    ADD COLUMN valid_upto     TIMESTAMPTZ,
    ADD COLUMN mode           TEXT NOT NULL DEFAULT '',
    ADD COLUMN vehicle_no     TEXT NOT NULL DEFAULT '',
    ADD COLUMN vehicle_type   TEXT NOT NULL DEFAULT 'R',
    ADD COLUMN distance_km    INT NOT NULL DEFAULT 0,
    ADD COLUMN transporter_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN extensions     INT NOT NULL DEFAULT 0,
    ADD COLUMN error_code     TEXT,
    ADD COLUMN error_message  TEXT,
    ADD COLUMN provider       TEXT NOT NULL DEFAULT '',
    ADD COLUMN history        JSONB NOT NULL DEFAULT '[]',
    ADD COLUMN cancelled_at   TIMESTAMPTZ,
    ADD COLUMN cancel_reason  INT,
    ADD COLUMN cancel_remark  TEXT,
    ADD COLUMN updated_at     TIMESTAMPTZ NOT NULL DEFAULT now();
CREATE INDEX eway_bills_list ON eway_bills (tenant_id, status, doc_date DESC);

-- ---------- GSP simulator state (GSP_PROVIDER=simulator; never used in prod) ----------
CREATE TABLE gsp_sim_irns (
    irn            TEXT PRIMARY KEY,
    gstin          TEXT NOT NULL,
    doc_type       TEXT NOT NULL,
    doc_no         TEXT NOT NULL,
    doc_date       TEXT NOT NULL,
    fy             TEXT NOT NULL,
    ack_no         TEXT NOT NULL,
    ack_dt         TIMESTAMPTZ NOT NULL,
    signed_invoice TEXT NOT NULL,
    signed_qr      TEXT NOT NULL,
    status         TEXT NOT NULL,
    cancelled_at   TIMESTAMPTZ,
    cancel_reason  INT NOT NULL DEFAULT 0,
    cancel_remark  TEXT NOT NULL DEFAULT '',
    ewb_no         TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE gsp_sim_ewbs (
    ewb_no       TEXT PRIMARY KEY,
    gstin        TEXT NOT NULL,
    doc_key      TEXT NOT NULL,
    doc_date     TEXT NOT NULL,
    irn          TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL,
    vehicle_no   TEXT NOT NULL DEFAULT '',
    trans_mode   TEXT NOT NULL DEFAULT '',
    distance_km  INT NOT NULL DEFAULT 0,
    odc          BOOLEAN NOT NULL DEFAULT false,
    ewb_date     TIMESTAMPTZ NOT NULL,
    valid_upto   TIMESTAMPTZ,
    cancelled_at TIMESTAMPTZ,
    extended     INT NOT NULL DEFAULT 0,
    raw          JSONB NOT NULL DEFAULT '{}',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX gsp_sim_ewbs_active_doc ON gsp_sim_ewbs (doc_key) WHERE status = 'ACT';
CREATE SEQUENCE gsp_sim_ewb_seq;
