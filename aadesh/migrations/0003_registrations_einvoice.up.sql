-- aadesh phase 3: numbering per GST registration, purchase ITC eligibility, and the
-- e-invoice (IRN) / e-way bill details kar reports back (PHASE3-CONTRACTS.md §2).

-- The registration (GSTIN) a document was issued under, from pehchaan's store lookup.
-- registration_key is the number series' owner: the nil UUID for the tenant-wide series
-- (the primary registration — every existing document), else the registration id.
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS registration_id UUID;
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS registration_key UUID NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000';

-- Serial numbers are unique per GSTIN: two registrations may both issue INV/26-27/00001.
DROP INDEX IF EXISTS invoices_number_uq;
CREATE UNIQUE INDEX IF NOT EXISTS invoices_registration_number_uq ON invoices (tenant_id, registration_key, doc_type, fy, number);
CREATE INDEX IF NOT EXISTS invoices_registration ON invoices (tenant_id, registration_id, invoice_date);
CREATE INDEX IF NOT EXISTS invoices_store_gstin ON invoices (tenant_id, store_gstin, invoice_date);

-- Which registration owns the tenant-wide (nil-key) series. Claimed by the first primary
-- registration that numbers a document, so a later change of primary never makes two
-- GSTINs share — or restart — one series.
CREATE TABLE IF NOT EXISTS registration_series (
    tenant_id       UUID PRIMARY KEY,
    registration_id UUID NOT NULL,
    claimed_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Purchase lines: is the input tax creditable? '' on sales documents.
ALTER TABLE invoice_lines ADD COLUMN IF NOT EXISTS itc_eligibility TEXT NOT NULL DEFAULT ''
    CHECK (itc_eligibility IN ('', 'eligible', 'ineligible_17_5', 'ineligible_other'));
UPDATE invoice_lines l SET itc_eligibility = 'eligible'
FROM invoices i
WHERE i.id = l.invoice_id AND i.doc_type IN ('purchase', 'purchase_return') AND l.itc_eligibility = '';

-- e-Invoice (kar.einvoice.generated). irn already exists (0002).
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS ack_no TEXT;
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS ack_date TIMESTAMPTZ;
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS signed_qr TEXT;
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS einvoice_status TEXT
    CHECK (einvoice_status IS NULL OR einvoice_status IN ('generated', 'cancelled', 'failed'));
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS einvoice_error TEXT;
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS einvoice_error_code TEXT;
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS irn_cancelled_at TIMESTAMPTZ;

-- e-Way bill (kar.eway.generated).
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS ewb_no TEXT;
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS ewb_date TIMESTAMPTZ;
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS ewb_valid_upto TIMESTAMPTZ;
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS ewb_status TEXT
    CHECK (ewb_status IS NULL OR ewb_status IN ('generated', 'updated', 'extended', 'cancelled', 'failed'));
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS ewb_error TEXT;
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS ewb_vehicle_no TEXT;
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS ewb_distance_km INT;
