-- nirmaan 0002: scrap method, yield, effective dates & alternates, routing hours,
-- run numbering, material issue (WIP), partial completion, failed batches,
-- by-products / unplanned consumption, unit conversion, job work (ITC-04).

-- ---------- BOMs ----------
ALTER TABLE boms ADD COLUMN scrap_method TEXT NOT NULL DEFAULT 'divide'
    CHECK (scrap_method IN ('divide','multiply'));
-- BOMs made before the switch keep the gross-up they were costed with.
UPDATE boms SET scrap_method = 'multiply';
ALTER TABLE boms ADD COLUMN yield_percent NUMERIC(5,2) NOT NULL DEFAULT 100
    CHECK (yield_percent > 0 AND yield_percent <= 100);
ALTER TABLE boms ADD COLUMN effective_from DATE;
ALTER TABLE boms ADD COLUMN effective_to DATE;
ALTER TABLE boms ADD COLUMN is_primary BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE boms ADD COLUMN alternate_name TEXT;
ALTER TABLE boms ADD CONSTRAINT boms_effective_range
    CHECK (effective_from IS NULL OR effective_to IS NULL OR effective_to >= effective_from);
ALTER TABLE boms ADD CONSTRAINT boms_alternate_named CHECK (is_primary OR alternate_name IS NOT NULL);
-- Several active BOMs per item are now allowed (date ranges, alternates);
-- still at most one open-ended primary. Overlaps are resolved on activation.
DROP INDEX IF EXISTS boms_one_active;
CREATE UNIQUE INDEX boms_one_open_primary ON boms (tenant_id, item_id)
    WHERE status = 'active' AND is_primary AND effective_to IS NULL;
CREATE INDEX boms_active_idx ON boms (tenant_id, item_id) WHERE status = 'active';

ALTER TABLE bom_operations ADD COLUMN work_center TEXT;
ALTER TABLE bom_operations ADD COLUMN hours NUMERIC(10,3) NOT NULL DEFAULT 0 CHECK (hours >= 0);
ALTER TABLE bom_operations ADD COLUMN rate_paise_per_hour BIGINT NOT NULL DEFAULT 0 CHECK (rate_paise_per_hour >= 0);

-- ---------- document numbers ----------
CREATE TABLE number_series (
    tenant_id UUID NOT NULL,
    series    TEXT NOT NULL,
    fy        TEXT NOT NULL,
    last_no   INT NOT NULL,
    PRIMARY KEY (tenant_id, series, fy)
);

-- ---------- production runs ----------
ALTER TABLE production_runs ADD COLUMN run_no TEXT;
UPDATE production_runs SET run_no = 'PR-' || upper(left(id::text, 8)) WHERE run_no IS NULL;
ALTER TABLE production_runs ALTER COLUMN run_no SET NOT NULL;
CREATE UNIQUE INDEX production_runs_no ON production_runs (tenant_id, run_no);

ALTER TABLE production_runs DROP CONSTRAINT IF EXISTS production_runs_status_check;
ALTER TABLE production_runs ADD CONSTRAINT production_runs_status_check
    CHECK (status IN ('planned','in_progress','completed','failed','cancelled'));
ALTER TABLE production_runs ADD COLUMN rejected_qty NUMERIC(14,3) NOT NULL DEFAULT 0;
ALTER TABLE production_runs ADD COLUMN planned_hours NUMERIC(12,3) NOT NULL DEFAULT 0;
ALTER TABLE production_runs ADD COLUMN actual_hours NUMERIC(12,3) NOT NULL DEFAULT 0;
ALTER TABLE production_runs ADD COLUMN material_cost_paise BIGINT NOT NULL DEFAULT 0;
ALTER TABLE production_runs ADD COLUMN operations_cost_paise BIGINT NOT NULL DEFAULT 0;
ALTER TABLE production_runs ADD COLUMN byproduct_value_paise BIGINT NOT NULL DEFAULT 0;
ALTER TABLE production_runs ADD COLUMN issued_value_paise BIGINT NOT NULL DEFAULT 0;
ALTER TABLE production_runs ADD COLUMN receipts INT NOT NULL DEFAULT 0;
ALTER TABLE production_runs ADD COLUMN failure_reason TEXT;
UPDATE production_runs SET material_cost_paise = COALESCE(total_cost_paise, 0) WHERE status = 'completed';
CREATE INDEX production_runs_open_idx ON production_runs (tenant_id, store_id) WHERE status = 'in_progress';

ALTER TABLE production_run_lines DROP CONSTRAINT IF EXISTS production_run_lines_kind_check;
ALTER TABLE production_run_lines ADD CONSTRAINT production_run_lines_kind_check
    CHECK (kind IN ('consume','produce','byproduct'));
-- planned_qty stays in the line's unit; base_* / issued / actual are in the item's stock unit.
ALTER TABLE production_run_lines ADD COLUMN base_unit TEXT;
ALTER TABLE production_run_lines ADD COLUMN factor NUMERIC(18,6) NOT NULL DEFAULT 1 CHECK (factor > 0);
ALTER TABLE production_run_lines ADD COLUMN planned_base_qty NUMERIC(14,3);
ALTER TABLE production_run_lines ADD COLUMN issued_qty NUMERIC(14,3) NOT NULL DEFAULT 0;
ALTER TABLE production_run_lines ADD COLUMN unplanned BOOLEAN NOT NULL DEFAULT false;
UPDATE production_run_lines SET base_unit = unit, planned_base_qty = planned_qty;
ALTER TABLE production_run_lines ALTER COLUMN base_unit SET NOT NULL;
ALTER TABLE production_run_lines ALTER COLUMN planned_base_qty SET NOT NULL;

-- Material issued to a run (work in progress). Stock moves in bhandar on
-- completion; open issues reserve stock for other runs' shortage checks.
CREATE TABLE production_issues (
    id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    run_id    UUID NOT NULL REFERENCES production_runs(id) ON DELETE CASCADE,
    seq       INT NOT NULL,
    note      TEXT,
    issued_by TEXT NOT NULL,
    issued_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (run_id, seq)
);
CREATE TABLE production_issue_lines (
    issue_id        UUID NOT NULL REFERENCES production_issues(id) ON DELETE CASCADE,
    run_line_no     INT NOT NULL,
    item_id         UUID NOT NULL,
    qty             NUMERIC(14,3) NOT NULL CHECK (qty > 0), -- base unit
    unit_cost_paise BIGINT,
    PRIMARY KEY (issue_id, run_line_no)
);

-- Each (partial or final) completion of a run; posting_id is the
-- production_run_id carried by its nirmaan.production.completed event.
CREATE TABLE production_receipts (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id             UUID NOT NULL,
    run_id                UUID NOT NULL REFERENCES production_runs(id) ON DELETE CASCADE,
    seq                   INT NOT NULL,
    posting_id            TEXT NOT NULL,
    is_final              BOOLEAN NOT NULL,
    good_qty              NUMERIC(14,3) NOT NULL CHECK (good_qty >= 0),
    rejected_qty          NUMERIC(14,3) NOT NULL DEFAULT 0 CHECK (rejected_qty >= 0),
    batch_no              TEXT,
    labour_hours          NUMERIC(12,3) NOT NULL DEFAULT 0,
    material_paise        BIGINT NOT NULL,
    operations_paise      BIGINT NOT NULL,
    byproduct_paise       BIGINT NOT NULL DEFAULT 0,
    total_paise           BIGINT NOT NULL,
    unit_cost_paise       BIGINT NOT NULL,
    created_by            TEXT NOT NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (run_id, seq),
    UNIQUE (tenant_id, posting_id)
);
CREATE TABLE production_receipt_lines (
    receipt_id      UUID NOT NULL REFERENCES production_receipts(id) ON DELETE CASCADE,
    run_line_no     INT NOT NULL,
    kind            TEXT NOT NULL CHECK (kind IN ('consume','produce','byproduct')),
    item_id         UUID NOT NULL,
    qty             NUMERIC(14,3) NOT NULL, -- base unit
    unit_cost_paise BIGINT NOT NULL,
    price_source    TEXT,
    PRIMARY KEY (receipt_id, run_line_no)
);

-- ---------- job work (Sec 143 CGST Act, Rule 45, ITC-04) ----------
CREATE TABLE job_workers (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  UUID NOT NULL,
    name       TEXT NOT NULL,
    gstin      TEXT,
    state_code TEXT NOT NULL,
    address    TEXT,
    phone      TEXT,
    party_id   UUID,           -- the same party in aadesh/bahi, for processing-charge bills
    active     BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);

CREATE TABLE job_work_challans (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id            UUID NOT NULL,
    store_id             UUID NOT NULL,
    challan_no           TEXT NOT NULL CHECK (length(challan_no) <= 16),
    challan_date         DATE NOT NULL,
    job_worker_id        UUID NOT NULL REFERENCES job_workers(id),
    principal_gstin      TEXT,
    principal_state      TEXT,
    nature_of_processing TEXT NOT NULL,
    production_run_id    UUID REFERENCES production_runs(id),
    eway_bill_no         TEXT,
    vehicle_no           TEXT,
    status               TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','partially_received','closed','cancelled')),
    total_value_paise    BIGINT NOT NULL,
    notes                TEXT,
    created_by           TEXT NOT NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    cancelled_at         TIMESTAMPTZ,
    UNIQUE (tenant_id, challan_no)
);
CREATE INDEX job_work_challans_worker_idx ON job_work_challans (tenant_id, job_worker_id, challan_date);

CREATE TABLE job_work_challan_lines (
    challan_id          UUID NOT NULL REFERENCES job_work_challans(id) ON DELETE CASCADE,
    line_no             INT NOT NULL,
    item_id             UUID NOT NULL,
    item_name           TEXT NOT NULL,
    hsn                 TEXT,
    unit                TEXT NOT NULL,
    qty                 NUMERIC(14,3) NOT NULL CHECK (qty > 0),
    factor              NUMERIC(18,6) NOT NULL DEFAULT 1 CHECK (factor > 0), -- base units per `unit`
    taxable_value_paise BIGINT NOT NULL CHECK (taxable_value_paise >= 0),
    tax_rate_bp         INT NOT NULL DEFAULT 0,
    goods_type          TEXT NOT NULL CHECK (goods_type IN ('inputs','capital_goods')),
    return_exempt       BOOLEAN NOT NULL DEFAULT false, -- moulds, dies, jigs, fixtures, tools (Sec 143(1) proviso)
    due_date            DATE,                           -- NULL when return_exempt
    PRIMARY KEY (challan_id, line_no)
);

CREATE TABLE job_work_receipts (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id                UUID NOT NULL,
    challan_id               UUID NOT NULL REFERENCES job_work_challans(id),
    receipt_no               TEXT NOT NULL,
    receipt_date             DATE NOT NULL,
    jw_challan_no            TEXT,  -- challan/invoice issued by the job worker
    jw_challan_date          DATE,
    processing_charges_paise BIGINT NOT NULL DEFAULT 0 CHECK (processing_charges_paise >= 0),
    posting_id               TEXT,  -- set when a stock event was published
    notes                    TEXT,
    created_by               TEXT NOT NULL,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, receipt_no)
);
CREATE INDEX job_work_receipts_challan_idx ON job_work_receipts (challan_id);

CREATE TABLE job_work_receipt_lines (
    receipt_id      UUID NOT NULL REFERENCES job_work_receipts(id) ON DELETE CASCADE,
    line_no         INT NOT NULL,
    challan_line_no INT NOT NULL,
    kind            TEXT NOT NULL CHECK (kind IN ('processed','returned','waste')),
    item_id         UUID NOT NULL,     -- processed: the item received; else the item sent
    item_name       TEXT NOT NULL,
    unit            TEXT NOT NULL,
    qty             NUMERIC(14,3) NOT NULL CHECK (qty >= 0), -- received qty in `unit`
    input_qty       NUMERIC(14,3) NOT NULL CHECK (input_qty > 0), -- challan-line qty this settles (challan unit)
    value_paise     BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (receipt_id, line_no)
);
