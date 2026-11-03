-- bahi phase 4: accountant features. Additive only.
--   chart additions · bill-wise allocation · bank statement import + BRS ·
--   cheque register columns · TDS/TCS · GST set-off · edit audit trail.

-- 1. Chart additions for every tenant that already has a chart (new tenants
--    get them from the seed). Groups first so leaves can find their parent.
INSERT INTO accounts (tenant_id, code, name, grp, parent_id, is_group, is_system)
SELECT t.tenant_id, n.code, n.name, n.grp, p.id, true, true
  FROM (SELECT DISTINCT tenant_id FROM accounts WHERE code='1100') t
  CROSS JOIN (VALUES
      ('1450', 'GST Electronic Cash Ledger', 'asset',     '1000'),
      ('1500', 'Fixed Assets',               'asset',     '1000'),
      ('2400', 'Loans (Liability)',          'liability', '2000')
  ) AS n(code, name, grp, parent)
  JOIN accounts p ON p.tenant_id=t.tenant_id AND p.code=n.parent
ON CONFLICT (tenant_id, code) DO NOTHING;

INSERT INTO accounts (tenant_id, code, name, grp, parent_id, is_group, is_system)
SELECT t.tenant_id, n.code, n.name, n.grp, p.id, false, true
  FROM (SELECT DISTINCT tenant_id FROM accounts WHERE code='1100') t
  CROSS JOIN (VALUES
      ('1250', 'Advance to Suppliers',          'asset',     '1000'),
      ('1420', 'TDS Receivable',                'asset',     '1400'),
      ('1425', 'TCS Receivable',                'asset',     '1400'),
      ('1451', 'Electronic Cash Ledger - IGST', 'asset',     '1450'),
      ('1452', 'Electronic Cash Ledger - CGST', 'asset',     '1450'),
      ('1453', 'Electronic Cash Ledger - SGST', 'asset',     '1450'),
      ('1454', 'Electronic Cash Ledger - Cess', 'asset',     '1450'),
      ('1510', 'Furniture & Fixtures',          'asset',     '1500'),
      ('1520', 'Plant & Machinery',             'asset',     '1500'),
      ('1530', 'Computers & Office Equipment',  'asset',     '1500'),
      ('1540', 'Vehicles',                      'asset',     '1500'),
      ('1590', 'Accumulated Depreciation',      'asset',     '1500'),
      ('2150', 'Advance from Customers',        'liability', '2000'),
      ('2220', 'TDS Payable',                   'liability', '2200'),
      ('2225', 'TCS Payable',                   'liability', '2200'),
      ('2410', 'Secured Loans',                 'liability', '2400'),
      ('2420', 'Unsecured Loans',               'liability', '2400'),
      ('3200', 'Drawings',                      'equity',    '3000'),
      ('4200', 'Discount Received',             'income',    '4000'),
      ('5210', 'Freight Inward',                'expense',   '5200'),
      ('5520', 'Salaries & Wages',              'expense',   '5500'),
      ('5530', 'Rent',                          'expense',   '5500'),
      ('5540', 'Electricity',                   'expense',   '5500'),
      ('5550', 'Depreciation',                  'expense',   '5500'),
      ('5560', 'Freight Outward',               'expense',   '5500'),
      ('5570', 'Interest on Loans',             'expense',   '5500')
  ) AS n(code, name, grp, parent)
  JOIN accounts p ON p.tenant_id=t.tenant_id AND p.code=n.parent
ON CONFLICT (tenant_id, code) DO NOTHING;

-- Store-wise reports filter entries on store_id.
CREATE INDEX IF NOT EXISTS entries_store ON voucher_entries (tenant_id, store_id, vdate) WHERE store_id IS NOT NULL;

-- 2. Bill-wise allocation. A bill is what a sales/purchase/opening voucher
--    puts on a party (side dr = receivable, cr = payable); allocations tie a
--    settling voucher (receipt, payment, credit/debit note, TDS journal) to
--    specific bills. Whatever a settling voucher does not allocate is on
--    account. Outstanding per bill = amount − Σ allocations.
CREATE TABLE IF NOT EXISTS bills (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID NOT NULL,
    party_id     UUID NOT NULL REFERENCES parties(id),
    voucher_id   UUID NOT NULL REFERENCES vouchers(id),
    side         TEXT NOT NULL CHECK (side IN ('dr','cr')),
    ref          TEXT NOT NULL,
    bill_date    DATE NOT NULL,
    due_date     DATE NOT NULL,
    amount_paise BIGINT NOT NULL CHECK (amount_paise > 0),
    status       TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','cancelled')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, voucher_id, party_id)
);
CREATE INDEX IF NOT EXISTS bills_party ON bills (tenant_id, party_id, due_date);

CREATE TABLE IF NOT EXISTS bill_allocations (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID NOT NULL,
    bill_id      UUID NOT NULL REFERENCES bills(id) ON DELETE CASCADE,
    voucher_id   UUID NOT NULL REFERENCES vouchers(id),
    party_id     UUID NOT NULL REFERENCES parties(id),
    amount_paise BIGINT NOT NULL CHECK (amount_paise > 0),
    alloc_date   DATE NOT NULL, -- date of the settling voucher
    method       TEXT NOT NULL DEFAULT 'manual' CHECK (method IN ('manual','auto','event')),
    created_by   TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (bill_id, voucher_id)
);
CREATE INDEX IF NOT EXISTS bill_alloc_voucher ON bill_allocations (tenant_id, voucher_id);
CREATE INDEX IF NOT EXISTS bill_alloc_party ON bill_allocations (tenant_id, party_id);

-- Backfill bills for vouchers posted before phase 4 (due = date + credit_days).
INSERT INTO bills (tenant_id, party_id, voucher_id, side, ref, bill_date, due_date, amount_paise)
SELECT v.tenant_id, e.party_id, v.id,
       CASE WHEN sum(e.debit_paise - e.credit_paise) > 0 THEN 'dr' ELSE 'cr' END,
       COALESCE(v.source_ref, v.number), v.vdate, v.vdate + p.credit_days,
       abs(sum(e.debit_paise - e.credit_paise))
  FROM vouchers v
  JOIN voucher_entries e ON e.voucher_id=v.id AND e.party_id IS NOT NULL
  JOIN parties p ON p.id=e.party_id
 WHERE v.vtype IN ('sales','purchase','opening') AND v.status='posted' AND v.reversal_of IS NULL
 GROUP BY v.tenant_id, v.id, e.party_id, p.credit_days
HAVING (v.vtype='sales' AND sum(e.debit_paise - e.credit_paise) > 0)
    OR (v.vtype='purchase' AND sum(e.debit_paise - e.credit_paise) < 0)
    OR (v.vtype='opening' AND sum(e.debit_paise - e.credit_paise) <> 0)
ON CONFLICT (tenant_id, voucher_id, party_id) DO NOTHING;

-- 3. Bank statement import and reconciliation. A statement line is matched
--    one-to-one with a posting on the bank account (voucher_entries row).
CREATE TABLE IF NOT EXISTS bank_statements (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL,
    bank_account_id UUID NOT NULL REFERENCES accounts(id),
    preset          TEXT NOT NULL,
    file_name       TEXT,
    from_date       DATE,
    to_date         DATE,
    line_count      INT NOT NULL DEFAULT 0,
    duplicate_count INT NOT NULL DEFAULT 0,
    imported_by     TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS bank_statements_account ON bank_statements (tenant_id, bank_account_id, created_at);

CREATE TABLE IF NOT EXISTS bank_statement_lines (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        UUID NOT NULL,
    statement_id     UUID NOT NULL REFERENCES bank_statements(id) ON DELETE CASCADE,
    bank_account_id  UUID NOT NULL REFERENCES accounts(id),
    row_no           INT NOT NULL,
    txn_date         DATE NOT NULL,
    value_date       DATE,
    description      TEXT NOT NULL DEFAULT '',
    reference        TEXT,
    withdrawal_paise BIGINT NOT NULL DEFAULT 0 CHECK (withdrawal_paise >= 0),
    deposit_paise    BIGINT NOT NULL DEFAULT 0 CHECK (deposit_paise >= 0),
    balance_paise    BIGINT,
    line_hash        TEXT NOT NULL,
    status           TEXT NOT NULL DEFAULT 'unmatched' CHECK (status IN ('unmatched','matched','ignored')),
    entry_id         UUID REFERENCES voucher_entries(id),
    voucher_id       UUID REFERENCES vouchers(id),
    match_rule       TEXT, -- reference | amount_date | manual | created
    matched_by       TEXT,
    matched_at       TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((withdrawal_paise = 0) <> (deposit_paise = 0)),
    UNIQUE (tenant_id, bank_account_id, line_hash)
);
CREATE UNIQUE INDEX IF NOT EXISTS bank_line_entry ON bank_statement_lines (entry_id) WHERE entry_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS bank_lines_account ON bank_statement_lines (tenant_id, bank_account_id, txn_date);

-- 4. Cheque register. voucher_id stays the voucher that books the cheque
--    against the party (receipt Dr Cheques in Hand / payment Cr Bank; for
--    cheques recorded before phase 4 the clearing voucher Dr Bank / Cr Party).
ALTER TABLE cheques ADD COLUMN IF NOT EXISTS clear_voucher_id  UUID REFERENCES vouchers(id);
ALTER TABLE cheques ADD COLUMN IF NOT EXISTS bounce_voucher_id UUID REFERENCES vouchers(id);
ALTER TABLE cheques ADD COLUMN IF NOT EXISTS received_on   DATE;
ALTER TABLE cheques ADD COLUMN IF NOT EXISTS deposited_on  DATE;
ALTER TABLE cheques ADD COLUMN IF NOT EXISTS cleared_on    DATE;
ALTER TABLE cheques ADD COLUMN IF NOT EXISTS bounced_on    DATE;
ALTER TABLE cheques ADD COLUMN IF NOT EXISTS bounce_reason TEXT;
CREATE INDEX IF NOT EXISTS cheques_date ON cheques (tenant_id, cheque_date) WHERE status IN ('received','issued','deposited');

-- 5. TDS / TCS.
ALTER TABLE parties ADD COLUMN IF NOT EXISTS pan               TEXT;
ALTER TABLE parties ADD COLUMN IF NOT EXISTS tds_section       TEXT;
ALTER TABLE parties ADD COLUMN IF NOT EXISTS deductee_type     TEXT;
ALTER TABLE parties ADD COLUMN IF NOT EXISTS lower_tds_rate_bp INT;

CREATE TABLE IF NOT EXISTS tds_settings (
    tenant_id     UUID PRIMARY KEY,
    tan           TEXT,
    deductor_name TEXT,
    auto_194q     BOOLEAN NOT NULL DEFAULT false, -- deduct 194Q on purchase bills of suppliers tagged 194Q
    updated_by    TEXT,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS tds_challans (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL,
    kind            TEXT NOT NULL CHECK (kind IN ('tds','tcs')),
    bsr_code        TEXT NOT NULL,
    challan_no      TEXT NOT NULL,
    paid_on         DATE NOT NULL,
    amount_paise    BIGINT NOT NULL CHECK (amount_paise > 0),
    interest_paise  BIGINT NOT NULL DEFAULT 0 CHECK (interest_paise >= 0),
    bank_account_id UUID NOT NULL REFERENCES accounts(id),
    voucher_id      UUID NOT NULL REFERENCES vouchers(id),
    created_by      TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, bsr_code, challan_no, paid_on)
);

-- Every bill/payment tested against a section is recorded (even below the
-- threshold), so the per-party FY aggregate is the sum of base_paise.
CREATE TABLE IF NOT EXISTS tds_entries (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      UUID NOT NULL,
    kind           TEXT NOT NULL CHECK (kind IN ('tds','tcs')),
    direction      TEXT NOT NULL CHECK (direction IN ('payable','receivable')),
    section        TEXT NOT NULL,
    party_id       UUID NOT NULL REFERENCES parties(id),
    source_key     TEXT NOT NULL,           -- idempotency (invoice id, request key …)
    ref_voucher_id UUID REFERENCES vouchers(id), -- bill/payment the base came from
    voucher_id     UUID REFERENCES vouchers(id), -- the deduction journal (NULL when nothing deducted)
    txn_date       DATE NOT NULL,
    fy             TEXT NOT NULL,
    base_paise     BIGINT NOT NULL CHECK (base_paise >= 0),
    taxable_paise  BIGINT NOT NULL DEFAULT 0,
    rate_bp        INT NOT NULL DEFAULT 0,
    tds_paise      BIGINT NOT NULL DEFAULT 0 CHECK (tds_paise >= 0),
    pan            TEXT,
    deductee_type  TEXT,
    higher_rate    BOOLEAN NOT NULL DEFAULT false,
    challan_id     UUID REFERENCES tds_challans(id),
    status         TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','reversed')),
    created_by     TEXT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, source_key)
);
CREATE INDEX IF NOT EXISTS tds_entries_party ON tds_entries (tenant_id, party_id, section, fy);
CREATE INDEX IF NOT EXISTS tds_entries_open ON tds_entries (tenant_id, kind, direction) WHERE challan_id IS NULL AND tds_paise > 0;

-- 6. GST set-off: one row per GSTIN and return period.
CREATE TABLE IF NOT EXISTS gst_setoffs (
    tenant_id         UUID NOT NULL,
    gstin             TEXT NOT NULL,
    period            TEXT NOT NULL, -- MMYYYY
    setoff_voucher_id UUID REFERENCES vouchers(id),
    cash_voucher_id   UUID REFERENCES vouchers(id),
    computation       JSONB NOT NULL,
    created_by        TEXT NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, gstin, period)
);

-- 7. Edit audit trail (Companies (Accounts) Rules, rule 3(1)): every change
--    to ledger masters, vouchers, allocations, cheques, matches, TDS and
--    settings is captured with before/after images by a trigger, so it cannot
--    be bypassed by any code path. The actor comes from the transaction-local
--    setting bahi.actor (set by Post and the HTTP handlers).
CREATE TABLE IF NOT EXISTS audit_log (
    id        BIGSERIAL PRIMARY KEY,
    tenant_id UUID,
    at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    actor     TEXT NOT NULL,
    action    TEXT NOT NULL, -- insert | update | delete
    entity    TEXT NOT NULL, -- table name
    entity_id TEXT,
    before    JSONB,
    after     JSONB
);
CREATE INDEX IF NOT EXISTS audit_entity ON audit_log (tenant_id, entity, entity_id, at);
CREATE INDEX IF NOT EXISTS audit_at ON audit_log (tenant_id, at);

CREATE OR REPLACE FUNCTION bahi_audit() RETURNS trigger AS $$
DECLARE
    o JSONB; n JSONB;
BEGIN
    IF TG_OP <> 'INSERT' THEN o := to_jsonb(OLD); END IF;
    IF TG_OP <> 'DELETE' THEN n := to_jsonb(NEW); END IF;
    IF TG_OP = 'UPDATE' AND o = n THEN
        RETURN NULL;
    END IF;
    INSERT INTO audit_log (tenant_id, actor, action, entity, entity_id, before, after)
    VALUES ((COALESCE(n->>'tenant_id', o->>'tenant_id'))::uuid,
            COALESCE(NULLIF(current_setting('bahi.actor', true), ''), 'db:' || current_user),
            lower(TG_OP), TG_TABLE_NAME,
            COALESCE(n->>'id', o->>'id', n->>'tenant_id', o->>'tenant_id'), o, n);
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

DO $$
DECLARE t TEXT;
BEGIN
    FOREACH t IN ARRAY ARRAY['accounts','parties','vouchers','cheques','period_locks','bill_allocations',
                             'bank_statements','tds_entries','tds_challans','tds_settings','gst_setoffs'] LOOP
        EXECUTE format('DROP TRIGGER IF EXISTS %I_audit ON %I', t, t);
        EXECUTE format('CREATE TRIGGER %I_audit AFTER INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION bahi_audit()', t, t);
    END LOOP;
    FOREACH t IN ARRAY ARRAY['voucher_entries','bills','bank_statement_lines','bank_transactions'] LOOP
        EXECUTE format('DROP TRIGGER IF EXISTS %I_audit ON %I', t, t);
        EXECUTE format('CREATE TRIGGER %I_audit AFTER UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION bahi_audit()', t, t);
    END LOOP;
END $$;
