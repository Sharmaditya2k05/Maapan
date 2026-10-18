# buddhi (बुद्धि) — AI & Insights Service

"Buddhi" means *intelligence*. It is Taraju's AI initiative plus the live analytics that feed it:

1. **Purchase-bill scanner** — photo/PDF of a supplier bill → Claude vision → a *draft* purchase bill for `aadesh`, with GSTIN checksum, line-sum and GST re-computation checks, catalog item matching, confidence and warnings. **Never auto-posts.**
2. **Ask your books** — questions in Hindi / English / Hinglish ("is mahine kitna udhaar baaki hai?") answered by Claude using read-only tools over the tenant's own ledger, sales, stock and invoices. Optional SSE streaming.
3. **Re-order forecasting** — nightly exponential smoothing with day-of-week seasonality (pure Go, no LLM) → items whose demand over the supplier lead time exceeds stock.
4. **Anomaly & GST checks** — unusual discounts per cashier (3σ), high cancellation rates, purchases/B2B sales missing a GSTIN, invalid GSTINs, the same HSN billed at different GST rates.
5. **Live dashboard KPIs** — sales, bills, GST, gross margin, payment mix and top items per store per day, kept in Redis with Postgres as the source of truth (spec Week 23 "Redis Analytics Dashboards").
6. **Weekly WhatsApp summary** — a short Hinglish business update for the owner every Monday via `soochna`.

| | |
|---|---|
| Port (local) | `7010` |
| Database | `taraju_buddhi` (+ Redis db 3, optional) |
| Publishes | — |
| Subscribes | `aadesh.invoice.created`, `aadesh.invoice.cancelled`, `lenden.payment.captured`, `bhandar.stock.low` |
| Calls | `bahi` (reports summary), `bhandar` (stock summary), `aadesh` (invoices), `pehchaan` (tenant/store), `soochna` (WhatsApp), Claude API |
| Gateway prefix | `https://api.taraju.in/api/buddhi/...` |
| First deployed | **Week 23** (dashboards). AI features piloted from **Week 12** behind the per-tenant flag `tenant_settings.ai_enabled` |

## Run locally

Prerequisites: Go 1.25+, Docker (Postgres/Redis), `mool` checked out next to this repo.

```bash
# 1. infra (from sanchalan)
cd ../sanchalan && docker compose up -d postgres redis && cd ../buddhi

# 2. database + env
make db-create
cp .env.example .env          # set ANTHROPIC_API_KEY to try the AI features

# 3. run (migrations apply automatically)
make run                      # → http://localhost:7010/healthz
```

Without `ANTHROPIC_API_KEY` the service still runs: dashboards, forecasts and anomaly checks work;
`/v1/scan/*` and `/v1/ask` answer **503** with a clear message.

Enable AI for your local tenant (the pilot flag), then try it:

```bash
TOKEN=...   # super_admin token from pehchaan
curl -X PUT localhost:7010/v1/ai/settings -H "Authorization: Bearer $TOKEN" \
  -d '{"ai_enabled": true, "owner_phone": "+919999999999"}'

curl -X POST localhost:7010/v1/scan/purchase-bill -H "Authorization: Bearer $TOKEN" -F file=@bill.jpg

curl -N -X POST 'localhost:7010/v1/ask?stream=true' -H "Authorization: Bearer $TOKEN" \
  -d '{"question": "Pichhle hafte sabse zyada kya bika?"}'

make job-forecast             # nightly job, run by hand
```

## API

Via the gateway prefix with `/api/buddhi`. All `/v1` routes need a user JWT.

| Method & path | Permission | Purpose |
|---|---|---|
| `POST /v1/scan/purchase-bill` | `ai:use` + `purchase:manage` | multipart `file` (JPEG/PNG/WebP/GIF/PDF ≤ 10 MB) → `{result: {draft, supplier, matches, confidence, warnings, ...}, usage}`. `draft` is the body for aadesh `POST /v1/invoices` once a supplier `party_id` is picked |
| `POST /v1/ask` | `ai:use` | `{"question"}` → `{answer, usage}`. `?stream=true` → SSE events `delta` `{text}`, then `done` `{answer, usage}` or `error` |
| `GET /v1/forecast/reorder` | `ai:use` | `store_id` (default active store), `lead_days` → items to re-order with suggested qty |
| `GET /v1/insights/anomalies` | `ai:use` | `days` (30), `kind`, `store_id`, `include_resolved` → findings + high-cancellation cashiers |
| `POST /v1/insights/anomalies/{id}/resolve` | `ai:use` | mark handled |
| `GET /v1/dashboard` | `report:read` | `store_id` (`all` = business), `date` → today vs yesterday, last 7 days, top 10 items, payment mix |
| `GET /v1/ai/usage` | `ai:use` | tokens used this month vs budget, by feature/model |
| `GET /v1/ai/settings` / `PUT /v1/ai/settings` | `ai:use` / `settings:manage` | `{ai_enabled, monthly_token_budget, owner_phone}` |
| `POST /internal/v1/jobs/forecast` | service secret | nightly (Cloud Scheduler 02:00 IST) |
| `POST /internal/v1/jobs/weekly-summary` | service secret | Mondays 09:00 IST |
| `POST /events` | service secret / IAM | Pub/Sub push |
| `GET /healthz`, `GET /readyz` | none | probes |

Cashiers and store managers only ever see their own stores (dashboard, anomalies, re-order, and the ask tools).

## How the AI is used

| Feature | Model (env) | Effort | Notes |
|---|---|---|---|
| Bill scanning | `AI_MODEL_FAST` (default `claude-opus-5-5`) | medium | Vision + structured outputs (strict JSON schema). Every number is re-validated in Go (`internal/scan`) |
| Ask your books | `AI_MODEL_SMART` (default `claude-opus-5-5`) | medium | Streaming agent loop with 4 read-only tools, **max 6 tool calls**; tool inputs validated in Go; `*_paise` fields are annotated with exact `₹` strings so the model never does currency maths |
| Weekly summary | `AI_MODEL_SMART` | low | ≤ 600 characters, numbers pre-formatted |

- Server-side **refusal fallback** is on (`AI_REFUSAL_FALLBACK=true`, beta `server-side-fallback-2026-07-01`, `fallbacks: "default"`): a declined request is re-served by a fallback model within the same call; if it is still declined the API returns 422 with the category.
- System prompts are static and prompt-cached; dates and scope go in the user message.

### Cost controls

- **Per-tenant monthly token budget**: `AI_MONTHLY_TOKEN_BUDGET` (default 2M tokens), overridable per tenant via `PUT /v1/ai/settings`. Over budget → 429 until the 1st (IST).
- **Pilot flag**: with `AI_REQUIRE_TENANT_FLAG=true` only tenants with `ai_enabled` can call AI endpoints.
- Every call (including failed ones) is logged in `ai_usage` with input/output/cache tokens; see `GET /v1/ai/usage`.
- Tool results are capped at 24 KB; the agent loop is capped at 6 tool calls; `max-instances=10` on Cloud Run caps concurrency.
- Bill scanning is the high-volume feature: once you have a labelled set of bills, measure `AI_MODEL_FAST=claude-haiku-5-5` against it before switching.
- Forecasting, anomaly rules and dashboards use **no LLM at all**.

## Code layout

```
cmd/server/main.go        wiring
internal/llm/             Client interface + Anthropic SDK implementation (streaming, tools, structured output)
internal/scan/            extraction schema, validation, fuzzy item matching, draft builder   (unit-tested)
internal/ask/             system prompt, tools, input validation, ₹ annotation              (unit-tested)
internal/forecast/        SES + day-of-week model, re-order rule                           (unit-tested)
internal/anomaly/         Welford stats, discount/cancellation/GSTIN rules (+ listing SQL) (unit-tested)
internal/kpi/             event ingestion → Postgres rollups + Redis counters; dashboard
internal/jobs/            nightly forecast, re-order report, weekly summary
internal/store/           tenant AI settings, usage log, budget guard
internal/clients/         calls to bahi, bhandar, aadesh, pehchaan, soochna
internal/api/             HTTP routes
migrations/               *.up.sql, embedded
```

## Deploy (Cloud Run)

`cloudbuild.yaml` tests, builds with `mool` as a build context and deploys:

- ingress **internal**, IAM auth; reached through `dwaar` and Pub/Sub only
- `min-instances=0`, `max-instances=10`, 1 GiB, `timeout=300s` (streaming answers)
- secrets: `buddhi-database-url`, `redis-url`, `jwt-secret`, `service-secret`, **`anthropic-api-key`**
- Egress to `api.anthropic.com` goes direct (VPC egress is `private-ranges-only`); Redis (Memorystore) via the VPC.

Cloud Scheduler jobs (HTTP target with OIDC token for the `buddhi` URL **and** header `X-Service-Secret`):

| Job | Schedule (Asia/Kolkata) | Target |
|---|---|---|
| `buddhi-forecast` | `0 2 * * *` | `POST /internal/v1/jobs/forecast` |
| `buddhi-weekly-summary` | `0 9 * * 1` | `POST /internal/v1/jobs/weekly-summary` |

Rollout: deploy in **Week 12** with `AI_REQUIRE_TENANT_FLAG=true` and enable `ai_enabled` only for the 2–3 pilot stores; dashboards go live for everyone in **Week 23**.
