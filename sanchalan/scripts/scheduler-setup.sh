#!/usr/bin/env bash
# Cloud Scheduler jobs that call /internal/v1/jobs/* (times are IST).
#
# Jobs authenticate with an OIDC token for the scheduler service account only — the shared
# service secret is NOT stored in job headers (anyone with cloudscheduler.jobs.get could read
# it). Cloud Run's IAM admits the token (scheduler has run.invoker, see iam-invokers.sh) and the
# service verifies it again itself (signature, audience, e-mail), e.g. soochna/internal/jobauth.
#
# Services that do not verify the scheduler token yet still need X-Service-Secret. List them in
# LEGACY_SECRET_SERVICES; the default covers the services whose job endpoints are behind mool's
# v.Internal today. Empty it once they adopt the OIDC check (move soochna/internal/jobauth to mool).
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/env.sh

SA="scheduler@${PROJECT_ID}.iam.gserviceaccount.com"
read -r -a LEGACY <<< "${LEGACY_SECRET_SERVICES-lenden bhandar buddhi}"
SECRET=""
if [[ ${#LEGACY[@]} -gt 0 ]]; then
  SECRET="$(gcloud secrets versions access latest --secret=service-secret)"
fi

job() { # name schedule service path [json-body]
  local url; url="$(run_url "$3")"
  local body="${5:-}"; [[ -n "$body" ]] || body='{}'
  local args=(--location="$REGION" --schedule="$2" --time-zone="Asia/Kolkata" --http-method=POST
              --uri="${url}$4" --oidc-service-account-email="$SA" --oidc-token-audience="$url"
              --message-body="$body" --attempt-deadline=300s)
  local hdr="Content-Type=application/json" mode="oidc"
  if contains "$3" "${LEGACY[@]}"; then
    hdr="X-Service-Secret=${SECRET},${hdr}"; mode="oidc+secret (legacy)"
  fi
  if gcloud scheduler jobs describe "$1" --location="$REGION" >/dev/null 2>&1; then
    # --clear-headers drops a secret header left by earlier versions of this script.
    gcloud scheduler jobs update http "$1" "${args[@]}" --clear-headers >/dev/null
    gcloud scheduler jobs update http "$1" --location="$REGION" --update-headers="$hdr" >/dev/null
  else
    gcloud scheduler jobs create http "$1" "${args[@]}" --headers="$hdr" >/dev/null
  fi
  echo "  $1  [$2]  → $3$4  ($mode)"
}

job soochna-retry-failed       "*/10 * * * *"  soochna /internal/v1/jobs/retry-failed       # from W3
job soochna-payment-reminders  "30 10 * * *"   soochna /internal/v1/jobs/payment-reminders  # needs bahi /internal/v1/receivables/overdue
job lenden-reconcile           "*/15 * * * *"  lenden  /internal/v1/jobs/reconcile          # from W10
job bhandar-reorder-scan       "0 8 * * *"     bhandar /internal/v1/jobs/reorder-scan       # from W17
job bhandar-expiry-scan        "0 7 * * *"     bhandar /internal/v1/jobs/expiry-scan        # from W18
job buddhi-forecast            "0 2 * * *"     buddhi  /internal/v1/jobs/forecast           # from W23
job buddhi-weekly-summary      "0 9 * * MON"   buddhi  /internal/v1/jobs/weekly-summary     # from W23
