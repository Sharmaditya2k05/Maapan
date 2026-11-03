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
- **Bank** — deposits/withdrawals/charges/interest; **bank statement import** (CSV/XLSX, SBI/HDFC/ICICI/Axis/Kotak presets)
  with rule-based auto-matching and a real **BRS**; **cheque register** (Cheques in Hand → Bank, bounce reverses, PDC report)
- **Bill-wise** — against-reference / on-account allocation of receipts and payments, outstanding per bill, bill-wise
  party statement, aging by **due date**
- **TDS / TCS** — 194Q / 194C / 194J(a)(b) with FY thresholds per party and the no-PAN rate, 206C(1H) tracking,
  challans, 26Q-ready CSV
- **GST set-off** — month-end Rule 88A set-off journal and PMT-06 cash deposit from kar's GSTR-3B computation
- **Year-end close**, **Tally Prime XML export**, trigger-based **edit audit trail**
- **Reports** — trial balance, P&L (gross + net, per store), balance sheet, receivables/payables aging, cash-flow; all with `?format=csv`

| | |
|---|---|
| Port (local) | `7002` |
| Database | `maapan_bahi` |
| Publishes | — |
| Subscribes | `pehchaan.tenant.created`, `aadesh.invoice.created`, `aadesh.note.created`, `aadesh.invoice.cancelled`, `lenden.payment.captured`, `lenden.payment.refunded`, `lenden.settlement.processed`, `bhandar.stock.moved`, `nirmaan.production.completed`, `kar.return.filed` |
| Calls | `kar` (`POST /internal/v1/gstin/verify` for `?verify=true`; `GET /v1/returns/gstr3b` with the caller's token for `POST /v1/gst/setoff` without a computation) |
| Called by | `aadesh` (party lookup, credit check), `aayaat` (bulk import), `buddhi` (summary), `soochna` (overdue receivables) |
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
| `kar.return.filed` (GSTR-3B) | Journal `JV/` | set-off journal from the payload's utilisation + cash (paise), dated the filing date; skipped if the period was already set off by hand |

**TCS on invoices.** `tcs_paise` on an invoice (part of its grand total) is credited to *TCS Payable* 2225 on a sale and
debited to *TCS Receivable* 1425 on a purchase (returns mirror it). Before phase 4 such invoices did not balance and
were quarantined.

**Bills.** Every sales, purchase and opening voucher raises a **bill** per party (due date = aadesh's `due_date`, else
voucher date + party `credit_days`). A `lenden.payment.captured` with an `invoice_id`, and a credit/debit note with a
`ref_invoice_id`, are allocated against that invoice's bill automatically (up to its outstanding; the rest stays on
account). Reversing a voucher cancels its bill and frees every allocation to or from it.

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

### Bill-wise accounting

Party ledger balance = Σ bill outstanding + unallocated movements. `PUT /v1/vouchers/{id}/allocations` sets a settling
voucher's against-reference allocations (the rest is on account); `POST /v1/vouchers` and `POST /v1/cheques` accept
the same `allocations` inline; `POST /v1/parties/{id}/allocations/auto` settles open bills FIFO by due date with
unallocated receipts/payments. Allocating more than a bill's outstanding or a voucher's unallocated amount is `422`.
Aging (`basis=due`, default) buckets each bill's outstanding by days **past its due date** (not due / 0-30 / 31-60 /
61-90 / 90+) and shows on-account money separately (`net` = ledger balance); `basis=date` keeps the phase-1 FIFO report.

### Cheque register

| Step | Voucher |
|---|---|
| received (cheque date reached) | receipt Dr *Cheques in Hand* / Cr party |
| received, post-dated | nothing until deposited on/after its date (earlier deposit is `409`) |
| deposited → cleared | receipt Dr Bank / Cr *Cheques in Hand* (`source=cheque.clear`) |
| deposited → bounced, received → cancelled | reversal of the receipt (Dr party / Cr Cheques in Hand), `bounce_reason` kept |
| issued (cheque date reached) | payment Dr party / Cr Bank (BRS then shows it unpresented until the bank debits it) |
| issued, post-dated | payment booked when it clears |
| issued → bounced / cancelled | reversal of the payment |

Cheques recorded before phase 4 have no party voucher; they keep the old behaviour (Dr Bank / Cr party on clearing).
`GET /v1/cheques/pdc` buckets pending cheques by cheque date: `overdue` (date reached, not presented), `due_soon`
(within `days`, default 7), `later`, `awaiting_clearance` (deposited).

### Bank statement import and BRS

`POST /v1/bank/statements/import` takes multipart (`file`, `bank_account_id`, `preset`, optional `mapping` JSON) or JSON
(`content_base64`). CSV (comma/semicolon/tab) and `.xlsx` (first sheet; Excel serial dates) are read; `.xls` is
refused. Presets `sbi`, `hdfc`, `icici`, `axis`, `kotak`, `generic`; the header row is found among the first 60 rows,
preamble and footer rows are skipped, and a `mapping` (`date, value_date, description, reference, debit, credit,
amount, drcr, balance, date_format`) overrides any column. Re-importing the same file adds nothing (line hash).
Auto-match (after import, or `POST /v1/bank/auto-match`) pairs a line with an unreconciled posting on that bank account
of the same amount within −3/+10 days: first when a reference matches (cheque no. with leading zeros ignored, UTR,
voucher number, bank-transaction reference — also inside the narration), then when the amount+date pairing is unique
both ways; anything ambiguous stays for manual `match`/`unmatch`/`ignore`. `POST …/{id}/voucher` books an unmatched
line (charges, interest, a direct deposit from a party) and matches it.

**BRS** (`GET /v1/reports/brs?bank_account_id=&as_of=[&from=]`): balance per books, + cheques issued not presented,
− deposits not credited, + bank credits / − bank debits not in the books = computed bank balance, compared with the
last imported running balance on or before `as_of` (`difference_paise` 0 = reconciled). A posting is cleared as of a
date when its matched line is dated on/before it (or a legacy `bank_transactions.reconciled_at` is). Pass `from` to
treat postings older than your first imported statement as cleared.

### TDS / TCS

| Section | Rate | Threshold (per party, per FY) | No PAN |
|---|---|---|---|
| 194Q purchase of goods | 0.1 % | only the part of purchases above ₹50 lakh | 5 % |
| 194C contractors | 1 % individual/HUF, 2 % others | single > ₹30,000 or aggregate > ₹1,00,000 (catch-up on crossing) | 20 % |
| 194J(a) technical / 194J(b) professional (`194J` = b) | 2 % / 10 % | aggregate > ₹50,000 (catch-up) | 20 % |
| 206C(1H) TCS on sale of goods | 0.1 % | receipts above ₹50 lakh | 1 % |

Parties carry `pan`, `tds_section`, `deductee_type` (else from the PAN's 4th letter) and `lower_tds_rate_bp` (Sec 197).
Every bill/payment tested is recorded in `tds_entries`, so the FY aggregate is the sum of recorded bases; TDS is rounded
to the rupee. A deduction posts Dr party / Cr *TDS Payable* 2220 (and is allocated against the bill named in
`ref_voucher_id`); TDS a customer deducted from us (`direction: receivable`) posts Dr *TDS Receivable* 1420 / Cr party.
`POST /v1/vouchers` takes `tds: {party_id, section, base_paise}` to add the deduction to a payment. With
`PUT /v1/settings/tds {"auto_194q":true}`, purchase bills from suppliers tagged 194Q are deducted automatically on the
taxable value. A challan (`POST /v1/tds/challans`, by `entry_ids` or `month`) posts Dr TDS/TCS Payable (+ Interest on
Loans for `interest_paise`) / Cr Bank and links the entries; `GET /v1/tds/26q?fy=&quarter=&format=csv` is the 26Q
deductee annexure (section code, deductee code, PAN or PANNOTAVBL, amounts, rate, challan BSR/date/serial, reason `C`
for the higher rate). 206C(1H) was omitted from 1 April 2025 (Finance Act 2025): bahi still books TCS an invoice
carries and `GET /v1/tcs/206c1h` tracks buyers against the threshold, but proposes no collection after that date.
Section codes are the 1961-Act numbers the RPU/TRACES use; the Income-tax Act 2025 renumbers them from 1 April 2026.

### GST set-off

`POST /v1/gst/setoff {gstin, period:"MMYYYY", date?, computation?, pay_from_bank_account_id?}` (`ledger:write` +
`gst:file`). `computation` is kar's GSTR-3B `computation` block as kar returns it (rupees); when omitted bahi fetches
`GET /v1/returns/gstr3b?period=&gstin=` from kar with the caller's token. Posted:

| Voucher | Entries |
|---|---|
| PMT-06 deposit (with `pay_from_bank_account_id`) | Dr *Electronic Cash Ledger* IGST/CGST/SGST/Cess (1451-1454) · Cr Bank |
| Set-off journal (`source=gst.setoff`, key `GSTIN/period`) | per utilisation Dr Output ‹liability head› / Cr Input ‹credit head›; cash part Dr Output / Cr Electronic Cash Ledger |

Utilisation breaking Sec 49/49A (CGST↔SGST, cess paying other heads) is `422`; a second set-off for the period is
`409`. Warnings flag a set-off larger than the book liability or ITC. The Input GST accounts are the books' electronic
credit ledger (unutilised ITC stays there). Reverse-charge tax (cash only) is reported, not posted.

### Year-end close

`POST /v1/year-end/close {"fy":"25-26", "close_drawings":true, "lock":true}` (`settings:manage`, FY must have ended):
one journal dated 31 March zeroes every income/expense account (and Drawings) and carries the net to *Capital* 3100;
the period lock moves to 31 March. The P&L report leaves closing vouchers out (a closed year still shows its profit);
trial balance and balance sheet include them. Balance-sheet accounts need no carry-forward (statements open with all
earlier entries). `POST /v1/year-end/{fy}/reopen` reverses it (unlock first); closing twice is `409`.

### Tally export and audit trail

`GET /v1/export/tally?from=&to=&company=` returns a Tally Prime "Import Data" XML: ledgers used in the period (mapped to
Tally's primary groups, opening balance at `from`, party GSTIN/PAN/address, bill-wise on with credit period) followed by
every voucher (debits negative, `ISDEEMEDPOSITIVE`), with bill allocations as New Ref / Agst Ref / On Account.

Every insert/update/delete of accounts, parties, vouchers, cheques, period lock, bill allocations, bank statements,
TDS entries/challans/settings and GST set-offs, and every update/delete of voucher entries, bills, statement lines and
bank transactions, is written to `audit_log` by a Postgres trigger with before/after images and the actor
(`bahi.actor`, set per transaction). `GET /v1/audit?entity=&entity_id=&actor=&action=&from=&to=` reads it.

### Import support (aayaat)

All `/internal/v1/bulk/*` calls answer `{created, updated, errors:[{index, message}], results:[{index, id, created}]}`;
each record runs in its own transaction, so one bad row never sinks the batch, and `results` tells an importer which
records this call created (rollback archives only those).

| Endpoint | What is stored / posted | Key (re-delivery = no-op) |
|---|---|---|
| `bulk/parties` | party + ledger (`created_via='import'`); opening voucher `party:<id>` when `opening_balance_paise` > 0 | `external_ref` |
| `bulk/opening-balances` | Opening voucher Dr/Cr target · Cr/Dr *Opening Balance Difference* 3900, dated `as_of` | `bulk:<as_of>:acct:<code>` / `…:party:<id>` |
| `bulk/open-bills` | per bill: opening voucher Dr/Cr party · Cr/Dr 3900 dated `as_of` (exactly what a party-totals line posts, so the party balance is the same either way), raising one bill with the original `bill_no`, `bill_date`, `due_date` (else bill date + party credit days) and `bill_amount_paise` → due-date aging and outstanding match bill by bill. Refused when the party already has a party-level opening (party master or party totals): that would count the outstanding twice | `bill:<external_ref>` |
| `bulk/invoice-history` | dated before `as_of`: an `invoice_history` row only (shown in `GET /v1/invoice-history` and as `history` in the party statement; the opening balances already hold its effect). On/after `as_of`: posted like aadesh's invoice/note (sales `SV`, purchase `PV`, credit note `CN`, debit note `DN`; a bill with `due_date`), period lock respected; a cash bill (no party) is settled to Cash-in-hand. Taxable + taxes + round off must equal the total. No stock is ever moved | `external_ref` (voucher `source=import`) |
| `bulk/accounts` | ledger or group under `parent_code`, or under `group` by name — a bahi group, else a Tally group (*Duties & Taxes* → 2200, *Indirect Expenses* → 5500, *Bank Accounts* → Assets + `is_bank`, *Capital Account* → Capital & Equity …). Code generated after the parent's highest numeric child when not given | `external_ref`, then `code`, then name |

**Rollback** (`kind`: `parties`, `opening_balances`, `open_bills`, `invoices`, `accounts`): imported vouchers are
**reversed** on their own date (source `import.rollback`, the original becomes `reversed`, its bill cancelled) and
their key is retired so the record can be imported again; history rows are deleted; a party is archived (its opening
reversed, `external_ref` released) only if the import created it (`created: true`, or unknown and `created_via='import'`)
and it has no other vouchers, imported invoices or cheques; an imported account is deleted only when nothing was posted
to it. A record that was only *updated* by the import (`created: false`) is left as it is. Refusals are per record:
bill settled since the import, voucher allocated against bills, TDS entries, the locked period (`books are locked up
to …`), party activity. Records already undone, or never imported, succeed. Item kinds belong to bhandar (`422`).

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
| `GET /v1/reports/trial-balance` | `report:read` | `?as_of=` |
| `GET /v1/reports/profit-loss` | `report:read` | `?from=&to=` |
| `GET /v1/reports/balance-sheet` | `report:read` | `?as_of=` |
| `GET /v1/reports/cash-flow` | `report:read` | Cash + bank in/out by voucher type |
| `GET /v1/reports/summary` | `report:read` | Sales, purchases, receivables, payables, cash, bank |
| `GET /v1/quarantine` | `ledger:read` | Unresolved quarantined events (`event_id, event_type, reason, payload, created_at`) |
| `POST /v1/quarantine/{event_id}/retry` | `ledger:write` | Replay the stored envelope through its handler; `200` resolved, `409` still failing |
| `GET /v1/settings/period-lock` | `ledger:read` | `{locked_until, locked_by, locked_at}` (`locked_until` empty = open) |
| `PUT /v1/settings/period-lock` | `settings:manage` | `{"locked_until":"YYYY-MM-DD"}`; `""` unlocks |
| `GET /v1/parties/{id}/bills` | `ledger:read` | Bills with allocated/outstanding/overdue days; `?status=all`, `?as_of=`, `?format=csv` |
| `GET /v1/parties/{id}/statement?view=bills` | `ledger:read` | Bill-wise statement: bills with their settlements, on-account vouchers, balance, overdue |
| `POST /v1/parties/{id}/allocations/auto` | `ledger:write` | FIFO-by-due-date allocation of unallocated receipts/payments |
| `GET/PUT /v1/vouchers/{id}/allocations` | read / `ledger:write` | `{allocations:[{bill_id,amount_paise}]}` replaces the voucher's against-reference allocations |
| `GET /v1/reports/aging` | `report:read` | `?kind=receivables|payables&as_of=&basis=due|date` (due-date buckets + on account by default) |
| `GET /v1/reports/profit-loss?store_id=` · `GET /v1/reports/profit-loss/stores` | `report:read` | One store's P&L · P&L per store |
| `GET /v1/cashbook?store_id=` · `GET /v1/bankbook?store_id=` | `ledger:read` | Store-wise books |
| `GET /v1/cheques?status=pending` · `GET /v1/cheques/pdc` | `ledger:read` | Register (`?format=csv`) · PDC due-date report (`?as_of=&days=&direction=`) |
| `POST /v1/cheques/{id}/status` | `ledger:write` | `{status: deposited|cleared|bounced|cancelled, date, reason}` |
| `GET /v1/bank/statement-presets` | `ledger:read` | Column presets |
| `POST /v1/bank/statements/import` · `GET /v1/bank/statements` | `ledger:write` / read | Import (auto-matches) · imports with line/duplicate/matched counts |
| `GET /v1/bank/statement-lines` | `ledger:read` | `?bank_account_id=&status=unmatched|matched|ignored&statement_id=&from=&to=` |
| `POST /v1/bank/auto-match` | `ledger:write` | `{bank_account_id, from, to, before_days, after_days}` |
| `POST /v1/bank/statement-lines/{id}/match` · `/unmatch` · `/ignore` | `ledger:write` | Manual match `{entry_id|voucher_id}` (amounts must agree) |
| `POST /v1/bank/statement-lines/{id}/voucher` | `ledger:write` | `{account_id|party_id, narration, allocations}` book the line and match it |
| `GET /v1/reports/brs` | `report:read` | `?bank_account_id=&as_of=&from=` bank reconciliation statement (`?format=csv`) |
| `GET/PUT /v1/settings/tds` | read / `settings:manage` | `{tan, deductor_name, auto_194q}` |
| `GET /v1/tds/sections` · `POST /v1/tds/compute` | `ledger:read` | Sections · preview `{party_id, section?, base_paise, date}` with the FY history |
| `POST /v1/tds/deductions` | `ledger:write` | `{party_id, section?, base_paise, date, direction: payable|receivable, tds_paise?, ref_voucher_id?, key?}` |
| `GET /v1/tds/entries` · `GET /v1/tds/summary` | read | `?fy=&section=&kind=&direction=&party_id=&open=true` · by section, deposited vs pending |
| `POST/GET /v1/tds/challans` | `ledger:write` / read | `{kind, bsr_code, challan_no, paid_on, bank_account_id, entry_ids | month, section?, interest_paise}` |
| `GET /v1/tds/26q` | `report:read` | `?fy=26-27&quarter=1..4&format=csv` |
| `GET /v1/tcs/206c1h` | `report:read` | `?fy=` buyers vs the ₹50 lakh threshold, TCS booked |
| `POST /v1/gst/setoff` · `GET /v1/gst/setoffs` | `ledger:write`+`gst:file` / read | Set-off journal (+ PMT-06 deposit) · periods set off |
| `POST /v1/year-end/close` · `GET /v1/year-end` · `POST /v1/year-end/{fy}/reopen` | `settings:manage` / read | Year-end close |
| `GET /v1/export/tally` | `report:read` | `?from=&to=&company=` Tally Prime XML |
| `GET /v1/audit` | `ledger:read` | Edit audit trail |
| `GET /internal/v1/parties/{id}/credit` | service secret | `{credit_limit_paise, credit_days, balance_paise, overdue_paise, available_paise (−1 = no limit), over_limit}` |
| `GET /internal/v1/receivables/overdue` | service secret | `?as_of=` → `{rows:[{party_id, party_name, phone, email, overdue_paise, oldest_due_date, days_overdue}]}`: customers with bill-wise outstanding past due (for soochna reminders) |
| `GET /internal/v1/parties/{id}` | service secret | Party for billing (ARCHITECTURE §6) + `credit_days` so aadesh can compute due dates and enforce credit |
| `POST /internal/v1/bulk/parties` | service secret | Upsert by `external_ref` (+ `pan`, `shipping_address`); a re-import also updates email, credit limit and days; optional opening balance (never re-posted) |
| `POST /internal/v1/bulk/opening-balances` | service secret | One opening voucher per line (key `as_of` + account / party, optional `external_ref` kept on the voucher); re-runs are no-ops |
| `POST /internal/v1/bulk/open-bills` | service secret | `{as_of, bills:[{party_external_ref, party_name, bill_no, bill_date, due_date, bill_amount_paise, amount_paise, type: dr\|cr, external_ref}]}` — one opening voucher + bill per row, idempotent by `external_ref` (see *Import support*) |
| `POST /internal/v1/bulk/invoice-history` | service secret | `{as_of, invoices:[{doc_type, number, date, party_external_ref, …, total_paise, external_ref}]}` — before `as_of` history only, on/after posted (source `import`); never moves stock |
| `POST /internal/v1/bulk/accounts` | service secret | `{accounts:[{code?, name, parent_code \| group, is_group, is_bank, is_cash, external_ref?}]}` — ledgers/groups under the chart (Tally group names accepted); idempotent by external_ref, code, name |
| `POST /internal/v1/bulk/rollback` | service secret | `{kind, job_id, as_of, records:[{external_ref, account_code, party_external_ref, created}]}` — undo what an import created; per-record refusal reasons in `errors` |
| `GET /v1/invoice-history` | `ledger:read` | Imported invoices `?party_id=&doc_type=&status=history\|posted&from=&to=&format=csv` (`from` defaults to 2000-01-01) |
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
internal/ledger/bills.go       bill-wise allocation, bill-wise statement, credit check
internal/ledger/aging.go       due-date aging, overdue receivables feed
internal/ledger/cheques.go     cheque register, PDC report
internal/ledger/bankrec.go     statement import, matching, BRS
internal/ledger/tds.go         TDS/TCS deductions, challans, 26Q, 206C(1H)
internal/ledger/gstsetoff.go   GSTR-3B set-off (+ kar.return.filed)
internal/ledger/yearend.go     year-end close / reopen
internal/ledger/tally.go       Tally Prime XML export
internal/ledger/audit.go       audit trail reader
internal/ledger/imports.go     aayaat import support: open bills, invoice history, accounts, rollback
internal/posting/tds.go, setoff.go, bankmatch.go   pure TDS rules, set-off voucher, auto-match
internal/bankstmt/             CSV/XLSX statement parser with bank presets
migrations/                    schema + balance trigger; 0002 = phase-1 accounts, reversed_by, quarantine, period lock;
                               0003 = phase-4 accounts, bills, bank statements, cheque columns, TDS, GST set-off, audit trail;
                               0004 = import support (party shipping address / origin, voucher external_ref, invoice_history)
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

### Chart of accounts added in phase 4

`migrations/0003_phase4.up.sql` adds these to every existing tenant (idempotent); new tenants get them from the seed.

| Code | Account | Group |
|---|---|---|
| 1250 | Advance to Suppliers | Assets |
| 1420 / 1425 | TDS Receivable / TCS Receivable | Duties & Taxes (Input) |
| 1450 (group) 1451-1454 | GST Electronic Cash Ledger — IGST / CGST / SGST / Cess | Assets |
| 1500 (group) 1510-1540, 1590 | Fixed Assets: Furniture & Fixtures, Plant & Machinery, Computers & Office Equipment, Vehicles; Accumulated Depreciation | Assets |
| 2150 | Advance from Customers | Liabilities |
| 2220 / 2225 | TDS Payable / TCS Payable | Duties & Taxes (Output) |
| 2400 (group) 2410, 2420 | Loans: Secured, Unsecured | Liabilities |
| 3200 | Drawings | Capital & Equity |
| 4200 | Discount Received | Income |
| 5210 | Freight Inward | Direct Expenses |
| 5520-5570 | Salaries & Wages, Rent, Electricity, Depreciation, Freight Outward, Interest on Loans | Indirect Expenses |

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
- Bank statements: MT940 and old `.xls` are not read; matching is one line ↔ one posting (no split/batch deposits).
- TDS: purchase returns do not reverse an auto-194Q deduction; no Form 16A, 27Q (non-residents), 27EQ (TCS) export;
  interest/late fee under 201(1A)/234E is not computed (enter `interest_paise` on the challan).
- GST set-off: reverse-charge cash, interest and late fee are not posted; a set-off cannot be reopened (post a
  correcting journal).
- Cheque printing layout and bounce charges are not implemented.
- Multiple bank accounts work (`POST /v1/accounts` with `is_bank`), but settlements and manual online receipts
  always post to the default *Bank Account* (1110); `bank_account_last` on the settlement is not yet matched.
- `stock.moved` always uses *Stock - Finished Goods* 1310; raw-material purchases therefore show in 1310 while
  production credits 1320 — correct at the *Stock-in-hand* group level, not per sub-account.
- A reversed stock adjustment follows the sign rules (a reversed write-off books Stock Gain, not a credit to
  Stock Loss); bhandar does not reverse adjustments today.
