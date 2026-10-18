#!/usr/bin/env bash
# Who may call whom on Cloud Run (roles/run.invoker). Run after the services exist.
# Mirrors ARCHITECTURE.md §6 (sync calls), plus the gateway → every service.
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/env.sh

allow() { # caller callee
  gcloud run services add-iam-policy-binding "$2" --region="$REGION" \
    --member="serviceAccount:$1@${PROJECT_ID}.iam.gserviceaccount.com" --role=roles/run.invoker >/dev/null \
    && echo "  $1 → $2"
}

echo "==> gateway → services"
for svc in "${DB_SERVICES[@]}"; do allow dwaar "$svc"; done

echo "==> service → service"
allow pehchaan soochna
allow aadesh pehchaan; allow aadesh bahi; allow aadesh bhandar; allow aadesh lenden
allow bahi kar; allow aadesh kar
allow kar aadesh;   allow kar pehchaan
allow aayaat bahi;  allow aayaat bhandar
allow nirmaan bhandar
allow soochna pehchaan
allow buddhi bahi;  allow buddhi aadesh; allow buddhi bhandar; allow buddhi soochna

echo "==> Pub/Sub and Scheduler identities → every service"
for svc in "${DB_SERVICES[@]}"; do allow pubsub-pusher "$svc"; allow scheduler "$svc"; done
