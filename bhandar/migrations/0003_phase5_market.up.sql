-- bhandar Phase 5: price lists & schemes, serial numbers, variants, stock take,
-- bin locations, supplier–item mapping, weighing-scale formats, import rollback.

-- ---------- item master additions ----------
ALTER TABLE items ADD COLUMN IF NOT EXISTS parent_id UUID REFERENCES items(id);   -- variant → parent item
ALTER TABLE items ADD COLUMN IF NOT EXISTS variant_attrs JSONB;                    -- {"size":"M","colour":"Red"}
ALTER TABLE items ADD COLUMN IF NOT EXISTS variant_key TEXT;                       -- canonical attrs, unique per parent
ALTER TABLE items ADD COLUMN IF NOT EXISTS track_serials BOOLEAN NOT NULL DEFAULT false;
-- Set by user edits (PUT, re-order, status, variants) but not by bulk imports:
-- an import rollback only archives items nobody has touched since.
ALTER TABLE items ADD COLUMN IF NOT EXISTS edited_at TIMESTAMPTZ;
-- Archived by an import rollback (status inactive). A re-import revives the row.
ALTER TABLE items ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ;
ALTER TABLE items ADD COLUMN IF NOT EXISTS archived_job_id TEXT;
CREATE INDEX IF NOT EXISTS items_parent_idx ON items (tenant_id, parent_id) WHERE parent_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS items_variant_uq ON items (tenant_id, parent_id, variant_key) WHERE parent_id IS NOT NULL;

-- ---------- price lists ----------
CREATE TABLE IF NOT EXISTS price_lists (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL,
    name        TEXT NOT NULL,
    kind        TEXT NOT NULL CHECK (kind IN ('retail','wholesale','party')),
    is_default  BOOLEAN NOT NULL DEFAULT false,   -- the list used for its kind when nothing more specific applies
    priority    INT NOT NULL DEFAULT 0,           -- higher wins among lists of the same rank
    valid_from  DATE,
    valid_to    DATE,                             -- inclusive
    active      BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name),
    CHECK (valid_to IS NULL OR valid_from IS NULL OR valid_to >= valid_from)
);
CREATE UNIQUE INDEX IF NOT EXISTS price_lists_default_uq ON price_lists (tenant_id, kind) WHERE is_default;

-- Parties (bahi party ids) a list applies to.
CREATE TABLE IF NOT EXISTS price_list_parties (
    tenant_id     UUID NOT NULL,
    price_list_id UUID NOT NULL REFERENCES price_lists(id) ON DELETE CASCADE,
    party_id      UUID NOT NULL,
    PRIMARY KEY (price_list_id, party_id)
);
CREATE INDEX IF NOT EXISTS price_list_parties_party_idx ON price_list_parties (tenant_id, party_id);

-- One price per item, unit ('' = base unit), minimum qty and effective date.
CREATE TABLE IF NOT EXISTS price_list_items (
    tenant_id     UUID NOT NULL,
    price_list_id UUID NOT NULL REFERENCES price_lists(id) ON DELETE CASCADE,
    item_id       UUID NOT NULL REFERENCES items(id),
    unit          TEXT NOT NULL DEFAULT '',
    min_qty       NUMERIC(14,3) NOT NULL DEFAULT 0,
    valid_from    DATE NOT NULL DEFAULT '2000-01-01',
    valid_to      DATE,
    price_paise   BIGINT NOT NULL CHECK (price_paise >= 0),
    PRIMARY KEY (price_list_id, item_id, unit, min_qty, valid_from)
);
CREATE INDEX IF NOT EXISTS price_list_items_item_idx ON price_list_items (tenant_id, item_id);

-- ---------- schemes ----------
CREATE TABLE IF NOT EXISTS schemes (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID NOT NULL,
    name          TEXT NOT NULL,
    kind          TEXT NOT NULL CHECK (kind IN ('free_qty','percent','flat')),
    buy_qty       NUMERIC(14,3) NOT NULL DEFAULT 0,  -- free_qty: buy X …
    free_qty      NUMERIC(14,3) NOT NULL DEFAULT 0,  -- … get Y
    free_item_id  UUID REFERENCES items(id),         -- NULL = the same item
    slabs         JSONB NOT NULL DEFAULT '[]',       -- percent / flat: [{min_qty, percent_bp | off_per_unit_paise}]
    category      TEXT,                              -- applies to a whole category (in addition to scheme_items)
    party_ids     UUID[],                            -- NULL = every party
    valid_from    DATE NOT NULL,
    valid_to      DATE,
    active        BOOLEAN NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);
CREATE TABLE IF NOT EXISTS scheme_items (
    scheme_id UUID NOT NULL REFERENCES schemes(id) ON DELETE CASCADE,
    item_id   UUID NOT NULL REFERENCES items(id),
    PRIMARY KEY (scheme_id, item_id)
);
CREATE INDEX IF NOT EXISTS scheme_items_item_idx ON scheme_items (item_id);

-- ---------- serial numbers ----------
CREATE TABLE IF NOT EXISTS serials (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL,
    item_id     UUID NOT NULL REFERENCES items(id),
    serial_no   TEXT NOT NULL,
    status      TEXT NOT NULL CHECK (status IN ('in_stock','sold','returned_to_vendor','written_off','in_transit')),
    store_id    UUID,                 -- where it is (in_stock) or was last (sold, …)
    batch_no    TEXT,
    supplier_id UUID,
    party_id    UUID,                 -- customer it was sold to
    received_at TIMESTAMPTZ,
    sold_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, item_id, serial_no)
);
CREATE INDEX IF NOT EXISTS serials_no_idx ON serials (tenant_id, serial_no);
CREATE INDEX IF NOT EXISTS serials_stock_idx ON serials (tenant_id, item_id, store_id) WHERE status = 'in_stock';

-- History of every serial; prev_* let a cancellation restore the earlier state.
CREATE TABLE IF NOT EXISTS serial_events (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL,
    serial_id   UUID NOT NULL REFERENCES serials(id),
    event       TEXT NOT NULL,        -- received | sold | returned | vendor_returned | adjusted_in | written_off | reverted
    store_id    UUID,
    source      TEXT NOT NULL,
    source_id   TEXT NOT NULL,
    line_no     INT NOT NULL DEFAULT 0,
    prev_status TEXT,                 -- NULL = the serial did not exist before
    prev_store  UUID,
    reverted    BOOLEAN NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, serial_id, source, source_id)
);
CREATE INDEX IF NOT EXISTS serial_events_source_idx ON serial_events (tenant_id, source_id);

-- ---------- bins / racks ----------
CREATE TABLE IF NOT EXISTS bins (
    tenant_id  UUID NOT NULL,
    store_id   UUID NOT NULL,
    code       TEXT NOT NULL,
    name       TEXT,
    active     BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, store_id, code)
);
CREATE TABLE IF NOT EXISTS item_bins (
    tenant_id UUID NOT NULL,
    store_id  UUID NOT NULL,
    item_id   UUID NOT NULL REFERENCES items(id),
    bin_code  TEXT NOT NULL,
    PRIMARY KEY (tenant_id, store_id, item_id),
    FOREIGN KEY (tenant_id, store_id, bin_code) REFERENCES bins (tenant_id, store_id, code)
);

-- ---------- supplier–item mapping ----------
CREATE TABLE IF NOT EXISTS supplier_items (
    tenant_id          UUID NOT NULL,
    supplier_id        UUID NOT NULL,          -- bahi party id
    item_id            UUID NOT NULL REFERENCES items(id),
    supplier_sku       TEXT,
    supplier_item_name TEXT,
    lead_time_days     INT,
    last_price_paise   BIGINT,                 -- per base unit, taxable (GST excluded)
    last_purchase_date DATE,
    last_invoice_id    TEXT,
    last_invoice_no    TEXT,
    last_qty           NUMERIC(14,3),
    purchases          INT NOT NULL DEFAULT 0,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, supplier_id, item_id)
);
CREATE INDEX IF NOT EXISTS supplier_items_item_idx ON supplier_items (tenant_id, item_id);

-- ---------- stock take / cycle count ----------
CREATE TABLE IF NOT EXISTS stock_counts (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL,
    store_id    UUID NOT NULL,
    name        TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'counting' CHECK (status IN ('counting','posted','cancelled')),
    category    TEXT,
    bin_code    TEXT,
    item_ids    UUID[],
    note        TEXT,
    frozen_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    posted_at   TIMESTAMPTZ,
    cancelled_at TIMESTAMPTZ,
    created_by  TEXT NOT NULL,
    posted_by   TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS stock_counts_list_idx ON stock_counts (tenant_id, store_id, created_at DESC);

CREATE TABLE IF NOT EXISTS stock_count_lines (
    count_id       UUID NOT NULL REFERENCES stock_counts(id) ON DELETE CASCADE,
    item_id        UUID NOT NULL REFERENCES items(id),
    batch_id       UUID NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000',
    line_no        INT NOT NULL,
    frozen_qty     NUMERIC(14,3) NOT NULL DEFAULT 0,
    avg_cost_paise BIGINT NOT NULL DEFAULT 0,
    counted_qty    NUMERIC(14,3),
    counted_at     TIMESTAMPTZ,
    counted_by     TEXT,
    in_scope       BOOLEAN NOT NULL DEFAULT true,  -- false: scanned though not in the frozen snapshot
    PRIMARY KEY (count_id, item_id, batch_id)
);

-- ---------- weighing-scale barcode layouts ----------
CREATE TABLE IF NOT EXISTS scale_formats (
    tenant_id      UUID NOT NULL,
    prefix         TEXT NOT NULL CHECK (prefix ~ '^2[0-9]$'),
    kind           TEXT NOT NULL CHECK (kind IN ('price','weight','none')),
    plu_len        INT NOT NULL DEFAULT 5 CHECK (plu_len BETWEEN 4 AND 6),
    check_digit    BOOLEAN NOT NULL DEFAULT false,  -- an internal price/weight check digit after the PLU
    value_decimals INT NOT NULL DEFAULT 2,          -- price: decimals of rupees; weight: decimals of kg
    PRIMARY KEY (tenant_id, prefix)
);
