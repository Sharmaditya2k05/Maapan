-- aayaat: MargBooks / Excel / CSV import jobs and their rows

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

CREATE TABLE IF NOT EXISTS processed_events (
    event_id     TEXT PRIMARY KEY,
    event_type   TEXT NOT NULL,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE import_jobs (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      UUID NOT NULL,
    kind           TEXT NOT NULL CHECK (kind IN ('parties','items','opening_balances','opening_stock')),
    status         TEXT NOT NULL DEFAULT 'uploaded'
                   CHECK (status IN ('uploaded','mapped','validated','queued','running','completed','failed')),
    file_name      TEXT NOT NULL,
    file_key       TEXT NOT NULL,
    file_size      BIGINT NOT NULL,
    content_type   TEXT,
    sheet          TEXT,
    header_row     INT NOT NULL DEFAULT 0,               -- 0 = auto-detect
    mapping        JSONB NOT NULL DEFAULT '{}',          -- field → header text
    params         JSONB NOT NULL DEFAULT '{}',          -- as_of, store_id, notify_phone
    total_rows     INT NOT NULL DEFAULT 0,
    valid_rows     INT NOT NULL DEFAULT 0,
    error_rows     INT NOT NULL DEFAULT 0,
    imported       INT NOT NULL DEFAULT 0,
    failed         INT NOT NULL DEFAULT 0,
    dr_total_paise BIGINT NOT NULL DEFAULT 0,
    cr_total_paise BIGINT NOT NULL DEFAULT 0,
    error          TEXT,
    created_by     TEXT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at     TIMESTAMPTZ,
    finished_at    TIMESTAMPTZ
);
CREATE INDEX import_jobs_tenant ON import_jobs (tenant_id, created_at DESC);

CREATE TABLE import_rows (
    job_id    UUID NOT NULL REFERENCES import_jobs(id) ON DELETE CASCADE,
    tenant_id UUID NOT NULL,
    row_no    INT NOT NULL,                              -- 1-based row number in the sheet
    status    TEXT NOT NULL CHECK (status IN ('valid','invalid','imported','failed')),
    record    JSONB,                                     -- payload sent to bahi/bhandar
    raw       JSONB NOT NULL,                            -- mapped field → original text
    errors    JSONB NOT NULL DEFAULT '[]',
    PRIMARY KEY (job_id, row_no)
);
CREATE INDEX import_rows_status ON import_rows (job_id, status, row_no);
