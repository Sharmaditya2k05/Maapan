-- bhandar: tax classification per item (mool/gst supply types) and ledger indexes.

ALTER TABLE items ADD COLUMN IF NOT EXISTS cess_per_unit_paise BIGINT NOT NULL DEFAULT 0;
ALTER TABLE items ADD COLUMN IF NOT EXISTS tax_category TEXT NOT NULL DEFAULT 'taxable'
    CHECK (tax_category IN ('taxable','exempt','nil_rated','non_gst'));
ALTER TABLE items ADD COLUMN IF NOT EXISTS plu TEXT;              -- weighing-scale item code
CREATE UNIQUE INDEX IF NOT EXISTS items_plu_uq ON items (tenant_id, plu) WHERE plu IS NOT NULL;

-- Per-document lookups (cancellation reversal, return-to-vendor references).
CREATE INDEX IF NOT EXISTS stock_movements_source_idx ON stock_movements (tenant_id, source, source_id);
-- Dead-stock and aging scans per store.
CREATE INDEX IF NOT EXISTS stock_balances_store_idx ON stock_balances (tenant_id, store_id, item_id);
