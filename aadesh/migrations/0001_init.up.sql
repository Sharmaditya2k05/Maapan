-- aadesh: invoices (sales, purchases, credit/debit notes), numbering, offline sync

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

-- One running sequence per tenant/store/document type/financial year.
CREATE TABLE number_series (
    tenant_id  UUID NOT NULL,
    store_id   UUID NOT NULL,
    doc_type   TEXT NOT NULL,
    fy         TEXT NOT NULL,             -- "26-27"
    prefix     TEXT NOT NULL,
    next_no    BIGINT NOT NULL DEFAULT 1,
    PRIMARY KEY (tenant_id, store_id, doc_type, fy)
);

-- Ranges handed to a POS device so it can number bills while offline.
CREATE TABLE number_blocks (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  UUID NOT NULL,
    store_id   UUID NOT NULL,
    doc_type   TEXT NOT NULL,
    fy         TEXT NOT NULL,
    device_id  TEXT NOT NULL,
    start_no   BIGINT NOT NULL,
    end_no     BIGINT NOT NULL,              -- inclusive
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX number_blocks_lookup ON number_blocks (tenant_id, store_id, doc_type, fy, device_id);

CREATE TABLE invoices (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID NOT NULL,
    store_id            UUID NOT NULL,
    doc_type            TEXT NOT NULL CHECK (doc_type IN ('sale','purchase','sale_return','purchase_return')),
    number              TEXT NOT NULL,
    seq_no              BIGINT NOT NULL,
    fy                  TEXT NOT NULL,
    invoice_date        DATE NOT NULL,
    supplier_invoice_no TEXT,                                   -- purchases: the vendor's bill number
    ref_invoice_id      UUID REFERENCES invoices(id),           -- notes: the original invoice
    party_id            UUID,
    party_name          TEXT NOT NULL DEFAULT 'Cash Sale',
    party_phone         TEXT,
    party_gstin         TEXT,
    party_state         TEXT,
    store_state         TEXT NOT NULL,
    place_of_supply     TEXT NOT NULL,
    inter_state         BOOLEAN NOT NULL,
    status              TEXT NOT NULL DEFAULT 'posted' CHECK (status IN ('posted','cancelled')),
    payment_status      TEXT NOT NULL DEFAULT 'unpaid' CHECK (payment_status IN ('unpaid','partial','paid')),
    taxable_paise       BIGINT NOT NULL,
    discount_paise      BIGINT NOT NULL DEFAULT 0,
    cgst_paise          BIGINT NOT NULL DEFAULT 0,
    sgst_paise          BIGINT NOT NULL DEFAULT 0,
    igst_paise          BIGINT NOT NULL DEFAULT 0,
    cess_paise          BIGINT NOT NULL DEFAULT 0,
    round_off_paise     BIGINT NOT NULL DEFAULT 0,
    grand_total_paise   BIGINT NOT NULL,
    paid_paise          BIGINT NOT NULL DEFAULT 0,
    notes               TEXT,
    client_uuid         TEXT,                                    -- idempotency key from POS (offline sync)
    device_id           TEXT,
    created_by          TEXT NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    client_created_at   TIMESTAMPTZ,
    cancelled_at        TIMESTAMPTZ,
    cancel_reason       TEXT,
    UNIQUE (tenant_id, store_id, doc_type, fy, number)
);
CREATE UNIQUE INDEX invoices_client_uuid ON invoices (tenant_id, client_uuid) WHERE client_uuid IS NOT NULL;
CREATE INDEX invoices_list ON invoices (tenant_id, doc_type, invoice_date DESC);
CREATE INDEX invoices_party ON invoices (tenant_id, party_id);

CREATE TABLE invoice_lines (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_id         UUID NOT NULL REFERENCES invoices(id) ON DELETE CASCADE,
    line_no            INT NOT NULL,
    item_id            UUID,
    name               TEXT NOT NULL,
    hsn                TEXT,
    unit               TEXT NOT NULL DEFAULT 'PCS',
    batch_no           TEXT,
    qty                NUMERIC(14,3) NOT NULL,
    unit_price_paise   BIGINT NOT NULL,
    discount_paise     BIGINT NOT NULL DEFAULT 0,
    tax_rate_bp        INT NOT NULL,
    cess_rate_bp       INT NOT NULL DEFAULT 0,
    price_includes_tax BOOLEAN NOT NULL DEFAULT false,
    taxable_paise      BIGINT NOT NULL,
    cgst_paise         BIGINT NOT NULL,
    sgst_paise         BIGINT NOT NULL,
    igst_paise         BIGINT NOT NULL,
    cess_paise         BIGINT NOT NULL,
    total_paise        BIGINT NOT NULL,
    UNIQUE (invoice_id, line_no)
);

-- How the bill was settled at the counter (cash/card/upi). Online gateway
-- payments arrive later from lenden and only update paid_paise.
CREATE TABLE invoice_tenders (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_id   UUID NOT NULL REFERENCES invoices(id) ON DELETE CASCADE,
    mode         TEXT NOT NULL CHECK (mode IN ('cash','card','upi','bank','credit','other')),
    amount_paise BIGINT NOT NULL CHECK (amount_paise > 0),
    reference    TEXT
);

-- Payments received through lenden, keyed by payment id for idempotency.
CREATE TABLE invoice_receipts (
    payment_id   TEXT PRIMARY KEY,
    invoice_id   UUID NOT NULL REFERENCES invoices(id),
    amount_paise BIGINT NOT NULL,
    refunded_paise BIGINT NOT NULL DEFAULT 0,       -- supports several partial refunds
    received_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
