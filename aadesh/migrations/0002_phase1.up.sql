-- aadesh phase 1: numbering per GSTIN, credit notes reduce balances, supplier
-- bill date + duplicate check, change due, store GSTIN, richer lines.

-- GST wants one unique serial per GSTIN per financial year, not per store.
ALTER TABLE invoices DROP CONSTRAINT IF EXISTS invoices_tenant_id_store_id_doc_type_fy_number_key;
CREATE UNIQUE INDEX IF NOT EXISTS invoices_number_uq ON invoices (tenant_id, doc_type, fy, number);

ALTER TABLE invoices ADD COLUMN IF NOT EXISTS supplier_invoice_date DATE;
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS due_date DATE;
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS store_gstin TEXT;
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS change_paise BIGINT NOT NULL DEFAULT 0;     -- cash returned to the customer
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS credited_paise BIGINT NOT NULL DEFAULT 0;   -- posted credit/debit notes against this invoice
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS supply_category TEXT NOT NULL DEFAULT '';
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS reverse_charge BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS invoice_discount_paise BIGINT NOT NULL DEFAULT 0;
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS tcs_paise BIGINT NOT NULL DEFAULT 0;
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS irn TEXT;

-- The same supplier bill must not be entered twice (GSTR-2B matching, ITC).
CREATE UNIQUE INDEX IF NOT EXISTS invoices_supplier_bill_uq
    ON invoices (tenant_id, party_id, supplier_invoice_no, supplier_invoice_date)
    WHERE doc_type = 'purchase' AND status = 'posted' AND supplier_invoice_no IS NOT NULL;

ALTER TABLE invoice_lines ADD COLUMN IF NOT EXISTS discount_bp INT NOT NULL DEFAULT 0;
ALTER TABLE invoice_lines ADD COLUMN IF NOT EXISTS cess_per_unit_paise BIGINT NOT NULL DEFAULT 0;
ALTER TABLE invoice_lines ADD COLUMN IF NOT EXISTS tax_category TEXT NOT NULL DEFAULT 'taxable';

-- A counter UPI/card tender that lenden confirmed carries the payment request it answered,
-- so bahi books it once (on lenden's capture event) and aadesh does not count it twice.
ALTER TABLE invoice_tenders ADD COLUMN IF NOT EXISTS payment_request_id TEXT;

-- Numbering: by default one series per tenant (GSTIN) and document type, shared by every
-- store and keyed by the nil UUID. A store gets its own series only when it has a custom
-- prefix here (prefix ≤ 4 chars keeps "PFX/26-27/00001" within GST's 16-character limit).
CREATE TABLE IF NOT EXISTS numbering_settings (
    tenant_id UUID NOT NULL,
    store_id  UUID NOT NULL,
    doc_type  TEXT NOT NULL,
    prefix    TEXT NOT NULL CHECK (prefix ~ '^[A-Z0-9][A-Z0-9-]{0,3}$'),
    PRIMARY KEY (tenant_id, store_id, doc_type)
);

-- Seed the shared series from the highest per-store series so new numbers never collide
-- with bills issued under the old per-store numbering.
INSERT INTO number_series (tenant_id, store_id, doc_type, fy, prefix, next_no)
SELECT tenant_id, '00000000-0000-0000-0000-000000000000', doc_type, fy, min(prefix), max(next_no)
FROM number_series
WHERE store_id <> '00000000-0000-0000-0000-000000000000'
GROUP BY tenant_id, doc_type, fy
ON CONFLICT (tenant_id, store_id, doc_type, fy) DO NOTHING;

-- Offline number blocks: which series they came from, how far the device used them, and
-- when the remainder was handed back (unused numbers are reported as gaps, never reused).
ALTER TABLE number_blocks ADD COLUMN IF NOT EXISTS series_store_id UUID NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000';
ALTER TABLE number_blocks ADD COLUMN IF NOT EXISTS last_used_no BIGINT;
ALTER TABLE number_blocks ADD COLUMN IF NOT EXISTS released_at TIMESTAMPTZ;
UPDATE number_blocks SET series_store_id = store_id WHERE series_store_id = '00000000-0000-0000-0000-000000000000' AND created_at < now();
