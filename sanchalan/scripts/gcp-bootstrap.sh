#!/usr/bin/env bash
# One-time setup of a Maapan environment on Google Cloud.
# Safe to re-run: every step tolerates "already exists".
#
#   ENV=dev ./scripts/gcp-bootstrap.sh
#
# Needs: gcloud (logged in as project owner), openssl.
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/env.sh
ok() { "$@" 2>/dev/null || true; }
: "${DB_PROVIDER:=supabase}"     # supabase (default) | cloudsql
: "${REDIS_PROVIDER:=upstash}"   # upstash (default) | memorystore

echo "==> APIs"
gcloud services enable run.googleapis.com sqladmin.googleapis.com redis.googleapis.com \
  pubsub.googleapis.com cloudscheduler.googleapis.com secretmanager.googleapis.com \
  artifactregistry.googleapis.com cloudbuild.googleapis.com compute.googleapis.com \
  servicenetworking.googleapis.com vpcaccess.googleapis.com storage.googleapis.com

echo "==> Artifact Registry"
ok gcloud artifacts repositories create "$REPO" --repository-format=docker --location="$REGION"

echo "==> VPC (Direct VPC egress for Cloud Run, private IP for Cloud SQL & Redis)"
ok gcloud compute networks create "$VPC" --subnet-mode=custom
ok gcloud compute networks subnets create "$SUBNET" --network="$VPC" --region="$REGION" --range=10.10.0.0/24
ok gcloud compute addresses create google-managed-services --global --purpose=VPC_PEERING --prefix-length=16 --network="$VPC"
ok gcloud services vpc-peerings connect --service=servicenetworking.googleapis.com --ranges=google-managed-services --network="$VPC"

if [[ "$DB_PROVIDER" == cloudsql ]]; then
  echo "==> Cloud SQL (PostgreSQL 16, private IP)"
  TIER=db-custom-1-3840; [[ "$ENV" == prod ]] && TIER=db-custom-2-7680
  ok gcloud sql instances create "$SQL_INSTANCE" --database-version=POSTGRES_16 --region="$REGION" \
    --tier="$TIER" --network="projects/$PROJECT_ID/global/networks/$VPC" --no-assign-ip \
    --storage-auto-increase --backup-start-time=20:30 --enable-point-in-time-recovery \
    $([[ "$ENV" == prod ]] && echo "--availability-type=REGIONAL")
  SQL_IP="$(gcloud sql instances describe "$SQL_INSTANCE" --format='value(ipAddresses[0].ipAddress)')"
fi

if [[ "$REDIS_PROVIDER" == memorystore ]]; then
  echo "==> Memorystore Redis"
  ok gcloud redis instances create "$REDIS_INSTANCE" --size=1 --region="$REGION" --network="$VPC" --redis-version=redis_7_0
  REDIS_HOST="$(gcloud redis instances describe "$REDIS_INSTANCE" --region="$REGION" --format='value(host)')"
fi

echo "==> Storage bucket"
ok gcloud storage buckets create "gs://$BUCKET" --location="$REGION" --uniform-bucket-level-access

secret() { # name value
  if gcloud secrets describe "$1" >/dev/null 2>&1; then
    printf '%s' "$2" | gcloud secrets versions add "$1" --data-file=- >/dev/null
  else
    printf '%s' "$2" | gcloud secrets create "$1" --replication-policy=automatic --data-file=- >/dev/null
  fi
}
have_secret() { gcloud secrets describe "$1" >/dev/null 2>&1; }

echo "==> Shared secrets"
have_secret jwt-secret     || secret jwt-secret "$(openssl rand -hex 32)"
have_secret service-secret || secret service-secret "$(openssl rand -hex 32)"
# lenden encrypts per-tenant gateway keys with this (AES-256-GCM); prod refuses to start without it.
have_secret lenden-credentials-key || secret lenden-credentials-key "$(openssl rand -hex 32)"
if [[ "$REDIS_PROVIDER" == memorystore ]]; then
  have_secret redis-url || secret redis-url "redis://${REDIS_HOST}:6379/0"
else
  have_secret redis-url || secret redis-url "CHANGE_ME_rediss://default:password@host:6379"
fi
for s in github-token razorpay-key-id razorpay-key-secret razorpay-webhook-secret cashfree-client-id cashfree-client-secret \
         msg91-auth-key gupshup-api-key soochna-webhook-token gsp-client-id gsp-client-secret anthropic-api-key; do
  have_secret "$s" || secret "$s" "CHANGE_ME"
done

echo "==> Per-service database, user, secret and service account"
if [[ "$DB_PROVIDER" == cloudsql ]]; then
  for svc in "${DB_SERVICES[@]}"; do
    ok gcloud sql databases create "maapan_${svc}" --instance="$SQL_INSTANCE"
    if ! have_secret "${svc}-database-url"; then
      pw="$(openssl rand -hex 16)"
      ok gcloud sql users create "$svc" --instance="$SQL_INSTANCE" --password="$pw"
      secret "${svc}-database-url" "postgres://${svc}:${pw}@${SQL_IP}:5432/maapan_${svc}?sslmode=require"
    fi
  done
else
  echo "  Supabase: run supabase/setup.sql, then scripts/supabase-secrets.sh (see sanchalan/README.md)"
fi
for svc in "${GO_SERVICES[@]}" dukaan; do
  ok gcloud iam service-accounts create "$svc" --display-name="Maapan $svc"
  sa="${svc}@${PROJECT_ID}.iam.gserviceaccount.com"
  ok gcloud projects add-iam-policy-binding "$PROJECT_ID" --member="serviceAccount:$sa" --role=roles/secretmanager.secretAccessor --condition=None
  ok gcloud projects add-iam-policy-binding "$PROJECT_ID" --member="serviceAccount:$sa" --role=roles/pubsub.publisher --condition=None
  ok gcloud projects add-iam-policy-binding "$PROJECT_ID" --member="serviceAccount:$sa" --role=roles/cloudsql.client --condition=None
done
# aayaat + buddhi read/write uploaded files.
for svc in aayaat buddhi; do
  ok gcloud storage buckets add-iam-policy-binding "gs://$BUCKET" \
    --member="serviceAccount:${svc}@${PROJECT_ID}.iam.gserviceaccount.com" --role=roles/storage.objectAdmin
done

echo "==> Pub/Sub push identity"
ok gcloud iam service-accounts create pubsub-pusher --display-name="Pub/Sub push to Cloud Run"
ok gcloud iam service-accounts create scheduler --display-name="Cloud Scheduler jobs"

echo "==> Cloud Build may deploy as the service accounts"
CB_SA="${PROJECT_NUMBER}@cloudbuild.gserviceaccount.com"
for role in roles/run.admin roles/iam.serviceAccountUser roles/artifactregistry.writer roles/secretmanager.secretAccessor; do
  ok gcloud projects add-iam-policy-binding "$PROJECT_ID" --member="serviceAccount:$CB_SA" --role="$role" --condition=None
done

cat <<MSG

Bootstrap done for $PROJECT_ID.
Next:
  1. Supabase: run supabase/setup.sql in the SQL Editor, then ENV=$ENV ./scripts/supabase-secrets.sh creds.csv
     Upstash: put the rediss:// URL into secret redis-url
  2. Put real values into the CHANGE_ME secrets:
       printf '%s' 'xxx' | gcloud secrets versions add razorpay-key-id --data-file=-
  3. Deploy services in the order of docs/DEPLOYMENT.md (Cloud Build trigger per repo, or scripts/deploy.sh).
  4. After services exist:  ENV=$ENV ./scripts/iam-invokers.sh && ENV=$ENV ./scripts/pubsub-setup.sh && ENV=$ENV ./scripts/scheduler-setup.sh
MSG
