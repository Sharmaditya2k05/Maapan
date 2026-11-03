#!/usr/bin/env bash
# Who may call whom on Cloud Run (roles/run.invoker). Run after the services exist.
# Mirrors ARCHITECTURE.md §6 (sync calls), plus the gateway → every service.
#
# Source of truth is the code: every svc.New(config.String("<X>_URL" …)) in
# <service>/cmd/server/main.go is one caller → callee pair below. Re-check with
#   grep -rn 'svc.New(' */cmd */internal --include=*.go
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
allow pehchaan soochna                                         # OTP
allow aadesh pehchaan; allow aadesh bahi; allow aadesh bhandar; allow aadesh lenden
allow aadesh kar                                               # no aadesh → kar client today; kept for the planned e-invoice trigger
allow bahi kar                                                 # GSTIN verify (?verify=true)
allow kar aadesh;      allow kar pehchaan;  allow kar bahi
allow aayaat bahi;     allow aayaat bhandar
allow nirmaan bhandar
allow soochna pehchaan                                         # business name, owner phone
allow soochna bahi                                             # party e-mail, overdue bills (payment reminders)
allow buddhi pehchaan; allow buddhi bahi;   allow buddhi aadesh; allow buddhi bhandar; allow buddhi soochna

echo "==> aayaat → its import worker job (starts executions with overrides)"
if gcloud run jobs describe aayaat-worker --region="$REGION" >/dev/null 2>&1; then
  gcloud run jobs add-iam-policy-binding aayaat-worker --region="$REGION" \
    --member="serviceAccount:aayaat@${PROJECT_ID}.iam.gserviceaccount.com" --role=roles/run.developer >/dev/null \
    && echo "  aayaat → aayaat-worker (run.developer)"
else
  echo "  aayaat-worker job not deployed yet; re-run after the first aayaat deploy"
fi

echo "==> Pub/Sub push identity → every subscribing service"
for svc in "${DB_SERVICES[@]}"; do allow pubsub-pusher "$svc"; done

echo "==> Scheduler identity → services with /internal/v1/jobs/*"
for svc in "${SCHEDULED_SERVICES[@]}"; do allow scheduler "$svc"; done
