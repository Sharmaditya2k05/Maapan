#!/usr/bin/env bash
# Shared settings for every script. Usage:  ENV=dev source scripts/env.sh
set -euo pipefail

: "${ENV:=dev}"                                  # dev | staging | prod
: "${PROJECT_ID:=taraju-${ENV}}"                 # one GCP project per environment
: "${REGION:=asia-south1}"                       # Mumbai
: "${REPO:=taraju}"                              # Artifact Registry repository
: "${VPC:=taraju-vpc}"
: "${SUBNET:=taraju-run-subnet}"
: "${SQL_INSTANCE:=taraju-pg}"
: "${REDIS_INSTANCE:=taraju-redis}"
: "${BUCKET:=taraju-${ENV}-files}"

# Go services that own a database.
DB_SERVICES=(pehchaan bahi kar aadesh lenden soochna aayaat bhandar nirmaan buddhi)
# Every deployable Go service.
GO_SERVICES=(dwaar "${DB_SERVICES[@]}")
# Services that need Redis.
REDIS_SERVICES=(dwaar pehchaan kar buddhi)

PROJECT_NUMBER="$(gcloud projects describe "$PROJECT_ID" --format='value(projectNumber)' 2>/dev/null || true)"
run_url() { echo "https://$1-${PROJECT_NUMBER}.${REGION}.run.app"; }

gcloud config set project "$PROJECT_ID" >/dev/null
