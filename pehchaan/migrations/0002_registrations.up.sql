-- pehchaan phase 3: GST registrations. A registration is one GSTIN of a business (or its
-- unregistered identity). A tenant has one or more, exactly one of them primary, and every
-- store belongs to exactly one (PHASE3-CONTRACTS.md §1).

CREATE TABLE IF NOT EXISTS registrations (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID NOT NULL REFERENCES tenants(id),
    gstin               TEXT,                       -- NULL = unregistered
    legal_name          TEXT NOT NULL,
    trade_name          TEXT,
    state_code          TEXT NOT NULL,
    address             TEXT,
    city                TEXT,
    pin                 TEXT CHECK (pin IS NULL OR pin ~ '^[0-9]{6}$'),
    phone               TEXT,
    email               TEXT,
    composition         BOOLEAN NOT NULL DEFAULT false,
    einvoice_applicable BOOLEAN NOT NULL DEFAULT false,   -- AATO > ₹5 crore, set by the owner
    is_primary          BOOLEAN NOT NULL DEFAULT false,
    status              TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','inactive')),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS registrations_gstin_uq ON registrations (tenant_id, gstin) WHERE gstin IS NOT NULL;
-- At most one primary per tenant here; the service makes sure there is always one.
CREATE UNIQUE INDEX IF NOT EXISTS registrations_primary_uq ON registrations (tenant_id) WHERE is_primary;
CREATE INDEX IF NOT EXISTS registrations_tenant ON registrations (tenant_id, created_at);

-- Backfill 1: every tenant's own GSTIN (or unregistered identity) becomes its primary registration.
INSERT INTO registrations (tenant_id, gstin, legal_name, trade_name, state_code, address, phone, email, is_primary, created_at)
SELECT t.id, NULLIF(upper(btrim(t.gstin)), ''), t.business_name, t.business_name, t.state_code, t.address, t.phone, t.email, true, t.created_at
FROM tenants t
WHERE NOT EXISTS (SELECT 1 FROM registrations r WHERE r.tenant_id = t.id);

-- Backfill 2: a store billing under a GSTIN other than the tenant's gets a registration of its own
-- (one per distinct GSTIN, details from the earliest such store).
INSERT INTO registrations (tenant_id, gstin, legal_name, trade_name, state_code, address, phone, is_primary, created_at)
SELECT DISTINCT ON (s.tenant_id, upper(btrim(s.gstin)))
       s.tenant_id, upper(btrim(s.gstin)), t.business_name, s.name, left(upper(btrim(s.gstin)), 2), s.address, s.phone, false, s.created_at
FROM stores s JOIN tenants t ON t.id = s.tenant_id
WHERE NULLIF(btrim(s.gstin), '') IS NOT NULL
  AND upper(btrim(s.gstin)) IS DISTINCT FROM NULLIF(upper(btrim(t.gstin)), '')
  AND NOT EXISTS (SELECT 1 FROM registrations r WHERE r.tenant_id = s.tenant_id AND r.gstin = upper(btrim(s.gstin)))
ORDER BY s.tenant_id, upper(btrim(s.gstin)), s.created_at;

-- Link every store: to the registration of its own GSTIN, else to the tenant's primary.
ALTER TABLE stores ADD COLUMN IF NOT EXISTS registration_id UUID REFERENCES registrations(id);
UPDATE stores s SET registration_id = r.id
FROM registrations r
WHERE s.registration_id IS NULL AND NULLIF(btrim(s.gstin), '') IS NOT NULL
  AND r.tenant_id = s.tenant_id AND r.gstin = upper(btrim(s.gstin));
UPDATE stores s SET registration_id = r.id
FROM registrations r
WHERE s.registration_id IS NULL AND r.tenant_id = s.tenant_id AND r.is_primary;
ALTER TABLE stores ALTER COLUMN registration_id SET NOT NULL;
CREATE INDEX IF NOT EXISTS stores_registration ON stores (registration_id);
