#!/usr/bin/env bash
# Manual deploy of one or more repos through their cloudbuild.yaml (CI triggers do the same).
#   ENV=dev ./scripts/deploy.sh aadesh bahi
#   ENV=dev ./scripts/deploy.sh all
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/env.sh
ROOT="$(cd .. && pwd)"

ORDER=(soochna pehchaan dwaar kar bahi bhandar aadesh lenden aayaat nirmaan buddhi dukaan)
targets=("$@"); [[ "${1:-}" == "all" ]] && targets=("${ORDER[@]}")
[[ ${#targets[@]} -eq 0 ]] && { echo "usage: $0 <repo...|all>"; exit 1; }

for svc in "${targets[@]}"; do
  echo "==> $svc"
  sha="$(git -C "$ROOT/$svc" rev-parse --short HEAD 2>/dev/null || date +%s)"
  gcloud builds submit "$ROOT/$svc" --config="$ROOT/$svc/cloudbuild.yaml" \
    --substitutions="_ENV=${ENV},SHORT_SHA=${sha}"
done
