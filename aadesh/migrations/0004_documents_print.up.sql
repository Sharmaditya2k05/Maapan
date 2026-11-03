-- aadesh phase 4: the pre-invoice document flow (quotation → sales order → delivery challan →
-- tax invoice, proforma, purchase order → GRN → purchase bill), per-tenant round-off rules and
-- print settings, multi-unit lines, and serial-number tracking.
--
-- Orders live in their own tables, not in `invoices`: they are not tax documents, never reach
-- GSTR-1/2B, never post to stock or books, and must not show up in kar's /internal/v1/invoices.

CREATE TABLE IF NOT EXISTS orders (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID NOT NULL,
    store_id            UUID NOT NULL,
    kind                TEXT NOT NULL CHECK (kind IN ('quotation','proforma','sales_order','delivery_challan','purchase_order','grn')),
    number              TEXT NOT NULL,
    seq_no              BIGINT NOT NULL,
    fy                  TEXT NOT NULL,
    registration_key    UUID NOT NULL,
    registration_id     UUID,
    doc_date            DATE NOT NULL,
    valid_until         DATE,                 -- quotation / proforma
    expected_date       DATE,                 -- sales order delivery / purchase order arrival
    party_id            UUID,
    party_name          TEXT NOT NULL,
    party_phone         TEXT,
    party_gstin         TEXT,
    party_state         TEXT,
    store_state         TEXT NOT NULL,
    store_gstin         TEXT,
    place_of_supply     TEXT NOT NULL,
    inter_state         BOOLEAN NOT NULL,
    supplier_ref        TEXT,                 -- GRN: the supplier's delivery note / challan no
    challan_reason      TEXT,                 -- delivery challan (Rule 55): supply | job_work | approval | other
    status              TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','partial','completed','closed','cancelled')),
    taxable_paise       BIGINT NOT NULL DEFAULT 0,
    discount_paise      BIGINT NOT NULL DEFAULT 0,
    invoice_discount_bp INT NOT NULL DEFAULT 0,
    cgst_paise          BIGINT NOT NULL DEFAULT 0,
    sgst_paise          BIGINT NOT NULL DEFAULT 0,
    igst_paise          BIGINT NOT NULL DEFAULT 0,
    cess_paise          BIGINT NOT NULL DEFAULT 0,
    round_off_paise     BIGINT NOT NULL DEFAULT 0,
    grand_total_paise   BIGINT NOT NULL DEFAULT 0,
    source_order_id     UUID REFERENCES orders(id),
    notes               TEXT,
    terms               TEXT,
    client_uuid         TEXT,
    created_by          TEXT NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    closed_at           TIMESTAMPTZ,
    close_reason        TEXT,
    cancelled_at        TIMESTAMPTZ,
    cancel_reason       TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS orders_number_uq ON orders (tenant_id, registration_key, kind, fy, number);
CREATE UNIQUE INDEX IF NOT EXISTS orders_client_uuid ON orders (tenant_id, client_uuid) WHERE client_uuid IS NOT NULL;
CREATE INDEX IF NOT EXISTS orders_list ON orders (tenant_id, kind, doc_date DESC);
CREATE INDEX IF NOT EXISTS orders_party ON orders (tenant_id, party_id);

CREATE TABLE IF NOT EXISTS order_lines (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id            UUID NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    line_no             INT NOT NULL,
    item_id             UUID,
    name                TEXT NOT NULL,
    hsn                 TEXT,
    unit                TEXT NOT NULL DEFAULT 'PCS',
    unit_factor         NUMERIC(14,4) NOT NULL DEFAULT 1,   -- base units per `unit` (BOX of 48 → 48)
    batch_no            TEXT,
    serials             TEXT[] NOT NULL DEFAULT '{}',
    qty                 NUMERIC(14,3) NOT NULL CHECK (qty > 0),
    converted_qty       NUMERIC(14,3) NOT NULL DEFAULT 0,  -- moved on to the next document(s); open = qty − converted
    unit_price_paise    BIGINT NOT NULL,
    discount_paise      BIGINT NOT NULL DEFAULT 0,
    discount_bp         INT NOT NULL DEFAULT 0,
    tax_rate_bp         INT NOT NULL,
    cess_rate_bp        INT NOT NULL DEFAULT 0,
    cess_per_unit_paise BIGINT NOT NULL DEFAULT 0,
    tax_category        TEXT NOT NULL DEFAULT 'taxable',
    price_includes_tax  BOOLEAN NOT NULL DEFAULT false,
    taxable_paise       BIGINT NOT NULL DEFAULT 0,
    cgst_paise          BIGINT NOT NULL DEFAULT 0,
    sgst_paise          BIGINT NOT NULL DEFAULT 0,
    igst_paise          BIGINT NOT NULL DEFAULT 0,
    cess_paise          BIGINT NOT NULL DEFAULT 0,
    total_paise         BIGINT NOT NULL DEFAULT 0,
    source_line_id      UUID REFERENCES order_lines(id),
    UNIQUE (order_id, line_no)
);

-- One row per conversion of (part of) an order line into a later document. A link is
-- reversed (never deleted) when the later document is cancelled, which restores open qty.
CREATE TABLE IF NOT EXISTS document_links (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      UUID NOT NULL,
    from_order_id  UUID NOT NULL REFERENCES orders(id),
    from_line_id   UUID NOT NULL REFERENCES order_lines(id),
    to_kind        TEXT NOT NULL,              -- an order kind, or sale | purchase
    to_order_id    UUID REFERENCES orders(id),
    to_invoice_id  UUID REFERENCES invoices(id),
    to_line_no     INT NOT NULL,
    qty            NUMERIC(14,3) NOT NULL CHECK (qty > 0),
    serials        TEXT[] NOT NULL DEFAULT '{}',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    reversed_at    TIMESTAMPTZ,
    CHECK ((to_order_id IS NULL) <> (to_invoice_id IS NULL))
);
CREATE INDEX IF NOT EXISTS document_links_from ON document_links (from_order_id) WHERE reversed_at IS NULL;
CREATE INDEX IF NOT EXISTS document_links_to_order ON document_links (to_order_id) WHERE to_order_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS document_links_to_invoice ON document_links (to_invoice_id) WHERE to_invoice_id IS NOT NULL;

-- Invoices made from an order remember where they came from.
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS source_order_id UUID REFERENCES orders(id);
CREATE INDEX IF NOT EXISTS invoices_source_order ON invoices (source_order_id) WHERE source_order_id IS NOT NULL;

-- Multi-unit lines: qty is in `unit`; base_qty = qty × unit_factor in the item's base unit.
ALTER TABLE invoice_lines ADD COLUMN IF NOT EXISTS unit_factor NUMERIC(14,4) NOT NULL DEFAULT 1;
ALTER TABLE invoice_lines ADD COLUMN IF NOT EXISTS base_unit TEXT;
ALTER TABLE invoice_lines ADD COLUMN IF NOT EXISTS serials TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE invoice_lines ADD COLUMN IF NOT EXISTS source_line_id UUID;

-- Serial-number ledger: every posted document line that carries serials writes one row per
-- serial (+1 into the business: purchase, sale_return; −1 out: sale, purchase_return). The
-- latest row of a serial on a posted document says where it is.
CREATE TABLE IF NOT EXISTS invoice_serials (
    id          BIGSERIAL PRIMARY KEY,
    tenant_id   UUID NOT NULL,
    invoice_id  UUID NOT NULL REFERENCES invoices(id) ON DELETE CASCADE,
    line_no     INT NOT NULL,
    item_key    TEXT NOT NULL,                -- item id, or the lower-cased name of a free-text line
    serial_no   TEXT NOT NULL,
    direction   SMALLINT NOT NULL CHECK (direction IN (-1, 1)),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX IF NOT EXISTS invoice_serials_lookup ON invoice_serials (tenant_id, item_key, serial_no, created_at DESC);

-- Per-tenant billing settings: round-off rule and what the printed bill carries.
CREATE TABLE IF NOT EXISTS billing_settings (
    tenant_id            UUID PRIMARY KEY,
    round_off_mode       TEXT NOT NULL DEFAULT 'nearest' CHECK (round_off_mode IN ('nearest','up','down','none')),
    round_off_step_paise INT NOT NULL DEFAULT 100 CHECK (round_off_step_paise IN (1, 10, 50, 100, 500, 1000)),
    bank_name            TEXT,
    bank_account_name    TEXT,
    bank_account_no      TEXT,
    bank_ifsc            TEXT,
    bank_branch          TEXT,
    upi_id               TEXT,
    invoice_terms        TEXT,
    invoice_footer       TEXT,
    thermal_width_mm     INT NOT NULL DEFAULT 80 CHECK (thermal_width_mm IN (58, 80)),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);
