-- kar returns (Phase 3, track C): ITC opening balances for the GSTR-3B set-off,
-- imported GSTR-2B documents and IMS (Invoice Management System) actions.
-- Amounts are paise. Every table is tenant-scoped and keyed per GSTIN
-- (registration), because returns are filed per GSTIN.

-- Electronic credit ledger balance brought into a period's set-off.
CREATE TABLE IF NOT EXISTS itc_opening (
    tenant_id  UUID   NOT NULL,
    gstin      TEXT   NOT NULL,
    period     TEXT   NOT NULL CHECK (period ~ '^(0[1-9]|1[0-2])[0-9]{4}$'),   -- MMYYYY
    igst       BIGINT NOT NULL DEFAULT 0 CHECK (igst >= 0),
    cgst       BIGINT NOT NULL DEFAULT 0 CHECK (cgst >= 0),
    sgst       BIGINT NOT NULL DEFAULT 0 CHECK (sgst >= 0),
    cess       BIGINT NOT NULL DEFAULT 0 CHECK (cess >= 0),
    updated_by TEXT   NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, gstin, period)
);

-- One imported GSTR-2B statement per GSTIN and period (re-import replaces it).
CREATE TABLE IF NOT EXISTS gstr2b_imports (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID NOT NULL,
    gstin        TEXT NOT NULL,
    period       TEXT NOT NULL,
    source       TEXT NOT NULL CHECK (source IN ('upload','gsp')),
    generated_on DATE,
    doc_count    INT  NOT NULL,
    skipped      JSONB NOT NULL DEFAULT '[]',   -- sections present but not reconciled (impg, isd, …)
    raw          JSONB NOT NULL,                -- the file as received, for audit
    imported_by  TEXT NOT NULL,
    imported_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, gstin, period)
);

CREATE TABLE IF NOT EXISTS gstr2b_docs (
    tenant_id         UUID   NOT NULL,
    gstin             TEXT   NOT NULL,          -- our (recipient) GSTIN
    period            TEXT   NOT NULL,          -- 2B period MMYYYY
    doc_key           TEXT   NOT NULL,          -- supplier_doctype_normno_fy (recon.DocKey)
    section           TEXT   NOT NULL,          -- b2b | b2ba | cdnr | cdnra
    supplier_gstin    TEXT   NOT NULL,
    supplier_name     TEXT   NOT NULL DEFAULT '',
    doc_type          TEXT   NOT NULL CHECK (doc_type IN ('INV','CN','DN')),
    doc_no            TEXT   NOT NULL,
    norm_no           TEXT   NOT NULL,
    doc_date          DATE,
    supply_type       TEXT   NOT NULL DEFAULT '',
    pos               TEXT   NOT NULL DEFAULT '',
    reverse_charge    BOOLEAN NOT NULL DEFAULT false,
    value             BIGINT NOT NULL DEFAULT 0,
    taxable           BIGINT NOT NULL DEFAULT 0,
    igst              BIGINT NOT NULL DEFAULT 0,
    cgst              BIGINT NOT NULL DEFAULT 0,
    sgst              BIGINT NOT NULL DEFAULT 0,
    cess              BIGINT NOT NULL DEFAULT 0,
    itc_available     TEXT   NOT NULL DEFAULT 'Y',
    reason            TEXT   NOT NULL DEFAULT '',
    supplier_period   TEXT   NOT NULL DEFAULT '',
    supplier_filed_on DATE,
    irn               TEXT   NOT NULL DEFAULT '',
    PRIMARY KEY (tenant_id, gstin, period, doc_key)
);
CREATE INDEX IF NOT EXISTS gstr2b_docs_ref ON gstr2b_docs (tenant_id, gstin, supplier_gstin, norm_no);
CREATE INDEX IF NOT EXISTS gstr2b_docs_key ON gstr2b_docs (tenant_id, gstin, doc_key);

-- Current IMS action per document (a document keeps its key across periods).
CREATE TABLE IF NOT EXISTS ims_actions (
    tenant_id UUID NOT NULL,
    gstin     TEXT NOT NULL,
    doc_key   TEXT NOT NULL,
    action    TEXT NOT NULL CHECK (action IN ('accept','reject','pending')),
    remark    TEXT NOT NULL DEFAULT '',
    actor     TEXT NOT NULL,
    acted_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, gstin, doc_key)
);

-- Every IMS action ever taken (audit trail).
CREATE TABLE IF NOT EXISTS ims_action_log (
    id        BIGSERIAL PRIMARY KEY,
    tenant_id UUID NOT NULL,
    gstin     TEXT NOT NULL,
    doc_key   TEXT NOT NULL,
    action    TEXT NOT NULL,
    remark    TEXT NOT NULL DEFAULT '',
    actor     TEXT NOT NULL,
    acted_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ims_action_log_doc ON ims_action_log (tenant_id, gstin, doc_key, acted_at);
