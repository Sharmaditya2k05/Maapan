# bahi (बही) — Ledger Service

"Bahi" is the traditional Indian account book. This service is Maapan's **double-entry accounting core**:

- **Chart of accounts** — Indian retail default (Cash, Bank, Cheques in Hand, Payment Gateway Clearing, Sundry
  Debtors/Creditors, Sales, Purchases, Cost of Goods Sold, Input/Output CGST·SGST·IGST·Cess, Stock, WIP, Round Off,
  Capital…) seeded per tenant
- **Parties** — customers/suppliers, each with its own ledger account; GSTIN validation (+ optional live verification via `kar`)
- **Vouchers** — sales, purchase, receipt, payment, journal, contra, credit/debit note, production, opening.
  Debits = credits is enforced in Go **and** by a deferred Postgres constraint trigger
- **Automatic posting** from events: invoices, returns, cancellations, online payments, refunds, gateway settlements,
  costed stock movements (perpetual inventory), production runs
- **Quarantine** — an event that cannot be posted as delivered is parked (and acknowledged) for review and replay,
  never dropped and never retried forever
- **Period lock** — nothing is posted on or before the locked date: manual entries get `409`, events are quarantined
- **Books** — party/account statements with running balance, cash book, bank book, day book
- **Bank** — deposits/withdrawals/charges/interest, reconciliation; **cheques** lifecycle (received → deposited → cleared/bounced, issued → cleared/bounced)
- **Reports** — trial balance, P&L (gross + net), balance sheet, receivables/payables aging (FIFO, 0-30/31-60/61-90/90+), cash-flow; all with `?format=csv`

| | |
|---|---|
| Port (local) | `7002` |
| Database | `maapan_bahi` |
| Publishes | — |
| Subscribes | `pehchaan.tenant.created`, `aadesh.invoice.created`, `aadesh.note.created`, `aadesh.invoice.cancelled`, `lenden.payment.captured`, `lenden.payment.refunded`, `lenden.settlement.processed`, `bhandar.stock.moved`, `nirmaan.production.completed` |
| Calls | `kar` (`POST /internal/v1/gstin/verify`, only for `?verify=true`) |
| Called by | `aadesh` (party lookup), `aayaat` (bulk import), `buddhi` (summary) |
| Gateway prefix | `https://api.maapan.in/api/bahi/...` |
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
# database-backed tests (fresh schema per test, dropped afterwards) run only when this is set:
TEST_DATABASE_URL='postgres://maapan:maapan@localhost:5432/maapan_test?sslmode=disable' go test ./...
```

The chart of accounts is seeded on `pehchaan.tenant.created`, and lazily on the first request/event
for a tenant, so a fresh tenant works even if that event was missed. Accounts added later
(`migrations/0002_phase1.up.sql`: 1115, 1130, 4150, 5150, 5160, 5290, 5590) are inserted for every
existing tenant by the migration, idempotently.

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
| `aadesh.invoice.created` sale | Sales `SV/` | Dr Cash (cash tenders) / Bank (card·UPI·bank tenders **without** a `payment_request_id`) / Party or *Walk-in Customers* (balance) · Cr Sales, Output CGST/SGST/IGST/Cess, Round Off |
| `aadesh.invoice.created` purchase | Purchase `PV/` | Dr Purchases, Input GST, Round Off · Cr Cash/Bank (paid) / Supplier or *Cash Suppliers* (balance) |
| `aadesh.note.created` sale_return | Credit note `CN/` | mirror of a sale, using *Sales Returns* |
| `aadesh.note.created` purchase_return | Debit note `DN/` | mirror of a purchase, using *Purchase Returns* |
| `aadesh.invoice.cancelled` | same type, reversal | swaps the original voucher; the **original** is marked `reversed` and points at the reversal (`reversed_by`), the reversal stays `posted` (`reversal_of`). Retried until the created event has posted |
| `lenden.payment.captured` | Receipt `RV/` | Dr *receipt account* (below) · Cr Party (from payment, else from the invoice's voucher, else Walk-in) |
| `lenden.payment.refunded` | Payment `PY/` | Dr Party · Cr the same receipt account. Keyed on `refund_id`, so every partial refund is its own voucher (`payment_id` only when `refund_id` is missing) |
| `lenden.settlement.processed` | Receipt `RV/` | Dr Bank (net) · Dr Bank Charges (fee) · Dr Input CGST + Input SGST (fee GST, odd paisa on CGST) · Cr *Payment Gateway Clearing* (gross − refunds). Must satisfy gross − refunds = net + fee + fee tax, else quarantined. Idempotent on `settlement_id` |
| `bhandar.stock.moved` | Journal `JV/` | perpetual-inventory reclassification, see below. `source_id` = `<source>/<source_id>` (+`/rev` for reversals) |
| `nirmaan.production.completed` | Production `MF/` | Dr Stock-FG (total cost) · Cr Stock-RM (consumed) · Cr WIP (overhead absorbed) |
| `pehchaan.tenant.created` | — | seeds the chart of accounts |

**Counter tenders vs. lenden captures.** A tender that carries a `payment_request_id` was collected through a
lenden QR / payment link; lenden's `payment.captured` books it (Dr Gateway Clearing, Cr Party), so the sale
voucher leaves that amount on the party instead of debiting Bank — otherwise the same rupees would be booked
twice. Tenders without a `payment_request_id` (cash drawer, the shop's own card terminal, a bank transfer
keyed in by hand) are booked on the sale as before. Excess cash tendered is change, not revenue.

**Receipt routing** (`posting.ReceiptAccount`): gateway `razorpay` / `cashfree` (any method) → *Payment Gateway
Clearing* 1130, which `lenden.settlement.processed` later empties into Bank net of MDR; gateway `manual` or empty →
by method: `cash` → Cash-in-hand 1100, `cheque` → *Cheques in Hand* 1115, `bank` / `neft` / `rtgs` / `imps` /
`netbanking` / `upi` / `card` → Bank 1110.

Every event is idempotent twice over: `processed_events` (event id) and a unique `(source, source_id)` on
vouchers (e.g. the invoice id). `Post` also takes a transaction-scoped advisory lock on
`(tenant, source, source_id)`, so two concurrent deliveries of the same document never race into a unique
violation — the second simply sees the first voucher.

### Perpetual inventory

bhandar costs every stock movement at weighted-average cost and publishes `bhandar.stock.moved`. bahi turns
that into a reclassification so the invoice vouchers stay exactly as above:

| Reason | Entries (value = Σ movement value, signed) |
|---|---|
| `purchase` | Dr Inventory · Cr Purchases |
| `purchase_return` | Dr Purchase Returns · Cr Inventory |
| `sale` | Dr *Cost of Goods Sold* 5150 · Cr Inventory |
| `sale_return` | Dr Inventory · Cr Cost of Goods Sold |
| `adjustment` / `expiry_writeoff`, qty < 0 | Dr *Stock Loss / Write-off* 5160 · Cr Inventory |
| `adjustment`, qty > 0 | Dr Inventory · Cr *Stock Gain* 4150 |
| `opening` | Dr Inventory · Cr Opening Balance Difference |
| `transfer_in` / `transfer_out` | nothing — both stores use the same Inventory account |
| `production_consume` / `production_output` | nothing — already posted from `nirmaan.production.completed` |

A `reversal: true` payload (cancelled invoice) is just the negative movement and follows the same rules, keyed
`<source>/<source_id>/rev`. Inventory is *Stock - Finished Goods* 1310 for every movement — bahi cannot tell raw
material from finished goods on a movement; the *Stock-in-hand* group total is what the balance sheet shows.

Consequences for the P&L: for stock-tracked items a purchase bill's debit to Purchases is cancelled by the
reclassification, and the cost reaches the P&L only when the goods are sold (or lost), so
**gross profit = Sales − Sales Returns − Cost of Goods Sold − stock losses**. Purchases of lines that are not
stock-tracked (no `item_id`, services, consumables) are never moved and remain an expense in the period
bought, as before. Cost of Goods Sold, Stock Loss and *Other Direct Expenses* 5290 sit under the *Direct
Expenses* 5200 group, which the gross-profit formula includes in full.

### Quarantine and retries

An event bahi cannot post is **quarantined**: in one transaction it is marked processed and recorded in
`quarantined_events` (event id, type, reason, the full envelope), logged at `ERROR`, and acknowledged. That
happens at once for permanent failures (malformed payload, invoice/settlement that does not balance, unknown
stock reason, a voucher dated inside the locked period — reason `period locked …`), and after the **3rd failed
delivery** for retryable ones (a party aadesh references that bahi has not seen yet, a cancellation delivered
before its invoice); attempts are counted in `event_attempts`. `GET /v1/quarantine` lists what is waiting;
`POST /v1/quarantine/{event_id}/retry` un-marks the event and re-runs the original handler on the stored
envelope, resolving the row on success (or answering `409` with the new reason).

Unknown parties are **not** re-pointed at Walk-in Customers / Cash Suppliers any more — that silently kept the
receivable off the party's ledger.

### Period lock

`PUT /v1/settings/period-lock {"locked_until":"YYYY-MM-DD"}` (`settings:manage`) locks the books up to and
including that date (an empty `locked_until` removes the lock; a future date is refused). Manual vouchers,
bank transactions, cheque clearances and reversals dated on or before it are rejected with
`409 period_locked`; event-driven vouchers are quarantined with reason `period locked`, to be retried after the
lock is moved. Opening balances are checked too, so unlock before importing into a closed year.

## API

All `/v1` routes need `Authorization: Bearer <token>`; via the gateway prefix them with `/api/bahi`.
Dates are `YYYY-MM-DD`; `from`/`to` default to the start of the current financial year … today.

| Method & path | Permission | Purpose |
|---|---|---|
| `GET /v1/accounts` | `ledger:read` | Flat list with balances; `?view=tree`, `?include_parties=true` |
| `POST /v1/accounts` | `ledger:write` | `{code,name,parent_code,is_group,is_bank,is_cash,bank_details}` |
| `PATCH /v1/accounts/{id}` · `DELETE /v1/accounts/{id}` | `ledger:write` | Rename/archive; delete only unused non-system accounts |
| `GET /v1/accounts/{id}/statement` | `ledger:read` | Running-balance ledger (groups include all children) |
| `GET/POST /v1/parties` | `party:manage` | List (`q`, `type`, paging) / create (`?verify=true` checks GSTIN via kar). `opening_as_of` defaults to 1 April of the current FY |
| `GET/PATCH/DELETE /v1/parties/{id}` | `party:manage` | Delete = archive |
| `GET /v1/parties/{id}/statement` | `ledger:read` | Party ledger |
| `GET /v1/vouchers` · `GET /v1/vouchers/{id}` | `ledger:read` | Filters: `type, source, q, from, to` |
| `POST /v1/vouchers` | `ledger:write` | Manual journal/receipt/payment/contra `{type,date,narration,reference,entries:[{account_id|party_id,debit_paise|credit_paise}]}`; `409 period_locked` inside the locked period |
| `POST /v1/vouchers/{id}/reverse` | `ledger:write` | Manual/opening vouchers only (`{reason,date}`); the original becomes `reversed` with `reversed_by`, the reversal is `posted` with `reversal_of` |
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
| `GET /v1/quarantine` | `ledger:read` | Unresolved quarantined events (`event_id, event_type, reason, payload, created_at`) |
| `POST /v1/quarantine/{event_id}/retry` | `ledger:write` | Replay the stored envelope through its handler; `200` resolved, `409` still failing |
| `GET /v1/settings/period-lock` | `ledger:read` | `{locked_until, locked_by, locked_at}` (`locked_until` empty = open) |
| `PUT /v1/settings/period-lock` | `settings:manage` | `{"locked_until":"YYYY-MM-DD"}`; `""` unlocks |
| `GET /internal/v1/parties/{id}` | service secret | Party for billing (ARCHITECTURE §6) + `credit_days` so aadesh can compute due dates and enforce credit |
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
internal/ledger/events.go      Pub/Sub handlers (handle = once per event id, classify, retry budget)
internal/ledger/quarantine.go  quarantined_events, event_attempts, list/retry endpoints
internal/ledger/periodlock.go  period_locks, the 409 check Post applies, settings endpoints
internal/ledger/integration_test.go  DB-backed tests (TEST_DATABASE_URL)
migrations/                    schema + balance trigger; 0002 = phase-1 accounts, reversed_by, quarantine, period lock
```

### Chart of accounts added in phase 1

| Code | Account | Group | Used by |
|---|---|---|---|
| 1115 | Cheques in Hand | Assets | manual `cheque` receipts |
| 1130 | Payment Gateway Clearing | Assets | gateway captures/refunds, emptied by settlements |
| 4150 | Stock Gain | Income | positive stock adjustments |
| 5150 | Cost of Goods Sold | Direct Expenses (group) | `stock.moved` sale / sale_return |
| 5160 | Stock Loss / Write-off | Direct Expenses (group) | shrinkage, expiry write-offs |
| 5290 | Other Direct Expenses | Direct Expenses (group) | manual direct costs (5200 is a group now) |
| 5590 | Other Indirect Expenses | Indirect Expenses (group) | manual overheads (5500 is a group now; its old entries moved here) |

## Deploy (Cloud Run)

`cloudbuild.yaml` tests, builds with `mool` as a build context, pushes to Artifact Registry and deploys:

- ingress **internal**, IAM auth required (reached by `dwaar`, `aadesh`, `aayaat`, `buddhi`, Pub/Sub)
- `min-instances=1` — aadesh reads parties while billing B2B customers
- env: `KAR_URL`; secrets: `bahi-database-url`, `jwt-secret`, `service-secret`
- Pub/Sub push subscriptions `bahi--<topic>` from `sanchalan/scripts/pubsub-setup.sh`. A dead-letter topic is
  still a good backstop, but bahi quarantines what it cannot post itself (see *Quarantine and retries*), so
  the dead-letter queue should stay empty in practice

```bash
gcloud builds submit --config cloudbuild.yaml --substitutions=_ENV=dev,SHORT_SHA=$(git rev-parse --short HEAD)
```

## Known gaps / next steps

- Changing a party's `type` does not move its account between Debtors and Creditors.
- No year-end closing voucher yet (P&L accumulates in the balance sheet); the period lock protects closed months.
- Bank-statement import (CSV/MT940) for auto-reconciliation.
- Multiple bank accounts work (`POST /v1/accounts` with `is_bank`), but settlements and manual online receipts
  always post to the default *Bank Account* (1110); `bank_account_last` on the settlement is not yet matched.
- `stock.moved` always uses *Stock - Finished Goods* 1310; raw-material purchases therefore show in 1310 while
  production credits 1320 — correct at the *Stock-in-hand* group level, not per sub-account.
- A reversed stock adjustment follows the sign rules (a reversed write-off books Stock Gain, not a credit to
  Stock Loss); bhandar does not reverse adjustments today.
