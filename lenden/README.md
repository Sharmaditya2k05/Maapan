# lenden (लेनदेन) — Payments Service

"Lenden" means *transactions*. It owns every rupee that comes in or goes back:

- **Two gateways, switchable:** Razorpay and Cashfree (plus a mock for local dev). `PAYMENT_GATEWAY` picks the default for new requests; every gateway with keys stays enabled, so old payments still refund and reconcile through the gateway that took them
- **Per-tenant gateway keys** (MATURITY-PLAN decision D5): each store can bring its own Razorpay/Cashfree keys so money lands in its own bank account. Keys are AES-256-GCM encrypted at rest; platform keys remain the fallback
- **Dynamic UPI QR** at the POS counter (Razorpay single-use QR, or a Cashfree order + UPI QR) and **payment links** sent by SMS/WhatsApp
- **Webhooks** from Razorpay and Cashfree — each signature-verified (HMAC-SHA256, constant time) with the tenant's own secret or the platform secret, idempotent on event id *and* on payment id
- **Manual receipts** — cash, cheque, bank transfer against an invoice or on account, idempotent on a caller-supplied key
- **Refunds** — partial or full, through the gateway or manual, with an explicit `unknown` state when the gateway did not answer
- **Settlements** — Razorpay `settlement.processed` webhooks are stored (gross, fee, GST on fee, net, UTR) and published to bahi
- **Reconciliation** — a scheduled job picks up payments and refunds whose webhook never arrived, settles refunds in limbo and expires stale requests

Every captured payment, refund and settlement is published (via the transactional outbox) so `bahi` posts the
vouchers, `aadesh` marks the invoice paid, `soochna` sends the receipt and `buddhi` updates KPIs.

| | |
|---|---|
| Port (local) | `7005` |
| Database | `maapan_lenden` |
| Publishes | `lenden.payment.captured`, `lenden.payment.refunded` (payload: `mool/events.PaymentPayload`), `lenden.settlement.processed` (`mool/events.SettlementPayload`) |
| Subscribes | none |
| Calls | Razorpay and/or Cashfree PG API (`PAYMENT_GATEWAY=razorpay` or `cashfree` = default) |
| Called by | `aadesh` → `POST /internal/v1/payment-requests`; Cloud Scheduler → reconcile |
| Gateway prefix | `https://api.maapan.in/api/lenden/...` |
| First deployed | **Week 10** (Razorpay & WhatsApp alerts) |

## Run locally

Prerequisites: Go 1.25+, Docker (Postgres), `mool` checked out next to this repo.

```bash
cd ../sanchalan && docker compose up -d postgres && cd ../lenden
make db-create
cp .env.example .env        # PAYMENT_GATEWAY=mock — no Razorpay keys needed
make run                    # → http://localhost:7005/healthz
```

### Try the counter flow with the mock gateway

```bash
TOKEN=...   # from pehchaan OTP login

# 1. POS asks for a UPI QR for an invoice
curl -s localhost:7005/v1/payment-requests -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"invoice_id":"<invoice uuid>","amount_paise":11800,"purpose":"invoice","kind":"upi_qr"}'
# → {"id":"<req>","status":"created","upi_intent":"upi://pay?...","gateway_ref":"mockqr_..."}

# 2. Customer "pays" (mock only)
curl -s -X POST localhost:7005/v1/dev/simulate-payment/<req> -H "Authorization: Bearer $TOKEN"

# 3. POS polling sees it paid; lenden.payment.captured goes to bahi/aadesh/soochna/buddhi
curl -s localhost:7005/v1/payment-requests/<req> -H "Authorization: Bearer $TOKEN"
```

### With Razorpay test keys

Set `PAYMENT_GATEWAY=razorpay`, `RAZORPAY_KEY_ID`, `RAZORPAY_KEY_SECRET`, `RAZORPAY_WEBHOOK_SECRET`.
To receive webhooks locally, tunnel dwaar (e.g. `cloudflared tunnel --url http://localhost:8000`) and
point the Razorpay webhook to `https://<tunnel>/api/lenden/v1/webhooks/razorpay` with events
`payment.captured`, `qr_code.credited`, `payment_link.paid`, `refund.processed`, `settlement.processed`.

### With Cashfree test keys

Set `CASHFREE_CLIENT_ID`, `CASHFREE_CLIENT_SECRET` (sandbox keys from the Cashfree merchant dashboard),
`CASHFREE_ENV=sandbox`, and `PAYMENT_GATEWAY=cashfree` to make it the default. Cashfree requires a customer
phone on every order, so set `CASHFREE_FALLBACK_PHONE` (e.g. the store's number) for walk-in QR bills.
In the dashboard → Developers → Webhooks, add `https://<tunnel>/api/lenden/v1/webhooks/cashfree` for
**Payment success**, **Payment link** and **Refund** events. Webhooks are signed with the client secret.

How Cashfree maps onto lenden:

| lenden | Cashfree |
|---|---|
| UPI QR | `POST /orders` (order_id = our request id) then `POST /orders/sessions` with `upi.channel=qrcode` (QR returned as a `data:image/png` URL) |
| Payment link | `POST /links` (link_id = our request id) |
| Payment ref | `<order_id>/<cf_payment_id>` — the same key in webhooks and in `GET /orders/{id}/payments`, so reconcile never double-books (older link webhooks that only carry `transaction_id` use that, it is the same id) |
| Refund | `POST /orders/{order_id}/refunds` with our refund id as `refund_id` (no double refunds); `GET /orders/{order_id}/refunds` to settle refunds in limbo |
| Reconcile | `GET /orders/{id}` + `/payments` + `/refunds`, `GET /links/{id}` + `/orders` |

### Switching gateways

Keep both gateways' keys configured and change `PAYMENT_GATEWAY`. New QRs and links go to the new default;
anything already taken on the other gateway still refunds and reconciles there. A single request can also
choose explicitly: `{"kind":"upi_qr", ..., "gateway":"razorpay"}`. Placeholder secrets that still say
`CHANGE_ME` count as "not configured".

### Per-tenant gateway keys (D5)

A store owner (`settings:manage`) stores the store's own keys:

```bash
curl -s -X PUT localhost:7005/v1/settings/gateways/razorpay -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"key_id":"rzp_live_...","key_secret":"...","webhook_secret":"..."}'
# → {"gateway":"razorpay","key_id_masked":"****Ab12","env":"live","has_webhook_secret":true,"updated_at":"..."}
curl -s localhost:7005/v1/settings/gateways -H "Authorization: Bearer $TOKEN"     # masked: last 4 chars of key_id only
```

- Resolution: the tenant's keys when stored, else the platform keys from the environment — existing deployments keep working unchanged. A tenant with its own Cashfree keys may use Cashfree even when the platform has none.
- `env`: Razorpay `test`|`live` (default from the `rzp_test_` prefix), Cashfree `sandbox`|`production` (default `sandbox` for `TEST…` client ids). Razorpay needs `webhook_secret`; Cashfree signs webhooks with the client secret.
- Secrets are sealed with AES-256-GCM under `CREDENTIALS_KEY` (32 bytes, hex or base64), bound to their row (tenant, gateway, column). Outside prod an empty `CREDENTIALS_KEY` derives a key from `SERVICE_SECRET` with SHA-256 and logs a warning; **prod refuses to start without it**. Rotating the key requires re-saving every tenant's keys.
- Webhooks arrive without a tenant in the path. lenden parses the body, resolves the tenant from the payment request / payment it references (order id, QR id, payment id), verifies the signature with **that** tenant's webhook secret, then falls back to the platform secret; a settlement names no payment, so every tenant's secret for that gateway is tried. Nothing verifies → 401.
- The webhook URL in each store's Razorpay/Cashfree dashboard is the same public one (`/api/lenden/v1/webhooks/{gateway}`).

## API

User routes need `Authorization: Bearer <token>`; via the gateway prefix them with `/api/lenden`.

| Method & path | Auth | Purpose |
|---|---|---|
| `POST /v1/payment-requests` | `payment:collect` | Create UPI QR / payment link. Body: `invoice_id, party_id, party_phone, amount_paise, purpose (invoice\|on_account), kind (upi_qr\|payment_link), expires_in_sec, gateway` |
| `GET /v1/payment-requests/{id}` | `payment:collect` | Status for POS polling (`created → partial → paid`, or `expired / failed`) |
| `GET /v1/payments` | `payment:collect` | Filters: `store_id, invoice_id, party_id, method, gateway, status, from, to, limit, offset` |
| `GET /v1/payments/{id}` | `payment:collect` | One payment (`short_paise`, `excess_paise` show how it compared with the request) |
| `POST /v1/payments/manual` | `payment:collect` | Cash/cheque/bank receipt: `amount_paise, method, invoice_id\|party_id, reference, received_at, idempotency_key` (or `Idempotency-Key` header). 201 new, 200 replay |
| `POST /v1/payments/{id}/refund` | `invoice:cancel` | `{"amount_paise": 0 = all refundable, "reason": "..."}` → payment + `refund{id,status,...}`; 200 processed, 202 pending/unknown, 422 over-refund, 502 rejected |
| `GET /v1/settlements` | `payment:collect` or `ledger:read` | Gateway payouts: `gateway, from, to, limit, offset` |
| `GET /v1/settings/gateways` | `settings:manage` | Tenant's stored gateways, key ids masked |
| `PUT /v1/settings/gateways/{gateway}` | `settings:manage` | Store `key_id, key_secret, webhook_secret, env` for `razorpay` or `cashfree` |
| `DELETE /v1/settings/gateways/{gateway}` | `settings:manage` | Remove the tenant's keys (platform keys apply again) |
| `POST /v1/webhooks/razorpay` | Razorpay signature | Public through dwaar |
| `POST /v1/webhooks/cashfree` | Cashfree signature | Public through dwaar |
| `GET /v1/gateways` | `payment:collect` | `{"default":"cashfree","available":["cashfree","razorpay"]}` for this tenant |
| `POST /v1/dev/simulate-payment/{id}` | `payment:collect` | **Mock gateway only** — marks a request paid |
| `POST /internal/v1/payment-requests` | service secret | Same as the user route (called by aadesh) |
| `GET /internal/v1/payment-requests/{id}` | service secret | Status |
| `POST /internal/v1/jobs/reconcile` | service secret | Cloud Scheduler, every 10 min |
| `GET /healthz`, `GET /readyz` | none | Probes |

### Idempotency rules

1. `webhook_events.event_id` (`X-Razorpay-Event-Id`; for Cashfree, a hash of the body) — a redelivered webhook is a no-op.
2. `payments (gateway, gateway_ref)` unique — `payment.captured` and `qr_code.credited` for the same
   payment record it once; reconciliation can never double count.
3. `payments (tenant_id, idempotency_key)` unique — a manual receipt submitted twice (double tap, retry
   after a timeout) returns the first receipt with 200 instead of booking a second payment and voucher. The
   key is required (body field `idempotency_key` or `Idempotency-Key` header, ≤ 64 chars); the same key with
   a different amount or method is a 409.
4. Webhook log, payment row, request update and outbox event commit in **one transaction**.
5. Refunds pass our refund id as the Razorpay idempotency key / receipt and as Cashfree's `refund_id`.
6. `settlements (gateway, gateway_ref)` unique — one row and one event per gateway settlement.

### Captured vs requested

A capture is compared with what the request still had outstanding. `short_paise` (requested − captured)
and `excess_paise` (captured − requested), whichever is positive, are recorded on the payment and logged;
the event payload is unchanged. A short capture leaves the request `partial` (another capture may
complete it); `paid` otherwise. `lenden.payment.captured` carries `payment_request_id` for counter
QR/link captures.

### Refund states

| status | meaning |
|---|---|
| `pending` | row written (before the gateway is called) or the gateway accepted the refund and will confirm by webhook |
| `unknown` | the gateway call timed out, failed in transport or answered 5xx — the refund **may** have executed; the amount stays reserved |
| `processed` | confirmed (gateway response, `refund.processed` webhook or reconcile); `lenden.payment.refunded` published with `refund_id` |
| `failed` | definitive 4xx rejection, gateway reports it failed, or an `unknown` refund the gateway has no record of after an hour; the amount is refundable again |

The payment row is locked (`FOR UPDATE`) while the refundable amount (amount − Σ refunds not `failed`) is
recomputed and the pending row written, so concurrent refunds cannot both pass (422 on over-refund).
Webhooks and reconcile match a refund by gateway refund id first, then by our refund id (receipt/notes),
and promote any non-processed row. Refunds made from the gateway dashboard get a row with `source=gateway`.
`refunds.status` and `gateway_ref` are recorded as soon as they are known.

### Settlements

Razorpay's `settlement.processed` webhook reports the net payout (`amount`), the fee (`fees`) and GST on
the fee (`tax`); refunds are already netted into the payout. lenden stores
`gross = net + fee + fee_tax`, `refunds_paise = 0`, UTR and the raw payload, and publishes
`lenden.settlement.processed` (`SettlementPayload`: `fee_paise` ex-tax, `fee_tax_paise` GST) so bahi can
move gateway clearing to bank and book MDR + input credit. If a gateway's figures do not satisfy
gross − refunds = net + fee + fee_tax they are stored and emitted as reported with a warning in the log.
A settlement verified only by the platform secret (shared gateway account, no tenant reference) is stored
with a NULL tenant for audit and no event is published. Cashfree settlements are not handled yet.

### Reconciliation (`POST /internal/v1/jobs/reconcile`)

Every run, over the last 7 days:

1. Open (`created`/`partial`) requests are checked at their gateway (with the tenant's keys): payments the
   webhook missed are captured; refunds the gateway processed are booked through the same path as the
   webhook; requests closed at the gateway or past `expires_at` (+5 min grace) become `expired`.
2. Open requests past expiry that could not be asked about (older than the window, never created at the
   gateway, gateway removed) are expired too — except those whose gateway could not be reached this run.
3. Refunds `pending`/`unknown` for over 2 minutes are looked up (`GET /payments/{id}/refunds`,
   `GET /orders/{id}/refunds`) and settled: processed → booked + event, failed → `failed`, `unknown` with no
   record at the gateway after an hour → `failed`.

The report: `{"checked","captured","refunded","expired","refunds_checked","refunds_settled","errors"}`.

## Environment

See `.env.example`: `DATABASE_URL`, `JWT_SECRET`, `SERVICE_SECRET`, `CREDENTIALS_KEY` (per-tenant key
encryption; required in prod), `PAYMENT_GATEWAY`, `RAZORPAY_KEY_ID`, `RAZORPAY_KEY_SECRET`,
`RAZORPAY_WEBHOOK_SECRET`, `CASHFREE_*`, `PAYEE_DISPLAY_NAME`, `MOCK_UPI_VPA`, `EVENTS_DRIVER`,
subscriber URLs for local direct events.

## Code layout

```
cmd/server/main.go             wiring (credentials cipher, gateway registry, ledger, webhooks)
internal/creds/                AES-256-GCM cipher + gateway_credentials store (per-tenant keys)
internal/gateway/              Gateway interface + Registry (tenant keys → platform keys), Razorpay and Cashfree drivers,
                               mock driver, gateway error classification, webhook signature + parsing (incl. settlements)
internal/payment/model.go      request/response types and validation
internal/payment/service.go    payment requests, manual receipts, refunds, reconciliation, settlements, settings
internal/payment/ledger.go     atomic capture/refund/settlement + outbox (Postgres)
internal/payment/webhook.go    webhook processor (tenant resolution + signature)
internal/payment/handlers.go   HTTP routes
migrations/                    *.up.sql, embedded and applied at startup (0002_phase1: refund states, idempotency,
                               short/excess, settlements, gateway_credentials)
```

Tests: `GOWORK=off go test ./...`. The DB-backed tests in `internal/payment/integration_test.go` run when
`TEST_DATABASE_URL` is set (e.g. `postgres://maapan:maapan@localhost:5432/maapan_test?sslmode=disable`),
each in a throw-away schema, and skip otherwise.

## Deploy (Cloud Run)

`cloudbuild.yaml` tests, builds with `mool` as a build context, pushes and deploys:

- ingress **internal**, IAM auth; webhooks enter through dwaar's public route
- `min-instances=1`, `--no-cpu-throttling` (QR at the counter, outbox relay)
- secrets: `lenden-database-url`, `jwt-secret`, `service-secret`, `lenden-credentials-key`, `razorpay-key-id`,
  `razorpay-key-secret`, `razorpay-webhook-secret`, `cashfree-client-id`, `cashfree-client-secret`
  (leave a gateway's secrets as `CHANGE_ME` to keep the platform keys switched off; tenants' own keys still work)
- substitutions: `_PAYMENT_GATEWAY` (`razorpay` or `cashfree`), `_CASHFREE_ENV` (`sandbox` / `production`)
- Cloud Scheduler job → `POST /internal/v1/jobs/reconcile` every 10 minutes

```bash
gcloud builds submit --config cloudbuild.yaml --substitutions=_ENV=dev,SHORT_SHA=$(git rev-parse --short HEAD)
```
