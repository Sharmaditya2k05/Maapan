# Taraju — Platform Architecture (the contract every repo follows)

Taraju is a cloud-native, multi-tenant GST billing, accounting, inventory and
manufacturing ERP for Indian retail (the "Beyond MargBooks" programme).
Backend services are **Go**, the frontend is **Next.js**, and everything runs on
**Google Cloud Run**.

This file is the single source of truth for service names, ports, URLs, event
names, database names and cross-service rules. If you change one of these,
change it here first.

---

## 1. Repositories

Every folder under `taraju/` is its own git repository.

| Repo | Hindi | Meaning | What it owns | Local port | Database |
|---|---|---|---|---|---|
| `mool` | मूल | root | Shared Go library: config, logging, HTTP kit, JWT/RBAC, Postgres, Redis, events, GST maths, money | – | – |
| `dwaar` | द्वार | gateway | **Middleware / API gateway** — the only public backend. CORS, request IDs, rate limiting, JWT verification, RBAC pre-check, tenant headers, reverse proxy to services | 8000 | – (Redis) |
| `pehchaan` | पहचान | identity | OTP login (SMS/WhatsApp), JWT access + rotating refresh tokens, users, tenants (businesses), stores, role assignment | 7001 | `taraju_pehchaan` |
| `bahi` | बही | ledger book | Chart of accounts, parties (customers/suppliers), opening balances, double-entry vouchers, cashbook, bank & cheque reconciliation, trial balance, P&L, aging, cash-flow | 7002 | `taraju_bahi` |
| `kar` | कर | tax | GSTIN live verification, HSN/SAC master, tax computation API, e-Way bill & e-Invoice JSON, GSTR-1 / GSTR-3B | 7003 | `taraju_kar` |
| `aadesh` | आदेश | order | **Order / billing service** — POS sales invoices, purchase bills, sales & purchase returns, credit/debit notes, document numbering, offline POS sync, invoice PDF/print payloads | 7004 | `taraju_aadesh` |
| `lenden` | लेनदेन | transactions | **Payments** — Razorpay or Cashfree (switchable, both can run) dynamic UPI QR & payment links, webhook ingestion (idempotent, signature-verified), payment records, allocation to invoices, refunds | 7005 | `taraju_lenden` |
| `soochna` | सूचना | notification | SMS & WhatsApp delivery (MSG91 / Gupshup), templates, delivery log; sends OTPs, bills, ledger statements, alerts | 7006 | `taraju_soochna` |
| `aayaat` | आयात | import | MargBooks Excel/CSV migration: upload, column mapping, validation, dedupe, opening-balance reconciliation, push to bahi/bhandar | 7007 | `taraju_aayaat` |
| `bhandar` | भंडार | storehouse | Item master, units, barcodes, multi-store stock ledger, batches & expiry, transfers, re-order rules, dead-stock analytics | 7008 | `taraju_bhandar` |
| `nirmaan` | निर्माण | manufacturing | Multi-level Bill of Materials, cost roll-up, production runs that consume raw material and produce finished goods | 7009 | `taraju_nirmaan` |
| `buddhi` | बुद्धि | intelligence | **AI initiative** — purchase-bill photo → draft bill (Claude vision), ask-your-books Q&A, re-order forecasting, anomaly & GST-mismatch detection, live KPI dashboards (Redis) | 7010 | `taraju_buddhi` |
| `dukaan` | दुकान | shop | **Frontend** — Next.js PWA: POS counter, invoices, ledgers, stock, BOM, reports, settings. Offline billing via IndexedDB | 3000 | – |
| `sanchalan` | संचालन | operations | Local docker-compose, Cloud Run / Pub/Sub / Cloud SQL setup scripts, deployment plan, runbooks, this document | – | – |

Go module path: `github.com/taraju/<repo>`. Change the org with one `sed` when the GitHub org is created.

## 2. Request flow

```
Browser / POS (dukaan, Next.js PWA)
        │  HTTPS  https://api.taraju.in/api/<service>/<path>
        ▼
┌──────────────────────────── dwaar (Cloud Run, public) ───────────────────────┐
│ request-id → CORS → rate-limit (Redis) → JWT verify → RBAC pre-check → proxy │
└──────────────────────────────────────────────────────────────────────────────┘
        │  /api/aadesh/v1/invoices  ──►  aadesh  /v1/invoices
        │  (Authorization forwarded, X-Request-ID, X-Tenant-ID, X-User-ID, X-Role, X-Store-ID added)
        ▼
 pehchaan  bahi  kar  aadesh  lenden  soochna  aayaat  bhandar  nirmaan  buddhi
 (Cloud Run, ingress = internal, each with its own Postgres database)
        │                                 ▲
        └──── publish events ──► Pub/Sub ─┘ push to POST /events on subscribers
```

### Gateway routing rule

`/api/{service}/{rest}` is proxied to `{SERVICE}_URL/{rest}` for every service in
the table above (except `mool`, `sanchalan`, `dukaan`).

Public (no JWT) routes on the gateway:

- `GET /healthz`
- `POST /api/pehchaan/v1/auth/otp/request`
- `POST /api/pehchaan/v1/auth/otp/verify`
- `POST /api/pehchaan/v1/auth/refresh`
- `POST /api/pehchaan/v1/auth/logout`
- `POST /api/pehchaan/v1/onboarding` (carries pehchaan's own onboarding token, checked by pehchaan)
- `POST /api/lenden/v1/webhooks/razorpay` (verified by Razorpay signature inside lenden)
- `POST /api/lenden/v1/webhooks/cashfree` (verified by Cashfree signature inside lenden)
- `POST /api/soochna/v1/webhooks/{gupshup|msg91}` (verified by `?token=` inside soochna)

Everything else needs `Authorization: Bearer <access token>`.

### Every Go service exposes

| Route | Auth | Purpose |
|---|---|---|
| `GET /healthz` | none | liveness (Cloud Run startup/liveness probe) |
| `GET /readyz` | none | DB / Redis ping |
| `/v1/...` | JWT (forwarded by dwaar) | public API, reached via the gateway |
| `/internal/v1/...` | `X-Service-Secret` | service-to-service calls only |
| `POST /events` | `X-Service-Secret` locally; Pub/Sub OIDC push on Cloud Run | event subscriber endpoint |

## 3. Identity, tenancy and RBAC

- **Tenant** = one business (one GSTIN family). **Store** = one physical location / godown under a tenant.
- Every business table has `tenant_id UUID NOT NULL` and every query filters on it. Store-level tables also carry `store_id`.
- Access token: HS256 JWT, 15 minutes, signed with `JWT_SECRET` (shared by all Go services through Secret Manager). Claims:

```json
{ "sub": "<user uuid>", "tid": "<tenant uuid>", "sid": "<active store uuid>",
  "role": "store_manager", "stores": ["<uuid>", "..."], "iss": "pehchaan", "exp": 0 }
```

- Refresh token: opaque 256-bit random string, stored hashed in Redis (`pehchaan:rt:<sha256>`), 30 days, rotated on every use; reuse of an old token revokes the whole family.
- OTP: 6 digits, 5-minute expiry, max 3 requests/minute per phone, max 5 wrong attempts, plus per-IP throttle.
- Roles and permissions live in `mool/auth/rbac.go`:

| Permission | super_admin | store_manager | cashier | accountant |
|---|:-:|:-:|:-:|:-:|
| `invoice:create` | ✓ | ✓ | ✓ | |
| `invoice:cancel` | ✓ | ✓ | | |
| `invoice:read` | ✓ | ✓ | ✓ | ✓ |
| `purchase:manage` | ✓ | ✓ | | ✓ |
| `payment:collect` | ✓ | ✓ | ✓ | ✓ |
| `ledger:read` | ✓ | ✓ | | ✓ |
| `ledger:write` | ✓ | | | ✓ |
| `party:manage` | ✓ | ✓ | ✓ | ✓ |
| `item:manage` | ✓ | ✓ | | |
| `stock:manage` | ✓ | ✓ | | |
| `bom:manage` | ✓ | ✓ | | |
| `report:read` | ✓ | ✓ | | ✓ |
| `gst:file` | ✓ | | | ✓ |
| `import:run` | ✓ | | | ✓ |
| `user:manage` | ✓ | | | |
| `settings:manage` | ✓ | | | |
| `ai:use` | ✓ | ✓ | | ✓ |

Cashiers are limited to the stores in their `stores` claim.

## 4. Money, quantity and tax conventions

- Money is **int64 paise** in Go and `BIGINT` in Postgres. JSON fields carrying money end in `_paise`. Never use floats for money.
- Quantities are `NUMERIC(14,3)` in Postgres and `float64` in Go rounded to 3 decimals.
- Tax rates are **basis points** (`1800` = 18%). GST maths lives in `mool/gst` and is used in-process by `aadesh` (POS speed) and exposed over HTTP by `kar`.
- Intra-state supply (store state == place of supply) → CGST + SGST (half each). Inter-state → IGST.
- Invoice totals are rounded to the nearest rupee; the difference is stored as `round_off_paise`.
- All timestamps are `TIMESTAMPTZ`, stored UTC, displayed in `Asia/Kolkata`. Financial year starts 1 April.

## 5. Events (Pub/Sub)

Envelope (JSON, same for Pub/Sub and local direct delivery):

```json
{ "id": "uuid", "type": "aadesh.invoice.created", "tenant_id": "uuid", "store_id": "uuid",
  "source": "aadesh", "occurred_at": "2026-10-16T10:00:00Z", "data": { } }
```

- One Pub/Sub **topic per event type**, named exactly as the type.
- One push **subscription per (subscriber, topic)** named `<subscriber>--<topic>`, pushing to `https://<subscriber>/events` with OIDC auth.
- Handlers must be **idempotent** on `id` (each service keeps a `processed_events` table).

| Event type | Publisher | Subscribers | Purpose |
|---|---|---|---|
| `aadesh.invoice.created` | aadesh | bahi, bhandar, soochna, buddhi | post sales/purchase voucher, move stock, WhatsApp bill, KPIs |
| `aadesh.invoice.cancelled` | aadesh | bahi, bhandar, buddhi | reverse voucher and stock |
| `aadesh.note.created` | aadesh | bahi, bhandar, buddhi | credit/debit note (returns) |
| `lenden.payment.captured` | lenden | bahi, aadesh, soochna, buddhi | receipt voucher, mark invoice paid, receipt message |
| `lenden.payment.refunded` | lenden | bahi, aadesh | reverse receipt (one event per refund, `refund_id`) |
| `lenden.settlement.processed` | lenden | bahi | gateway payout: move gateway clearing to bank, book MDR + GST on fee |
| `bhandar.stock.low` | bhandar | soochna, buddhi | re-order alert, purchase draft suggestion |
| `bhandar.batch.expiring` | bhandar | soochna | expiry early warning |
| `bhandar.stock.moved` | bhandar | bahi, buddhi | every costed stock movement of a document (weighted-average cost) → COGS / inventory vouchers, margin KPIs |
| `nirmaan.production.completed` | nirmaan | bhandar, bahi | consume raw material, add finished goods, WIP costing |
| `aayaat.import.completed` | aayaat | soochna | tell the owner the migration finished |
| `pehchaan.tenant.created` | pehchaan | bahi, bhandar, kar | seed chart of accounts, default store/units, tax slabs |
| `kar.einvoice.generated` | kar | aadesh, soochna | IRN, ack no/date and signed QR stored on the invoice (or the IRP's error) |
| `kar.eway.generated` | kar | aadesh | e-way bill number and validity stored on the invoice |

The subscription matrix is also coded in `mool/events/events.go` (`Subscriptions`) (used by the local
direct driver) and in `sanchalan/scripts/pubsub-setup.sh`. Keep the three in sync.

Locally (`EVENTS_DRIVER=direct`) the publisher POSTs the envelope straight to each
subscriber's `/events` using `<SERVICE>_URL` and `X-Service-Secret`. On Cloud Run
(`EVENTS_DRIVER=pubsub`) it publishes through the Pub/Sub REST API using the
service account from the metadata server.

## 6. Synchronous service-to-service calls

Only where the user is waiting:

| Caller → callee | Call | Why sync |
|---|---|---|
| pehchaan → soochna | `POST /internal/v1/messages` | OTP must be sent now |
| aadesh → pehchaan | `GET /internal/v1/stores/{id}` | store state code & GSTIN (cached 10 min) |
| aadesh → bahi | `GET /internal/v1/parties/{id}` | party GSTIN/state for place of supply |
| aadesh → bhandar | `POST /internal/v1/items/lookup` | item price/HSN/tax at billing |
| aadesh → lenden | `POST /internal/v1/payment-requests` | UPI QR at the counter |
| bahi/aadesh → kar | `POST /internal/v1/gstin/verify` | GSTIN auto-fill |
| aayaat → bahi, bhandar | `POST /internal/v1/bulk/*` | batched migration writes |
| buddhi → bahi, aadesh, bhandar | `GET /internal/v1/...` | read-only facts for AI |

Internal calls carry `X-Service-Secret`, `X-Tenant-ID`, `X-Request-ID`, and on Cloud Run a
Google-signed ID token (`X-Serverless-Authorization: Bearer`) for the callee's URL. `mool/svc` does all of this.

### Internal API contracts (exact shapes)

All bodies are JSON; money in paise; errors are `{"error":{"code","message","details"}}`.

```text
pehchaan  GET  /internal/v1/stores/{id}
          → {"id","tenant_id","name","code","state_code","gstin","address","phone"}
pehchaan  GET  /internal/v1/tenants/{id}
          → {"id","business_name","gstin","state_code","owner_phone"}

soochna   POST /internal/v1/messages
          {"channel":"sms|whatsapp","to":"+91XXXXXXXXXX","template":"otp|invoice|payment_receipt|ledger_statement|stock_low|batch_expiring|import_done|weekly_summary",
           "params":{"k":"v"},"media_url":"optional https url (WhatsApp PDF)","idempotency_key":"optional"}
          → 202 {"id","status":"queued|sent|failed"}

bahi      GET  /internal/v1/parties/{id}
          → {"id","name","type":"customer|supplier|both","phone","email","gstin","state_code","address","credit_limit_paise","balance_paise"}
bahi      POST /internal/v1/bulk/parties          {"parties":[{"name","type","phone","gstin","state_code","address","opening_balance_paise","opening_type":"dr|cr","external_ref"}]}
          → {"created":n,"updated":n,"errors":[{"index":i,"message":"..."}]}
bahi      POST /internal/v1/bulk/opening-balances {"as_of":"YYYY-MM-DD","balances":[{"account_code"|"party_external_ref","amount_paise","type":"dr|cr"}]}
          → same result shape
bahi      GET  /internal/v1/reports/summary?from=&to=  → {"sales_paise","purchases_paise","receivables_paise","payables_paise","cash_paise","bank_paise"}

bhandar   POST /internal/v1/items/lookup          {"ids":["uuid"],"barcodes":["..."]}
          → {"items":[{"id","name","sku","barcode","hsn","unit","sale_price_paise","mrp_paise","purchase_price_paise","tax_rate_bp","cess_rate_bp","price_includes_tax","track_batches"}]}
bhandar   POST /internal/v1/bulk/items            {"items":[{"name","sku","barcode","hsn","unit","sale_price_paise","mrp_paise","purchase_price_paise","tax_rate_bp","reorder_level","external_ref"}]}
          → {"created","updated","errors"}
bhandar   POST /internal/v1/bulk/opening-stock    {"store_id","as_of","lines":[{"item_external_ref"|"item_id","qty","unit_cost_paise","batch_no","expiry_date"}]}
          → {"created","updated","errors"}
bhandar   GET  /internal/v1/stock/summary?store_id= → {"items":[{"item_id","name","on_hand","value_paise","reorder_level"}]}

kar       POST /internal/v1/gstin/verify          {"gstin"}
          → {"gstin","valid","legal_name","trade_name","status":"Active|Cancelled|Suspended","state_code","address","registration_type","source":"gsp|cache|checksum_only"}
kar       POST /internal/v1/tax/compute           {"store_state","place_of_supply","lines":[mool/gst.Line]} → mool/gst.Summary

lenden    POST /internal/v1/payment-requests      {"invoice_id","party_id","party_phone","amount_paise","purpose":"invoice|on_account","kind":"upi_qr|payment_link","expires_in_sec","gateway":"razorpay|cashfree (optional, default PAYMENT_GATEWAY)"}
          → {"id","status","upi_qr_image_url","upi_intent","short_url","gateway_ref","expires_at"}
```

## 7. Google Cloud layout

| Need | GCP product |
|---|---|
| Containers | Cloud Run services (one per Go repo + dukaan); Cloud Run **jobs** for big imports & nightly runs |
| Database | **Supabase** Postgres (Mumbai), one project per environment, one schema + login role per service (`supabase/setup.sql`), session pooler. Cloud SQL remains an option (`DB_PROVIDER=cloudsql`) |
| Cache / OTP / rate limits / KPIs | Upstash Redis (Mumbai, TLS) — or Memorystore with `REDIS_PROVIDER=memorystore` |
| Async events | Pub/Sub (push subscriptions) |
| Schedules | Cloud Scheduler → HTTPS (OIDC) |
| Files (imports, PDFs, bill photos) | Cloud Storage `taraju-<env>-files` |
| Secrets | Secret Manager, mounted as env vars |
| Images | Artifact Registry `asia-south1-docker.pkg.dev/<project>/taraju` |
| CI/CD | Cloud Build trigger per repo (`cloudbuild.yaml` in each repo) |
| Networking | VPC + Direct VPC egress, internal ingress for every service except dwaar & dukaan |
| Edge | Global external HTTPS load balancer + Cloud Armor in front of dwaar and dukaan |
| Observability | Cloud Logging (structured JSON from `mool/logx`), Cloud Monitoring, Error Reporting; Sentry in dukaan |

Region: `asia-south1` (Mumbai). Environments: `dev`, `staging`, `prod` (separate projects).

See [DEPLOYMENT.md](./DEPLOYMENT.md) for **what is deployed to Cloud Run and in which sprint week**.
