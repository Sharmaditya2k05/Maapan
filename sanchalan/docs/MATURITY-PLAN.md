# Maapan — per-model maturity plan (proposal for approval)

Basis: static read of `kinbox-ctrl/maapan` at commit `3b4cd74` ("Replace Vite demo with the Maapan ERP monorepo"), 23 Oct 2026.
Every `.go`, `.sql`, `.ts` and README was read; the architecture contract, deployment plan and the
"External Integrations, Supabase & Cloud Run Setup" PDF were read. **Nothing was compiled or run** — Go is
not installed on this machine — so everything below comes from reading code, and a few items are marked
"re-verify with tests" where execution could change the answer.

GST rules checked against October-2026 reality: slabs are 0 / 5 / 18 / 40 % since 22 Sep 2025 (12 % and 28 %
abolished); e-invoice threshold is still ₹5 crore AATO; GSTR-3B outward liability is hard-locked from the
July-2025 period (corrections only via GSTR-1A); IMS accept/reject drives ITC; MFA is mandatory on the
e-invoice / e-way portals from 1 Apr 2026; e-way bills must be generated within 180 days of the invoice.

---

## 0. The picture in one table

| Model | Role | Go/TS lines | State today (verified) | Decision |
|---|---|---:|---|---|
| mool | shared lib: GST maths, money, events, auth | 2,517 | **Production-grade for its scope.** Tax engine, GSTIN checksum, words, outbox all correct. Tax model is "regular taxpayer, forward charge, ad-valorem" only; slab list still has 12 %/28 %. | **Keep & extend** |
| dwaar | API gateway | 716 | Works. Routing, JWT, header hygiene, IAM tokens correct. Rate limit fail-open and spoofable; webhooks share the OTP bucket. | **Keep & harden** |
| pehchaan | identity, OTP, RBAC, tenants, stores | 1,754 | Works, close to production. One shared HS256 secret, OTP HMAC keyed by JWT secret, no multi-GSTIN model. | **Keep & harden** |
| bahi | ledger & reports | 3,492 | Works but immature. Real double-entry enforced by a deferred Postgres trigger. Cash receipts post to Bank, partial refunds lost, P&L ignores stock, no period lock, "reconciliation" is a date stamp. | **Keep; fix money bugs, then add accountant features** |
| kar | GST: GSTIN, e-invoice, e-way, GSTR | 2,546 | **Scaffold with a working aggregator.** GSTIN verify is checksum-only in practice and reports "valid" on provider failure. e-Invoice and e-Way are JSON builders with no IRP/NIC call and would be rejected on field limits. GSTR-1/3B miss CDNUR, EXP, Table 8, HSN B2B/B2C split, ITC eligibility. HSN master is 69 demo rows. | **Keep skeleton; rebuild the compliance layer** |
| aadesh | orders, POS billing, returns, offline sync | 1,160 | Works but immature, **zero tests**. Numbering is per store, not per GSTIN. Server never validates item/price. Offline bills get server date. Cancel has no store check. | **Keep; fix, then widen the document model** |
| lenden | payments: Razorpay, Cashfree | 2,875 | Works but immature. Both gateways are real, webhooks HMAC-verified, capture is idempotent. Failed-then-succeeded refunds are silently lost; refunds can race; no settlement/fee accounting. | **Keep; fix refund path, add settlement** |
| soochna | SMS / WhatsApp | 1,395 | Works but immature. MSG91 + Gupshup real. Two template misconfigs, no opt-out, no DLQ. | **Keep & fix** |
| aayaat | MargBooks import | 2,705 | Works but immature, closest to usable. Parties + opening balances complete. Items/stock blocked by bhandar. No invoice history / bill-wise outstanding. | **Keep & widen** |
| bhandar | inventory | **9** | **Scaffold only.** README describes 29 endpoints and 9 source files; the folder has a schema and `embed.go`, nothing else. Cannot build. Everything that touches stock is dead. | **Build from scratch** |
| nirmaan | BOM & production | 1,677 | Works but immature. Explosion, cycle detection, costing correct and unit-tested. Unusable until bhandar exists; costs missing prices at ₹0; ignores units. | **Keep; fix after bhandar** |
| buddhi | AI: scan, ask, forecast, anomalies, KPIs | 3,688 | Works but immature; the most complete service. Anthropic SDK usage verified current. Needs bhandar for re-order and item matching; misses `note.created`. | **Keep & polish, stays behind flag** |
| dukaan | Next.js PWA | 886 | **Scaffold only.** `src/lib` only (API client, offline queue, GST parity port with tests, service worker). **No pages, no components; `next build` cannot succeed.** README documents an app that is not in the repo. | **Build from scratch, salvaging the retired Vite demo** |
| sanchalan | ops | — | Works but immature. Compose, Supabase roles, Pub/Sub matrix in sync. Two missing invoker grants, one missing secret, connection limits undersized, all bhandar wiring dangles. | **Keep & fix; add monorepo CI** |

Net: of 13 models, **2 must be built** (bhandar, dukaan), **1 must be rebuilt in its core** (kar), **10 are kept and matured**.

---

## 1. Cross-cutting decisions I need from you

| # | Decision | My recommendation |
|---|---|---|
| D1 | Toolchain on this machine | Install Go 1.25 and Docker Desktop (Postgres 16 + Redis 7 via `sanchalan/docker-compose.yml`). Without Go nothing can be compiled, tested or run. I will not install software without your yes. |
| D2 | bhandar | Build from scratch against its existing schema and README (they are a good spec), plus the market additions in §2.10. This is the first thing to build; it unblocks aadesh, nirmaan, aayaat, buddhi, dukaan POS and five Pub/Sub subscriptions. |
| D3 | dukaan | Build from scratch on Next.js 15 App Router + Tailwind v4, keeping the existing `src/lib`. Salvage screens, forms and the UI kit from the retired Vite demo (commit `b0f031d`, 13 pages, same brand) as the visual reference so we are not designing from zero. |
| D4 | GST Suvidha Provider | Pick one GSP covering GSTIN search + e-way + e-invoice with multi-GSTIN access (your PDF shortlists Masters India, ClearTax, Cygnet, IRIS, Vayana). I will build a provider-agnostic adapter in kar and implement one concrete driver against that GSP's sandbox. Until the contract exists kar runs on checksum with the UI clearly saying "not portal-verified". |
| D5 | Payment money flow | Per-tenant Razorpay/Cashfree keys (option A in your PDF): money lands in the store's account, Maapan never handles funds, no aggregator licence. Needs a settings screen and encrypted key storage in lenden. |
| D6 | Inventory valuation | Weighted average cost as default (what Tally/Marg/Busy users expect), FIFO as a per-tenant option later. Perpetual costing: every stock movement posts COGS / Inventory to bahi, so P&L is right for any period. |
| D7 | Monorepo tooling | Add a root `go.work`, a root Makefile, and one GitHub Actions workflow that builds, vets and tests every service + dukaan on every push. Keep the "each folder is its own repo" story for Cloud Build; local dev becomes one command. |
| D8 | Tax regime scope | Support regular taxpayer **and** composition (bill of supply, CMP-08), reverse charge, exempt/nil/non-GST, SEZ and export (LUT) supply types in mool, aadesh, kar and bahi. Retail pilots will hit composition dealers and RCM (freight, legal) in month one. |

---

## 2. Per model: current state → decision → work

Work in each model is grouped as **Fix** (bugs found), **Formulas** (make the maths market-standard), **Interconnect** (wire to other models), **Add** (what the market expects). File references are to the current code.

### 2.1 mool — shared library — Keep & extend

**Fix**
- `events/outbox.go:77-120` — `Flush` holds the DB transaction and `FOR UPDATE` row locks while doing up to 100 sequential HTTP publishes. Claim rows, publish outside the lock, mark per row. Rows past 20 attempts are silently abandoned: add an `outbox_dead` table and a metric/alert.
- `events/publishers.go:81-113` — direct driver fails the whole event if one subscriber is down (locally, bhandar) so bahi/soochna/buddhi get redelivered. Track per-subscriber success.
- `svc/client.go` — `WithRequestID` is never called by any service; traces break at every service boundary. Make the client propagate `X-Request-ID` by default.
- `gst/gstin.go:14` — regex allows any 10 alphanumerics where PAN must be `AAAAA9999A`; hard-codes `Z` at position 14 (rejects UINs). State map lacks legacy 25 (Daman & Diu) and 99.
- `money.go:12` — `FromRupees` via float; use decimal string parsing (spreadsheet imports hit 1.005 → 100).

**Formulas**
- `gst/compute.go:12` — `StandardRatesBP` still lists 1200 and 2800 (abolished 22 Sep 2025) and is used for validation by aayaat and buddhi. Replace with an **effective-dated rate table** (rate valid-from/valid-to) so August-2025 documents still validate and today's do not accept dead slabs.
- Add **supply type** to `Line`/`Summary`: regular, reverse-charge, composition, exempt, nil-rated, non-GST, zero-rated (SEZ/export with or without payment). Today the engine cannot produce a bill of supply or an RCM invoice.
- Add **specific (per-unit) cess** alongside ad-valorem (tobacco, aerated water, coal).
- Add **percentage discount** and **invoice-level post-tax discount** allocated pro-rata across lines (Busy/Marg standard).
- Add a **per-tax rounding option** (Sec 170 literal) next to the current invoice-total rounding (Tally practice).
- `ByRate` must include cess (needed for HSN summary Table 12).
- Add a **place-of-supply helper** (Sec 10/12 IGST: goods vs services, B2C inter-state services, bill-to/ship-to).
- Add TCS (Sec 52) and TDS (Sec 51) fields on `Summary`.

**Interconnect**: event payloads (`events/payloads.go`) gain `cess_rate_bp`, `supply_type`, `supplier_invoice_no/date`, `unit`, `store_gstin`, `irn`, so downstream services stop calling back for basics.

**Tests**: golden tests against official worked examples (inclusive/exclusive, cess, RCM, rounding) shared with dukaan's parity suite; property test that CGST+SGST never differs from IGST by more than 1 paisa per line.

### 2.2 dwaar — gateway — Keep & harden

**Fix**
- `gateway.go:185-188` — rate limiting fails open when Redis is down; add a per-instance in-memory fallback so an outage does not remove the OTP limit.
- `mool/httpx/middleware.go:133-136` — `ClientIP` trusts the first `X-Forwarded-For` hop (client-spoofable). Trust only the load balancer's appended hop (configurable trusted-proxy count).
- `routes.go:28-29` + `main.go:54` — MSG91/Gupshup delivery-report webhooks share the 20/min/IP public bucket and will be 429'd in bursts. Separate bucket (or none, token-verified downstream).
- `cmd/server/main.go:72` — `GET /api` catalog is unauthenticated; fine for dev, gate in prod.

**Add**: `Idempotency-Key` passthrough for POST/PUT (POS retries), request/response size metrics per upstream, a `/api/_status` that reports upstream health so dukaan can show "stock service down" instead of a generic error.

### 2.3 pehchaan — identity — Keep & harden

**Fix**
- `service.go:84` — OTP HMAC uses `JWT_SECRET`; derive a separate key (HKDF) so one secret does not do two jobs.
- `service.go:130-146` — verify is GET→compare→INCR, not atomic; make attempt counting a Lua script.
- `handlers.go` `/internal/v1/tenants/{id}` — no tenant match check; any secret holder reads any tenant. Enforce `X-Tenant-ID`.
- Add `kid` to JWTs and support two live secrets so the shared key can be rotated without downtime.

**Add (market)**
- **Multi-GSTIN per tenant**: a `registrations` table (GSTIN, legal name, state, address, PIN, e-invoice applicable, composition flag) and a store → registration mapping. Today tenant = one GSTIN; a business with a Maharashtra and a Gujarat GSTIN cannot exist. This is the prerequisite for correct invoice numbering and GSTR returns per GSTIN.
- Onboarding collects **store address and PIN code** (your PDF open point; IRP rejects e-invoices without them).
- User invites by phone, device registration for POS terminals (device_id → store), per-user audit of role changes.

### 2.4 bahi — ledger — Keep; fix money bugs, then add accountant features

**Fix (money correctness, before any pilot)**
- `posting/rules.go:218-233` — every receipt debits Bank 1110 regardless of method. Route by method: cash → Cash, cheque → Cheques-in-Hand (moves to Bank on clearance), bank/NEFT → Bank, gateway → **Gateway Clearing** (new account; settlement voucher moves it to Bank net of MDR + 18 % GST on MDR).
- `posting/rules.go:226-229` + `service.go:91-97` — refund voucher keyed by payment id, so the second partial refund of one payment is silently not booked. Key by refund id (lenden must emit it, see 2.7).
- `events.go:61-64` — an invoice whose totals do not reconcile is dropped and acknowledged; README says it dead-letters. Add a `quarantined_events` table and alert; never ack-drop money.
- `rules.go:162-169` + `events.go:113-134` — a UPI/card tender on the invoice debits Bank, and the lenden `payment.captured` for the same invoice debits Bank again. Link tenders to lenden payment requests (aadesh change) so one of the two posts.
- `service.go:239` — `Reverse` marks the reversal voucher itself as reversed.
- `posting/chart.go:99-102` — Indirect Expenses 5500 is a postable leaf that is also a parent; statements omit its children. Make it a group.
- `parties.go:157` — opening balance `as_of` defaults to today; default to FY start and require it on import.
- `events.go:84-87` — unknown party is silently re-pointed to Walk-in; retry instead (party may not have replicated yet).
- Add **period lock** (per tenant, per FY/month; `ledger:write` + `settings:manage` to override) and a **year-end close** that carries P&L to Capital and opening balances forward. Today anyone can post to any date.

**Formulas**
- `reports.go:118-136` — P&L = Sales − Returns − Purchases − Direct Expenses with no opening/closing stock. With D6 (perpetual), purchases debit Inventory, sales debit COGS at weighted-average cost from bhandar's `stock.moved` events; gross profit becomes correct for any period and matches the stock valuation report. Keep a periodic-mode fallback (closing-stock journal) for tenants not tracking stock.
- `posting/aging.go:30-76` — aging buckets by voucher date; use **due date = invoice date + party credit_days** (stored, unused today).
- Add **bill-wise (against-reference) allocation**: receipts allocate to specific invoices or on-account; outstanding per bill; this is what every Tally/Marg user calls "ledger".
- Add **GST set-off voucher** at month end (IGST → CGST → SGST per Rule 88A, which kar already computes) and Electronic Cash / Credit Ledger accounts.
- Chart additions: COGS, Inventory (FG/RM/WIP already present), Gateway Clearing, Cheques-in-Hand, TDS Receivable/Payable, TCS Payable, Discount Received, Advance from Customers, Advance to Suppliers, Fixed Assets + Accumulated Depreciation, Loans, Drawings, Salaries, Rent, Electricity, Freight.
- Store-wise P&L and cash book (`store_id` is stored on every entry but no report filters on it).

**Interconnect**: consume `bhandar.stock.moved` (COGS), `kar.return.filed` (set-off voucher), `lenden.settlement.processed` (clearing → bank). Expose `GET /internal/v1/parties/{id}/credit` (outstanding vs credit limit) for aadesh to enforce at billing.

**Add (market)**
- **Bank statement import** (CSV/XLSX from SBI/HDFC/ICICI/Axis/Kotak, MT940 later) with rule-based auto-matching and a real **BRS** (book vs bank, unpresented cheques, uncredited deposits).
- Cheque register with PDC due-date report and cheque printing layout.
- **TDS** (194Q purchases > ₹50 lakh, 194C contractors, 194J professional) and **TCS 206C(1H)** with threshold tracking, 26Q-ready export.
- Interest on overdue receivables, balance-confirmation letters, Tally XML export (CA handover), full edit audit trail, cost centres, budgets (later), multi-currency for exporters (later).

### 2.5 kar — GST compliance — Keep skeleton; rebuild the compliance layer

**Fix**
- `gstin/service.go:56-61` + bahi `parties.go:174-180` — provider failure returns the checksum result and bahi shows "valid on the GST portal"; `Status` (Cancelled/Suspended) is never checked. Return `source: checksum_only`, make bahi/dukaan show "format valid, not portal-verified", block B2B billing to a Cancelled GSTIN.
- `gstin/provider.go:65-104` — the GSP driver is a generic GET with static headers; real GSPs use OAuth/token exchange. Replace with a provider interface + one concrete driver per D4.
- `api/handlers.go:375-380` — invoices with empty `store_state` are counted in every GSTIN's return; `?gstin=` filters by state, so two GSTINs in one state cannot be separated. Map store → registration (pehchaan, 2.3) and filter by registration id.
- `gstr1.go:229-233` — document series from/to use lexical compare.
- `clients/clients.go:136-151` — a whole month of invoices with lines loaded into one request; stream by page and cache per return period.
- `eway.go:98` rejects BH-series vehicle numbers; `eway.go:263-268` byte-wise truncate splits Devanagari; `einvoice.go:117` hard-codes `SupTyp=B2B`; `:136` accepts 4-digit HSN (IRP needs 6); `:130` passes `+91…` phones (IRP wants 6–12 digits); `format.go:94-108` address split at 120 (IRP Addr1 limit 100).
- `api/handlers.go:421` `TrimRight(…"0123456789- ")` turns "Sector 15" into "Sector".

**Formulas / coverage**
- **HSN master**: replace 69 demo rows with the full CBIC 8-digit schedule, effective-dated rates (pre/post 22 Sep 2025), value-based slabs (footwear/apparel ₹2,500), pre-packaged vs loose, specific cess. Seed from a maintained CSV in `kar/data/`; a job refreshes from notifications.
- **e-Invoice**: real IRP flow via GSP — auth, generate IRN, signed QR, Ack no/date, cancel within 24 h, store `irn/ack_no/ack_dt/signed_qr` and write back to aadesh. Applicability auto-detected from AATO (₹5 crore) per registration, with the manual override kept. B2C dynamic QR for > ₹500 crore (flag only).
- **e-Way bill**: generate, update Part-B, extend, cancel, consolidated; validity by distance (1 day per 200 km); state-specific intra-state thresholds (₹1 lakh in MH/DL/WB/TN/BR/RJ etc.); 180-day window; auto-trigger from aadesh when invoice ≥ threshold and transporter/vehicle present.
- **GSTR-1**: add CDNUR (today credit notes to unregistered B2CL buyers are wrongly netted into B2CS), EXP 6A, Table 8 (nil/exempt/non-GST, currently reported as 0 % lines inside B2B/B2CS), HSN Table 12 split B2B vs B2C (mandatory since May 2025), 11A/11B advances, 9A/9C/10 amendments, Table 14/15 ECO supplies; emit the **GSTN offline-tool JSON** (`version`, `hash`, `gt`) so an accountant can upload it as-is; GSTR-1A corrections.
- **GSTR-3B**: 3.1(b)/(d)/(e), 3.1(c) split nil vs exempt vs non-GST, 3.2 for composition/UIN, **4(A)(3) RCM**, **ITC eligibility** (Sec 17(5) blocked flag per purchase line, Rule 42/43, 180-day reversal), 4(B) reversals, 4(D) ineligible, Table 5, 5.1 interest/late fee, opening ITC and cash-ledger balances carried forward. Today 4(A)(5) claims every purchase-line tax — a direct tax-exposure risk.
- **GSTR-2B / IMS**: pull 2B via GSP, match against purchase bills (invoice no + GSTIN + date tolerance), accept/reject/pending actions, mismatch report; feed buddhi's GST-mismatch anomaly.
- Composition: bill of supply, CMP-08, GSTR-4; QRMP/IFF for small taxpayers; GSTR-9/9C annual (phase 3); LUT tracking for exporters.

**Interconnect**: aadesh → kar `POST /internal/v1/documents/{id}/einvoice|eway` (async, outbox-driven); kar → aadesh write-back; kar → bahi set-off voucher; new events `kar.einvoice.generated`, `kar.eway.generated`, `kar.return.filed`.

### 2.6 aadesh — orders & billing — Keep; fix, then widen the document model

**Fix**
- `migrations/0001_init.up.sql:83` + `service.go:136` — numbering is unique per (tenant, store); two stores under one GSTIN both issue `INV/26-27/00001`, violating the unique-per-GSTIN rule. Number per **registration** with a store-specific prefix (`MUM-A/26-27/00001`), prefix configurable per store/doc type.
- `service.go:251-262` — offline number blocks create permanent, unexplained gaps and any `invoice:create` holder can burn 1,000 numbers per call; block FY is server-now so a block reserved 30 Mar cannot sync a 2 Apr bill. Cap block size per role, expire and release unused ranges, carry FY in the block, report gaps in GSTR-1 Table 13.
- `service.go:70-74` — offline bills are dated by server `now` because dukaan never sends `invoice_date`/`client_created_at`; fix both sides; bound `invoice_date` (no future dates, back-dating only inside an open period per bahi's lock).
- `service.go:275-303` — `Cancel` has no store-access check.
- `service.go:87-96` + `store.go:65-68` — change due is never computed; stored tenders are uncapped so Σ tenders ≠ paid.
- `service.go:353-357` — payments apply to cancelled invoices and purchase bills; credit notes never reduce the original's balance.
- `service.go:187-204` — credit/debit notes do not check store, party, item or quantity against the original; lines are repriced from client input. Validate against the original and copy tax rates.
- Purchase bills store `supplier_invoice_no` but no **supplier invoice date** (needed for 2B matching) and no duplicate check on (supplier, number, FY).
- `clients.go` — server never looks up items; price, HSN and rate come from the POS body, so a tampered client bills at any price. Resolve items from bhandar, validate rate/HSN, enforce MRP ceiling and allowed discount per role.
- `service.go:244-250` — `ReserveBlock` with no store for a non-store-scoped role inserts `''` as UUID.
- **Zero tests today**: add service tests (numbering, FY rollover, sync idempotency, tenders, CN/DN) and a contract test against mool's golden GST cases.

**Interconnect** (all promised in ARCHITECTURE §6, none implemented): aadesh → bhandar `items/lookup`; aadesh → lenden `payment-requests` (UPI QR at the counter) with the tender linked to the payment request; aadesh → kar (async e-invoice/e-way after post); aadesh → bahi credit check (outstanding + this bill ≤ credit limit, overridable with permission). Also consume `kar.einvoice.generated` to store IRN/QR and `bhandar` item changes for catalog ETag.

**Add (market)**
- Document flow: **quotation/estimate → sales order → delivery challan → tax invoice**, proforma, **purchase order → GRN → purchase bill**, with conversion links and open-quantity tracking.
- Pricing: MRP-based billing, **price lists** (retail/wholesale/party-wise), scheme/free-quantity discounts, % and invoice-level discounts (via mool), round-off rules per tenant.
- Credit terms and due date on every B2B document; payment reminders via soochna.
- e-way/e-invoice fields on the invoice (transporter, vehicle, distance, dispatch address, ship-to) and triggers.
- Supply types from D8: RCM purchase (self-invoice), composition bill of supply, exempt, export/SEZ.
- Serial numbers and validated batch/expiry lines (FEFO suggestion from bhandar); multi-unit lines (BOX of 48 → PCS).
- **Print payloads and PDF**: A4 tax invoice, A5, 2"/3" thermal (ESC/POS commands prepared server-side), with tenant templates; `GET /v1/invoices/{id}/pdf` so soochna can attach the bill on WhatsApp (today nothing in the system renders a PDF).
- Invoice amendment rules (edit before e-invoice/period lock, CN/DN after).

### 2.7 lenden — payments — Keep; fix refund path, add settlement

**Fix**
- `service.go:266-270` + `ledger.go:205-210` — a refund that times out is marked `failed`; when the gateway's `refund.processed` webhook arrives, `applyRefund` only updates `pending` rows, so the refund is never booked and no `payment.refunded` event fires. Money left the account; books unchanged. Treat gateway errors as `unknown`, reconcile by gateway refund id, and let the webhook promote any non-processed row.
- `service.go:238-254` — refundable amount computed without locking the payment; two concurrent refunds both pass. `SELECT … FOR UPDATE`.
- `ledger.go:223-227` — refund event carries payment id, not refund id (causes bahi's lost partial refunds). Emit `refund_id`.
- `0001_init.up.sql:65` + `service.go:203-219` — manual receipts have no idempotency; a double-submitted cheque receipt creates two payments and two vouchers. Require an idempotency key.
- `ledger.go:124-126` — captured amount never compared with requested; handle under/over-payment explicitly.
- `service.go:298` — reconcile window is 3 days; older unpaid requests never expire; provider-side refunds seen during reconcile are not applied.
- `webhooks.go:155` vs `cashfree.go:166,312-316` — Cashfree link payments keyed on `transaction_id` in webhooks but `cf_payment_id` in fetch; if they differ reconcile double-books.

**Add (market)**
- **Settlement reconciliation**: `settlement.processed` webhook / settlements API, MDR + 18 % GST on fees, settlement voucher to bahi (clearing → bank), UTR stored.
- Per-tenant gateway keys (D5), encrypted at rest, with a settings endpoint.
- **Static VPA / zero-MDR UPI** for small shops (store's own UPI id as a tender with manual confirmation, as Vyapar does), partial payments on links, allocation of one payment across several invoices or on-account, chargebacks/disputes, vendor payouts (phase 3).

**Interconnect**: aadesh → lenden counter QR; new event `lenden.settlement.processed` → bahi.

### 2.8 soochna — notifications — Keep & fix

**Fix**: `.env.example` and provider id lists omit `WEEKLY_SUMMARY` (goes out free-form); `templates.go:62-64` `batch_expiring` WhatsApp body references `qty` that the positional params never pass; retries stop silently after 5 attempts (add DLQ + alert); add **opt-out/consent** per phone (DND, TRAI) — today every sale with a phone triggers a customer WhatsApp.

**Add**: email channel (invoice PDF, statements) via one transactional provider; WhatsApp PDF attachment using aadesh's PDF endpoint; payment reminders on due date (from aadesh/bahi); scheduled ledger statements; per-tenant sender/template registry so each store can have its own DLT header later.

### 2.9 aayaat — MargBooks import — Keep & widen

**Fix**: `validate.go:142-148` a landline phone fails the whole party row; `:184` shared GSTIN across branch ledgers (very common) rejected as duplicate; `service.go:443-449` crash between a successful bulk POST and `recordBatch` can double-post opening balances keyed by account code (add `external_ref`); `RecoverStale` only runs at start (make periodic); `service.go:287-317` validate holds every row in memory (stream); orphaned GCS object on oversize; re-validating a failed job deletes its audit; `parse.go:164-166` bare "2027" read as an Excel serial.

**Add (market)**: **invoice/voucher history** and **bill-by-bill outstanding** (the thing that actually decides a Tally/Marg migration), chart of accounts and groups, **Tally XML**, Busy and Vyapar exports, `.xls`, item cess/category/brand/alt-units/batch flag/opening qty, party PAN/credit limit/credit days/addresses, per-batch MRP and mfg date, per-row store, saved mapping templates, dry-run and rollback. Items/opening stock become live once bhandar exists.

### 2.10 bhandar — inventory — Build from scratch

The schema (`migrations/0001_init.up.sql`) and README are a good spec; build to them and add what is listed under "Add". Nothing exists today, so every item below is new.

**Core (README promises)**: item master with search by name/SKU/barcode; units and alt-units; EAN-13 barcode generation with check digit and SVG/label output; `pos-catalog` with ETag for the offline POS; append-only stock movements idempotent on (source, source_id, line); **weighted-average cost** per store/item/batch; event consumers for `aadesh.invoice.created/cancelled`, `aadesh.note.created`, `nirmaan.production.completed`, `pehchaan.tenant.created` (default units); adjustments with mandatory note; transfers (draft → dispatched → received / cancelled); re-order suggestions and purchase-draft; batches with expiry, expiring/expired report, return-to-vendor draft; dead stock, stock aging, valuation; internal `items/lookup`, `bulk/items`, `bulk/opening-stock`, `stock/summary`; publish `bhandar.stock.low`, `bhandar.batch.expiring`; the two scheduler jobs.

**Formulas**: purchase cost = taxable value ÷ qty **plus allocated landed cost** (freight/other charges on the purchase bill, allocated by value or qty); sale/consumption out at current average; returns in at the original document's cost; negative stock allowed at POS (per README) but flagged; re-order point = lead-time demand + safety stock (buddhi supplies the forecast; fallback = `reorder_level`); FEFO pick suggestion for batch items.

**Add (market)**: price lists (retail/wholesale/party-wise) and schemes, serial numbers, item variants (size/colour), **stock take / cycle count** with variance posting, PO/GRN support with aadesh, supplier–item mapping and last purchase price, bin/rack locations, multi-unit conversion on every movement, weighing-scale barcode parsing (price-embedded EAN), HSN auto-suggest from kar's master, new event `bhandar.stock.moved` (qty, cost) for bahi's perpetual COGS.

### 2.11 nirmaan — manufacturing — Keep; fix after bhandar

**Fix**: `bom/model.go:136-138` scrap gross-up is `×(1+scrap%)`; market (Tally/SAP) uses `÷(1−scrap%)` — switch, keep the old one as an option; `model.go:304-308` + `production.go:394-401` missing prices cost at ₹0 and `Complete` proceeds, booking finished goods at zero — block or require a manual cost; cost roll-up uses list purchase price — use bhandar's weighted average; `production.go:435-442` component unit is free text and the event has no unit, so 100 GM of yeast is consumed as 100 base units — add unit conversion; items not in the BOM are silently ignored (allow unplanned consumption and by-products); `output_qty=0` means "planned", so a failed batch cannot be recorded; `bom/service.go:45-53` drops `rows.Err()`; `Archive` can retire the active BOM; money computed in float64.

**Add**: run numbering, WIP/material issue on start, partial completion, routing/labour hours and yield %, BOM effective dates and alternates, **job work** (ITC-04 challans, very common in Indian SME manufacturing).

### 2.12 buddhi — AI — Keep & polish, stays behind the per-tenant flag

**Fix**: `api/api.go:65-70` no handler for `aadesh.note.created` although subscribed (returns never reduce KPIs); `store/usage.go:80-100` budget is checked before a call but not reserved (one call can overshoot); `kpi/dashboard.go:58-108` Redis preferred even when the post-commit mirror failed (stale KPIs); `anthropic.go:67-75` the server-side-fallback beta header is sent for every model, which Haiku 5.5 may reject when used as `AI_MODEL_FAST`; delimit tool results in ask-your-books (tenant-authored text) to lower injection surface; use mool's effective-dated rates in scan validation.

**Add**: GST mismatch detection on 2B vs books (once kar does 2B), re-order and item matching go live with bhandar, Hindi/Hinglish voice-to-bill later. Keep the pilot flag.

### 2.13 dukaan — frontend — Build from scratch, salvaging the Vite demo

Keep `src/lib` (API client, session, offline queue, GST parity, RBAC mirror, service worker) and build the app on Next.js 15 App Router + Tailwind v4. Pull screens, forms, UI kit, command palette and the landing page from the retired Vite demo (commit `b0f031d`, 13 pages, same navy/green brand) as the starting point.

**Screens**: login/OTP, onboarding (with address/PIN and GSTIN registrations), **POS counter** (barcode/scale input, held bills, MRP/price lists, discount within role limit, tenders with change, UPI QR via lenden, offline with number blocks, thermal + A4 print), invoices/purchases/returns/quotation/SO/PO/GRN, parties and bill-wise ledgers with statements, items/stock/batches/transfers/labels/stock take, reports (day book, cash/bank, TB, P&L, BS, aging, stock valuation, dead stock), GST centre (GSTIN check, e-invoice/e-way actions, GSTR-1/3B review, 2B matching), bank reconciliation, import wizard, BOM/production, AI (scan bill, ask your books, KPI dashboard), settings (business, registrations, stores, users/roles, gateway keys, templates, period lock).

**Fix in lib**: `session.ts:4` refresh token in localStorage → httpOnly cookie via a Next route handler; `offline.ts` sends `invoice_date` and `client_created_at`; catalog endpoint exists once bhandar does.

**Tests**: keep the GST parity suite, add Playwright smoke for login → POS bill → ledger.

### 2.14 sanchalan — ops — Keep & fix; add monorepo CI

**Fix**: `iam-invokers.sh:18-25` missing `buddhi → pehchaan` and `kar → bahi` grants (403 on Cloud Run); `gcp-bootstrap.sh:66-69` never creates `soochna-webhook-token` (first soochna deploy fails); `supabase/setup.sql:27-34` role `CONNECTION LIMIT 40` vs aadesh 20 instances × 5 conns = 100 (size per service or use the transaction pooler for read paths); `pubsub-setup.sh` bhandar subscriptions dead-letter until bhandar exists; `roles/pubsub.publisher` granted to dwaar/dukaan unnecessarily; Scheduler secret embedded in job headers (use OIDC only).

**Add**: root `go.work` + Makefile (`make build test run`), GitHub Actions CI for all services + dukaan, contract tests for every `/internal/v1` shape and event payload, k6 load script for dwaar/aadesh, runbooks (dead-letter replay, outbox dead rows, period reopen), Terraform later.

---

## 3. Interconnection after the plan

**Events** (existing + new in bold):

| Event | Publisher | Subscribers |
|---|---|---|
| aadesh.invoice.created / cancelled / note.created | aadesh | bahi, **bhandar**, soochna, buddhi (+ note handler), **kar** (e-invoice/e-way trigger) |
| lenden.payment.captured / refunded (with refund_id) | lenden | bahi, aadesh, soochna, buddhi |
| **lenden.settlement.processed** | lenden | bahi |
| **bhandar.stock.moved** (qty, avg cost) | bhandar | bahi (COGS/Inventory), buddhi |
| bhandar.stock.low / batch.expiring | **bhandar** | soochna, buddhi, **aadesh** (purchase draft) |
| nirmaan.production.completed (with units) | nirmaan | bahi, **bhandar** |
| **kar.einvoice.generated / eway.generated / return.filed** | kar | aadesh (IRN/QR write-back), bahi (set-off), soochna |
| aayaat.import.completed | aayaat | soochna |
| pehchaan.tenant.created / **registration.created** | pehchaan | bahi, **bhandar**, kar, aadesh (number series) |

**Synchronous calls added**: aadesh → bhandar (lookup, FEFO), aadesh → lenden (QR), aadesh → bahi (credit check), aadesh → kar (none; async), dukaan → everything only via dwaar.

Outcome of the "first bill" test: POS bill → aadesh posts, bhandar moves stock and emits cost, bahi posts sale + COGS, kar generates IRN if applicable, lenden QR captured, soochna sends the PDF bill, buddhi updates KPIs — every arrow exists in code, none dead-letters.

---

## 4. Sequence (what becomes demonstrable at each step)

| Phase | Models | Demonstrable result |
|---|---|---|
| 0 Foundation | toolchain, sanchalan CI, mool | Every service builds, vets and tests in one command; effective-dated rates, supply types, outbox fix. |
| 1 Unblock stock | **bhandar**, aadesh fixes, bahi money fixes, lenden refund fixes | A cash bill moves stock and posts correctly; refunds and partials book correctly; nothing dead-letters locally. |
| 2 Counter | **dukaan** POS + core screens, aadesh ↔ bhandar/lenden wiring, soochna fixes, print/PDF | A cashier can bill offline, print, take UPI, and the owner gets the WhatsApp bill. |
| 3 Compliance | kar rebuild (GSP, HSN master, e-invoice, e-way, GSTR-1/3B tables, 2B/IMS), pehchaan registrations | An accountant can download filing-grade GSTR-1/3B JSON per GSTIN and generate IRN/e-way from the invoice. |
| 4 Accountant | bahi bill-wise, bank import/BRS, TDS/TCS, period close, set-off; aayaat invoice history/Tally XML | A CA can take over a migrated Marg/Tally business and close a month. |
| 5 Depth | nirmaan fixes + job work, buddhi polish, price lists/schemes/serials/stock take, document flow (quote/SO/PO/GRN) | Wholesale and manufacturing tenants are fully served. |

Phases 1 and 2 are the longest; everything in 3–5 is additive and can be re-ordered to match your pilot's business type.

---

## 5. What I could not verify

- No code was compiled or executed (Go absent). Audit findings are from reading; line references were spot-checked for the four highest-impact items (bahi receipts to Bank, kar checksum fallback, aadesh per-store numbering, lenden lost refund) and confirmed.
- Unit tests were read but not run; "tests pass" claims in READMEs are unverified until D1 is done.
- Gateway/provider behaviour (Razorpay, Cashfree, MSG91, Gupshup, Anthropic) is verified against the code's use of their APIs, not against live sandboxes.
- The exact 2026 GSTR-1 table changes beyond those listed could not be confirmed from an authoritative source; the HSN master and return layouts should be checked against the GSTN offline tool of the month we ship.
