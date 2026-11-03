#!/usr/bin/env bash
# Store each service's Supabase connection string in Secret Manager as <svc>-database-url.
#
#   ENV=dev SUPABASE_REF=abcdefghijklmnop SUPABASE_POOLER_HOST=aws-0-ap-south-1.pooler.supabase.com \
#     ./scripts/supabase-secrets.sh creds.csv
#
# creds.csv = the rows returned by supabase/setup.sql, one per line:  service,role,password
# (paste the SQL Editor result, then delete the file:  shred -u creds.csv  /  rm -P creds.csv)
#
# Uses the SESSION pooler (port 5432). Do not use the transaction pooler (6543): the
# migration runner holds a session advisory lock and pgx uses prepared statements.
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/env.sh
: "${SUPABASE_REF:?set SUPABASE_REF (Dashboard → Project Settings → General → Reference ID)}"
: "${SUPABASE_POOLER_HOST:?set SUPABASE_POOLER_HOST (Dashboard → Connect → Session pooler host)}"
: "${POOL_MAX_CONNS:=}"   # set to force one pool size for every service
CREDS="${1:?usage: $0 creds.csv}"

# pgx pool size per Cloud Run instance. Must match `pool` in supabase/setup.sql, which sizes
# each role's CONNECTION LIMIT as (max_instances + 1) x pool + extra + 2.
pool_for() {
  if [[ -n "$POOL_MAX_CONNS" ]]; then echo "$POOL_MAX_CONNS"; return; fi
  case "$1" in
    pehchaan|bahi|aadesh|bhandar|aayaat) echo 5 ;;   # request-heavy paths
    *) echo 3 ;;                                     # kar lenden soochna nirmaan buddhi
  esac
}

while IFS=, read -r svc role pw; do
  svc="$(echo "$svc" | tr -d ' "')"; role="$(echo "$role" | tr -d ' "')"; pw="$(echo "$pw" | tr -d ' "\r')"
  [[ -z "$svc" || "$svc" == service ]] && continue
  url="postgres://${role}.${SUPABASE_REF}:${pw}@${SUPABASE_POOLER_HOST}:5432/postgres?sslmode=require&pool_max_conns=$(pool_for "$svc")"
  if gcloud secrets describe "${svc}-database-url" >/dev/null 2>&1; then
    printf '%s' "$url" | gcloud secrets versions add "${svc}-database-url" --data-file=- >/dev/null
  else
    printf '%s' "$url" | gcloud secrets create "${svc}-database-url" --replication-policy=automatic --data-file=- >/dev/null
  fi
  echo "  ${svc}-database-url  (role ${role}, pool $(pool_for "$svc"))"
done < "$CREDS"
echo "Done. Delete ${CREDS} now."
