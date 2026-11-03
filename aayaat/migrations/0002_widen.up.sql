-- aayaat: open bills + invoice history kinds, Tally / .xls sources, warnings,
-- skipped rows, crash-safe batch journal, dry-run, rollback, saved mappings.

ALTER TABLE import_jobs DROP CONSTRAINT IF EXISTS import_jobs_kind_check;
ALTER TABLE import_jobs ADD CONSTRAINT import_jobs_kind_check
    CHECK (kind IN ('parties','items','opening_balances','opening_stock','open_bills','invoices'));

ALTER TABLE import_jobs DROP CONSTRAINT IF EXISTS import_jobs_status_check;
ALTER TABLE import_jobs ADD CONSTRAINT import_jobs_status_check
    CHECK (status IN ('uploaded','mapped','validated','queued','running','completed','failed','rolled_back'));

ALTER TABLE import_jobs
    ADD COLUMN IF NOT EXISTS source        TEXT NOT NULL DEFAULT '',      -- marg | tally | busy | vyapar | '' (generic)
    ADD COLUMN IF NOT EXISTS skipped_rows  INT  NOT NULL DEFAULT 0,       -- nothing to import (zero qty / balance)
    ADD COLUMN IF NOT EXISTS warning_rows  INT  NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS template_id   UUID,                          -- mapping template applied
    ADD COLUMN IF NOT EXISTS rolled_back   INT  NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS rolled_back_at TIMESTAMPTZ;

ALTER TABLE import_rows DROP CONSTRAINT IF EXISTS import_rows_status_check;
ALTER TABLE import_rows ADD CONSTRAINT import_rows_status_check
    CHECK (status IN ('valid','invalid','imported','failed','skipped','rolled_back'));

ALTER TABLE import_rows
    ADD COLUMN IF NOT EXISTS warnings JSONB NOT NULL DEFAULT '[]',
    ADD COLUMN IF NOT EXISTS batch_no INT,                                -- import_batches.batch_no that sent it
    ADD COLUMN IF NOT EXISTS created  BOOLEAN;                            -- downstream said created (vs updated), when it says

-- One row per bulk call. A batch is written 'sent' before the POST and turned
-- 'recorded' in the same transaction that records the row results, so a crash
-- in between is visible on resume (the batch is re-sent; every downstream call
-- is idempotent on external_ref / source key).
CREATE TABLE IF NOT EXISTS import_batches (
    job_id      UUID NOT NULL REFERENCES import_jobs(id) ON DELETE CASCADE,
    tenant_id   UUID NOT NULL,
    batch_no    INT  NOT NULL,
    phase       TEXT NOT NULL DEFAULT 'main',                         -- main | opening_stock (items with opening qty)
    path        TEXT NOT NULL,
    store_id    TEXT,
    first_row   INT  NOT NULL,
    last_row    INT  NOT NULL,
    records     INT  NOT NULL,
    status      TEXT NOT NULL CHECK (status IN ('sent','recorded','failed','rolled_back')),
    created     INT  NOT NULL DEFAULT 0,
    updated     INT  NOT NULL DEFAULT 0,
    errors      INT  NOT NULL DEFAULT 0,
    error       TEXT,
    sent_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    recorded_at TIMESTAMPTZ,
    PRIMARY KEY (job_id, batch_no)
);

-- Saved column mappings ("our MargBooks party export"), reused across uploads.
CREATE TABLE IF NOT EXISTS mapping_templates (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  UUID NOT NULL,
    kind       TEXT NOT NULL,
    name       TEXT NOT NULL,
    source     TEXT NOT NULL DEFAULT '',
    sheet      TEXT,
    header_row INT  NOT NULL DEFAULT 0,
    mapping    JSONB NOT NULL,
    params     JSONB NOT NULL DEFAULT '{}',
    created_by TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, kind, name)
);
