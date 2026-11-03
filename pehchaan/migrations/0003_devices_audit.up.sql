-- pehchaan: POS device registration (device_id -> store, revocable) and
-- audit-log lookups by target (who changed this user's role/stores, when).

CREATE TABLE IF NOT EXISTS devices (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID NOT NULL REFERENCES tenants(id),
    store_id      UUID NOT NULL REFERENCES stores(id),
    device_id     TEXT NOT NULL,            -- the terminal's own stable id (app install id, serial…)
    name          TEXT,
    platform      TEXT,                     -- android | windows | web | ios | linux | other
    status        TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','revoked')),
    registered_by UUID,
    revoked_by    UUID,
    revoked_at    TIMESTAMPTZ,
    last_seen_at  TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- One active registration per device per business; revoked rows are kept as history.
CREATE UNIQUE INDEX IF NOT EXISTS devices_active_device ON devices (tenant_id, device_id) WHERE status = 'active';
CREATE INDEX IF NOT EXISTS devices_store ON devices (tenant_id, store_id);

CREATE INDEX IF NOT EXISTS audit_log_target ON audit_log (tenant_id, target, id DESC);
