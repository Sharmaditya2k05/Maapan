-- pehchaan: tenants (businesses), stores, users, memberships (role per tenant), audit log

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

CREATE TABLE tenants (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    business_name TEXT NOT NULL,
    gstin         TEXT,
    pan           TEXT,
    state_code    TEXT NOT NULL,
    address       TEXT,
    email         TEXT,
    phone         TEXT,
    fy_start_month INT NOT NULL DEFAULT 4,
    status        TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','suspended')),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE stores (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  UUID NOT NULL REFERENCES tenants(id),
    name       TEXT NOT NULL,
    code       TEXT NOT NULL,
    state_code TEXT NOT NULL,
    gstin      TEXT,
    address    TEXT,
    phone      TEXT,
    active     BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, code)
);
CREATE INDEX stores_tenant ON stores (tenant_id);

-- Users are global (one phone = one person); access to a business is a membership.
CREATE TABLE users (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    phone      TEXT NOT NULL UNIQUE,          -- +91XXXXXXXXXX
    name       TEXT,
    email      TEXT,
    status     TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
    last_login_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE memberships (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  UUID NOT NULL REFERENCES tenants(id),
    user_id    UUID NOT NULL REFERENCES users(id),
    role       TEXT NOT NULL CHECK (role IN ('super_admin','store_manager','cashier','accountant')),
    store_ids  UUID[] NOT NULL DEFAULT '{}',   -- stores a scoped role may access
    active     BOOLEAN NOT NULL DEFAULT true,
    invited_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, user_id)
);
CREATE INDEX memberships_user ON memberships (user_id);

CREATE TABLE audit_log (
    id         BIGSERIAL PRIMARY KEY,
    tenant_id  UUID,
    actor_id   TEXT NOT NULL,
    action     TEXT NOT NULL,          -- login | onboarding | user.invite | user.update | store.create ...
    target     TEXT,
    details    JSONB NOT NULL DEFAULT '{}',
    ip         TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX audit_log_tenant ON audit_log (tenant_id, created_at DESC);
