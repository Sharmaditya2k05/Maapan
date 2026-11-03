# buddhi (बुद्धि) — AI & Insights Service

"Buddhi" means *intelligence*. It is Maapan's AI initiative plus the live analytics that feed it:

1. **Purchase-bill scanner** — photo/PDF of a supplier bill → Claude vision → a *draft* purchase bill for `aadesh`, with GSTIN checksum, line-sum and GST re-computation checks, GST slabs validated **as of the bill date** (`mool/gst.ValidRate`), **item matching against bhandar's item master** (name + SKU/barcode + HSN + unit + last purchase price, with alternatives for the picker), confidence and warnings. **Never auto-posts.**
2. **Ask your books** — questions in Hindi / English / Hinglish ("is mahine kitna udhaar baaki hai?") answered by Claude using read-only tools over the tenant's own ledger, sales, stock and invoices. Optional SSE streaming. The question and every tool result are delimited (`<user_question>`, `<tool_data>`) and treated as untrusted data.
3. **Re-order suggestions** — nightly exponential smoothing with day-of-week seasonality (pure Go, no LLM) **combined with bhandar's live stock and re-order rules**: the larger of forecast need and re-order-rule need, days of cover, stock-out-before-delivery flag, priced and grouped by preferred supplier.
4. **Anomaly & GST checks** — unusual discounts per cashier (3σ), high cancellation rates, purchases/B2B sales missing a GSTIN, invalid GSTINs, the same HSN billed at different GST rates.
5. **GST 2B insights** — kar's GSTR-2B vs purchase-books matching summarised: mismatched bills, suppliers missing in 2B, documents missing in books, **ITC at risk**; an endpoint plus a dashboard KPI card (no LLM).
6. **Live dashboard KPIs** — net sales (after sale returns), bills, returns, GST, gross margin, payment mix and top items per store per day, kept in Redis with Postgres as the source of truth (spec Week 23 "Redis Analytics Dashboards"). Redis is used only when its version matches Postgres (see below).
7. **Weekly WhatsApp summary** — a short Hinglish business update for the owner every Monday via `soochna`.

| | |
|---|---|
| Port (local) | `7010` |
| Database | `maapan_buddhi` (+ Redis db 3, optional) |
| Publishes | — |
| Subscribes | `aadesh.invoice.created`, `aadesh.invoice.cancelled`, `aadesh.note.created` (sale returns reduce KPIs), `lenden.payment.captured`, `bhandar.stock.low` |
| Calls | `bahi` (reports summary), `bhandar` (stock summary, item lookup), `aadesh` (invoices), `pehchaan` (tenant/store), `kar` (GSTR-2B matching), `soochna` (WhatsApp), Claude API |
| Gateway prefix | `https://api.maapan.in/api/buddhi/...` |
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
| `POST /v1/scan/purchase-bill` | `ai:use` + `purchase:manage` | multipart `file` (JPEG/PNG/WebP/GIF/PDF ≤ 10 MB) → `{result: {draft, supplier, matches, confidence, warnings, ...}, usage}`. `draft` is the body for aadesh `POST /v1/invoices` once a supplier `party_id` is picked. Each `matches[]` entry: `item_id`, `matched_name`, `score`, `method` (`identifier` \| `name` \| `none`), `reasons`, `alternatives[]` |
| `POST /v1/scan/match-items` | `ai:use` + `purchase:manage` | `{store_id?, lines: [{name, hsn, unit, rate, gst_rate_percent, prices_include_tax}]}` → `{matches, warnings}`; re-match edited lines, no AI call |
| `POST /v1/ask` | `ai:use` | `{"question"}` → `{answer, usage}`. `?stream=true` → SSE events `delta` `{text}`, then `done` `{answer, usage}` or `error` |
| `GET /v1/forecast/reorder` | `ai:use` | `store_id` (default active store), `lead_days` → `{items: [{item_id, name, on_hand, reorder_level, forecast_daily_demand, days_of_cover, stockout_before_delivery, forecast_qty, reorder_rule_qty, suggested_qty, reason, preferred_supplier_id, unit_cost_paise, est_cost_paise}], by_supplier, est_cost_paise, stock_source, warnings}` |
| `GET /v1/insights/gst` | `ai:use` + `report:read` (not store-scoped roles) | `gstin` (default primary), `period` MMYYYY (default last month) → `{itc_at_risk_paise, missing_in_2b_tax_paise, mismatch_excess_paise, missing_in_books_tax_paise, itc_available/pending/rejected/not_available_paise, missing_in_2b_suppliers, missing_in_books_suppliers, mismatches, highlights, card, warnings}`; cached for the dashboard card |
| `GET /v1/insights/anomalies` | `ai:use` | `days` (30), `kind`, `store_id`, `include_resolved` → findings + high-cancellation cashiers |
| `POST /v1/insights/anomalies/{id}/resolve` | `ai:use` | mark handled |
| `GET /v1/dashboard` | `report:read` | `store_id` (`all` = business), `date` → today vs yesterday (net of returns; `returns`, `returns_paise`), last 7 days, top 10 items, payment mix, `cards` (business view, AI-pilot tenants: the latest `gst_2b` card), `source` |
| `GET /v1/ai/usage` | `ai:use` | tokens used this month vs budget, by feature/model |
| `GET /v1/ai/settings` / `PUT /v1/ai/settings` | `ai:use` / `settings:manage` | `{ai_enabled, monthly_token_budget, owner_phone}` |
| `POST /internal/v1/jobs/forecast` | service secret | nightly (Cloud Scheduler 02:00 IST) |
| `POST /internal/v1/jobs/weekly-summary` | service secret | Mondays 09:00 IST |
| `POST /internal/v1/jobs/gst-insights` | service secret | `period` (default last month); refreshes the 2B insight (primary GSTIN) for every AI-enabled tenant |
| `POST /events` | service secret / IAM | Pub/Sub push |
| `GET /healthz`, `GET /readyz` | none | probes |

Cashiers and store managers only ever see their own stores (dashboard, anomalies, re-order, and the ask tools).

## How the AI is used

| Feature | Model (env) | Effort | Notes |
|---|---|---|---|
| Bill scanning | `AI_MODEL_FAST` (default `claude-opus-5-5`) | medium | Vision + structured outputs (strict JSON schema). Every number is re-validated in Go (`internal/scan`) |
| Ask your books | `AI_MODEL_SMART` (default `claude-opus-5-5`) | medium | Streaming agent loop with 4 read-only tools, **max 6 tool calls**; tool inputs validated in Go; `*_paise` fields are annotated with exact `₹` strings so the model never does currency maths |
| Weekly summary | `AI_MODEL_SMART` | low | ≤ 600 characters, numbers pre-formatted |

- Server-side **refusal fallback** (`AI_REFUSAL_FALLBACK=true`, beta `server-side-fallback-2026-07-01`, `fallbacks: "default"`) is sent **only to models that accept it** (`claude-opus-5-5`, `claude-opus-5`, `claude-sonnet-5-5`, `claude-fable-5`/`-5-1`, `claude-mythos-5-1`; `llm.SupportsServerFallback`). Claude Haiku 5.5 has no server-side fallback, so with `AI_MODEL_FAST=claude-haiku-5-5` scans go out without the header and a decline surfaces as 422. A declined request on a supported model is re-served by a fallback model within the same call; if it is still declined the API returns 422 with the category.
- System prompts are static and prompt-cached; dates and scope go in the user message.
- Ask your books: the question is wrapped in `<user_question>` and each tool result in `<tool_data tool="…">`; the system prompt tells Claude that text inside is tenant/supplier-authored data and never instructions. JSON tool output has `<`/`>` escaped, and tag-like text is neutralised, so a party name cannot close the block.

### Cost controls

- **Per-tenant monthly token budget**: `AI_MONTHLY_TOKEN_BUDGET` (default 2M tokens), overridable per tenant via `PUT /v1/ai/settings`. Over budget → 429 until the 1st (IST).
- **Reserve before the call**: each AI call first holds an estimate (scan 40k, ask 80k, weekly 8k tokens) in `ai_reservations` under a per-tenant advisory lock; used + held + estimate must fit the budget or the call gets 429 *before* Claude is called. Settling logs the real usage and frees the hold in one transaction; holds of crashed calls expire after 15 min. Ask also passes its hold as `TokenBudget`: once spent, the agent loop stops calling tools and answers. `GET /v1/ai/usage` reports used tokens; held tokens are visible in `ai_reservations`.
- **Pilot flag**: with `AI_REQUIRE_TENANT_FLAG=true` only tenants with `ai_enabled` can call AI endpoints — including the non-LLM AI features (GST insights and their card, re-order suggestions, item matching).
- Every call (including failed ones) is logged in `ai_usage` with input/output/cache tokens; see `GET /v1/ai/usage`.
- Tool results are capped at 24 KB; the agent loop is capped at 6 tool calls; `max-instances=10` on Cloud Run caps concurrency.
- Bill scanning is the high-volume feature: once you have a labelled set of bills, measure `AI_MODEL_FAST=claude-haiku-5-5` against it before switching.
- Forecasting, re-order suggestions, item matching, GST insights, anomaly rules and dashboards use **no LLM at all**.
- Tests never call the Claude API: `llm.Fake` drives the handlers and a local `httptest` server stands in for the Messages endpoint.

### Dashboard: Redis is never trusted blindly

Every KPI change bumps `kpi_versions(tenant, store|all, day)` in the same transaction. The post-commit mirror is a Lua script that applies the delta only when Redis is exactly one version behind (otherwise it drops the keys), and records the version in the hash. `GET /v1/dashboard` uses Redis only when its version equals Postgres'; on a mismatch (a failed or reordered mirror, expired keys) it answers from one repeatable-read Postgres snapshot and rebuilds the Redis keys from it. `source` in the response says which was used.

## Tests

```bash
make test                                    # unit tests
TEST_DATABASE_URL=postgres://...?sslmode=disable go test ./...   # + Postgres tests (fresh schema each)
TEST_REDIS_URL=redis://localhost:6379/15 ...                     # + the Redis mirror test
```

## Code layout

```
cmd/server/main.go        wiring
internal/llm/             Client interface + Anthropic SDK implementation (streaming, tools, structured output); Fake for tests
internal/scan/            extraction schema, dated-rate validation, item-master matching, draft builder (unit-tested)
internal/ask/             system prompt, tools, input validation, ₹ annotation, delimiters  (unit-tested)
internal/forecast/        SES + day-of-week model, re-order rule, forecast+stock Suggest    (unit-tested)
internal/anomaly/         Welford stats, discount/cancellation/GSTIN rules (+ listing SQL) (unit-tested)
internal/gstinsight/      kar 2B reconciliation → mismatches, missing suppliers, ITC at risk, card
internal/kpi/             event ingestion (invoices, notes, payments) → Postgres rollups + versioned Redis mirror; dashboard
internal/jobs/            nightly forecast, re-order report, weekly summary
internal/store/           tenant AI settings, usage log, budget reservations
internal/clients/         calls to bahi, bhandar, aadesh, pehchaan, kar, soochna
internal/testdb/          fresh migrated schema on TEST_DATABASE_URL for tests
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
| `buddhi-gst-insights` | `0 6 * * *` | `POST /internal/v1/jobs/gst-insights` |

`KAR_URL` points at kar; buddhi needs `roles/run.invoker` on kar (and on bhandar for item lookup).

Rollout: deploy in **Week 12** with `AI_REQUIRE_TENANT_FLAG=true` and enable `ai_enabled` only for the 2–3 pilot stores; dashboards go live for everyone in **Week 23**.
