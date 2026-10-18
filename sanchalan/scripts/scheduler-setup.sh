#!/usr/bin/env bash
# Cloud Scheduler jobs that call /internal/v1/jobs/* (times are IST).
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/env.sh

SA="scheduler@${PROJECT_ID}.iam.gserviceaccount.com"
SECRET="$(gcloud secrets versions access latest --secret=service-secret)"

job() { # name schedule service path
  local url; url="$(run_url "$3")"
  local args=(--location="$REGION" --schedule="$2" --time-zone="Asia/Kolkata" --http-method=POST
              --uri="${url}$4" --oidc-service-account-email="$SA" --oidc-token-audience="$url"
              --headers="X-Service-Secret=${SECRET},Content-Type=application/json" --message-body='{}'
              --attempt-deadline=300s)
  if gcloud scheduler jobs describe "$1" --location="$REGION" >/dev/null 2>&1; then
    gcloud scheduler jobs update http "$1" "${args[@]}" >/dev/null
  else
    gcloud scheduler jobs create http "$1" "${args[@]}" >/dev/null
  fi
  echo "  $1  [$2]  → $3$4"
}

job soochna-retry-failed   "*/10 * * * *"  soochna /internal/v1/jobs/retry-failed      # from W3
job lenden-reconcile       "*/15 * * * *"  lenden  /internal/v1/jobs/reconcile         # from W10
job bhandar-reorder-scan   "0 8 * * *"     bhandar /internal/v1/jobs/reorder-scan      # from W17
job bhandar-expiry-scan    "0 7 * * *"     bhandar /internal/v1/jobs/expiry-scan       # from W18
job buddhi-forecast        "0 2 * * *"     buddhi  /internal/v1/jobs/forecast          # from W23
job buddhi-weekly-summary  "0 9 * * MON"   buddhi  /internal/v1/jobs/weekly-summary    # from W23
