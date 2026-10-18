-- bhandar: item master, batches, multi-store stock ledger, transfers, alerts

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

CREATE TABLE units (
    tenant_id UUID NOT NULL,
    code      TEXT NOT NULL,
    name      TEXT NOT NULL,
    PRIMARY KEY (tenant_id, code)
);

CREATE TABLE items (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id             UUID NOT NULL,
    name                  TEXT NOT NULL,
    sku                   TEXT,
    barcode               TEXT,
    hsn                   TEXT,
    unit                  TEXT NOT NULL DEFAULT 'PCS',
    category              TEXT,
    brand                 TEXT,
    sale_price_paise      BIGINT NOT NULL DEFAULT 0,
    mrp_paise             BIGINT NOT NULL DEFAULT 0,
    purchase_price_paise  BIGINT NOT NULL DEFAULT 0,
    tax_rate_bp           INT NOT NULL DEFAULT 0,
    cess_rate_bp          INT NOT NULL DEFAULT 0,
    price_includes_tax    BOOLEAN NOT NULL DEFAULT true,
    track_batches         BOOLEAN NOT NULL DEFAULT false,
    reorder_level         NUMERIC(14,3) NOT NULL DEFAULT 0,
    reorder_qty           NUMERIC(14,3) NOT NULL DEFAULT 0,
    preferred_supplier_id UUID,
    status                TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','inactive')),
    external_ref          TEXT,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX items_barcode_uq ON items (tenant_id, barcode) WHERE barcode IS NOT NULL;
CREATE UNIQUE INDEX items_sku_uq ON items (tenant_id, sku) WHERE sku IS NOT NULL;
CREATE UNIQUE INDEX items_extref_uq ON items (tenant_id, external_ref) WHERE external_ref IS NOT NULL;
CREATE INDEX items_name_idx ON items (tenant_id, lower(name));

-- Alternate units: 1 <unit> = factor × base unit (e.g. 1 BOX = 12 PCS).
CREATE TABLE item_units (
    tenant_id UUID NOT NULL,
    item_id   UUID NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    unit      TEXT NOT NULL,
    factor    NUMERIC(14,6) NOT NULL CHECK (factor > 0),
    PRIMARY KEY (item_id, unit)
);

-- Per-tenant sequence for internal EAN-13 barcodes.
CREATE TABLE barcode_seq (
    tenant_id UUID PRIMARY KEY,
    next_no   BIGINT NOT NULL DEFAULT 1
);

CREATE TABLE batches (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL,
    item_id     UUID NOT NULL REFERENCES items(id),
    batch_no    TEXT NOT NULL,
    mfg_date    DATE,
    expiry_date DATE,
    mrp_paise   BIGINT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, item_id, batch_no)
);
CREATE INDEX batches_expiry_idx ON batches (tenant_id, expiry_date) WHERE expiry_date IS NOT NULL;

-- Append-only stock ledger. (source, source_id, line_no) makes every write idempotent.
CREATE TABLE stock_movements (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL,
    store_id        UUID NOT NULL,
    item_id         UUID NOT NULL REFERENCES items(id),
    batch_id        UUID REFERENCES batches(id),
    qty             NUMERIC(14,3) NOT NULL,
    unit_cost_paise BIGINT NOT NULL DEFAULT 0,
    reason          TEXT NOT NULL CHECK (reason IN ('sale','purchase','sale_return','purchase_return','transfer_in',
                        'transfer_out','adjustment','opening','production_consume','production_output','expiry_writeoff')),
    source          TEXT NOT NULL,
    source_id       TEXT NOT NULL,
    line_no         INT NOT NULL DEFAULT 0,
    note            TEXT,
    created_by      TEXT NOT NULL DEFAULT 'system',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, source, source_id, line_no)
);
CREATE INDEX stock_movements_item_idx ON stock_movements (tenant_id, item_id, created_at DESC);
CREATE INDEX stock_movements_store_idx ON stock_movements (tenant_id, store_id, created_at DESC);

-- Current position per store/item/batch. batch_id uses the nil UUID for non-batch stock.
CREATE TABLE stock_balances (
    tenant_id      UUID NOT NULL,
    store_id       UUID NOT NULL,
    item_id        UUID NOT NULL REFERENCES items(id),
    batch_id       UUID NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000',
    qty            NUMERIC(14,3) NOT NULL DEFAULT 0,
    avg_cost_paise BIGINT NOT NULL DEFAULT 0,
    last_in_at     TIMESTAMPTZ,
    last_out_at    TIMESTAMPTZ,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, store_id, item_id, batch_id)
);
CREATE INDEX stock_balances_item_idx ON stock_balances (tenant_id, item_id);

CREATE TABLE transfers (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID NOT NULL,
    from_store_id UUID NOT NULL,
    to_store_id   UUID NOT NULL,
    status        TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','dispatched','received','cancelled')),
    notes         TEXT,
    created_by    TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    dispatched_at TIMESTAMPTZ,
    received_at   TIMESTAMPTZ,
    cancelled_at  TIMESTAMPTZ,
    CHECK (from_store_id <> to_store_id)
);
CREATE INDEX transfers_list_idx ON transfers (tenant_id, created_at DESC);

CREATE TABLE transfer_lines (
    transfer_id     UUID NOT NULL REFERENCES transfers(id) ON DELETE CASCADE,
    line_no         INT NOT NULL,
    item_id         UUID NOT NULL REFERENCES items(id),
    batch_id        UUID REFERENCES batches(id),
    qty             NUMERIC(14,3) NOT NULL CHECK (qty > 0),
    unit_cost_paise BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (transfer_id, line_no)
);

-- Alert de-duplication: one alert per item/store (or batch/store) per day.
CREATE TABLE reorder_alerts (
    tenant_id  UUID NOT NULL,
    store_id   UUID NOT NULL,
    item_id    UUID NOT NULL,
    alert_date DATE NOT NULL,
    PRIMARY KEY (tenant_id, store_id, item_id, alert_date)
);

CREATE TABLE expiry_alerts (
    tenant_id  UUID NOT NULL,
    store_id   UUID NOT NULL,
    batch_id   UUID NOT NULL,
    alert_date DATE NOT NULL,
    PRIMARY KEY (tenant_id, store_id, batch_id, alert_date)
);
