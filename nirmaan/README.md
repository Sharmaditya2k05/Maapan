# nirmaan (निर्माण) — Manufacturing Service

"Nirmaan" means *making / construction*. It turns raw material into finished goods:

- **Bills of Materials** — versioned recipes per finished item (`draft → active → archived`, one active version per item), components with qty per batch, unit and scrap %, plus operation costs (labour / overhead per batch)
- **Multi-level BOMs** — a component can be a sub-assembly with its own BOM (pinned with `sub_bom_id` or the item's active BOM); explosion is recursive with **cycle detection** (also checked on activation)
- **Cost roll-up** — raw material at bhandar purchase price, sub-assemblies rolled up recursively, scrap and operations included; per batch and per unit; unpriced items are flagged
- **Production runs** — plan (with shortages from bhandar stock) → start (blocks on shortages unless `force`) → complete (actual consumption, output qty, batch no) → `nirmaan.production.completed`, which makes bhandar consume raw material and receive finished goods at production cost

| | |
|---|---|
| Port (local) | `7009` |
| Database | `taraju_nirmaan` |
| Publishes | `nirmaan.production.completed` |
| Subscribes | nothing |
| Calls | `bhandar` — `POST /internal/v1/items/lookup` (prices), `GET /internal/v1/stock/summary` (availability) |
| Gateway prefix | `https://api.taraju.in/api/nirmaan/...` |
| First deployed | **Week 20** (BOM data model), Week 21 (BOM builder UI & costing), Week 22 (production run ↔ stock sync) |

## Run locally

Prerequisites: Go 1.25+, Docker (Postgres), the `mool` repo next to this one, and `bhandar`
running for cost roll-up and availability checks.

```bash
cd ../sanchalan && docker compose up -d postgres && cd ../nirmaan
make db-create
cp .env.example .env
make run                         # → http://localhost:7009/healthz
make test                        # explosion, scrap, cycles, cost roll-up, run costing
```

### Try it

```bash
TOKEN=...   # user with bom:manage (super_admin / store_manager)
# 1. a BOM: 10 loaves from 5 kg flour (2% scrap) + 100 g yeast, baking ₹50 per batch
curl -s localhost:7009/v1/boms -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{
  "item_id": "<bread item uuid>", "item_name": "Bread 400g", "output_qty": 10, "unit": "PCS",
  "components": [
    {"item_id": "<flour uuid>", "item_name": "Flour", "qty": 5, "unit": "KG", "scrap_percent": 2},
    {"item_id": "<yeast uuid>", "item_name": "Yeast", "qty": 100, "unit": "GM"}
  ],
  "operations": [{"name": "Baking", "cost_paise": 5000}]
}'
curl -s -X POST localhost:7009/v1/boms/<id>/activate -H "Authorization: Bearer $TOKEN"
curl -s "localhost:7009/v1/boms/<id>/cost?qty=100" -H "Authorization: Bearer $TOKEN"

# 2. produce 20 loaves
curl -s localhost:7009/v1/production-runs -H "Authorization: Bearer $TOKEN" -d '{"bom_id":"<id>","qty":20}'
curl -s -X POST localhost:7009/v1/production-runs/<run>/start -H "Authorization: Bearer $TOKEN"
curl -s localhost:7009/v1/production-runs/<run>/complete -H "Authorization: Bearer $TOKEN" \
  -d '{"output_qty": 19, "output_batch_no": "B-1016", "consumed": [{"item_id": "<flour uuid>", "qty": 10}]}'
```

## API

`/v1` routes need `Authorization: Bearer <token>`; via the gateway prefix them with `/api/nirmaan`.
Read = `bom:manage` or `report:read`; write = `bom:manage`.

| Method & path | Permission | Purpose |
|---|---|---|
| `GET /v1/boms?item_id=&status=&q=` | read | List BOM versions |
| `POST /v1/boms` | write | Create a draft (next version for the item) |
| `GET /v1/boms/{id}` | read | BOM with components & operations |
| `PUT /v1/boms/{id}` | write | Replace a **draft** |
| `POST /v1/boms/{id}/activate` | write | Make it the active version (archives the previous one; rejects cycles) |
| `POST /v1/boms/{id}/archive` | write | Retire |
| `POST /v1/boms/{id}/new-version` | write | Copy into a new draft |
| `GET /v1/boms/{id}/explode?qty=` | read | Indented tree + flattened raw-material requirement |
| `GET /v1/boms/{id}/cost?qty=` | read | Material + operations, total and per unit, per-component lines |
| `GET /v1/production-runs?store_id=&status=` | read | List runs |
| `POST /v1/production-runs` | write | `{bom_id, qty, store_id?, planned_date?, notes?}` → planned run + shortages |
| `GET /v1/production-runs/{id}?availability=false` | read | Run with lines (and shortages while open) |
| `POST /v1/production-runs/{id}/start` | write | `{"force": false}` — 409 with `details.shortages` when short |
| `POST /v1/production-runs/{id}/complete` | write | `{output_qty?, output_batch_no?, consumed?:[{item_id, qty}]}` |
| `POST /v1/production-runs/{id}/cancel` | write | Before completion only |
| `GET /healthz`, `GET /readyz` | none | Probes |

### Costing rules

- Component need = `qty / output_qty × run qty × (1 + scrap% / 100)`
- Raw material unit cost = bhandar `purchase_price_paise`; sub-assembly unit cost = its BOM's rolled-up cost per unit
- Operations cost is per BOM batch, scaled by `run qty / output_qty`
- Finished-goods unit cost = total cost ÷ **actual** output qty (so wastage raises unit cost)
- A production run consumes its **direct** components; sub-assemblies are stock items, produced by their own runs

## Code layout

```
cmd/server/main.go               wiring
internal/bom/model.go            BOM types, validation, Explode, Cost (pure, unit-tested)
internal/bom/service.go          storage, versions, activation, DB resolver
internal/bom/handlers.go         BOM routes
internal/production/production.go plan/start/complete/cancel, run costing, outbox event
internal/production/handlers.go  run routes
internal/clients/bhandar.go      prices and stock from bhandar
migrations/                      *.up.sql, embedded and applied at startup
```

## Deploy (Cloud Run)

`cloudbuild.yaml` tests, builds with `mool` as a build context, pushes to Artifact Registry and deploys:

- ingress **internal**, IAM auth required; `min-instances=0`, `max-instances=5` (back-office workload)
- `--no-cpu-throttling` so the outbox relay can publish right after a run completes
- env `BHANDAR_URL`; secrets `nirmaan-database-url`, `jwt-secret`, `service-secret`
- Pub/Sub topic `nirmaan.production.completed` with push subscriptions for `bhandar` and `bahi` (created by `sanchalan/scripts/pubsub-setup.sh`)

```bash
gcloud builds submit --config cloudbuild.yaml --substitutions=_ENV=dev,SHORT_SHA=$(git rev-parse --short HEAD)
```
