-- bahi: chart of accounts, parties, double-entry vouchers, bank & cheques

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

-- Chart of accounts. Groups (is_group) organise the tree and cannot be posted to.
CREATE TABLE accounts (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL,
    code        TEXT NOT NULL,
    name        TEXT NOT NULL,
    grp         TEXT NOT NULL CHECK (grp IN ('asset','liability','income','expense','equity')),
    parent_id   UUID REFERENCES accounts(id),
    is_group    BOOLEAN NOT NULL DEFAULT false,
    is_system   BOOLEAN NOT NULL DEFAULT false,
    is_cash     BOOLEAN NOT NULL DEFAULT false,
    is_bank     BOOLEAN NOT NULL DEFAULT false,
    bank_details JSONB,
    archived    BOOLEAN NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, code)
);
CREATE INDEX accounts_parent ON accounts (tenant_id, parent_id);

CREATE TABLE parties (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id          UUID NOT NULL,
    account_id         UUID NOT NULL REFERENCES accounts(id),
    name               TEXT NOT NULL,
    type               TEXT NOT NULL CHECK (type IN ('customer','supplier','both')),
    phone              TEXT,
    email              TEXT,
    gstin              TEXT,
    state_code         TEXT,
    address            TEXT,
    credit_limit_paise BIGINT NOT NULL DEFAULT 0,
    credit_days        INT NOT NULL DEFAULT 0,
    gst_verified       BOOLEAN NOT NULL DEFAULT false,
    legal_name         TEXT,
    external_ref       TEXT,
    archived           BOOLEAN NOT NULL DEFAULT false,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX parties_external_ref ON parties (tenant_id, external_ref) WHERE external_ref IS NOT NULL;
CREATE INDEX parties_search ON parties (tenant_id, lower(name));
CREATE INDEX parties_phone ON parties (tenant_id, phone);

CREATE TABLE voucher_series (
    tenant_id UUID NOT NULL,
    vtype     TEXT NOT NULL,
    fy        TEXT NOT NULL,
    next_no   BIGINT NOT NULL DEFAULT 1,
    PRIMARY KEY (tenant_id, vtype, fy)
);

CREATE TABLE vouchers (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID NOT NULL,
    vtype        TEXT NOT NULL CHECK (vtype IN ('sales','purchase','receipt','payment','journal','contra',
                                               'credit_note','debit_note','production','opening')),
    number       TEXT NOT NULL,
    fy           TEXT NOT NULL,
    vdate        DATE NOT NULL,
    narration    TEXT,
    source       TEXT NOT NULL DEFAULT 'manual',  -- manual | aadesh.invoice | lenden.payment | ...
    source_id    TEXT,                            -- id in the source system (idempotency)
    source_ref   TEXT,                            -- human reference, e.g. invoice number
    status       TEXT NOT NULL DEFAULT 'posted' CHECK (status IN ('posted','reversed')),
    reversal_of  UUID REFERENCES vouchers(id),
    total_paise  BIGINT NOT NULL,
    created_by   TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, vtype, fy, number)
);
CREATE UNIQUE INDEX vouchers_source ON vouchers (tenant_id, source, source_id) WHERE source_id IS NOT NULL;
CREATE INDEX vouchers_date ON vouchers (tenant_id, vdate);

CREATE TABLE voucher_entries (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID NOT NULL,
    voucher_id   UUID NOT NULL REFERENCES vouchers(id) ON DELETE CASCADE,
    line_no      INT NOT NULL,
    account_id   UUID NOT NULL REFERENCES accounts(id),
    party_id     UUID REFERENCES parties(id),
    store_id     UUID,
    vdate        DATE NOT NULL,                       -- denormalised for fast statements
    debit_paise  BIGINT NOT NULL DEFAULT 0 CHECK (debit_paise >= 0),
    credit_paise BIGINT NOT NULL DEFAULT 0 CHECK (credit_paise >= 0),
    CHECK ((debit_paise = 0) <> (credit_paise = 0))   -- exactly one side
);
CREATE INDEX entries_account ON voucher_entries (tenant_id, account_id, vdate);
CREATE INDEX entries_party ON voucher_entries (tenant_id, party_id, vdate);
CREATE INDEX entries_voucher ON voucher_entries (voucher_id);

-- Database-level guarantee of double entry: checked at COMMIT, so all lines of a
-- voucher can be inserted first.
CREATE FUNCTION check_voucher_balanced() RETURNS trigger AS $$
DECLARE
    d BIGINT; c BIGINT;
BEGIN
    SELECT COALESCE(sum(debit_paise),0), COALESCE(sum(credit_paise),0) INTO d, c
      FROM voucher_entries WHERE voucher_id = NEW.voucher_id;
    IF d <> c THEN
        RAISE EXCEPTION 'voucher % is not balanced: debit % <> credit %', NEW.voucher_id, d, c
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE CONSTRAINT TRIGGER voucher_balanced
    AFTER INSERT OR UPDATE ON voucher_entries
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION check_voucher_balanced();

-- Deposits/withdrawals/transfers on bank accounts, for reconciliation.
CREATE TABLE bank_transactions (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      UUID NOT NULL,
    bank_account_id UUID NOT NULL REFERENCES accounts(id),
    kind           TEXT NOT NULL CHECK (kind IN ('deposit','withdrawal','charges','interest')),
    amount_paise   BIGINT NOT NULL CHECK (amount_paise > 0),
    txn_date       DATE NOT NULL,
    reference      TEXT,
    voucher_id     UUID NOT NULL REFERENCES vouchers(id),
    reconciled_at  DATE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX bank_txn_account ON bank_transactions (tenant_id, bank_account_id, txn_date);

CREATE TABLE cheques (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL,
    direction       TEXT NOT NULL CHECK (direction IN ('received','issued')),
    party_id        UUID NOT NULL REFERENCES parties(id),
    bank_account_id UUID NOT NULL REFERENCES accounts(id),
    cheque_no       TEXT NOT NULL,
    cheque_date     DATE NOT NULL,
    drawee_bank     TEXT,
    amount_paise    BIGINT NOT NULL CHECK (amount_paise > 0),
    status          TEXT NOT NULL CHECK (status IN ('received','issued','deposited','cleared','bounced','cancelled')),
    voucher_id      UUID REFERENCES vouchers(id),
    status_date     DATE,
    reconciled_at   DATE,
    notes           TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, direction, bank_account_id, cheque_no, party_id)
);
CREATE INDEX cheques_status ON cheques (tenant_id, status);
