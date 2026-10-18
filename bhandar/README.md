# bhandar (भंडार) — Inventory Service

"Bhandar" means *storehouse*. It owns everything about stock:

- **Item master**: name, SKU, barcode, HSN, units + alternate units (1 BOX = 12 PCS), prices (sale / MRP / purchase), GST & cess rate, batch tracking, re-order rules
- **Barcodes**: internal EAN-13s (prefix `21`, inside GS1's 200–299 in-store range) generated for items without one; SVG rendering (EAN-13 or Code 128-B) in pure Go; bulk sticker-sheet data
- **POS catalog**: compact item + on-hand list for the PWA to cache in IndexedDB (ETag → `304` when unchanged)
- **Multi-store stock ledger**: append-only `stock_movements`, idempotent on `(source, source_id, line_no)`, with `stock_balances` per store/item/batch kept in the same transaction; **weighted-average cost**
- **Batches & expiry**: batch no, mfg/expiry dates, MRP; expiring-soon list; return-to-vendor debit-note drafts
- **Inter-store transfers**: draft → dispatched (out of source) → received (into destination at the dispatch cost)
- **Re-order**: suggestions, purchase drafts grouped by preferred supplier (body is ready for aadesh `POST /v1/invoices`), `bhandar.stock.low` alerts (once per item/store/day)
- **Analytics**: dead stock (no outward movement in N days), stock aging buckets (0-30 / 31-60 / 61-90 / 90+ days since last receipt), valuation

| | |
|---|---|
| Port (local) | `7008` |
| Database | `taraju_bhandar` |
| Publishes | `bhandar.stock.low`, `bhandar.batch.expiring` |
| Subscribes | `aadesh.invoice.created`, `aadesh.invoice.cancelled`, `aadesh.note.created`, `nirmaan.production.completed`, `pehchaan.tenant.created` |
| Calls | nothing synchronously |
| Called by | aadesh (item lookup), aayaat (bulk items / opening stock), nirmaan (lookup, stock summary), buddhi (stock summary) |
| Gateway prefix | `https://api.taraju.in/api/bhandar/...` |
| First deployed | **Week 5** (item master lite for the POS); full stock engine Weeks 14–19 (W14 schema, W15 item master & barcodes, W16 movements & transfers, W17 re-order, W18 expiry, W19 dead stock) |

## How stock moves

| Trigger | Movement |
|---|---|
| `aadesh.invoice.created` sale | out, at current average cost |
| `aadesh.invoice.created` purchase | in, at line taxable value ÷ qty (GST is input credit, not cost) |
| `aadesh.note.created` sale_return / purchase_return | in / out |
| `aadesh.invoice.cancelled` | exact reversal of the original document |
| `nirmaan.production.completed` | consumed lines out, produced lines in at production cost |
| `POST /v1/stock/adjustments` | signed qty with a mandatory note (`adjustment` or `expiry_writeoff`) |
| transfers | `transfer_out` on dispatch, `transfer_in` on receive |
| `POST /internal/v1/bulk/opening-stock` | `opening` (re-sending the same file is a no-op) |

Negative stock is allowed on purpose: a POS must never refuse a sale because the books are behind.
Invoice lines without `item_id` (free-text lines) do not touch stock.

## Run locally

Prerequisites: Go 1.25+, Docker (Postgres), the `mool` repo checked out next to this one.

```bash
cd ../sanchalan && docker compose up -d postgres && cd ../bhandar
make db-create
cp .env.example .env
make run                         # → http://localhost:7008/healthz
make test                        # barcode, weighted-average, transfer state machine, re-order maths
```

A new tenant gets default units (PCS, KG, GM, LTR, ML, BOX, DOZ, MTR) from `pehchaan.tenant.created`.

### Try it

```bash
TOKEN=...   # from pehchaan
curl -s localhost:7008/v1/items -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{
  "name": "Parle-G 100g", "hsn": "1905", "unit": "PCS", "sale_price_paise": 1000, "mrp_paise": 1000,
  "purchase_price_paise": 820, "tax_rate_bp": 1800, "reorder_level": 24, "reorder_qty": 96,
  "alt_units": [{"unit": "BOX", "factor": 48}]
}'
# → barcode "21000000000" + check digit is generated automatically
```

## API

`/v1` routes need `Authorization: Bearer <token>`; via the gateway prefix them with `/api/bhandar`.
"Read" = any of `item:manage, invoice:create, invoice:read, stock:manage, purchase:manage, report:read`.

| Method & path | Permission | Purpose |
|---|---|---|
| `GET /v1/items?q=&category=&status=` | read | Search by name / exact SKU / exact barcode |
| `POST /v1/items` | `item:manage` | Create (barcode auto-generated unless `generate_barcode:false`) |
| `GET /v1/items/{id}` · `PUT` · `DELETE` | read · `item:manage` | Get / replace / deactivate (soft) |
| `GET /v1/barcodes/{code}` | read | Scanner lookup (barcode or SKU) |
| `GET /v1/items/pos-catalog?store_id=` | read | Compact catalog + on-hand; honours `If-None-Match` |
| `PUT /v1/items/{id}/reorder` | `item:manage` or `stock:manage` | `{reorder_level, reorder_qty, preferred_supplier_id}` |
| `GET /v1/items/{id}/barcode.svg?module=2&height=60` | read | Barcode image |
| `POST /v1/labels` | read | `{"items":[{"item_id","copies"}],"show_price":true}` → labels + SVG per barcode |
| `GET /v1/items/{id}/ledger?store_id=&from=&to=` | stock read | Movements with running qty |
| `GET /v1/items/{id}/batches` | stock read | Batches with stock per store |
| `GET /v1/units` · `POST /v1/units` | read · `item:manage` | Units of measure |
| `GET /v1/stock?store_id=&item_id=&by_batch=true` | stock read | On hand, avg cost, value |
| `POST /v1/stock/adjustments` | `stock:manage` | `{store_id, reason, note, lines:[{item_id, batch_no, qty, unit_cost_paise}]}` |
| `GET /v1/transfers` · `POST` · `GET /{id}` | stock read · `stock:manage` | Transfers |
| `POST /v1/transfers/{id}/dispatch \| receive \| cancel` | `stock:manage` | State changes (receive needs access to destination store) |
| `GET /v1/reorder/suggestions?store_id=` | `stock:manage` or `purchase:manage` | Items at/below level with suggested qty |
| `POST /v1/reorder/purchase-draft` | same | `{store_id, supplier_id?, item_ids?}` → aadesh purchase bodies per supplier |
| `GET /v1/batches/expiring?days=30&store_id=` | stock read | Expired + expiring stock |
| `POST /v1/batches/{id}/return-to-vendor` | `stock:manage` | `{store_id, qty?, supplier_id?}` → `purchase_return` draft (references the original purchase bill) |
| `GET /v1/analytics/dead-stock?days=90` | `report:read` or `stock:manage` | Idle stock with value |
| `GET /v1/analytics/stock-aging` | same | Aging buckets |
| `GET /v1/analytics/valuation` | same | Weighted-average valuation |
| `POST /internal/v1/items/lookup` | service secret | `{"ids":[],"barcodes":[]}` |
| `POST /internal/v1/bulk/items` | service secret | Upsert by external_ref → sku → barcode |
| `POST /internal/v1/bulk/opening-stock` | service secret | Opening quantities |
| `GET /internal/v1/stock/summary?store_id=` | service secret | On hand + value + re-order level per item |
| `POST /internal/v1/jobs/expiry-scan` | service secret | Daily Cloud Scheduler job |
| `POST /internal/v1/jobs/reorder-scan` | service secret | Daily Cloud Scheduler job |
| `POST /events` | service secret / IAM | Pub/Sub push endpoint |
| `GET /healthz`, `GET /readyz` | none | Probes |

Internal calls need `X-Service-Secret` and `X-Tenant-ID` (jobs run across all tenants).

## Code layout

```
cmd/server/main.go              wiring
internal/barcode/               EAN-13 / Code 128 generation, validation, SVG
internal/inventory/model.go     types + pure rules (weighted average, transfer states, re-order maths, aging)
internal/inventory/items.go     item master, units, POS catalog, lookup, bulk items
internal/inventory/stock.go     movement engine, on hand, ledger, adjustments, opening stock
internal/inventory/transfers.go inter-store transfers
internal/inventory/reorder.go   suggestions, purchase drafts, expiry, return-to-vendor, scheduled scans
internal/inventory/analytics.go dead stock, aging, valuation, internal summary
internal/inventory/events.go    event subscribers
internal/inventory/handlers.go  HTTP routes
migrations/                     *.up.sql, embedded and applied at startup
```

## Deploy (Cloud Run)

`cloudbuild.yaml` tests, builds with `mool` as a build context, pushes to Artifact Registry and deploys:

- ingress **internal**, IAM auth required; `min-instances=1` (POS catalog & scanner lookups), `--no-cpu-throttling` (outbox relay)
- secrets: `bhandar-database-url`, `jwt-secret`, `service-secret`; env `EXPIRY_ALERT_DAYS`
- **Cloud Scheduler** (created by `sanchalan`), with header `X-Service-Secret` and OIDC auth:
  - `0 7 * * *` Asia/Kolkata → `POST /internal/v1/jobs/expiry-scan`
  - `30 7 * * *` Asia/Kolkata → `POST /internal/v1/jobs/reorder-scan`

```bash
gcloud builds submit --config cloudbuild.yaml --substitutions=_ENV=dev,SHORT_SHA=$(git rev-parse --short HEAD)
```
