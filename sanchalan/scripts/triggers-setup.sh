#!/usr/bin/env bash
# Cloud Build triggers: push to main → dev, tag v* → staging (prod promoted manually).
# Connect the GitHub org to Cloud Build first (Console → Cloud Build → Repositories, 2nd gen).
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/env.sh
: "${GITHUB_ORG:=maapan}"
: "${CONNECTION:=github}"

for svc in "${GO_SERVICES[@]}" dukaan mool; do
  [[ "$svc" == mool ]] && continue   # library: tagged, not deployed
  branch='^main$'; [[ "$ENV" != dev ]] && branch=''
  if [[ -n "$branch" ]]; then
    gcloud builds triggers create github --name="${svc}-${ENV}" --region="$REGION" \
      --repository="projects/$PROJECT_ID/locations/$REGION/connections/$CONNECTION/repositories/$svc" \
      --branch-pattern="$branch" --build-config=cloudbuild.yaml --substitutions="_ENV=$ENV" 2>/dev/null || true
  else
    gcloud builds triggers create github --name="${svc}-${ENV}" --region="$REGION" \
      --repository="projects/$PROJECT_ID/locations/$REGION/connections/$CONNECTION/repositories/$svc" \
      --tag-pattern='^v.*' --build-config=cloudbuild.yaml --substitutions="_ENV=$ENV" 2>/dev/null || true
  fi
  echo "  trigger ${svc}-${ENV}"
done
