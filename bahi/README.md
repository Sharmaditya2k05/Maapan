# bahi (बही) — Ledger Service

"Bahi" is the traditional Indian account book. This service is Taraju's **double-entry accounting core**:

- **Chart of accounts** — Indian retail default (Cash, Bank, Sundry Debtors/Creditors, Sales, Purchases,
  Input/Output CGST·SGST·IGST·Cess, Stock, WIP, Round Off, Capital…) seeded per tenant
- **Parties** — customers/suppliers, each with its own ledger account; GSTIN validation (+ optional live verification via `kar`)
- **Vouchers** — sales, purchase, receipt, payment, journal, contra, credit/debit note, production, opening.
  Debits = credits is enforced in Go **and** by a deferred Postgres constraint trigger
- **Automatic posting** from events: invoices, returns, cancellations, online payments, refunds, production runs
- **Books** — party/account statements with running balance, cash book, bank book, day book
- **Bank** — deposits/withdrawals/charges/interest, reconciliation; **cheques** lifecycle (received → deposited → cleared/bounced, issued → cleared/bounced)
- **Reports** — trial balance, P&L (gross + net), balance sheet, receivables/payables aging (FIFO, 0-30/31-60/61-90/90+), cash-flow; all with `?format=csv`

| | |
|---|---|
| Port (local) | `7002` |
| Database | `taraju_bahi` |
| Publishes | — |
| Subscribes | `pehchaan.tenant.created`, `aadesh.invoice.created`, `aadesh.note.created`, `aadesh.invoice.cancelled`, `lenden.payment.captured`, `lenden.payment.refunded`, `nirmaan.production.completed` |
| Calls | `kar` (`POST /internal/v1/gstin/verify`, only for `?verify=true`) |
| Called by | `aadesh` (party lookup), `aayaat` (bulk import), `buddhi` (summary) |
| Gateway prefix | `https://api.taraju.in/api/bahi/...` |
| First deployed | **Week 4** (accounts master & parties) → Week 6 (purchase posting) → **Week 7** (cashbook & banking) → **Week 8** (reports) |

## Run locally

Prerequisites: Go 1.25+, Docker (Postgres), the `mool` repo checked out next to this one.

```bash
# 1. start Postgres + Redis (once, from the sanchalan repo)
cd ../sanchalan && docker compose up -d postgres redis && cd ../bahi

# 2. database + env
make db-create
cp .env.example .env

# 3. run (migrations apply automatically)
make run                    # → http://localhost:7002/healthz

make test                   # posting-rule and aging unit tests
```

The chart of accounts is seeded on `pehchaan.tenant.created`, and lazily on the first request/event
for a tenant, so a fresh tenant works even if that event was missed.

### Try it

```bash
TOKEN=...   # access token from pehchaan

# customer with opening balance (₹500 receivable)
curl -s localhost:7002/v1/parties -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{
  "name":"Ramesh Traders","type":"customer","gstin":"27AAPFU0939F1ZV","phone":"9876543210",
  "opening_balance_paise":50000,"opening_type":"dr","opening_as_of":"2026-04-01"}'

curl -s "localhost:7002/v1/reports/trial-balance" -H "Authorization: Bearer $TOKEN"
curl -s "localhost:7002/v1/reports/aging?kind=receivables&format=csv" -H "Authorization: Bearer $TOKEN"
```

## Posting rules (events → vouchers)

| Event | Voucher | Entries |
|---|---|---|
| `aadesh.invoice.created` sale | Sales `SV/` | Dr Cash (cash tenders) / Bank (card·UPI·bank) / Party or *Walk-in Customers* (balance) · Cr Sales, Output CGST/SGST/IGST/Cess, Round Off |
| `aadesh.invoice.created` purchase | Purchase `PV/` | Dr Purchases, Input GST, Round Off · Cr Cash/Bank (paid) / Supplier or *Cash Suppliers* (balance) |
| `aadesh.note.created` sale_return | Credit note `CN/` | mirror of a sale, using *Sales Returns* |
| `aadesh.note.created` purchase_return | Debit note `DN/` | mirror of a purchase, using *Purchase Returns* |
| `aadesh.invoice.cancelled` | same type, reversal | swaps the original voucher; original marked `reversed` (retried until the created event has posted) |
| `lenden.payment.captured` | Receipt `RV/` | Dr Bank · Cr Party (from payment, else from the invoice's voucher, else Walk-in) |
| `lenden.payment.refunded` | Payment `PY/` | Dr Party · Cr Bank |
| `nirmaan.production.completed` | Production `MF/` | Dr Stock-FG (total cost) · Cr Stock-RM (consumed) · Cr WIP (overhead absorbed) |
| `pehchaan.tenant.created` | — | seeds the chart of accounts |

Excess cash tendered is change, not revenue. Every event is idempotent twice over: `processed_events`
(event id) and a unique `(source, source_id)` on vouchers (e.g. the invoice id).

## API

All `/v1` routes need `Authorization: Bearer <token>`; via the gateway prefix them with `/api/bahi`.
Dates are `YYYY-MM-DD`; `from`/`to` default to the start of the current financial year … today.

| Method & path | Permission | Purpose |
|---|---|---|
| `GET /v1/accounts` | `ledger:read` | Flat list with balances; `?view=tree`, `?include_parties=true` |
| `POST /v1/accounts` | `ledger:write` | `{code,name,parent_code,is_group,is_bank,is_cash,bank_details}` |
| `PATCH /v1/accounts/{id}` · `DELETE /v1/accounts/{id}` | `ledger:write` | Rename/archive; delete only unused non-system accounts |
| `GET /v1/accounts/{id}/statement` | `ledger:read` | Running-balance ledger (groups include all children) |
| `GET/POST /v1/parties` | `party:manage` | List (`q`, `type`, paging) / create (`?verify=true` checks GSTIN via kar) |
| `GET/PATCH/DELETE /v1/parties/{id}` | `party:manage` | Delete = archive |
| `GET /v1/parties/{id}/statement` | `ledger:read` | Party ledger |
| `GET /v1/vouchers` · `GET /v1/vouchers/{id}` | `ledger:read` | Filters: `type, source, q, from, to` |
| `POST /v1/vouchers` | `ledger:write` | Manual journal/receipt/payment/contra `{type,date,narration,reference,entries:[{account_id|party_id,debit_paise|credit_paise}]}` |
| `POST /v1/vouchers/{id}/reverse` | `ledger:write` | Manual/opening vouchers only (`{reason,date}`) |
| `GET /v1/cashbook` · `GET /v1/bankbook` | `ledger:read` | Optional `?account_id=` |
| `GET /v1/daybook` | `ledger:read` | Vouchers with entries (defaults to today) |
| `GET/POST /v1/bank/transactions` | read / `ledger:write` | deposit · withdrawal · charges · interest |
| `POST /v1/bank/transactions/{id}/reconcile` | `ledger:write` | `{cleared_on}` |
| `GET/POST /v1/cheques` | read / `payment:collect` | Record received/issued cheques |
| `POST /v1/cheques/{id}/status` | `ledger:write` | `{status,date}` — posts receipt/payment on `cleared` |
| `GET /v1/reports/trial-balance` | `report:read` | `?as_of=` |
| `GET /v1/reports/profit-loss` | `report:read` | `?from=&to=` |
| `GET /v1/reports/balance-sheet` | `report:read` | `?as_of=` |
| `GET /v1/reports/aging` | `report:read` | `?kind=receivables|payables&as_of=` |
| `GET /v1/reports/cash-flow` | `report:read` | Cash + bank in/out by voucher type |
| `GET /v1/reports/summary` | `report:read` | Sales, purchases, receivables, payables, cash, bank |
| `GET /internal/v1/parties/{id}` | service secret | Party for billing (ARCHITECTURE §6) |
| `POST /internal/v1/bulk/parties` | service secret | Upsert by `external_ref`, optional opening balance |
| `POST /internal/v1/bulk/opening-balances` | service secret | One opening voucher per line; re-runs are no-ops |
| `GET /internal/v1/reports/summary` | service secret | For buddhi dashboards |
| `POST /events` | service secret / IAM | Pub/Sub push endpoint |

## Code layout

```
cmd/server/main.go             wiring
internal/posting/              PURE rules: chart, event→voucher, aging (unit-tested)
internal/ledger/service.go     Post / Reverse / EnsureChart (the only way rows get into vouchers)
internal/ledger/accounts.go    chart of accounts
internal/ledger/parties.go     parties, internal party, bulk import
internal/ledger/vouchers.go    manual vouchers, list, reverse
internal/ledger/statements.go  statements, cash/bank/day books
internal/ledger/bank.go        bank transactions, cheques
internal/ledger/reports.go     TB, P&L, BS, aging, cash-flow, summary
internal/ledger/events.go      Pub/Sub handlers
migrations/                    schema + balance trigger
```

## Deploy (Cloud Run)

`cloudbuild.yaml` tests, builds with `mool` as a build context, pushes to Artifact Registry and deploys:

- ingress **internal**, IAM auth required (reached by `dwaar`, `aadesh`, `aayaat`, `buddhi`, Pub/Sub)
- `min-instances=1` — aadesh reads parties while billing B2B customers
- env: `KAR_URL`; secrets: `bahi-database-url`, `jwt-secret`, `service-secret`
- Pub/Sub push subscriptions `bahi--<topic>` from `sanchalan/scripts/pubsub-setup.sh`; give them a
  dead-letter topic — a voucher that fails validation is retried until it lands there

```bash
gcloud builds submit --config cloudbuild.yaml --substitutions=_ENV=dev,SHORT_SHA=$(git rev-parse --short HEAD)
```

## Known gaps / next steps

- Changing a party's `type` does not move its account between Debtors and Creditors.
- No period lock / year-end closing voucher yet (P&L accumulates in the balance sheet).
- Bank-statement import (CSV/MT940) for auto-reconciliation.
- Multiple bank accounts work (`POST /v1/accounts` with `is_bank`), but online payments always post to the default *Bank Account* (1110).
