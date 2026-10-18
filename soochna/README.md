# soochna (सूचना) — Notification Service

"Soochna" means *information / notice*. Every SMS and WhatsApp message Taraju sends goes through here:

- **OTP** for login (called synchronously by `pehchaan`)
- **WhatsApp bill** to the customer after a POS sale
- **Payment receipts**, **ledger statements**
- **Owner alerts**: low stock, batch expiry, migration finished
- Bilingual (English + हिन्दी) templates, provider abstraction, idempotent sends, delivery log, retries, delivery-report webhooks

| | |
|---|---|
| Port (local) | `7006` |
| Database | `taraju_soochna` |
| Publishes | – |
| Subscribes | `aadesh.invoice.created`, `lenden.payment.captured`, `bhandar.stock.low`, `bhandar.batch.expiring`, `aayaat.import.completed` |
| Calls | `pehchaan` `GET /internal/v1/tenants/{id}` (business name, owner phone) |
| Providers | SMS: **MSG91** Flow API · WhatsApp: **Gupshup** · `console` driver for local |
| Gateway prefix | `https://api.taraju.in/api/soochna/...` |
| First deployed | **Week 3** (OTP) · WhatsApp bills & alerts live **Week 10** · stock/expiry alerts **Weeks 17–18** |

## Run locally

```bash
cd ../sanchalan && docker compose up -d postgres && cd ../soochna
make db-create
cp .env.example .env        # SMS_PROVIDER=console, WHATSAPP_PROVIDER=console → messages are printed
make run                    # → http://localhost:7006/healthz
```

Send a test OTP:

```bash
curl -s localhost:7006/internal/v1/messages -H 'X-Service-Secret: local-dev-service-secret' -d '{
  "channel":"sms","to":"9876543210","template":"otp","params":{"otp":"123456","minutes":"5"}}'
# → 202 {"id":"...","status":"sent"}  and the body appears in the log
```

To test real delivery, set `SMS_PROVIDER=msg91` + `MSG91_AUTH_KEY` + `MSG91_TEMPLATE_OTP`, or
`WHATSAPP_PROVIDER=gupshup` + `GUPSHUP_*` in `.env`.

## API

| Method & path | Auth | Purpose |
|---|---|---|
| `POST /internal/v1/messages` | service secret | `{channel, to, template, params, media_url?, idempotency_key?}` → 202 `{id, status}`. Same key ⇒ same message, no resend |
| `POST /internal/v1/jobs/retry-failed` | service secret (Cloud Scheduler) | Resend failed non-OTP messages from the last 24 h (max 5 attempts) |
| `GET /v1/messages` | `settings:manage` | Delivery log: `status, channel, template, to, limit, offset` |
| `GET /v1/templates` | JWT | Template names |
| `POST /v1/webhooks/{gupshup\|msg91}?token=` | `WEBHOOK_TOKEN` | Provider delivery reports → `sent / delivered / read / failed` (never moves backwards) |
| `POST /events` | service secret / IAM | Pub/Sub push |
| `GET /healthz`, `/readyz` | none | Probes |

### Templates

| Template | Params (positional order for provider templates) | Sent on |
|---|---|---|
| `otp` | otp, minutes | pehchaan login |
| `invoice` | party_name, business, number, amount, date (+ optional link) | `aadesh.invoice.created` (sales with a phone) |
| `payment_receipt` | party_name, business, amount, ref | `lenden.payment.captured` |
| `ledger_statement` | party_name, business, balance, as_of (+ link) | bahi "send statement" |
| `stock_low` | item, on_hand, reorder_level, business | `bhandar.stock.low` → owner |
| `batch_expiring` | item, batch, expiry, days, business | `bhandar.batch.expiring` → owner |
| `import_done` | kind, imported, total, failed | `aayaat.import.completed` |

Register the same variables, in the same order, when creating the DLT template on MSG91 (`##otp##`, …)
and the WhatsApp template on Gupshup/Meta (`{{1}}`, `{{2}}`, …). Then set
`MSG91_TEMPLATE_<NAME>` / `GUPSHUP_TEMPLATE_<NAME>` to their ids.

**Privacy:** OTP codes are redacted (`******`) in the stored params and body; OTPs are never retried.

## Code layout

```
cmd/server/main.go           wiring, provider selection from env
internal/templates/          bilingual bodies, param order (tested)
internal/providers/          Sender interface, console, MSG91, Gupshup (tested with httptest)
internal/messaging/          send/retry/webhook/list service, event → message builders (tested), handlers
internal/clients/            pehchaan tenant lookup (10-min cache)
migrations/                  messages, processed_events
```

## Deploy (Cloud Run)

- ingress **internal**, IAM auth, `min-instances=1` (OTP latency), 256 Mi
- secrets: `soochna-database-url`, `jwt-secret`, `service-secret`, `msg91-auth-key`, `gupshup-api-key`, `soochna-webhook-token`
- template ids / `GUPSHUP_SOURCE` / `GUPSHUP_APP_NAME`: set once with `gcloud run services update soochna --update-env-vars=...` (deploy uses `--update-env-vars`, so they persist)
- Cloud Scheduler: `*/15 * * * *` → `POST /internal/v1/jobs/retry-failed` with OIDC token + `X-Service-Secret`
- Provider webhooks must reach `dwaar` at `https://api.taraju.in/api/soochna/v1/webhooks/{provider}?token=...` (public route on the gateway)

Deploy order in Week 3: **soochna → pehchaan → dwaar**.
