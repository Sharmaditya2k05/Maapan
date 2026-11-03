-- Maapan on Supabase: one schema + one login role per service, inside the
-- project's single `postgres` database.
--
-- Run once per Supabase project (dev / staging / prod) in Dashboard → SQL Editor.
-- It is idempotent: existing roles are left alone (their password is NOT shown again),
-- but their CONNECTION LIMIT is brought in line with the table below on every run.
-- The last statement returns service, role and a fresh password for each NEW role:
-- copy them straight into scripts/supabase-secrets.sh, then clear the editor.
--
-- Why schemas, not databases: Supabase's dashboard, backups and pooler are built
-- around the `postgres` database. Each service's role has search_path = its schema,
-- so the services' unqualified SQL and their schema_migrations table land there.
--
-- Connection limits. Services connect through the SESSION pooler, so every pgx
-- connection holds one Postgres backend. A role's limit is sized to what the service
-- can open at peak:
--     (max_instances + 1) × pool_max_conns + extra + 2
--   max_instances  = --max-instances in <service>/cloudbuild.yaml
--   +1 instance    = old and new revision overlap during a deploy
--   pool_max_conns = scripts/supabase-secrets.sh (pool_for), put into the URL
--   extra          = other clients of the same role (aayaat's import worker job)
--   +2             = psql / migrations by hand
-- Change the numbers here when you change max-instances or the pool size.
-- The sum of all limits may exceed the project's max_connections (instances rarely all
-- peak together); the NOTICE at the end shows by how much. If it is far above, lower
-- max-instances / pool sizes, move to a bigger compute add-on, or put read paths on the
-- transaction pooler (port 6543 — no prepared statements, no session advisory locks).

CREATE TEMP TABLE IF NOT EXISTS maapan_new_roles (service text, role text, password text);
TRUNCATE maapan_new_roles;

CREATE TEMP TABLE IF NOT EXISTS maapan_sizing (service text PRIMARY KEY, max_instances int, pool int, extra int);
TRUNCATE maapan_sizing;
INSERT INTO maapan_sizing (service, max_instances, pool, extra) VALUES
  ('pehchaan', 10, 5, 0),
  ('bahi',     20, 5, 0),
  ('kar',       5, 3, 0),
  ('aadesh',   20, 5, 0),
  ('lenden',   10, 3, 0),
  ('soochna',  10, 3, 0),
  ('aayaat',    3, 5, 10),   -- + aayaat-worker Cloud Run job (large imports)
  ('bhandar',  20, 5, 0),
  ('nirmaan',   5, 3, 0),
  ('buddhi',   10, 3, 0);

DO $$
DECLARE
  z      record;
  r      text;
  pw     text;
  lim    int;
  total  int := 0;
  maxc   int := current_setting('max_connections')::int;
BEGIN
  FOR z IN SELECT * FROM maapan_sizing ORDER BY service
  LOOP
    r := z.service || '_svc';
    lim := (z.max_instances + 1) * z.pool + z.extra + 2;
    total := total + lim;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = r) THEN
      pw := encode(extensions.gen_random_bytes(24), 'hex');
      EXECUTE format('CREATE ROLE %I LOGIN PASSWORD %L CONNECTION LIMIT %s', r, pw, lim);
      INSERT INTO maapan_new_roles VALUES (z.service, r, pw);
    ELSE
      EXECUTE format('ALTER ROLE %I CONNECTION LIMIT %s', r, lim);
    END IF;

    EXECUTE format('CREATE SCHEMA IF NOT EXISTS %I AUTHORIZATION %I', z.service, r);
    EXECUTE format('ALTER ROLE %I SET search_path = %I', r, z.service);
    EXECUTE format('ALTER ROLE %I SET statement_timeout = %L', r, '30s');
    EXECUTE format('ALTER ROLE %I SET idle_in_transaction_session_timeout = %L', r, '60s');

    -- Keep the Data API (anon/authenticated) and other services out of this schema.
    EXECUTE format('REVOKE ALL ON SCHEMA %I FROM PUBLIC', z.service);
    EXECUTE format('REVOKE ALL ON SCHEMA %I FROM anon, authenticated', z.service);
    -- Services never write to public.
    EXECUTE format('REVOKE CREATE ON SCHEMA public FROM %I', r);
  END LOOP;

  IF total > maxc THEN
    RAISE NOTICE 'Maapan role limits sum to % connections; this project allows max_connections=%. '
                 'Fine for normal load (services rarely peak together) — see the header comment if far above.', total, maxc;
  ELSE
    RAISE NOTICE 'Maapan role limits sum to % of max_connections=%.', total, maxc;
  END IF;
END $$;

-- buddhi reads nothing from other schemas: cross-service data goes over HTTP/events.

-- Check: SELECT rolname, rolconnlimit FROM pg_roles WHERE rolname LIKE '%\_svc' ORDER BY 1;
SELECT service, role, password FROM maapan_new_roles ORDER BY service;
