#!/usr/bin/env bash
# Run Go services on the host with `go run` (fast edit loop), logs in ./logs/<svc>.log.
#   ./scripts/run-local.sh                 # all services
#   ./scripts/run-local.sh pehchaan aadesh # some
#   ./scripts/run-local.sh stop
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT="$(cd .. && pwd)"
mkdir -p logs .pids

ALL=(dwaar pehchaan bahi kar aadesh lenden soochna aayaat bhandar nirmaan buddhi)
if [[ "${1:-}" == "stop" ]]; then
  for f in .pids/*.pid; do [[ -f "$f" ]] && kill "$(cat "$f")" 2>/dev/null || true; rm -f "$f"; done
  echo "stopped"; exit 0
fi
targets=("$@"); [[ ${#targets[@]} -eq 0 ]] && targets=("${ALL[@]}")

docker compose up -d postgres redis
for svc in "${targets[@]}"; do
  [[ -d "$ROOT/$svc" ]] || { echo "skip $svc (not checked out)"; continue; }
  [[ -f "$ROOT/$svc/.env" ]] || cp "$ROOT/$svc/.env.example" "$ROOT/$svc/.env"
  (cd "$ROOT/$svc" && nohup go run ./cmd/server > "$OLDPWD/logs/$svc.log" 2>&1 & echo $! > "$OLDPWD/.pids/$svc.pid")
  echo "started $svc  (logs/$svc.log)"
done
echo "gateway: http://localhost:8000  ·  frontend: cd ../dukaan && npm run dev"
