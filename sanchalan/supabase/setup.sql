-- Taraju on Supabase: one schema + one login role per service, inside the
-- project's single `postgres` database.
--
-- Run once per Supabase project (dev / staging / prod) in Dashboard → SQL Editor.
-- It is idempotent: existing roles are left alone (their password is NOT shown again).
-- The last statement returns service, role and a fresh password for each NEW role:
-- copy them straight into scripts/supabase-secrets.sh, then clear the editor.
--
-- Why schemas, not databases: Supabase's dashboard, backups and pooler are built
-- around the `postgres` database. Each service's role has search_path = its schema,
-- so the services' unqualified SQL and their schema_migrations table land there.

CREATE TEMP TABLE IF NOT EXISTS taraju_new_roles (service text, role text, password text);
TRUNCATE taraju_new_roles;

DO $$
DECLARE
  svc  text;
  r    text;
  pw   text;
BEGIN
  FOREACH svc IN ARRAY ARRAY['pehchaan','bahi','kar','aadesh','lenden','soochna','aayaat','bhandar','nirmaan','buddhi']
  LOOP
    r := svc || '_svc';
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = r) THEN
      pw := encode(extensions.gen_random_bytes(24), 'hex');
      EXECUTE format('CREATE ROLE %I LOGIN PASSWORD %L CONNECTION LIMIT 40', r, pw);
      INSERT INTO taraju_new_roles VALUES (svc, r, pw);
    END IF;

    EXECUTE format('CREATE SCHEMA IF NOT EXISTS %I AUTHORIZATION %I', svc, r);
    EXECUTE format('ALTER ROLE %I SET search_path = %I', r, svc);
    EXECUTE format('ALTER ROLE %I SET statement_timeout = %L', r, '30s');
    EXECUTE format('ALTER ROLE %I SET idle_in_transaction_session_timeout = %L', r, '60s');

    -- Keep the Data API (anon/authenticated) and other services out of this schema.
    EXECUTE format('REVOKE ALL ON SCHEMA %I FROM PUBLIC', svc);
    EXECUTE format('REVOKE ALL ON SCHEMA %I FROM anon, authenticated', svc);
    -- Services never write to public.
    EXECUTE format('REVOKE CREATE ON SCHEMA public FROM %I', r);
  END LOOP;
END $$;

-- buddhi reads nothing from other schemas: cross-service data goes over HTTP/events.

SELECT service, role, password FROM taraju_new_roles ORDER BY service;
