# aadesh (आदेश) — Order & Billing Service

"Aadesh" means *order*. It owns every billing document in Maapan:

- **POS sales invoices** (B2C walk-in and B2B with GSTIN)
- **Purchase bills** from suppliers
- **Sales returns → credit notes**, **purchase returns → debit notes**
- **GST-compliant numbering**: one series per GST registration (GSTIN), document type and financial year, shared by the registration's stores (`INV/26-27/00001`, ≤16 chars). The primary registration keeps the original tenant-wide series (nothing renumbers); every other GSTIN has its own, so two GSTINs may both issue `INV/26-27/00001`. A store gets its own series only when it has a custom prefix (`PUT /v1/settings/numbering`)
- **Registration on every document**: `registration_id` and `store_gstin` (the registration's GSTIN) from pehchaan's store lookup
- **Purchase ITC eligibility** per line: `eligible` (default) | `ineligible_17_5` (blocked credit, Sec 17(5)) | `ineligible_other`; debit notes inherit it from the bill
- **e-Invoice / e-way bill status** from kar: IRN, ack no/date, signed QR, e-way bill number and validity are stored on the invoice; an invoice with a live IRN cannot be cancelled until kar cancels the IRN
- **Item master enforcement**: lines with an `item_id` are checked against bhandar — the item must be active, sales carry the item's tax classification, prices never exceed MRP (per base unit, so a BOX price is divided by its factor); HSN and unit are filled from the master when missing
- **Credit control**: a credit sale that would push a party past its credit limit is refused unless a manager overrides it; due dates follow the party's credit days
- **Returns bound to the original**: credit/debit notes copy party, store, rates and prices from the original, cannot return more than was sold, and reduce the original's balance; a document with posted notes or unrefunded online payments cannot be cancelled
- **Supplier bills** need the supplier's invoice date (GSTR-2B matching) and the same bill cannot be entered twice
- **Offline POS sync** — idempotent upload of bills made without internet, numbered from device-reserved blocks
- Counter tenders (cash / card / UPI / credit) and payment status updated from `lenden`

GST maths comes from `mool/gst` in-process, so a bill is priced without a network hop.

| | |
|---|---|
| Port (local) | `7004` |
| Database | `maapan_aadesh` |
| Publishes | `aadesh.invoice.created`, `aadesh.invoice.cancelled`, `aadesh.note.created` |
| Subscribes | `lenden.payment.captured`, `lenden.payment.refunded`, `kar.einvoice.generated`, `kar.eway.generated` |
| Calls | `pehchaan` (store state/GSTIN/registration), `bahi` (party details, credit limit/days), `bhandar` (item lookup at billing), `lenden` (counter UPI QR / payment links) |
| Gateway prefix | `https://api.maapan.in/api/aadesh/...` |
| First deployed | Week 5 (POS Sales Billing Engine) |

## Run locally

Prerequisites: Go 1.25+, Docker (for Postgres/Redis), the `mool` repo checked out next to this one.

```bash
# 1. start Postgres + Redis (once, from the sanchalan repo)
cd ../sanchalan && docker compose up -d postgres redis && cd ../aadesh

# 2. create the database and env file
make db-create
cp .env.example .env        # already sane for local

# 3. run (migrations apply automatically)
make run                    # → http://localhost:7004/healthz
```

To bill end-to-end you also need `pehchaan` (stores) and `bahi` (parties) running. For a
walk-in bill only `pehchaan` is needed.

### Try it

```bash
TOKEN=...   # from pehchaan: POST /v1/auth/otp/verify

curl -s localhost:7004/v1/invoices -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{
  "doc_type": "sale",
  "party": {"name": "Walk-in"},
  "lines": [
    {"name": "Parle-G 100g", "hsn": "1905", "qty": 2, "unit_price_paise": 1000, "tax_rate_bp": 1800, "price_includes_tax": true},
    {"name": "Tata Salt 1kg", "hsn": "2501", "qty": 1, "unit_price_paise": 2800, "tax_rate_bp": 0}
  ],
  "tenders": [{"mode": "cash", "amount_paise": 5000}],
  "client_uuid": "pos-1-0001"
}'
```

## API

All `/v1` routes need `Authorization: Bearer <access token>`; via the gateway prefix them with `/api/aadesh`.

| Method & path | Permission | Purpose |
|---|---|---|
| `POST /v1/invoices` | `invoice:create` (+ `purchase:manage` for purchases) | Create & post an invoice/bill/note. Idempotent on `client_uuid` (200 on replay, 201 on create). Purchase/debit-note lines take `itc_eligibility` |
| `POST /v1/invoices/preview` | `invoice:create` | Totals only, nothing saved |
| `GET /v1/invoices` | `invoice:read` | Filters: `doc_type, store_id, party_id, registration_id, store_gstin, status, payment_status, from, to, q, limit, offset` |
| `GET /v1/invoices/{id}` | `invoice:read` | Invoice with lines (`tax_category`, `itc_eligibility`), tenders, amount in words, `registration_id`, and e-invoice / e-way fields: `irn, ack_no, ack_date, signed_qr, einvoice_status (generated\|cancelled\|failed), einvoice_error, einvoice_error_code, irn_cancelled_at, ewb_no, ewb_date, ewb_valid_upto, ewb_status, ewb_error, ewb_vehicle_no, ewb_distance_km` |
| `POST /v1/invoices/{id}/cancel` | `invoice:cancel` | `{"reason": "..."}` — publishes reversal event; refused (409) while notes, unrefunded online payments or a live IRN exist (cancel the IRN through kar `POST /api/kar/v1/einvoices/{id}/cancel` first) |
| `POST /v1/invoices/{id}/payment-request` | `payment:collect` | `{"kind":"upi_qr"\|"payment_link"}` → lenden request for the balance due (QR image / UPI intent / short URL) |
| `POST /v1/number-blocks` | `invoice:create` | Reserve `size` numbers for an offline device (cashier ≤ 100, store manager ≤ 500, owner ≤ 1000) |
| `POST /v1/number-blocks/{id}/release` | `invoice:create` | `{"last_used": n}` — hand back the unused rest of a block (gaps are reported, never reused) |
| `POST /v1/sync/invoices` | `invoice:create` | Upload up to 200 offline bills; per-bill `created / duplicate / error / retry` (`retry` = a downstream service was down, keep it queued) |
| `GET /v1/settings/numbering` · `PUT` | `invoice:read` · `settings:manage` | Store-specific series prefixes (`{"store_id","doc_type","prefix"}`, prefix ≤ 4 chars, unique per tenant) |
| `GET /internal/v1/invoices[/{id}]` | service secret | For kar (e-way/e-invoice/GSTR), buddhi, lenden. List accepts the same filters (incl. `registration_id`, `store_gstin`) and `?include=lines` to attach lines & tenders |
| `POST /events` | service secret / IAM | Pub/Sub push endpoint |
| `GET /healthz`, `GET /readyz` | none | Probes |

### Offline billing flow (PWA)

1. When online, the POS calls `POST /v1/number-blocks {"doc_type":"sale","device_id":"<device>","size":100}` and stores the range in IndexedDB. A block belongs to the series and financial year it was cut from, so a block reserved on 30 March still syncs bills dated 2 April.
   Every queued bill carries `client_created_at`; the server dates it by that moment (IST), never by the sync time, and refuses future dates.
2. Offline, each bill takes the next number from the block, a `client_uuid` (UUIDv4) and `client_created_at`, and is printed immediately.
3. On reconnect the service worker POSTs the queue to `/v1/sync/invoices`. Replays return `duplicate`, so retrying is always safe.
4. When a block is 80% used the POS reserves the next one.

## Code layout

```
cmd/server/main.go          wiring (boot, clients, routes)
internal/invoice/model.go   request/response types, validation, FY helper
internal/invoice/service.go create, numbering (per registration), cancel, blocks, payment events
internal/invoice/einvoice.go kar.einvoice.generated / kar.eway.generated consumers
internal/invoice/store.go   SQL
internal/invoice/handlers.go HTTP routes
internal/clients/           pehchaan + bahi calls (store cache)
migrations/                 *.up.sql, embedded and applied at startup
```

### Numbering and registrations

- The series key is the store's registration: the registration that owns the tenant-wide series
  (the primary — it claims it on its first document, table `registration_series`) uses the nil
  UUID, so every number issued before registrations existed continues; any other registration
  numbers under its own id. Changing the primary later does not move either GSTIN to another series.
- A non-primary registration's series starts after the last number its GSTIN already took from the
  tenant-wide series (stores with their own GSTIN shared it before), so a GSTIN never repeats a number.
- Uniqueness is `(tenant_id, registration_key, doc_type, fy, number)`. Store prefixes
  (`numbering_settings`) still win; number blocks and offline sync use the same series.

Conventions (money in paise, tax in basis points, tenant_id on every row, events via outbox) are in
`../sanchalan/docs/ARCHITECTURE.md`.

## Deploy (Cloud Run)

`cloudbuild.yaml` runs tests, builds the image with `mool` as a build context, pushes to Artifact
Registry and deploys:

- ingress **internal** (only `dwaar` and Pub/Sub reach it), IAM auth required
- `min-instances=1`, `--no-cpu-throttling` (billing must not cold-start; outbox relay runs between requests)
- secrets: `aadesh-database-url`, `jwt-secret`, `service-secret`

```bash
gcloud builds submit --config cloudbuild.yaml --substitutions=_ENV=dev,SHORT_SHA=$(git rev-parse --short HEAD)
```

Pub/Sub subscriptions for this service are created by `sanchalan/scripts/pubsub-setup.sh`.
