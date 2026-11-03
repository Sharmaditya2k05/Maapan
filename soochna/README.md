# soochna (सूचना) — Notification Service

"Soochna" means *information / notice*. Every SMS, WhatsApp message and e-mail Maapan sends goes through here:

- **OTP** for login (called synchronously by `pehchaan`)
- **WhatsApp bill** to the customer after a POS sale — as a **PDF document message** when `INVOICE_PDF_URL` is set — and the same bill by **e-mail** when the party has an e-mail in bahi
- **Payment receipts**, **ledger statements**, **payment reminders** on and after the due date
- **Owner alerts**: low stock, batch expiry, migration finished, weekly summary
- **Consent**: STOP/START keywords from WhatsApp and SMS, store-level "do not message", DND list; TRAI quiet hours for promotional messages
- **Dead letters**: messages that keep failing are parked, alerted on and replayable
- **Per-tenant sender registry**: a store's own DLT header, WhatsApp number, e-mail sender and template ids
- Bilingual (English + हिन्दी) templates, provider abstraction, idempotent sends, delivery log, delivery-report webhooks

| | |
|---|---|
| Port (local) | `7006` |
| Database | `maapan_soochna` |
| Publishes | – |
| Subscribes | `aadesh.invoice.created`, `lenden.payment.captured`, `bhandar.stock.low`, `bhandar.batch.expiring`, `aayaat.import.completed` |
| Calls | `pehchaan` `GET /internal/v1/tenants/{id}` (business name, owner phone) · `bahi` `GET /internal/v1/parties/{id}` (party e-mail) and `GET /internal/v1/receivables/overdue` (payment reminders — **not built in bahi yet**, see below) |
| Providers | SMS: **MSG91** Flow API · WhatsApp: **Gupshup** · E-mail: **SMTP** (SES / Postmark / SendGrid / ZeptoMail …) · `console` drivers for local |
| Gateway prefix | `https://api.maapan.in/api/soochna/...` |
| First deployed | **Week 3** (OTP) · WhatsApp bills & alerts live **Week 10** · stock/expiry alerts **Weeks 17–18** |

## Run locally

```bash
cd ../sanchalan && docker compose up -d postgres && cd ../soochna
make db-create
cp .env.example .env        # SMS/WHATSAPP/EMAIL_PROVIDER=console → messages are printed
make run                    # → http://localhost:7006/healthz
```

Send a test OTP:

```bash
curl -s localhost:7006/internal/v1/messages -H 'X-Service-Secret: local-dev-service-secret' -d '{
  "channel":"sms","to":"9876543210","template":"otp","params":{"otp":"123456","minutes":"5"}}'
# → 202 {"id":"...","status":"sent"}  and the body appears in the log
```

A bill by e-mail with a PDF attached (the console mailer logs it):

```bash
curl -s localhost:7006/internal/v1/messages -H 'X-Service-Secret: local-dev-service-secret' \
  -H 'X-Tenant-ID: 11111111-1111-1111-1111-111111111111' -d '{
  "channel":"email","to":"ravi@example.com","template":"invoice","media_url":"https://example.com/INV-1.pdf",
  "params":{"party_name":"Ravi","business":"Sharma Store","number":"INV/1","amount":"₹1,180.00","date":"03 Nov 2026"}}'
```

To test real delivery, set `SMS_PROVIDER=msg91` + `MSG91_AUTH_KEY` + `MSG91_TEMPLATE_OTP`,
`WHATSAPP_PROVIDER=gupshup` + `GUPSHUP_*`, or `EMAIL_PROVIDER=smtp` + `SMTP_*` + `EMAIL_FROM` in `.env`.

Tests: `go test ./...`. The database tests (retry → dead letter → replay, consent, quiet hours, registry,
reminders) run when `TEST_DATABASE_URL` points at a Postgres; each test uses its own schema.

## API

| Method & path | Auth | Purpose |
|---|---|---|
| `POST /internal/v1/messages` | service secret | `{channel: sms/whatsapp/email, to, template, params, media_url?, media_filename?, idempotency_key?}` → 202 `{id, status, reason?}`. Same key ⇒ same message, no resend. `media_url` (https) = WhatsApp document / e-mail attachment |
| `POST /internal/v1/jobs/retry-failed` | secret **or** scheduler OIDC | Resend due failures (backoff 1 m, 5 m, 15 m, 1 h), release quiet-hour messages, dead-letter after 5 attempts or 24 h |
| `POST /internal/v1/jobs/payment-reminders` | secret **or** scheduler OIDC | `{as_of?, tenant_ids?, dry_run?}` — per tenant with reminders on: ask bahi for overdue parties, message those whose days overdue are in `reminder_days` |
| `GET /internal/v1/dead-letters` | service secret | All tenants; filters `tenant_id, template, channel, reason, all=true, limit, offset` |
| `POST /internal/v1/dead-letters/replay` | service secret | `{ids?, tenant_id?, template?, channel?, reason?, limit?}` → resend now; `X-User-ID` is recorded as `replayed_by` |
| `POST /internal/v1/consents/bulk` | service secret | DND / opt-out import: `{addresses[], scope?: "global" or tenant id, channel?: "all", status?: "dnd"}` |
| `GET /v1/messages` | `settings:manage` | Delivery log: `status, channel, template, to, limit, offset` |
| `GET /v1/templates` | JWT | Template names + `details` (category, params, channels) |
| `GET /v1/dead-letters`, `POST /v1/dead-letters/replay` | `settings:manage` | The store's own dead letters |
| `GET /v1/consents?address=`, `POST /v1/consents` | `party:manage` | Store-level consent `{address, channel: sms/whatsapp/email/all, status: opted_out/opted_in/dnd, note?}`; the list includes global STOPs |
| `GET /v1/settings/messaging`, `PUT /v1/settings/messaging` | `settings:manage` | Sender/template registry (below) |
| `POST /v1/webhooks/{gupshup,msg91}?token=` | `WEBHOOK_TOKEN` | Delivery reports → `sent / delivered / read / failed` (never moves backwards); inbound `STOP` / `START` → consent |
| `POST /events` | service secret / IAM | Pub/Sub push |
| `GET /healthz`, `/readyz` | none | Probes |

Message statuses: `queued → sent → delivered → read`; `failed` (will be retried), `dead` (gave up — see
dead letters), `scheduled` (held for quiet hours), `suppressed` (opted out / DND — never sent; `error` says why).

### Templates

| Template | Category | Params (positional order for provider templates) | Channels | Sent on |
|---|---|---|---|---|
| `otp` | otp | otp, minutes | sms, whatsapp | pehchaan login |
| `invoice` | transactional | party_name, business, number, amount, date (+ optional link) | sms, whatsapp, email | `aadesh.invoice.created` (sales with a phone / party e-mail) |
| `payment_receipt` | transactional | party_name, business, amount, ref | sms, whatsapp, email | `lenden.payment.captured` |
| `payment_reminder` | transactional | party_name, business, amount, due_date (+ optional link) | sms, whatsapp, email | `jobs/payment-reminders` |
| `ledger_statement` | transactional | party_name, business, balance, as_of (+ link) | sms, whatsapp, email | bahi "send statement" |
| `stock_low` | alert | item, on_hand, reorder_level, business | sms, whatsapp | `bhandar.stock.low` → owner |
| `batch_expiring` | alert | item, batch, expiry, days, business, **qty** | sms, whatsapp | `bhandar.batch.expiring` → owner |
| `import_done` | alert | kind, imported, total, failed | sms, whatsapp | `aayaat.import.completed` |
| `weekly_summary` | alert | text | sms, whatsapp, email | buddhi weekly job |
| `promotion` | promotional | business, text | sms, whatsapp, email | store campaigns (via `POST /internal/v1/messages`) |

Register the same variables, in the same order, when creating the DLT template on MSG91 (`##otp##`, …)
and the WhatsApp template on Gupshup/Meta (`{{1}}`, `{{2}}`, …). Then set
`MSG91_TEMPLATE_<NAME>` / `GUPSHUP_TEMPLATE_<NAME>` to their ids — every template has a line in
`.env.example`, including `WEEKLY_SUMMARY`, `PAYMENT_REMINDER` and `PROMOTION`.

`batch_expiring` gained `qty` as its **6th** variable: the WhatsApp body always showed it, but it was never
passed positionally, so approved templates rendered without it. Add `{{6}}` / `##qty##` to the registered
templates. A test now fails whenever a body uses a variable that is not in the param order.

**Privacy:** OTP codes are redacted (`******`) in the stored params and body; OTPs are never retried.
Logs mask recipients in alert lines.

### Consent and quiet hours

| Category | Opt-out (STOP) | DND list | Opt-in needed | Quiet hours |
|---|---|---|---|---|
| otp, alert (owner/staff) | ignored | ignored | no | no |
| transactional (bill, receipt, reminder, statement) | **blocks** | ignored | no | no |
| promotional | **blocks** | **blocks** | yes (`PROMO_REQUIRES_OPT_IN=true`) | held until 09:00 IST (`QUIET_HOURS=21:00-09:00`) |

- `STOP`, `UNSUBSCRIBE`, `CANCEL`, `QUIT`, `OPTOUT`, `बंद` … sent on WhatsApp opts out of WhatsApp, on SMS of
  SMS; `STOP ALL` opts out of every channel; `START` / `UNSTOP` / `शुरू` restores every channel. Keywords are
  global: the sending number is Maapan's, shared by every store. Gupshup posts inbound messages to the same
  callback URL as delivery reports; point MSG91's inbound-SMS webhook at `/v1/webhooks/msg91?token=…` too.
- A store's own "do not message" (`POST /v1/consents`) applies to that store only.
- Every change is kept in `consent_log` (scope, source, time).
- Suppressed and scheduled messages are stored like any other, so the delivery log shows why a customer got nothing.
- Scheduled messages are re-checked when released, so a STOP sent at night still wins.

### Dead letters and alerts

A non-OTP message that fails is retried by the scheduler job with backoff. It is **dead-lettered**
(`status=dead`, a row in `dead_letters`) after 5 attempts, when it is still failing after 24 h, or at once
when the provider reports it is not configured (missing template id — retrying cannot help). Each dead
letter logs one ERROR line `"message":"ALERT: message dead-lettered"` with `"alert":"soochna.dead_letter"`;
create a log-based metric and alert on `jsonPayload.alert="soochna.dead_letter"`. Failed OTPs log
`alert="soochna.otp_failed"` and are never retried. A replay resets attempts, sends at once, and a message
that fails again goes through the normal retry cycle. Runbook: `sanchalan/docs/runbooks/dead-letter-replay.md`.

### E-mail and documents

- One `Mailer` interface with drivers `console` (local) and `smtp` (STARTTLS on 587, implicit TLS on 465;
  refuses to send credentials without TLS). Any transactional provider with SMTP works.
- The attachment is downloaded from `media_url` at send time (≤ 10 MiB). If the download fails the send
  fails and is retried, so a bill never goes out without its PDF.
- WhatsApp documents: with a template id the PDF is sent as the template's DOCUMENT header (register the
  template with a document header); without one as a free-form `file` message (24 h session window only).
- **Invoice PDF**: aadesh has **no PDF endpoint yet**. Set `INVOICE_PDF_URL` (for example
  `https://files.maapan.in/invoices/{tenant_id}/{invoice_id}.pdf`, a signed or public URL Gupshup can fetch)
  once one exists. Until then bills go as text, and any caller can attach a document through `media_url`.
- Bill and receipt e-mails go to the party's e-mail from bahi (`GET /internal/v1/parties/{id}`, needs
  `BAHI_URL`; the event payload carries no e-mail).

### Payment reminders

`POST /internal/v1/jobs/payment-reminders` (Scheduler, daily) runs for tenants that turned
`payment_reminders` on in the registry. For each one it calls bahi:

```
GET /internal/v1/receivables/overdue?as_of=YYYY-MM-DD        (X-Tenant-ID: <tenant>)
→ {"as_of":"2026-11-03","rows":[{"party_id":"…","party_name":"Ravi","phone":"+91…","email":"…",
    "overdue_paise":250000,"oldest_due_date":"2026-10-27","days_overdue":7,"pay_link":"https://…"}]}
```

**bahi does not expose this endpoint yet** (its aging report is user-only and ages by voucher date, not due
date). Until it does, the job reports `"error":"bahi has no GET /internal/v1/receivables/overdue yet"` per
tenant and sends nothing. A party is reminded when `days_overdue` is in the tenant's `reminder_days`
(default `0,7,15,30`) on each of its `reminder_channels` (default WhatsApp + e-mail). The idempotency key
`reminder:<party>:<as_of>:<channel>` makes re-runs safe; `dry_run:true` only counts. Each run is recorded
in `job_runs`.

### Sender / template registry

`PUT /v1/settings/messaging`:

```json
{"sms_sender_id":"SHRMST", "whatsapp_source":"+919111111111", "whatsapp_app_name":"sharmastore",
 "email_from":"bills@sharmastore.in", "email_from_name":"Sharma Store", "email_reply_to":"owner@sharmastore.in",
 "payment_reminders":true, "reminder_days":[0,7,15,30], "reminder_channels":["whatsapp","email"],
 "templates":[{"template":"invoice","channel":"whatsapp","provider_template_id":"…"}]}
```

Empty fields use Maapan's defaults. A tenant template id is used only together with the tenant's own
sender (a DLT template belongs to its header, a Meta template to its number). The SMS header is sent as
MSG91's `sender` and must be linked to the template in the MSG91 panel; e-mail senders must be verified on
the e-mail provider.

## Code layout

```
cmd/server/main.go           wiring, provider selection from env
internal/templates/          bilingual bodies, categories, param order, e-mail variants (tested)
internal/providers/          Sender (console, MSG91, Gupshup) and Mailer (console, SMTP),
                             tested with httptest and a fake SMTP server
internal/messaging/          send, consent gate, quiet hours, retry + dead letters, registry, reminders,
                             webhooks (delivery + STOP/START), event → message builders, handlers
internal/jobauth/            accepts Cloud Scheduler's Google OIDC token on /internal/v1/jobs/*
internal/clients/            pehchaan tenant lookup (10-min cache), bahi party / overdue
migrations/                  0001 messages, processed_events · 0002 dead_letters, consents, consent_log,
                             tenant_senders, tenant_templates, job_runs
```

## Deploy (Cloud Run)

- ingress **internal**, IAM auth, `min-instances=1` (OTP latency), 256 Mi
- secrets: `soochna-database-url`, `jwt-secret`, `service-secret`, `msg91-auth-key`, `gupshup-api-key`,
  `soochna-webhook-token`, `soochna-smtp-password` (all created by `sanchalan/scripts/gcp-bootstrap.sh`)
- template ids, `GUPSHUP_SOURCE`, `GUPSHUP_APP_NAME`, `EMAIL_PROVIDER=smtp`, `SMTP_HOST`, `SMTP_USERNAME`,
  `EMAIL_FROM`, `INVOICE_PDF_URL`: set once with `gcloud run services update soochna --update-env-vars=...`
  (deploy uses `--update-env-vars`, so they persist)
- Cloud Scheduler, **OIDC token only** (no secret in the job): `*/10 * * * *` → `POST /internal/v1/jobs/retry-failed`,
  `30 10 * * *` IST → `POST /internal/v1/jobs/payment-reminders`. The deploy sets `JOBS_OIDC_AUDIENCE` (the
  run.app URL) and `JOBS_OIDC_EMAILS` (the scheduler SA); soochna checks the token's Google signature,
  audience and e-mail itself, because dwaar and other services are invokers too
- invoker grants: soochna → pehchaan, soochna → bahi (`sanchalan/scripts/iam-invokers.sh`)
- Provider webhooks must reach `dwaar` at `https://api.maapan.in/api/soochna/v1/webhooks/{provider}?token=...`
  (public route on the gateway). Use it as Gupshup's callback (delivery + inbound) and as MSG91's
  delivery-report and inbound-SMS URL

Deploy order in Week 3: **soochna → pehchaan → dwaar**.
