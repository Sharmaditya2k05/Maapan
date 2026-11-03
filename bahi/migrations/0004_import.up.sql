-- bahi: import support for aayaat (bill-wise opening, invoice history,
-- chart import, rollback). Additive only.

-- Parties: shipping address, and whether the record was created by an import
-- (rollback may archive only those, and only while they have no activity).
ALTER TABLE parties ADD COLUMN IF NOT EXISTS shipping_address TEXT;
ALTER TABLE parties ADD COLUMN IF NOT EXISTS created_via      TEXT; -- NULL = keyed in / event, 'import' = bulk import

-- Accounts created by bulk/accounts (Tally ledgers): external key + origin.
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS external_ref TEXT;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS created_via  TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS accounts_external_ref ON accounts (tenant_id, external_ref) WHERE external_ref IS NOT NULL;

-- The importing system's key of an opening line / opening bill / invoice
-- (informational; idempotency stays on (source, source_id)).
ALTER TABLE vouchers ADD COLUMN IF NOT EXISTS external_ref TEXT;

-- Opening bills keep the original bill amount next to what was outstanding.
ALTER TABLE bills ADD COLUMN IF NOT EXISTS original_amount_paise BIGINT;

-- Past invoices imported from the old books. Rows dated before the import's
-- as_of are history only (their effect is already in the opening balances);
-- rows on/after as_of are posted and point at their voucher.
CREATE TABLE IF NOT EXISTS invoice_history (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        UUID NOT NULL,
    external_ref     TEXT NOT NULL,
    as_of            DATE NOT NULL,
    status           TEXT NOT NULL CHECK (status IN ('history','posted')),
    voucher_id       UUID REFERENCES vouchers(id),
    doc_type         TEXT NOT NULL CHECK (doc_type IN ('sale','purchase','sale_return','purchase_return')),
    number           TEXT NOT NULL,
    inv_date         DATE NOT NULL,
    due_date         DATE,
    party_id         UUID REFERENCES parties(id),
    party_name       TEXT,
    party_gstin      TEXT,
    place_of_supply  TEXT,
    taxable_paise    BIGINT NOT NULL DEFAULT 0,
    cgst_paise       BIGINT NOT NULL DEFAULT 0,
    sgst_paise       BIGINT NOT NULL DEFAULT 0,
    igst_paise       BIGINT NOT NULL DEFAULT 0,
    cess_paise       BIGINT NOT NULL DEFAULT 0,
    round_off_paise  BIGINT NOT NULL DEFAULT 0,
    total_paise      BIGINT NOT NULL,
    reference        TEXT,
    narration        TEXT,
    store_id         UUID,
    created_by       TEXT NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, external_ref)
);
CREATE INDEX IF NOT EXISTS invoice_history_party ON invoice_history (tenant_id, party_id, inv_date);
CREATE INDEX IF NOT EXISTS invoice_history_date ON invoice_history (tenant_id, inv_date);

DROP TRIGGER IF EXISTS invoice_history_audit ON invoice_history;
CREATE TRIGGER invoice_history_audit AFTER INSERT OR UPDATE OR DELETE ON invoice_history
    FOR EACH ROW EXECUTE FUNCTION bahi_audit();
