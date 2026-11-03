# nirmaan (निर्माण) — Manufacturing Service

"Nirmaan" means *making / construction*. It turns raw material into finished goods:

- **Bills of Materials** — versioned recipes per finished item (`draft → active → archived`), components with qty per batch, unit and scrap %, header **yield %**, and a **routing** (operations with work centre, hours × hourly rate plus a fixed cost per batch)
- **Effective dates and alternates** — several active BOMs per item: a primary BOM per date range (activating a later-dated primary end-dates the earlier one) plus named **alternates** (another machine, another resin) that run beside it
- **Multi-level BOMs** — a component can be a sub-assembly with its own BOM (pinned with `sub_bom_id` or the item's primary BOM effective on the date); explosion is recursive with **cycle detection** (also checked on activation)
- **Unit conversion** — a component in `GM` of an item stocked in `KG` is converted through bhandar's alternate units and the standard metric/count table; an unconvertible unit is refused (at activation and when planning), never treated as 1:1
- **Cost roll-up** — raw material at bhandar's **weighted-average cost** (store, then all stores, then purchase price), sub-assemblies rolled up recursively, scrap, yield and routing included; money in integer paise; items with no cost are listed and the roll-up is marked incomplete
- **Production runs** — numbered `PR/2026-27/00001`; plan (shortages net of material reserved by other open runs) → start (**issues material to WIP**) → one or more **completions** (partial or final, good + rejected output, actual labour hours, **unplanned consumption**, **by-products**) or a **failed batch** → `nirmaan.production.completed` per completion
- **Job work** (Sec 143 CGST Act) — job workers, delivery **challans** `JW/2026-27/00001`, receipts of processed goods / returns / waste, **pending with each job worker** with the 1-year (inputs) / 3-year (capital goods) return-limit status, and the **ITC-04** report (tables 4 and 5A as CSV)

| | |
|---|---|
| Port (local) | `7009` |
| Database | `maapan_nirmaan` |
| Publishes | `nirmaan.production.completed` (production completions and job-work receipts) |
| Subscribes | nothing |
| Calls | `bhandar` — `POST /internal/v1/items/lookup` (units, alternate units, HSN, tax rate, purchase price), `GET /internal/v1/stock/summary[?store_id=]` (on hand and value → weighted-average cost) |
| Gateway prefix | `https://api.maapan.in/api/nirmaan/...` |
| First deployed | **Week 20** (BOM data model), Week 21 (BOM builder UI & costing), Week 22 (production run ↔ stock sync); Phase 5 (WIP, partial completion, job work) |

## Run locally

Prerequisites: Go 1.25+, Docker (Postgres), the `mool` repo next to this one, and `bhandar`
running for costing, units and availability.

```bash
cd ../sanchalan && docker compose up -d postgres && cd ../nirmaan
make db-create
cp .env.example .env
make run                         # → http://localhost:7009/healthz
make test                        # pure tests: explosion, scrap, units, costing, receipts, ITC-04
TEST_DATABASE_URL=postgres://maapan:maapan@localhost:5432/maapan_test?sslmode=disable make test-db
```

DB tests (`internal/*/integration_test.go`) create a throw-away schema in `TEST_DATABASE_URL`,
run the migrations and use an in-memory bhandar (`internal/testdb`); without the variable they skip.

### Try it

```bash
TOKEN=...   # user with bom:manage (super_admin / store_manager)
# 1. a BOM: 10 loaves from 4.9 kg flour (2% scrap) + 100 g yeast, baking 1 h at ₹50/h
curl -s localhost:7009/v1/boms -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{
  "item_id": "<bread uuid>", "item_name": "Bread 400g", "output_qty": 10, "unit": "PCS",
  "components": [
    {"item_id": "<flour uuid>", "item_name": "Flour", "qty": 4.9, "unit": "KG", "scrap_percent": 2},
    {"item_id": "<yeast uuid>", "item_name": "Yeast", "qty": 100, "unit": "GM"}
  ],
  "operations": [{"name": "Baking", "work_center": "Oven 1", "hours": 1, "rate_paise_per_hour": 5000}]
}'
curl -s -X POST localhost:7009/v1/boms/<id>/activate -H "Authorization: Bearer $TOKEN"
curl -s "localhost:7009/v1/boms/<id>/cost?qty=100&store_id=<store>" -H "Authorization: Bearer $TOKEN"

# 2. produce 20 loaves: start issues the material, two completions
curl -s localhost:7009/v1/production-runs -H "Authorization: Bearer $TOKEN" -d '{"item_id":"<bread uuid>","qty":20}'
curl -s -X POST localhost:7009/v1/production-runs/<run>/start -H "Authorization: Bearer $TOKEN"
curl -s localhost:7009/v1/production-runs/<run>/complete -H "Authorization: Bearer $TOKEN" \
  -d '{"final": false, "output_qty": 9, "rejected_qty": 1, "output_batch_no": "B-1016"}'
curl -s localhost:7009/v1/production-runs/<run>/complete -H "Authorization: Bearer $TOKEN" \
  -d '{"consumed": [{"item_id": "<salt uuid>", "item_name": "Salt", "qty": 200, "unit": "GM"}],
       "by_products": [{"item_id": "<crumbs uuid>", "qty": 1, "unit_cost_paise": 2000}], "labour_hours": 1.2}'

# 3. job work: send yarn for weaving, receive cloth back, ITC-04
curl -s localhost:7009/v1/job-workers -H "Authorization: Bearer $TOKEN" -d '{"name":"Surat Weavers","gstin":"24..."}'
curl -s localhost:7009/v1/job-work/challans -H "Authorization: Bearer $TOKEN" -d '{
  "job_worker_id": "<jw>", "principal_gstin": "27...", "nature_of_processing": "Weaving",
  "lines": [{"item_id": "<yarn uuid>", "qty": 100}]}'
curl -s localhost:7009/v1/job-work/challans/<ch>/receipts -H "Authorization: Bearer $TOKEN" -d '{
  "jw_challan_no": "SW/118", "processing_charges_paise": 400000,
  "lines": [{"challan_line_no": 1, "kind": "processed", "item_id": "<cloth uuid>", "qty": 380, "input_qty": 95},
            {"challan_line_no": 1, "kind": "waste", "qty": 3}]}'
curl -s "localhost:7009/v1/job-work/itc04?period=2026-27-H2&table=4" -H "Authorization: Bearer $TOKEN" -o itc04_t4.csv
```

## API

`/v1` routes need `Authorization: Bearer <token>`; via the gateway prefix them with `/api/nirmaan`.
BOM/run read = `bom:manage` or `report:read`; write = `bom:manage`. Job-work read = `bom:manage`,
`stock:manage`, `report:read` or `gst:file`; write = `bom:manage` or `stock:manage`.

| Method & path | Permission | Purpose |
|---|---|---|
| `GET /v1/boms?item_id=&status=&q=` | read | List BOM versions (primary/alternate, effective dates) |
| `POST /v1/boms` | write | Create a draft (next version for the item); `scrap_method`, `yield_percent`, `effective_from/to`, `is_primary`, `alternate_name`, operations with `work_center`, `hours`, `rate_paise_per_hour`, `cost_paise` |
| `GET /v1/boms/{id}` | read | BOM with components & operations |
| `PUT /v1/boms/{id}` | write | Replace a **draft** |
| `POST /v1/boms/{id}/activate` | write | Activate; a primary supersedes overlapping primaries (end-dates earlier ones, archives later ones); rejects cycles and unconvertible units |
| `POST /v1/boms/{id}/archive?force=` | write | Retire. 409 for the item's active primary BOM (activate a replacement) or when open runs use it, unless `force=true` (discontinued product) |
| `POST /v1/boms/{id}/new-version` | write | Copy into a new draft |
| `GET /v1/boms/{id}/explode?qty=&date=` | read | Indented tree + flattened requirement in stock units |
| `GET /v1/boms/{id}/cost?qty=&store_id=&basis=avg\|purchase&date=` | read | Material + routing, total and per unit, per-component lines with `price_source`, `missing_prices`, `complete` |
| `GET /v1/production-runs?store_id=&status=&q=` | read | List runs (`q` matches run no / item) |
| `POST /v1/production-runs` | write | `{bom_id \| item_id, qty, store_id?, planned_date?, notes?}` → numbered planned run + shortages |
| `GET /v1/production-runs/wip?store_id=` | read | In-progress runs with issued value not yet consumed, and the total |
| `GET /v1/production-runs/{id}?availability=false` | read | Run with lines, completions, yield, WIP (and shortages while open) |
| `POST /v1/production-runs/{id}/start` | write | `{force?, issue_material? (default true), note?}` — 409 with `details.shortages` when short |
| `POST /v1/production-runs/{id}/issue` | write | `{lines:[{item_id, qty, unit?, item_name?}], note?}` — top-ups and items outside the BOM |
| `POST /v1/production-runs/{id}/complete` | write | See below |
| `POST /v1/production-runs/{id}/cancel` | write | Open runs with no completion only |
| `GET/POST /v1/job-workers`, `GET/PUT /v1/job-workers/{id}` | read / write | Job workers (`gstin` validated; `state_code` required without one; `party_id` links the aadesh/bahi party) |
| `GET /v1/job-work/challans?job_worker_id=&status=&from=&to=` | read | Challans |
| `POST /v1/job-work/challans` | write | Send goods: `{job_worker_id, principal_gstin \| principal_state, nature_of_processing, challan_date?, store_id?, production_run_id?, eway_bill_no?, vehicle_no?, lines:[{item_id, qty, unit?, taxable_value_paise?, tax_rate_bp?, hsn?, goods_type: inputs\|capital_goods, return_exempt?}]}` → challan + `warnings` |
| `GET /v1/job-work/challans/{id}` | read | Challan with sent / processed / returned / waste / pending per line, due dates, receipts, warnings |
| `POST /v1/job-work/challans/{id}/receipts` | write | Receive back: `{receipt_date?, jw_challan_no?, jw_challan_date?, processing_charges_paise?, lines:[{challan_line_no, kind: processed\|returned\|waste, qty, item_id?, unit?, input_qty?}]}` |
| `POST /v1/job-work/challans/{id}/cancel` | write | Open challans with no receipt only |
| `GET /v1/job-work/pending?job_worker_id=&as_of=&all=` | read | Goods still with job workers: qty, value, due date, `days_left`, `ok \| due_soon \| overdue \| exempt` |
| `GET /v1/job-work/itc04?period=2026-27[-H1\|-H2\|-Q1..Q4] \| from=&to= &table=4\|5A &format=csv\|json` | read | ITC-04 table 4 (sent) / 5A (received back, losses & waste) |
| `GET /healthz`, `GET /readyz` | none | Probes |

### Completing a run

`POST /v1/production-runs/{id}/complete`:

| Field | Meaning |
|---|---|
| `final` | default `true`. `false` = partial completion; the run stays `in_progress` |
| `output_qty` | good output of this completion (BOM unit). Omitted on a final: the rest of the plan. `0` on a final with nothing good before: **failed batch** (status `failed`, material written off, nothing produced) |
| `rejected_qty` | output made but rejected (counts toward material settled and yield) |
| `consumed` | `[{item_id, qty, unit?, item_name?}]` overrides of this completion's consumption; an item not on the run is **unplanned consumption** (needs `item_name` unless bhandar knows it) |
| `by_products` | `[{item_id, qty, unit?, unit_cost_paise?}]` received into stock at the given value (default bhandar average); their value is taken off the main product's cost |
| `unit_costs` | `[{item_id, unit_cost_paise}]` per stock unit for items bhandar cannot cost. **Without a cost the completion is refused (422, `details.missing_prices`) — nothing is booked at ₹0** |
| `labour_hours` | actual hours; charged at the routing's average rate instead of the planned hours |
| `output_batch_no`, `failure_reason` | stored on the run; the batch goes on the produced line |

### Costing rules

- Component need = `qty ÷ output_qty × run qty ÷ (yield% / 100)`, then scrap: **`÷ (1 − scrap%/100)`** (`scrap_method: divide`, the default and the Tally/SAP convention) or `× (1 + scrap%/100)` (`multiply`, the old formula; migration 0002 sets it on every BOM created before, so their numbers do not change)
- Quantities are converted to the item's stock unit: base unit → bhandar alternate units (`1 BOX = 48 PCS`) → standard table (`KG/GM/MG/QTL/TON`, `LTR/ML/KL`, `MTR/CM/MM/KM`, `PCS/NOS/DOZ/GROSS`); events carry stock-unit quantities with `unit` set
- Raw-material unit cost = bhandar weighted-average cost (`value ÷ on hand`) at the run's store → across stores → purchase price; sub-assembly = its stock average, else its BOM's roll-up; manual `unit_costs` fill gaps only
- Operation cost per batch = `cost_paise + hours × rate_paise_per_hour`, scaled by `qty ÷ output_qty`
- A completion settles material up to the cumulative portion made: `planned × (good + rejected so far) ÷ run qty − consumed so far`; a **final** completion settles at least the whole run, so shortfall and wastage raise the unit cost
- Finished-goods unit cost = (material + operations − by-products) ÷ good output of that completion
- Money is int64 paise throughout (`units.MulDiv`, 128-bit exact); quantities are kept to 3 decimals
- A run consumes its **direct** components; sub-assemblies are stock items, produced by their own runs

### Material issue (WIP)

Start issues the planned material (`issue_material: false` to skip) and `/issue` adds top-ups or
non-BOM items. Issues are valued at the store average and shown as `issued_value_paise` / `wip_paise`.
**Stock moves in bhandar on completion, not at issue** (bahi has no WIP posting yet — see below);
until then, material issued to open runs is reserved: other runs' shortage checks subtract it.

### Events

Each completion enqueues one `nirmaan.production.completed`. `production_run_id` is the run id for
the first completion and `<run id>/<n>` for later ones, so bhandar's and bahi's `(source, source_id)`
idempotency holds per completion. Beyond mool's payload the event carries `run_no`, `receipt_seq`,
`final`, `failed`, `good_qty`, `rejected_qty`, `material_paise`, `operations_paise`,
`byproduct_value_paise`. `total_cost_paise` is the gross cost (material + operations); the produced
lines (main product + by-products) add up to it.

A job-work receipt with processed or waste lines publishes the same event (`job_work: true`,
`production_run_id` = receipt id): the inputs used up leave stock at challan value and the processed
goods arrive at input value + processing charges. Returns of unprocessed goods publish nothing.

### Job work and GST

- Challan numbers are `JW/<FY>/nnnnn` (16 characters, the Rule 55 limit); receipts `JWR/<FY>/nnnn`
- Each line has a due date: **1 year** for inputs, **3 years** for capital goods (Sec 143(1)); `return_exempt` (moulds, dies, jigs, fixtures, tools) has none. Past the date the goods are deemed supplied on the challan date (Sec 143(3)/(4)) — the pending report and the challan show `overdue` with that warning, `due_soon` within 30 days
- Warnings on the challan: return limits, e-way bill (inter-state job work: always; otherwise above ₹50,000), missing e-way number, unregistered job worker (Sec 143(1)(b) additional place of business)
- Values default to qty × bhandar average cost; tax rate and HSN default from bhandar; IGST vs CGST/SGST from principal vs job-worker state
- ITC-04: half-yearly (AATO > ₹5 crore) or annual since Oct 2021 — `period=2026-27-H1` / `2026-27`; quarters (`-Q1..Q4`) and any `from`/`to` also work. Table 5A uses the job worker's challan number, or our receipt number when there is none; waste goes into the losses columns

## Code layout

```
cmd/server/main.go                wiring
internal/units/                   unit conversion, UQC codes, exact paise arithmetic
internal/numbering/               PR/JW/JWR numbers per tenant and financial year
internal/bom/model.go             BOM types, validation, scrap/yield, Explode, Cost (pure, unit-tested)
internal/bom/service.go           storage, versions, effective dates, alternates, activation, archive guard, pricing
internal/bom/handlers.go          BOM routes
internal/production/production.go state machine, PlanLines, Shortages, CalcReceipt (pure, unit-tested)
internal/production/run.go        create/start/issue/cancel/WIP
internal/production/complete.go   completions, by-products, manual costs, event
internal/jobwork/model.go         due dates, warnings, ITC-04 periods/rows/CSV (pure, unit-tested)
internal/jobwork/service.go       job workers, challans, receipts, pending, ITC-04 queries
internal/clients/bhandar.go       items, units and stock/average cost from bhandar
internal/testdb/                  DB-gated test schema + fake bhandar
migrations/                       *.up.sql, embedded and applied at startup
```

## Known gaps (need other services)

- **bahi** posts every `nirmaan.production.completed` as Dr Finished Goods / Cr Raw Materials / Cr WIP. A failed batch (nothing produced) should go to a production-loss account, and a job-work receipt's processing charges are absorbed into WIP while the job worker's bill is booked separately in aadesh. Stock leaves at completion until bahi can post a material issue (Dr WIP / Cr Raw) — then nirmaan can publish at issue time.
- **bhandar** has no "at job worker" location: goods on a challan stay in the store's on-hand until the receipt; nirmaan's pending report is the record of what is out.
- ITC-04 tables 5B (received from another job worker) and 5C (supplied from the job worker's premises), and extending return limits (Commissioner's 1/2-year extension), are not modelled.

## Deploy (Cloud Run)

`cloudbuild.yaml` tests, builds with `mool` as a build context, pushes to Artifact Registry and deploys:

- ingress **internal**, IAM auth required; `min-instances=0`, `max-instances=5` (back-office workload)
- `--no-cpu-throttling` so the outbox relay can publish right after a run completes
- env `BHANDAR_URL`; secrets `nirmaan-database-url`, `jwt-secret`, `service-secret`
- Pub/Sub topic `nirmaan.production.completed` with push subscriptions for `bhandar` and `bahi` (created by `sanchalan/scripts/pubsub-setup.sh`)

```bash
gcloud builds submit --config cloudbuild.yaml --substitutions=_ENV=dev,SHORT_SHA=$(git rev-parse --short HEAD)
```
