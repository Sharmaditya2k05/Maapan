#!/usr/bin/env bash
# Topics and push subscriptions. Keep the matrix identical to
# mool/events/events.go (Subscriptions) and ARCHITECTURE.md §5.
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/env.sh
ok() { "$@" 2>/dev/null || true; }

# topic|subscribers   (plain list so it runs on macOS bash 3.2 too)
SUBS=(
  "aadesh.invoice.created|bahi bhandar soochna buddhi"
  "aadesh.invoice.cancelled|bahi bhandar buddhi"
  "aadesh.note.created|bahi bhandar buddhi"
  "lenden.payment.captured|bahi aadesh soochna buddhi"
  "lenden.payment.refunded|bahi aadesh"
  "bhandar.stock.low|soochna buddhi"
  "bhandar.batch.expiring|soochna"
  "nirmaan.production.completed|bhandar bahi"
  "aayaat.import.completed|soochna"
  "pehchaan.tenant.created|bahi bhandar kar"
)
PUSHER="pubsub-pusher@${PROJECT_ID}.iam.gserviceaccount.com"

# Pub/Sub's own agent must be able to mint OIDC tokens for the pusher identity.
ok gcloud iam service-accounts add-iam-policy-binding "$PUSHER" \
  --member="serviceAccount:service-${PROJECT_NUMBER}@gcp-sa-pubsub.iam.gserviceaccount.com" \
  --role=roles/iam.serviceAccountTokenCreator

ok gcloud pubsub topics create taraju-dead-letter
ok gcloud pubsub subscriptions create taraju-dead-letter-sub --topic=taraju-dead-letter --message-retention-duration=7d

for row in "${SUBS[@]}"; do
  topic="${row%%|*}"; subs="${row#*|}"
  ok gcloud pubsub topics create "$topic"
  for sub in $subs; do
    name="${sub}--${topic}"
    url="$(run_url "$sub")"
    if gcloud pubsub subscriptions describe "$name" >/dev/null 2>&1; then
      gcloud pubsub subscriptions update "$name" --push-endpoint="$url/events" \
        --push-auth-service-account="$PUSHER" --push-auth-token-audience="$url" >/dev/null
    else
      gcloud pubsub subscriptions create "$name" --topic="$topic" \
        --push-endpoint="$url/events" --push-auth-service-account="$PUSHER" --push-auth-token-audience="$url" \
        --ack-deadline=60 --min-retry-delay=10s --max-retry-delay=600s \
        --dead-letter-topic=taraju-dead-letter --max-delivery-attempts=15 >/dev/null
    fi
    echo "  $topic → $sub"
  done
done
