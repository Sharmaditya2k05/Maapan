# aadesh (आदेश) — Order & Billing Service

"Aadesh" means *order*. It owns every billing document in Taraju:

- **POS sales invoices** (B2C walk-in and B2B with GSTIN)
- **Purchase bills** from suppliers
- **Sales returns → credit notes**, **purchase returns → debit notes**
- **GST-compliant numbering** per store / document type / financial year (`INV/26-27/00001`, ≤16 chars)
- **Offline POS sync** — idempotent upload of bills made without internet, numbered from device-reserved blocks
- Counter tenders (cash / card / UPI / credit) and payment status updated from `lenden`

GST maths comes from `mool/gst` in-process, so a bill is priced without a network hop.

| | |
|---|---|
| Port (local) | `7004` |
| Database | `taraju_aadesh` |
| Publishes | `aadesh.invoice.created`, `aadesh.invoice.cancelled`, `aadesh.note.created` |
| Subscribes | `lenden.payment.captured`, `lenden.payment.refunded` |
| Calls | `pehchaan` (store state/GSTIN), `bahi` (party details) |
| Gateway prefix | `https://api.taraju.in/api/aadesh/...` |
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
| `POST /v1/invoices` | `invoice:create` (+ `purchase:manage` for purchases) | Create & post an invoice/bill/note. Idempotent on `client_uuid` (200 on replay, 201 on create) |
| `POST /v1/invoices/preview` | `invoice:create` | Totals only, nothing saved |
| `GET /v1/invoices` | `invoice:read` | Filters: `doc_type, store_id, party_id, status, payment_status, from, to, q, limit, offset` |
| `GET /v1/invoices/{id}` | `invoice:read` | Invoice with lines, tenders, amount in words |
| `POST /v1/invoices/{id}/cancel` | `invoice:cancel` | `{"reason": "..."}` — publishes reversal event |
| `POST /v1/number-blocks` | `invoice:create` | Reserve `size` numbers for an offline device |
| `POST /v1/sync/invoices` | `invoice:create` | Upload up to 200 offline bills; per-bill `created / duplicate / error` |
| `GET /internal/v1/invoices[/{id}]` | service secret | For kar (e-way/e-invoice/GSTR), buddhi, lenden. List accepts `?include=lines` to attach lines & tenders |
| `POST /events` | service secret / IAM | Pub/Sub push endpoint |
| `GET /healthz`, `GET /readyz` | none | Probes |

### Offline billing flow (PWA)

1. When online, the POS calls `POST /v1/number-blocks {"doc_type":"sale","device_id":"<device>","size":200}` and stores the range in IndexedDB.
2. Offline, each bill takes the next number from the block, a `client_uuid` (UUIDv4) and `client_created_at`, and is printed immediately.
3. On reconnect the service worker POSTs the queue to `/v1/sync/invoices`. Replays return `duplicate`, so retrying is always safe.
4. When a block is 80% used the POS reserves the next one.

## Code layout

```
cmd/server/main.go          wiring (boot, clients, routes)
internal/invoice/model.go   request/response types, validation, FY helper
internal/invoice/service.go create, numbering, cancel, blocks, payment events
internal/invoice/store.go   SQL
internal/invoice/handlers.go HTTP routes
internal/clients/           pehchaan + bahi calls (store cache)
migrations/                 *.up.sql, embedded and applied at startup
```

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
