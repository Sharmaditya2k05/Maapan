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
- **Price lists & schemes**: retail / wholesale / party-wise lists with effective dates, per-unit and quantity-break prices; schemes (buy X get Y free, % or ₹/unit off by quantity slab, date-bound, per item or category, optionally per party); one quote call (`/internal/v1/pricing/quote`) for aadesh
- **Serial numbers**: serial-tracked items (`track_serials`); serials received on purchase, issued on sale, returned, written off, restored on cancellation; lookup with history; `serials/validate` for aadesh before it saves a document
- **Variants**: size / colour (any attributes) as child items of a parent, each with its own SKU, barcode, price and stock
- **Stock take / cycle count**: sessions per store (whole store, a category, a rack or a list of items) that freeze a snapshot; scanner-friendly count entry; variance report; posting adjusts by counted − frozen as `adjustment` moves with `bhandar.stock.moved`
- **Bin / rack locations** per store (optional per item), shown on count sheets
- **Supplier–item mapping**: supplier SKU / name / lead time, last purchase price, date, bill and quantity per supplier (kept from purchase bills); re-order drafts use the preferred supplier's last price
- **Weighing-scale barcodes**: EAN-13 prefixes 20–29 with a configurable layout per tenant (PLU length, internal check digit, value decimals, price / weight / off), resolved by the scanner lookup
- **HSN suggestions** from the tenant's own item master and kar's HSN master; `auto_hsn` on item create
- **Import support for aayaat**: alternate units on bulk items, batch mfg date / MRP on opening stock, per-record `results`, and `bulk/rollback`

| | |
|---|---|
| Port (local) | `7008` |
| Database | `maapan_bhandar` |
| Publishes | `bhandar.stock.low`, `bhandar.batch.expiring`, `bhandar.stock.moved` (every costed movement of a document, for bahi's COGS / inventory vouchers) |
| Subscribes | `aadesh.invoice.created`, `aadesh.invoice.cancelled`, `aadesh.note.created`, `nirmaan.production.completed`, `pehchaan.tenant.created` |
| Calls | kar `GET /internal/v1/hsn/{code}` and `GET /internal/v1/hsn?q=` (HSN suggestions only; optional, `KAR_URL`) |
| Called by | aadesh (item lookup, pricing quote, serial validation), aayaat (bulk items / opening stock / rollback), nirmaan (lookup, stock summary), buddhi (stock summary) |
| Gateway prefix | `https://api.maapan.in/api/bhandar/...` |
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
| `POST /internal/v1/bulk/rollback` (opening_stock) | `opening` reversal (source `opening.rollback`, `reversal: true`), only while nothing else moved the position |
| `POST /v1/stock-counts/{id}/post` | `adjustment` (source `stock_count`) of counted − frozen per line |

Negative stock is allowed on purpose: a POS must never refuse a sale because the books are behind.
Invoice lines without `item_id` (free-text lines) do not touch stock.

Costing rules (`internal/inventory/model.go`, unit-tested): an arrival re-averages the position
(`(old qty × old cost + in qty × in cost) ÷ total`); an issue leaves the average untouched and is
costed at it; an empty or negative position takes the next arrival's cost; a cancellation returns
goods at the cost the original document applied. Quantities on invoice, production and transfer
lines may be in an alternate unit (`BOX`) and are converted to the base unit through `item_units`.
Every write is one transaction: movement rows, balances, `bhandar.stock.moved` and any
`bhandar.stock.low` alert go into the outbox together, and `(source, source_id, line_no)` makes a
redelivered event a no-op.

## Pricing

`POST /internal/v1/pricing/quote` prices each line on `date` (default today, IST). **Price**: the first list that has
a price for the item wins, in rank order: lists assigned to the party (`party` lists before `wholesale` / `retail`
ones, then by `priority`), the requested `price_list_id`, the default list of the channel (`retail` unless
`channel=wholesale`); none → the item's `sale_price_paise`. Within a list a row in the line's unit wins (largest
`min_qty` ≤ qty, newest `valid_from`), else a base-unit row × the unit factor. Prices follow the item's
`price_includes_tax` convention (like the sale price) and may not exceed MRP. **Scheme**: of the schemes in force for the
item (listed or by category, for every party or for this one), the one worth most to the buyer applies: a % or ₹/unit
slab discount (`min_qty` in base units) on the line, or free units (`free_items[]`, same item unless `free_item_id`).
Schemes do not stack. GST stays with aadesh / kar: they tax `net_paise`.

## Serial numbers

Items with `track_serials` carry one serial per base unit. Invoice lines may send `serial_nos: []` (aadesh wraps
`events.InvoicePayload` with `lines[].serial_nos` until mool carries the field): a purchase receives them (in stock at the store, supplier recorded), a sale issues them (sold, to the
party), a sale return brings them back, a purchase return sends them to the vendor; adjustments take `serial_nos`
(found / written off). A cancellation restores each serial's previous status; serials a cancelled purchase introduced
disappear. Events never fail for a serial in an unexpected state (it is logged), so aadesh validates first with
`serials/validate`. Serials do not follow transfers yet.

## Import rollback

`POST /internal/v1/bulk/rollback` undoes per record and reports failures in `errors` (the rest still roll back):

- `opening_stock`: reverses the opening movement of store/item/batch at its cost (source `opening.rollback`,
  `bhandar.stock.moved` with `reversal: true`) only while no other movement has touched that position since; a
  later re-import posts the opening again (each rollback opens a new generation, so plain re-sends stay no-ops).
- `items`: archives (inactive, `archived_at`) an item the import created (`created: true`, or unknown) while it is
  untouched: not edited by a user, no movements besides rolled-back opening stock, no serials, variants, price-list
  rows, schemes or purchases. `created: false` (the import updated an existing item) is left alone. Re-importing
  the same item revives it and reports `created: true` again.

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
| `POST /v1/items` | `item:manage` | Create (barcode auto-generated unless `generate_barcode:false`; `track_serials`; `auto_hsn:true` fills an empty HSN / a 0 rate from the suggestions) |
| `GET /v1/items/{id}` · `PUT` · `DELETE` | read · `item:manage` | Get / replace / deactivate (soft) |
| `POST /v1/items/{id}/activate` | `item:manage` | Reactivate a deactivated item |
| `GET /v1/barcodes/{code}` | read | Scanner lookup (barcode, SKU, or a weighing-scale code whose PLU matches the item's `plu`/`sku`; the response then carries `scale: {kind: price\|weight, value, qty}`; layouts per prefix from `/v1/scale-formats`) |
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
| `GET /v1/price-lists` · `POST` | read · `item:manage` | Price lists `{name, kind: retail\|wholesale\|party, is_default, priority, valid_from, valid_to, active, party_ids}` |
| `GET /v1/price-lists/{id}` · `PUT` | read · `item:manage` | One list with its prices / update the header |
| `PUT /v1/price-lists/{id}/items` | `item:manage` | `{items:[{item_id, unit, min_qty, valid_from, valid_to, price_paise}], replace}` (upsert; never above MRP) |
| `PUT /v1/price-lists/{id}/parties` | `item:manage` | `{party_ids:[]}` the list applies to |
| `GET /v1/schemes?active_on=` · `POST` | read · `item:manage` | Schemes `{name, kind: free_qty\|percent\|flat, buy_qty, free_qty, free_item_id, slabs:[{min_qty, percent_bp\|off_per_unit_paise}], item_ids, category, party_ids, valid_from, valid_to}` |
| `GET /v1/schemes/{id}` · `PUT` · `DELETE` | read · `item:manage` | Get / replace / deactivate |
| `POST /v1/pricing/quote` | read | Same as the internal quote (POS / UI preview) |
| `GET /v1/serials?item_id=&store_id=&status=` | read | Serials (default `in_stock`; `status=all`) |
| `GET /v1/serials/{serial_no}` | read | Lookup across items with history |
| `POST /v1/serials/validate` | read | Same as the internal validate, for the active store |
| `GET /v1/items/{id}/variants?store_id=` · `POST` | read · `item:manage` | Variants with on-hand / `{variants:[{attrs:{size, colour}, name, sku, barcode, sale_price_paise, mrp_paise, purchase_price_paise}]}` (the rest is copied from the parent) |
| `GET /v1/stock-counts?store_id=&status=` · `POST` | read · `stock:manage` | Sessions / open one `{store_id, name, category?, bin_code?, item_ids?, note}` (freezes the snapshot) |
| `GET /v1/stock-counts/{id}?only=variance\|uncounted` | read | Count sheet + variance report with totals |
| `POST /v1/stock-counts/{id}/entries` | `stock:manage` | `{entries:[{code\|item_id, batch_no, qty, unit, mode: add\|set}]}`: a scan without qty counts 1 (a scale label counts its weight); bad entries are reported, good ones kept |
| `POST /v1/stock-counts/{id}/post` · `/cancel` | `stock:manage` | `{note, uncounted: skip\|zero}` → adjustment movements |
| `GET /v1/bins?store_id=` · `POST` | read · `stock:manage` or `item:manage` | Racks `{store_id, code, name, active}` |
| `GET /v1/items/{id}/bins` · `PUT` | read · `stock:manage` or `item:manage` | `{store_id, bin_code}` ("" clears) |
| `GET /v1/items/{id}/suppliers` | read | Suppliers of an item with last price / date / bill |
| `PUT /v1/items/{id}/suppliers/{supplier_id}` | `purchase:manage` or `item:manage` | `{supplier_sku, supplier_item_name, lead_time_days, preferred}` |
| `GET /v1/suppliers/{id}/items` | read | Items bought from a supplier |
| `GET /v1/scale-formats` · `PUT` | read · `item:manage` | Layout per prefix 20–29 / `{formats:[{prefix, kind: price\|weight\|none, plu_len, check_digit, value_decimals}]}` |
| `GET /v1/hsn/suggest?name=&code=&category=&limit=` | read | `{suggestions:[{code, description, rate_bp, cess_bp, source: items\|kar, uses}], kar: ok\|unavailable\|search_unsupported}` |
| `POST /internal/v1/items/lookup` | service secret | `{"ids":[],"barcodes":[]}` (items carry `track_serials`, `track_batches`) |
| `GET /internal/v1/items/{id}/batches?store_id=` | service secret | Internal twin of `GET /v1/items/{id}/batches` (same response) |
| `POST /internal/v1/bulk/items` | service secret | Upsert by external_ref → sku → barcode; `alt_units` replace the item's when sent; `results:[{index, id, created}]` |
| `POST /internal/v1/bulk/opening-stock` | service secret | Opening quantities; lines take `mfg_date`, `expiry_date`, `mrp_paise` (with a `batch_no`); `results` |
| `POST /internal/v1/bulk/rollback` | service secret | `{kind: items\|opening_stock, job_id, as_of, records:[{external_ref\|item_external_ref, item_id, batch_no, store_id, qty, created}]}` → `BulkResult` (see Import rollback) |
| `POST /internal/v1/pricing/quote` | service secret | `{party_id, price_list_id?, channel: retail\|wholesale, date?, lines:[{line_no, item_id, qty, unit}]}` → per line `unit_price_paise`, `price_source`, `discount_paise`, scheme; `free_items[]` |
| `POST /internal/v1/serials/validate` | service secret | `{store_id, doc_type: sale\|purchase\|sale_return\|purchase_return, lines:[{line_no, item_id, qty, unit, serial_nos}]}` → `{ok, errors:[{line_no, item_id, serial_no, message}]}` |
| `POST /internal/v1/serials/check` | service secret | `{item_id, store_id, serials:[]}` → `{serials:[{serial_no, status: in_stock\|sold\|returned_to_vendor\|written_off\|unknown, store_id}]}` (in request order, keyed as sent; a serial in stock elsewhere is `in_stock` with its store) |
| `GET /internal/v1/stock/summary?store_id=` | service secret | On hand + value + re-order level per item |
| `POST /internal/v1/jobs/expiry-scan` | service secret | Daily Cloud Scheduler job |
| `POST /internal/v1/jobs/reorder-scan` | service secret | Daily Cloud Scheduler job |
| `GET /internal/v1/outbox/dead` · `POST /internal/v1/outbox/replay?id=` | service secret | Events parked after 20 failed publishes, and their replay (ops) |
| `POST /events` | service secret / IAM | Pub/Sub push endpoint |
| `GET /healthz`, `GET /readyz` | none | Probes |

Internal calls need `X-Service-Secret` and `X-Tenant-ID` (jobs run across all tenants).

## Code layout

```
cmd/server/main.go              wiring
internal/barcode/               EAN-13 / Code 128-B generation, validation, weighing-scale parsing (configurable layouts), SVG (stdlib only)
internal/inventory/service.go   Service struct, store-access helpers, outbox emit
internal/inventory/model.go     types + pure rules (weighted average, transfer states, re-order maths, aging, unit conversion)
internal/inventory/items.go     item master, units, barcode/PLU lookup, POS catalog (ETag), lookup, bulk upsert
internal/inventory/stock.go     movement engine (post), on hand, ledger with running qty, adjustments, opening stock
internal/inventory/transfers.go inter-store transfers (draft → dispatched → received / cancelled)
internal/inventory/reorder.go   suggestions, purchase drafts, expiry list, return-to-vendor, scheduled scans
internal/inventory/analytics.go dead stock, aging, valuation, internal summary
internal/inventory/events.go    event subscribers (invoice, note, cancel, production, tenant)
internal/inventory/handlers.go  HTTP routes and permissions
internal/inventory/pricing.go   price lists, schemes, quote (EvaluateScheme is pure and unit-tested)
internal/inventory/serials.go   serial numbers: apply / revert per document, lookup, validate
internal/inventory/variants.go  variants under a parent item
internal/inventory/stockcount.go stock take / cycle count: sessions, entries, variance, posting
internal/inventory/masters.go   weighing-scale formats, bins, supplier–item mapping
internal/inventory/hsn.go       HSN suggestions (item master + kar)
internal/inventory/rollback.go  aayaat import rollback
internal/inventory/handlers_market.go HTTP routes of the above
migrations/                     *.up.sql, embedded and applied at startup
```

Tests: `go test ./...` runs the pure rules and the barcode package; set
`TEST_DATABASE_URL=postgres://maapan:maapan@localhost:5432/maapan_test?sslmode=disable` to also run
the end-to-end stock engine and HTTP tests (they create and drop their own schema).

## Deploy (Cloud Run)

`cloudbuild.yaml` tests, builds with `mool` as a build context, pushes to Artifact Registry and deploys:

- ingress **internal**, IAM auth required; `min-instances=1` (POS catalog & scanner lookups), `--no-cpu-throttling` (outbox relay)
- secrets: `bhandar-database-url`, `jwt-secret`, `service-secret`; env `EXPIRY_ALERT_DAYS`, `KAR_URL`
- **Cloud Scheduler** (created by `sanchalan`), with header `X-Service-Secret` and OIDC auth:
  - `0 7 * * *` Asia/Kolkata → `POST /internal/v1/jobs/expiry-scan`
  - `30 7 * * *` Asia/Kolkata → `POST /internal/v1/jobs/reorder-scan`

```bash
gcloud builds submit --config cloudbuild.yaml --substitutions=_ENV=dev,SHORT_SHA=$(git rev-parse --short HEAD)
```
