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
- **Document flow** before the tax document: quotation / estimate → sales order → delivery challan → tax invoice, proforma invoice, purchase order → GRN → purchase bill — conversion copies lines, links every document to its source, tracks open (pending) quantity per line, supports partial conversion, short-close and cancel (see [Document flow](#document-flow))
- **Printing**: A4 tax invoice PDF (GSTIN, HSN summary, tax breakup, amount in words, IRN / ack / signed QR, bank details), a **signed expiring public link** for WhatsApp / e-mail attachments, and 2"/3" thermal ESC/POS payloads for the POS
- **Round-off rules per tenant** (nearest / up / down / none, to 1 paisa … ₹10) and print settings (bank details, terms, footer, thermal width)
- **Multi-unit lines** (BOX of 48 → 48 PCS through bhandar's alternate units; `unit_factor` and `base_qty` stored), **batch lines** (required for batch-tracked items, FEFO pick when a sale names none, expired / unknown batches refused) and **serial-number lines** (one serial per base unit, a serial cannot be sold twice or bought while in stock, notes must return serials from the original)

GST maths comes from `mool/gst` in-process, so a bill is priced without a network hop.

| | |
|---|---|
| Port (local) | `7004` |
| Database | `maapan_aadesh` |
| Publishes | `aadesh.invoice.created`, `aadesh.invoice.cancelled`, `aadesh.note.created` |
| Subscribes | `lenden.payment.captured`, `lenden.payment.refunded`, `kar.einvoice.generated`, `kar.eway.generated` |
| Calls | `pehchaan` (store state/GSTIN/registration, store details for print), `bahi` (party details, credit limit/days), `bhandar` (item lookup at billing; batches and serial checks once bhandar serves them), `lenden` (counter UPI QR / payment links) |
| Called by | `soochna` (`/internal/v1/invoices/{id}/pdf-link` for bill attachments), kar, buddhi, lenden |
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
| `GET /v1/settings/billing` · `PUT` | `invoice:read` · `settings:manage` | `{"round_off_mode":"nearest\|up\|down\|none","round_off_step_paise":1\|10\|50\|100\|500\|1000,"bank_name","bank_account_name","bank_account_no","bank_ifsc","bank_branch","upi_id","invoice_terms","invoice_footer","thermal_width_mm":58\|80}`. The rule applies to invoices, notes, orders and `/preview`; a document's `"round_off": false` still bills to the paisa |
| `POST /v1/orders` | `invoice:create` (sales kinds) · `purchase:manage` (`purchase_order`, `grn`) | Create a `quotation \| proforma \| sales_order \| delivery_challan \| purchase_order \| grn` (`kind`, `party_id` or `party`, `lines` as for invoices, `doc_date, valid_until, expected_date, supplier_ref, challan_reason, invoice_discount_bp, terms, notes, client_uuid`). Idempotent on `client_uuid` |
| `GET /v1/orders` | `invoice:read` | Filters `kind, store_id, party_id, status, open=true` (pending = open or partial), `from, to, q, limit, offset` |
| `GET /v1/orders/{id}` | `invoice:read` | Order with lines (`qty, converted_qty, open_qty, unit_factor, base_qty, serials, source_line_id`), `converted_to` and `made_from` links |
| `POST /v1/orders/{id}/convert` | as for create (by the order's kind) | `{"to":"sales_order\|proforma\|delivery_challan\|grn\|invoice","lines":[{"line_no":1,"qty":4,"serials":[],"batch_no":""}]}` — `lines` omitted = everything open; `invoice` is the tax invoice for sales kinds and the purchase bill for PO/GRN and takes `doc_date, due_date, tenders, supplier_invoice_no, supplier_invoice_date, tcs_bp, itc_eligibility, override_credit_limit, round_off, client_uuid`. Returns `{"kind","order"\|"invoice"}` (201; 200 on a `client_uuid` replay) |
| `POST /v1/orders/{id}/close` | as for create | Short-close an open/partial order (`{"reason"}` optional): the rest is no longer expected |
| `POST /v1/orders/{id}/cancel` | `invoice:cancel` (+ `purchase:manage` for PO/GRN) | `{"reason"}` — refused (409) while documents made from it are live; gives back what it took from its own source |
| `GET /v1/invoices/{id}/links` | `invoice:read` | The order lines an invoice was converted from |
| `GET /v1/invoices/{id}/pdf` | `invoice:read` | A4 PDF (`?download=true` for attachment disposition) |
| `GET /v1/invoices/{id}/print/thermal` | `invoice:read` | Receipt for a 2"/3" printer: `?width=58\|80` (default: tenant setting), `?format=escpos` (raw bytes, default) `\| text` (preview) `\| json` (`escpos_base64` + `text`) |
| `GET /public/v1/invoices/{id}/pdf?exp=&sig=` | signed link | The same PDF for whoever holds an unexpired signed link (WhatsApp / e-mail). Any failure is 404 |
| `GET /internal/v1/invoices/{id}/pdf-link` | service secret + `X-Tenant-ID` | `{"url","expires_at","file_name","number"}` — a fresh signed public URL (`?ttl=` seconds, default 1800, max 86400) |
| `GET /internal/v1/invoices/{id}/pdf` · `GET /internal/v1/orders/{id}` | service secret | PDF / order for other services |
| `GET /internal/v1/invoices[/{id}]` | service secret | For kar (e-way/e-invoice/GSTR), buddhi, lenden. List accepts the same filters (incl. `registration_id`, `store_gstin`) and `?include=lines` to attach lines & tenders |
| `POST /events` | service secret / IAM | Pub/Sub push endpoint |
| `GET /healthz`, `GET /readyz` | none | Probes |

### Offline billing flow (PWA)

1. When online, the POS calls `POST /v1/number-blocks {"doc_type":"sale","device_id":"<device>","size":100}` and stores the range in IndexedDB. A block belongs to the series and financial year it was cut from, so a block reserved on 30 March still syncs bills dated 2 April.
   Every queued bill carries `client_created_at`; the server dates it by that moment (IST), never by the sync time, and refuses future dates.
2. Offline, each bill takes the next number from the block, a `client_uuid` (UUIDv4) and `client_created_at`, and is printed immediately.
3. On reconnect the service worker POSTs the queue to `/v1/sync/invoices`. Replays return `duplicate`, so retrying is always safe.
4. When a block is 80% used the POS reserves the next one.

## Document flow

```
quotation ─┬─► proforma ─┬─► sales order ─┬─► delivery challan ─► TAX INVOICE ─► credit note
           │             │                └──────────────────────► TAX INVOICE
           └─────────────┴──────────────────────────────────────► TAX INVOICE
purchase order ─┬─► GRN ─► PURCHASE BILL ─► debit note
                └────────► PURCHASE BILL
```

| From | May become |
|---|---|
| `quotation` | `proforma`, `sales_order`, `delivery_challan`, tax invoice |
| `proforma` | `sales_order`, `delivery_challan`, tax invoice |
| `sales_order` | `delivery_challan`, tax invoice |
| `delivery_challan` | tax invoice |
| `purchase_order` | `grn`, purchase bill |
| `grn` | purchase bill |

- **Numbering**: `QT/ PI/ SO/ DC/ PO/ GRN/` + FY + serial, one series per GST registration like invoices (≤ 16 chars, Rule 46/55).
- **Conversion** copies the lines (item, unit, batch, serials, price, discount pro-rata for part quantities, tax), stores `source_line_id` on each new line, writes a `document_links` row per line and moves `converted_qty` on the source line — all in the transaction that creates the new document, under a row lock on the source, so two conversions can never take the same quantity. An invoice made from an order carries `source_order_id`. A retried conversion with the same `client_uuid` returns the first result.
- **Status** is derived: `open` → `partial` → `completed`; `closed` (short-closed by hand) and `cancelled` are terminal. Cancelling a later document (order or invoice) reverses its links and the source's open quantity comes back (a closed source stays closed). An order cannot be cancelled while documents made from it are live.
- **Stock and books move exactly once, on the tax document.** Quotations, proformas, sales orders, **delivery challans and GRNs publish no events**: nothing posts to bhandar or bahi until the tax invoice / purchase bill / note does, with the existing `aadesh.invoice.created` / `aadesh.note.created` / `aadesh.invoice.cancelled` events. A bill converted from a GRN (or an invoice from a challan) therefore never double-moves stock. The trade-off: goods received on a GRN but not yet billed (or dispatched on a challan but not yet invoiced) are not in bhandar's on-hand. Moving stock at GRN/challan time needs a new mool event (`aadesh.goods.received` / `aadesh.goods.dispatched`) and a payload flag telling bhandar to skip the later bill — not done here.
- Orders are not tax documents: they are never in `/internal/v1/invoices` (kar's GSTR feed) and have no IRN / e-way bill.

## Lines: units, batches, serials

- **Units**: `unit` must be the item's base unit or one of its bhandar `alt_units` (`BOX` factor 48). The line stores `unit_factor` and `base_unit`; `base_qty = qty × unit_factor`. MRP is checked per base unit. Events keep `qty` in the line's unit (bhandar converts through its own `item_units`).
- **Batches** (item `track_batches`): inward lines (purchase, GRN) need `batch_no`; an outward line naming a batch must name an existing, unexpired one; a sale with none takes the **FEFO** batch (earliest unexpired expiry holding the whole base quantity) — otherwise 422 with the batch list. Batches come from bhandar `GET /internal/v1/items/{id}/batches?store_id=`; while bhandar lacks that route (it only has the user route today) a batch is still required but not checked.
- **Serials** (`serials: []`, required when the item has `track_serials`): exactly one per base unit, unique on the document. aadesh keeps a serial ledger (`invoice_serials`; the latest movement on a posted document wins): a serial whose last movement was out cannot be sold again until a credit note brings it back, one whose last movement was in cannot be bought again, a never-seen serial is accepted (opening stock). Notes must return serials that are on the original line. Cancelling a document releases its serials. Invoice / note events carry each line's serials as `lines[].serial_nos` (an extra field bhandar reads; other subscribers ignore it) until mool's `events.InvoiceLine` has it. When bhandar serves `POST /internal/v1/serials/check` its `sold` (or in stock at another store) answer refuses the sale; an older bhandar leaves the local ledger in charge.

## Printing and the signed PDF link

- `GET /v1/invoices/{id}/pdf` renders an A4 PDF with [go-pdf/fpdf](https://github.com/go-pdf/fpdf) and the embedded **Hind** font (Indian Type Foundry, SIL OFL 1.1, `internal/invoice/fonts/OFL.txt`) so `₹` and Devanagari names print. fpdf does not shape complex scripts, so conjuncts and the `ि` matra print in logical order. Title: TAX INVOICE / BILL OF SUPPLY (composition store) / CREDIT NOTE / DEBIT NOTE / PURCHASE BILL. Store details come from pehchaan (printing still works from the invoice alone if pehchaan is down); bank details and terms from `/v1/settings/billing` (a blank placeholder when unset).
- The IRN, ack no / date and the IRP's signed QR (as a QR image) print when kar has reported them.
- **Signed public link**: `exp` (unix seconds) and `sig` = base64url(HMAC-SHA256(key, lower(invoice_id) + "|" + exp)), with key = HMAC-SHA256(`SERVICE_SECRET`, `"maapan/aadesh/invoice-pdf-link/v1"`). Links default to 30 minutes, at most 24 h; rotating `SERVICE_SECRET` kills every outstanding link. `PUBLIC_BASE_URL` (e.g. `https://api.maapan.in/api/aadesh`) is the prefix the link is built on.
- **How soochna should use it**: `INVOICE_PDF_URL` is a static pattern (`{tenant_id}/{invoice_id}/{number}`) and cannot carry a signature, so do not point it at aadesh. Instead, when building the bill message soochna calls
  `GET {AADESH_URL}/internal/v1/invoices/{invoice_id}/pdf-link?ttl=3600` with the service secret and `X-Tenant-ID: {tenant_id}`, and uses the returned `url` as `media_url` (and `file_name` as the attachment name). The link outlives Gupshup / SMTP retries; on 503 (`PUBLIC_BASE_URL` not set) or any error it sends the bill as text. dwaar must let `GET /api/aadesh/public/v1/invoices/{id}/pdf` through without a JWT (a prefix rule; the service verifies the signature).
- **Thermal**: `GET /v1/invoices/{id}/print/thermal` prepares ESC/POS commands server-side for 58 mm (32 columns) and 80 mm (48 columns) rolls: reset, PC437, bold/double-size header, lines with batch and serials, tax totals, tenders and change, a GST-rate summary, amount in words, the e-invoice QR (or a UPI pay QR for the balance when a UPI id is set) via `GS ( k`, and a partial cut. Non-ASCII text is sent as `?` (`₹` as `Rs`) because common thermal printers have no Indic font; print Hindi names through the A4 PDF.

## Code layout

```
cmd/server/main.go          wiring (boot, clients, routes)
internal/invoice/model.go   request/response types, validation, FY helper
internal/invoice/service.go create, numbering (per registration), cancel, blocks, payment events
internal/invoice/einvoice.go kar.einvoice.generated / kar.eway.generated consumers
internal/invoice/store.go   SQL
internal/invoice/handlers.go HTTP routes
internal/invoice/handlers_docs.go routes for orders, print and billing settings
internal/invoice/orders.go  quotation / proforma / SO / challan / PO / GRN, conversion, open qty, close/cancel
internal/invoice/lines.go   units (alt-unit factor), batches (FEFO), serial ledger
internal/invoice/settings.go round-off rule and print settings per tenant
internal/invoice/pdf.go     A4 PDF (fpdf + embedded Hind font), HSN summary
internal/invoice/pdflink.go signed public PDF links
internal/invoice/escpos.go  2"/3" thermal ESC/POS receipts
internal/clients/           pehchaan + bahi + bhandar (items, batches, serials) + lenden calls (store cache)
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
