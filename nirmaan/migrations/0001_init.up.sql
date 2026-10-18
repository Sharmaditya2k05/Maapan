-- nirmaan: bills of materials, cost roll-up, production runs

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

-- item ids refer to bhandar items; names are snapshotted for display.
CREATE TABLE boms (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  UUID NOT NULL,
    item_id    UUID NOT NULL,
    item_name  TEXT NOT NULL,
    version    INT NOT NULL,
    output_qty NUMERIC(14,3) NOT NULL CHECK (output_qty > 0),
    unit       TEXT NOT NULL DEFAULT 'PCS',
    status     TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','active','archived')),
    notes      TEXT,
    created_by TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, item_id, version)
);
-- One active version per finished item.
CREATE UNIQUE INDEX boms_one_active ON boms (tenant_id, item_id) WHERE status = 'active';

CREATE TABLE bom_components (
    bom_id          UUID NOT NULL REFERENCES boms(id) ON DELETE CASCADE,
    line_no         INT NOT NULL,
    item_id         UUID NOT NULL,
    item_name       TEXT NOT NULL,
    qty             NUMERIC(14,3) NOT NULL CHECK (qty > 0),
    unit            TEXT NOT NULL DEFAULT 'PCS',
    scrap_percent   NUMERIC(5,2) NOT NULL DEFAULT 0 CHECK (scrap_percent >= 0 AND scrap_percent < 100),
    is_sub_assembly BOOLEAN NOT NULL DEFAULT false,
    sub_bom_id      UUID REFERENCES boms(id),
    PRIMARY KEY (bom_id, line_no)
);
CREATE INDEX bom_components_item_idx ON bom_components (item_id);

CREATE TABLE bom_operations (
    bom_id     UUID NOT NULL REFERENCES boms(id) ON DELETE CASCADE,
    line_no    INT NOT NULL,
    name       TEXT NOT NULL,
    cost_paise BIGINT NOT NULL DEFAULT 0 CHECK (cost_paise >= 0),
    PRIMARY KEY (bom_id, line_no)
);

CREATE TABLE production_runs (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        UUID NOT NULL,
    bom_id           UUID NOT NULL REFERENCES boms(id),
    item_id          UUID NOT NULL,
    item_name        TEXT NOT NULL,
    store_id         UUID NOT NULL,
    qty              NUMERIC(14,3) NOT NULL CHECK (qty > 0),
    output_qty       NUMERIC(14,3),
    planned_date     DATE NOT NULL,
    status           TEXT NOT NULL DEFAULT 'planned' CHECK (status IN ('planned','in_progress','completed','cancelled')),
    output_batch_no  TEXT,
    total_cost_paise BIGINT,
    notes            TEXT,
    created_by       TEXT NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at       TIMESTAMPTZ,
    completed_at     TIMESTAMPTZ,
    cancelled_at     TIMESTAMPTZ
);
CREATE INDEX production_runs_list_idx ON production_runs (tenant_id, planned_date DESC);

CREATE TABLE production_run_lines (
    run_id          UUID NOT NULL REFERENCES production_runs(id) ON DELETE CASCADE,
    line_no         INT NOT NULL,
    kind            TEXT NOT NULL CHECK (kind IN ('consume','produce')),
    item_id         UUID NOT NULL,
    item_name       TEXT NOT NULL,
    unit            TEXT NOT NULL DEFAULT 'PCS',
    is_sub_assembly BOOLEAN NOT NULL DEFAULT false,
    planned_qty     NUMERIC(14,3) NOT NULL,
    actual_qty      NUMERIC(14,3),
    unit_cost_paise BIGINT,
    PRIMARY KEY (run_id, line_no)
);
