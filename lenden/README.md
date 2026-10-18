# lenden (लेनदेन) — Payments Service

"Lenden" means *transactions*. It owns every rupee that comes in or goes back:

- **Two gateways, switchable:** Razorpay and Cashfree (plus a mock for local dev). `PAYMENT_GATEWAY` picks the default for new requests; every gateway with keys stays enabled, so old payments still refund and reconcile through the gateway that took them
- **Dynamic UPI QR** at the POS counter (Razorpay single-use QR, or a Cashfree order + UPI QR) and **payment links** sent by SMS/WhatsApp
- **Webhooks** from Razorpay and Cashfree — each signature-verified (HMAC-SHA256), idempotent on event id *and* on payment id
- **Manual receipts** — cash, cheque, bank transfer against an invoice or on account
- **Refunds** — partial or full, through the gateway or manual
- **Reconciliation** — a scheduled job picks up payments whose webhook never arrived and expires stale QRs

Every captured payment or refund is published (via the transactional outbox) so `bahi` posts the
receipt voucher, `aadesh` marks the invoice paid, `soochna` sends the receipt and `buddhi` updates KPIs.

| | |
|---|---|
| Port (local) | `7005` |
| Database | `taraju_lenden` |
| Publishes | `lenden.payment.captured`, `lenden.payment.refunded` (payload: `mool/events.PaymentPayload`) |
| Subscribes | none |
| Calls | Razorpay and/or Cashfree PG API (`PAYMENT_GATEWAY=razorpay` or `cashfree` = default) |
| Called by | `aadesh` → `POST /internal/v1/payment-requests`; Cloud Scheduler → reconcile |
| Gateway prefix | `https://api.taraju.in/api/lenden/...` |
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
`payment.captured`, `qr_code.credited`, `payment_link.paid`, `refund.processed`.

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
| Payment ref | `<order_id>/<cf_payment_id>` (refunds are made against the order) |
| Refund | `POST /orders/{order_id}/refunds` with our refund id as `refund_id` (no double refunds) |
| Reconcile | `GET /orders/{id}` + `/payments`, `GET /links/{id}` + `/orders` |

### Switching gateways

Keep both gateways' keys configured and change `PAYMENT_GATEWAY`. New QRs and links go to the new default;
anything already taken on the other gateway still refunds and reconciles there. A single request can also
choose explicitly: `{"kind":"upi_qr", ..., "gateway":"razorpay"}`. Placeholder secrets that still say
`CHANGE_ME` count as "not configured".

## API

User routes need `Authorization: Bearer <token>`; via the gateway prefix them with `/api/lenden`.

| Method & path | Auth | Purpose |
|---|---|---|
| `POST /v1/payment-requests` | `payment:collect` | Create UPI QR / payment link. Body: `invoice_id, party_id, party_phone, amount_paise, purpose (invoice\|on_account), kind (upi_qr\|payment_link), expires_in_sec` |
| `GET /v1/payment-requests/{id}` | `payment:collect` | Status for POS polling (`created → paid / expired / failed`) |
| `GET /v1/payments` | `payment:collect` | Filters: `store_id, invoice_id, party_id, method, gateway, status, from, to, limit, offset` |
| `GET /v1/payments/{id}` | `payment:collect` | One payment |
| `POST /v1/payments/manual` | `payment:collect` | Cash/cheque/bank receipt: `amount_paise, method, invoice_id\|party_id, reference, received_at` |
| `POST /v1/payments/{id}/refund` | `invoice:cancel` | `{"amount_paise": 0 = all refundable, "reason": "..."}` |
| `POST /v1/webhooks/razorpay` | Razorpay signature | Public through dwaar |
| `POST /v1/webhooks/cashfree` | Cashfree signature | Public through dwaar |
| `GET /v1/gateways` | `payment:collect` | `{"default":"cashfree","available":["cashfree","razorpay"]}` |
| `POST /v1/dev/simulate-payment/{id}` | `payment:collect` | **Mock gateway only** — marks a request paid |
| `POST /internal/v1/payment-requests` | service secret | Same as the user route (called by aadesh) |
| `GET /internal/v1/payment-requests/{id}` | service secret | Status |
| `POST /internal/v1/jobs/reconcile` | service secret | Cloud Scheduler, every 10 min |
| `GET /healthz`, `GET /readyz` | none | Probes |

### Idempotency rules

1. `webhook_events.event_id` (`X-Razorpay-Event-Id`; for Cashfree, a hash of the body) — a redelivered webhook is a no-op.
2. `payments (gateway, gateway_ref)` unique — `payment.captured` and `qr_code.credited` for the same
   payment record it once; reconciliation can never double count.
3. Webhook log, payment row, request update and outbox event commit in **one transaction**.
4. Refunds pass our refund id as the Razorpay idempotency key and receipt.

## Environment

See `.env.example`: `DATABASE_URL`, `JWT_SECRET`, `SERVICE_SECRET`, `PAYMENT_GATEWAY`,
`RAZORPAY_KEY_ID`, `RAZORPAY_KEY_SECRET`, `RAZORPAY_WEBHOOK_SECRET`, `PAYEE_DISPLAY_NAME`,
`MOCK_UPI_VPA`, `EVENTS_DRIVER`, subscriber URLs for local direct events.

## Code layout

```
cmd/server/main.go             wiring (gateway driver, ledger, webhooks)
internal/gateway/              Gateway interface + Registry, Razorpay and Cashfree drivers, mock driver, webhook signature + parsing
internal/payment/model.go      request/response types and validation
internal/payment/service.go    payment requests, manual receipts, refunds, reconciliation
internal/payment/ledger.go     atomic capture/refund + outbox (Postgres)
internal/payment/webhook.go    webhook processor
internal/payment/handlers.go   HTTP routes
migrations/                    *.up.sql, embedded and applied at startup
```

## Deploy (Cloud Run)

`cloudbuild.yaml` tests, builds with `mool` as a build context, pushes and deploys:

- ingress **internal**, IAM auth; webhooks enter through dwaar's public route
- `min-instances=1`, `--no-cpu-throttling` (QR at the counter, outbox relay)
- secrets: `lenden-database-url`, `jwt-secret`, `service-secret`, `razorpay-key-id`,
  `razorpay-key-secret`, `razorpay-webhook-secret`, `cashfree-client-id`, `cashfree-client-secret`
  (leave a gateway's secrets as `CHANGE_ME` to keep it switched off)
- substitutions: `_PAYMENT_GATEWAY` (`razorpay` or `cashfree`), `_CASHFREE_ENV` (`sandbox` / `production`)
- Cloud Scheduler job → `POST /internal/v1/jobs/reconcile` every 10 minutes

```bash
gcloud builds submit --config cloudbuild.yaml --substitutions=_ENV=dev,SHORT_SHA=$(git rev-parse --short HEAD)
```
