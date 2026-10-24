-- bahi phase 1: receipt routing + perpetual-inventory accounts, group fixes,
-- reversal linkage, event quarantine, retry budget, period lock. Additive only.

-- 1. Reversals: the ORIGINAL voucher is marked reversed and points at its
--    reversal; the reversal voucher stays posted. Repair rows written by the
--    old code, which marked the reversal voucher itself reversed.
ALTER TABLE vouchers ADD COLUMN IF NOT EXISTS reversed_by UUID REFERENCES vouchers(id);
UPDATE vouchers SET status='posted' WHERE reversal_of IS NOT NULL AND status='reversed';
UPDATE vouchers o SET reversed_by=r.id, status='reversed'
  FROM vouchers r
 WHERE r.reversal_of=o.id AND r.tenant_id=o.tenant_id AND o.reversed_by IS NULL;

-- 2. New system accounts for every tenant that already has a chart (new
--    tenants get them from the seed). Idempotent.
INSERT INTO accounts (tenant_id, code, name, grp, parent_id, is_group, is_system)
SELECT t.tenant_id, n.code, n.name, n.grp, p.id, false, true
  FROM (SELECT DISTINCT tenant_id FROM accounts WHERE code='1100') t
  CROSS JOIN (VALUES
      ('1115', 'Cheques in Hand',          'asset',   '1000'),
      ('1130', 'Payment Gateway Clearing', 'asset',   '1000'),
      ('4150', 'Stock Gain',               'income',  '4000'),
      ('5150', 'Cost of Goods Sold',       'expense', '5200'),
      ('5160', 'Stock Loss / Write-off',   'expense', '5200'),
      ('5290', 'Other Direct Expenses',    'expense', '5200'),
      ('5590', 'Other Indirect Expenses',  'expense', '5500')
  ) AS n(code, name, grp, parent)
  JOIN accounts p ON p.tenant_id=t.tenant_id AND p.code=n.parent
ON CONFLICT (tenant_id, code) DO NOTHING;

-- 3. Direct Expenses (5200) and Indirect Expenses (5500) become groups: a
--    postable account cannot also be a parent (group statements skipped their
--    children and POST /v1/accounts refused them as parent_code). Entries
--    posted straight to them move to the new "Other …" leaves first; voucher
--    totals are unchanged so the balance trigger is satisfied.
UPDATE voucher_entries e SET account_id = n.id
  FROM accounts o
  JOIN accounts n ON n.tenant_id=o.tenant_id
                 AND n.code = CASE o.code WHEN '5200' THEN '5290' ELSE '5590' END
 WHERE e.account_id=o.id AND e.tenant_id=o.tenant_id AND o.code IN ('5200','5500') AND o.is_system;
UPDATE accounts SET is_group=true WHERE code IN ('5200','5500') AND is_system AND NOT is_group;

-- 4. Events that cannot be posted as delivered are parked here and
--    acknowledged (instead of dropped, or retried forever). payload is the
--    full envelope; POST /v1/quarantine/{event_id}/retry replays it.
CREATE TABLE IF NOT EXISTS quarantined_events (
    event_id    TEXT PRIMARY KEY,
    tenant_id   UUID,
    event_type  TEXT,
    reason      TEXT,
    payload     JSONB,
    created_at  TIMESTAMPTZ DEFAULT now(),
    resolved_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS quarantine_open ON quarantined_events (tenant_id, created_at) WHERE resolved_at IS NULL;

-- 5. Retry budget for retryable failures (party not replicated yet,
--    cancellation delivered before its invoice): after the 3rd failed
--    delivery the event is quarantined.
CREATE TABLE IF NOT EXISTS event_attempts (
    event_id   TEXT PRIMARY KEY,
    attempts   INT NOT NULL DEFAULT 0,
    last_error TEXT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 6. Period lock: no voucher may be posted on or before locked_until.
CREATE TABLE IF NOT EXISTS period_locks (
    tenant_id    UUID PRIMARY KEY,
    locked_until DATE NOT NULL,
    locked_by    TEXT,
    locked_at    TIMESTAMPTZ DEFAULT now()
);
