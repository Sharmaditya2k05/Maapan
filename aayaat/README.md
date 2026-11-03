# aayaat (आयात) — Import & Migration Service

"Aayaat" means *import*. It moves a shop's existing books into Maapan — from **MargBooks** Excel exports,
**Tally Prime / ERP 9** XML exports, or any `.xlsx` / `.xls` / `.csv`:

| Kind | Goes to | Typical source |
|---|---|---|
| `parties` | bahi `POST /internal/v1/bulk/parties` | Marg Party / Ledger master; Tally *Party Ledgers* (Sundry Debtors/Creditors and their sub-groups) |
| `opening_balances` | bahi `POST /internal/v1/bulk/opening-balances` | Trial balance; Tally *Other Ledgers* (cash, bank, capital, taxes …) |
| `items` | bhandar `POST /internal/v1/bulk/items` (+ `bulk/opening-stock` for an opening qty column) | Marg Item master; Tally *Stock Items* |
| `opening_stock` | bhandar `POST /internal/v1/bulk/opening-stock`, one call per store | Marg batch-wise stock; Tally *Stock Batches* (batch allocations per godown) |
| `open_bills` | bahi `POST /internal/v1/bulk/open-bills` — or `party_totals` mode → `bulk/opening-balances` | Marg *Bill-wise Outstanding*; Tally *Bills* (opening bill allocations) |
| `invoices` | bahi `POST /internal/v1/bulk/invoice-history` | Marg *Sale / Purchase Register*; Tally *Vouchers* (sales, purchase, credit / debit notes) |

Flow: **upload → preview & auto-mapping (or a saved template) → validate (row-level errors and warnings) →
dry run (optional) → commit (batches of 500, journalled) → rollback (optional)**, then
`aayaat.import.completed` is published so `soochna` can tell the owner.

| | |
|---|---|
| Port (local) | `7007` |
| Database | `maapan_aayaat` |
| Publishes | `aayaat.import.completed` (via outbox) |
| Subscribes | — |
| Calls | `bahi` (bulk parties / opening balances / *open bills* / *invoice history* / *rollback*), `bhandar` (bulk items / opening stock / *rollback*), Cloud Storage, Cloud Run Admin API |
| Gateway prefix | `https://api.maapan.in/api/aayaat/...` |
| First deployed | **Week 9** (MargBooks Excel Importer); used for pilot data in Week 12 |

## Run locally

Prerequisites: Go 1.25+, Docker (for Postgres), the `mool` repo checked out next to this one.

```bash
# 1. Postgres from the sanchalan repo
cd ../sanchalan && docker compose up -d postgres && cd ../aayaat

# 2. database + env (uploads go to ./data/uploads)
make db-create
cp .env.example .env

# 3. run
make run                     # → http://localhost:7007/healthz
make test                    # unit + sample-file tests
# database-backed workflow tests (fresh schema per test, dropped afterwards):
TEST_DATABASE_URL='postgres://maapan:maapan@localhost:5432/maapan_test?sslmode=disable' go test ./...
```

`commit` needs `bahi` (parties, balances, bills, invoices) or `bhandar` (items, stock) running. Upload,
preview, mapping, validation and dry run work on their own.

### Walk-through

```bash
TOKEN=...   # access token of a super_admin or accountant (import:run)
H="Authorization: Bearer $TOKEN"

# 0. (optional) blank template
curl -s localhost:7007/v1/imports/templates/parties -H "$H" -o parties-template.csv

# 1. upload (params can also be sent as one JSON form field "params")
curl -s localhost:7007/v1/imports -H "$H" -F kind=parties -F file=@PartyMaster.xlsx -F as_of=2026-04-01
#   → {"id":"<job>","status":"uploaded",...}

# 2. preview: sheets, detected header row (title rows skipped), 20 sample rows, suggested mapping,
#    saved templates of this kind ("matches": true when this file has every mapped column)
curl -s localhost:7007/v1/imports/<job>/preview -H "$H"

# 3. confirm / adjust the mapping (field → column header); save it for next time
curl -s -X PUT localhost:7007/v1/imports/<job>/mapping -H "$H" -d '{
  "sheet": "Sheet1",
  "mapping": {"name":"Party Name","gstin":"GSTIN No","phone":"Mobile","pan":"PAN","credit_days":"Cr Days",
              "opening_balance":"Opening Bal","dr_cr":"Dr/Cr","type":"Group"},
  "params": {"as_of":"2026-04-01","notify_phone":"+919876543210"},
  "save_as": "Marg party master"
}'
#    next upload: {"template_id":"<id>"} applies it (mapping, sheet, header row and reusable params)

# 4. validate → counts, Dr/Cr reconciliation, first 20 errors and 20 warnings
curl -s -X POST localhost:7007/v1/imports/<job>/validate -H "$H"
curl -s "localhost:7007/v1/imports/<job>/errors?format=csv" -H "$H" -o errors.csv   # fix in Excel, re-upload

# 5. dry run: the exact bulk calls, a sample of each payload, unresolved references, fields not sent
curl -s -X POST localhost:7007/v1/imports/<job>/dry-run -H "$H"

# 6. commit (202) and poll; the batch journal shows every bulk call
curl -s -X POST localhost:7007/v1/imports/<job>/commit -H "$H"
curl -s localhost:7007/v1/imports/<job> -H "$H"
curl -s localhost:7007/v1/imports/<job>/batches -H "$H"

# 7. undo (whole job, or ?batch=N)
curl -s -X POST localhost:7007/v1/imports/<job>/rollback -H "$H"
```

## API

All routes need `Authorization: Bearer <access token>` with **`import:run`** (super_admin, accountant);
via the gateway prefix them with `/api/aayaat`.

| Method & path | Purpose |
|---|---|
| `POST /v1/imports` | multipart: `file` (.xlsx/.xls/.csv/.xml, ≤ 50 MB), `kind`, optional `params` (JSON) or the single fields `as_of`, `store_id`, `notify_phone`, `source`, `doc_type`, `side`, `bills_mode` |
| `GET /v1/imports?kind=&limit=&offset=` | Jobs, newest first |
| `GET /v1/imports/{id}` | Job with counts (valid / errors / skipped / warnings / imported / failed / rolled back) and Dr-Cr reconciliation (parties, balances, bills) |
| `GET /v1/imports/{id}/preview?sheet=` | Sheets, header row, headers, 20 rows, `suggested_mapping`, field list, matching `templates` |
| `PUT /v1/imports/{id}/mapping` | `{sheet, header_row (0=auto), mapping:{field:header}, params, template_id, save_as}` — clears earlier validation, **never** rows already imported |
| `POST /v1/imports/{id}/validate` | Validate every row (streamed to Postgres in chunks) → `{job, sample_errors, sample_warnings}` |
| `GET /v1/imports/{id}/errors[?format=csv]` | Invalid + failed rows with messages and original values |
| `GET /v1/imports/{id}/warnings[?format=csv]` | Rows that import but deserve a look (phone dropped, sale price capped at MRP, shared GSTIN …) |
| `GET /v1/imports/{id}/rows?status=valid\|invalid\|imported\|failed\|skipped\|rolled_back` | Stored rows |
| `GET /v1/imports/{id}/batches` | Bulk-call journal: path, store, rows, sent / recorded / failed / rolled_back, created / updated / errors |
| `POST /v1/imports/{id}/dry-run` (= `commit?dry_run=true`) | What a commit would send; nothing is sent, the job does not change |
| `POST /v1/imports/{id}/commit` | 202; pushes valid rows (also resumes a `failed` job) |
| `POST /v1/imports/{id}/rollback[?batch=N]` | Undo a job or one batch downstream; a job that never reached bahi/bhandar is just discarded |
| `GET /v1/imports/templates/{kind}` | CSV template with headers and an example row |
| `GET /v1/mapping-templates?kind=` · `POST /v1/mapping-templates` · `GET`/`DELETE /v1/mapping-templates/{id}` | Saved column mappings `{kind, name, source, sheet, header_row, mapping, params}` (upsert by kind + name) |
| `GET /healthz`, `GET /readyz` | Probes |

### Params

| Param | Used by | Meaning |
|---|---|---|
| `as_of` | all | Opening date `YYYY-MM-DD`, default 1 April of the current FY. Sent as `opening_as_of` with parties (their opening balance is dated here, not on the import day) |
| `store_id` | items, opening_stock, invoices | Default store |
| `store_map` | items, opening_stock, invoices | `{"Main Godown": "<store id>", "Counter": "<store id>"}` — the **Store / Godown column picks the store per row**. A store id (UUID) in the column also works. Every store is checked against the user's store access |
| `account_map` | opening_balances | `{"Output CGST": "2210", "Duties & Taxes": "2200"}` — old ledger *or group* name → bahi account code |
| `doc_type` | invoices | `sale` · `purchase` · `sale_return` · `purchase_return` when the file has no type column (default `sale`) |
| `side` | open_bills | `dr` (receivables, default) / `cr` (payables) when the file does not say |
| `bills_mode` | open_bills | `bill_wise` (default; bahi `bulk/open-bills`) or `party_totals` (one opening balance per party). bahi refuses bill-wise bills for a party that already has a party-level opening balance, so import the party master **without** opening balances when you import bills bill-wise |
| `source` | — | `marg` · `tally` · `busy` · `vyapar` · `excel` (informational; `.xml` is Tally) |

### Job and row states

Job: `uploaded → mapped → validated → running (or queued → running) → completed | failed`, and `→ rolled_back`.
Row: `valid | invalid | skipped` after validation (skipped = nothing to import: zero quantity, zero balance,
fully paid bill), then `imported | failed`, then `rolled_back`.

A failed or interrupted commit keeps already-imported rows; committing again continues with the rest.
Validating a failed job again re-checks every row **except the imported ones, whose status and audit stay**;
changing the column mapping of a partly imported job is refused (roll it back or upload a new job).

## Formats

| Format | How it is read |
|---|---|
| `.xlsx` / `.xlsm` | excelize, streaming rows |
| `.xls` | the content decides: real Excel 97-2003 (BIFF8, via `shakinm/xlsReader`; a damaged file is an error, never a crash), an **HTML table** (what MargBooks and many billing programs write as "Excel"), or tab-separated text |
| `.csv` / `.txt` / `.tsv` | `,` `;` `\|` or tab, detected; UTF-8 (BOM ok) or **UTF-16** |
| `.xml` | **Tally** "Export → XML" of masters and/or vouchers (UTF-8 or UTF-16, Tally's `&#4;` control references tolerated) |

### Tally XML

The export is flattened into fixed tables whose headers auto-map; the sheet that feeds each kind is picked
automatically:

| Sheet | Rows | Notes |
|---|---|---|
| Party Ledgers | ledgers whose group chain reaches *Sundry Debtors / Creditors* | name, group, mobile/phone, email, GSTIN (`PARTYGSTIN` or Prime's GST registration block), PAN (`INCOMETAXNUMBER`), state, address lines, PIN (also Prime 3 `LEDMAILINGDETAILS`), opening balance, credit limit, credit days |
| Other Ledgers | every other ledger | name, group, primary group, opening balance. Defaults: *Cash-in-Hand* → 1100, *Bank Accounts* → 1110, *Capital Account* → 3100, Sundry Debtors/Creditors → the party; *Stock-in-Hand* and P&L are skipped; ledgers with no balance need no mapping; anything else needs `account_map` |
| Bills | opening `BILLALLOCATIONS` of party ledgers | bill no, date, credit days, amount |
| Stock Items | `STOCKITEM` | alias as item code, stock group → category, stock category → brand, base / additional unit + conversion (`DENOMINATOR ÷ CONVERSION`), HSN + IGST and cess rate from `GSTDETAILS`, MRP, standard sale / cost price, batch-wise flag, opening qty / rate |
| Stock Batches | `BATCHALLOCATIONS` per godown (or the item's opening balance when it has none) | godown, batch, qty, rate, value, mfg date, expiry, MRP |
| Vouchers | Sales, Purchase, Credit Note, Debit Note (custom types by name); cancelled / optional vouchers, receipts, payments and journals are left out | type, number, date, party (empty for cash), GSTIN, place of supply, taxable = total − taxes − round off, CGST / SGST / IGST / cess, round off, total, reference, narration |

Tally amounts are signed (negative = Dr); they become `1,200.00 Dr` / `Cr` text so the usual Dr/Cr rules apply.

## What validation checks

- **Header detection**: MargBooks puts company name and report title above the table; the row with the most recognised headers in the first 15 is used.
- **Auto-mapping**: fuzzy match (aliases, prefixes, edit distance) of headers such as *Party Name, GSTIN No, PAN, Credit Days, Opening Bal, Dr/Cr, Item Name, Company, Item Group, HSN Code, Alt Unit, Conversion, MRP, Sale Rate, Purc Rate, GST %, Cess %, Batch Wise, Cl. Stock, Godown, Batch, Mfg Date, Expiry, Bill No, Pending Amt, Taxable Amt, CGST Amt, R/Off, Net Amt*.
- **Numbers**: Indian grouping `1,23,456.78`, `₹`/`Rs.` prefixes, `(1,200.00)` and `1200-` negatives, Excel scientific notation — parsed straight to paise without float error.
- **Dates**: `dd/mm/yyyy`, `dd-Mon-yyyy`, Tally `yyyymmdd`, Excel serials (20000–79999 only), MargBooks expiry `MM/YY` (→ month end). A bare year (`2027`) is an error, not Excel serial 2027.
- **Dr/Cr**: `1,200.50 Dr`, `Cr 300`, a separate Dr/Cr column, or a negative number (= Cr). Unspecified: Dr for customers, Cr for suppliers.
- **Parties**: GSTIN checksum (`mool/gst`), state must match the GSTIN's state; PAN format (taken from the GSTIN when missing; a different PAN is a warning); **mobiles and STD-code landlines** (`022-2345 6789` → `+912223456789`); a cell with several numbers takes the first mobile; an unusable phone or e-mail is **dropped with a warning, never fails the row**; address lines + city + PIN joined; credit limit (Tally's negative accepted) and credit days (`30 Days`).
- **Shared GSTIN**: branch ledgers of one business ("Sharma Agencies – Pune / – Nashik") share a GSTIN; they stay separate parties with a warning.
- **Items**: HSN 4/6/8 digits; GST % must be a slab **in force today** (a retired 12 % says so — 56th GST Council, 22 Sep 2025); cess %; sale price above MRP is capped at MRP with a warning (as bhandar does); units normalised (`Nos` → PCS, `Kgs` → KG …); alt unit + conversion (`48`, `12 PCS`, `1 BOX = 48 PCS`); batch flag `Yes/No/Y/N`; opening qty needs a store (without one it is ignored with a warning).
- **Opening stock**: item by code, name or id; store per row; zero qty skipped; negative qty refused; unit cost from rate or value ÷ qty; mfg ≤ expiry.
- **Open bills**: party by code or name; due date from the column or bill date + credit days; due before bill date refused; zero pending skipped; bills dated after `as_of` warned (import them as invoices).
- **Invoices**: type normalised (Tax Invoice / Sales / Credit Note …); taxable + taxes + round off must equal total (a register without a round-off column gets the ≤ ₹1 difference as round off); IGST xor CGST+SGST; negative returns stored as magnitudes; cash bills have no party.
- **Duplicates within the file**: parties by name / code, items by name / barcode / code, stock by store + item + batch, ledgers by target account, bills by party + bill no, invoices by type + number + date (+ supplier for purchases).
- **Reconciliation**: total Dr vs Cr of parties / opening balances / bills and the difference (a non-zero difference usually means capital, cash or stock ledgers are missing).

## Commit, crash safety, dry run, rollback

- **Batch journal** (`import_batches`): each bulk call is written `sent` before the POST and turned `recorded`
  in the same transaction that stores the row results. If the process dies in between, the rows are still
  `valid`; the next commit marks the orphaned batch `failed` ("re-sent") and sends them again. Downstream keys
  make the second delivery a no-op: party / item `external_ref`, bahi's opening key (`as_of` + account / party),
  bhandar's opening key (store + item + batch), and the bill / invoice `external_ref` aayaat computes.
- **Interrupted jobs** are released by a sweep at start and every `RECOVER_EVERY` (5 min): `running` with no
  progress for `STALE_AFTER` (15 min) or `queued` for an hour → `failed`; commit again to continue.
- **Items with an opening qty**: after each item batch, the accepted items' opening stock goes to
  `bulk/opening-stock` (one call per store). If that fails the row becomes `failed` ("item imported; opening
  stock failed: …"); committing again re-sends both, idempotently.
- **Payload shaping**: bahi and bhandar reject unknown JSON fields, so each record is cut to the fields the
  endpoint accepts today. `BAHI_EXTRAS=true` adds party `pan`, `shipping_address` and opening-line
  `external_ref`; `BHANDAR_EXTRAS=true` adds item `alt_units` and batch `mfg_date`, `mrp_paise`. The dry run lists
  what is validated but not sent.
- **Dry run**: the calls in order (service, path, store, row range, records), the first 3 payloads exactly as
  sent, references no aayaat import carries (e.g. stock for items never imported), fields not sent, and notes
  (bill-wise endpoint missing, parties already carrying an opening balance — importing their bills too would
  count the outstanding twice, an unbalanced trial balance).
- **Rollback** posts `{kind, job_id, as_of, records:[keys…, created]}` to `POST /internal/v1/bulk/rollback` on
  bahi or bhandar (items: opening stock first, then the items). The downstream should archive only what the
  import created and reverse opening vouchers / movements by key, never revert an update of a record that
  existed before. Until that endpoint exists the call answers **503** and nothing changes. Rolled-back rows are
  `rolled_back`; the job becomes `rolled_back` when no imported row is left.

## Needed from other services

| Service | Need | Shape |
|---|---|---|
| bahi | **Bill-wise opening** | `POST /internal/v1/bulk/open-bills {as_of, bills:[{party_external_ref, party_name, bill_no, bill_date, due_date, bill_amount_paise, amount_paise, type: dr\|cr, external_ref}]}` → `BulkResult`. One opening voucher line per bill against the party (source id = `external_ref`), carrying bill no / due date so aging and outstanding match bill by bill. **Built in bahi** (one opening voucher per bill; refused when the party already has a party-level opening) |
| bahi | **Invoice history** | `POST /internal/v1/bulk/invoice-history {as_of, invoices:[{doc_type, number, date, party_external_ref, party_name, party_gstin, place_of_supply, taxable_paise, cgst_paise, sgst_paise, igst_paise, cess_paise, round_off_paise, total_paise, due_date, reference, narration, store_id, external_ref}]}`. Before `as_of`: stored for reference / GSTR history only (the opening balances already hold their effect). On / after `as_of`: posted as sales / purchase / CN / DN vouchers with source `import`, source id `external_ref`, period lock respected. Never emits stock movements |
| bahi, bhandar | **Rollback** | `POST /internal/v1/bulk/rollback {kind, job_id, as_of, records:[…]}` → `BulkResult` (see above) |
| bahi, bhandar | Per-record bulk result | `results:[{index, id, created}]` in `BulkResult`, so rollback can tell created from updated records (aayaat stores it when present) |
| bahi | Accept party `pan`, `shipping_address`; opening line `external_ref`; update `email`, `credit_limit_paise`, `credit_days` on the upsert path (today only name / phone / GSTIN / state / address are updated on re-import) | then `BAHI_EXTRAS=true` |
| bhandar | Accept `alt_units` on `bulk/items` (ItemInput already has it) and `mfg_date`, `mrp_paise` on opening lines (`MoveLine` already has both) | then `BHANDAR_EXTRAS=true` |
| bahi | Chart of accounts import (`bulk/accounts`) | so Tally groups / ledgers can be created instead of mapped with `account_map` |

## Large imports — `aayaat-worker` Cloud Run Job

Up to `LARGE_IMPORT_ROWS` (50,000) pending rows the API instance commits in a background goroutine
(the service runs with `--no-cpu-throttling`). Above that, `commit` sets the job to `queued` and starts
an execution of the **`aayaat-worker` Cloud Run Job** (same image) with:

```
/app -job <job-id> -tenant <tenant-id>
```

Worker mode commits that one job, publishes `aayaat.import.completed` and exits. Run it locally the same way:

```bash
go run ./cmd/server -job <job-id> -tenant <tenant-id>
```

## Code layout

```
cmd/server/main.go        HTTP server + stale-job sweep, or worker mode with -job/-tenant
internal/sheet/           xlsx (excelize), xls (BIFF8 / HTML table / text), csv (UTF-8/16), Tally XML → tables;
                          Indian number / date / Dr-Cr parsing
internal/mapping/         target fields per kind, header aliases, fuzzy auto-mapping, header-row detection
internal/validate/        row → bulk record + errors / warnings, duplicate tracking, store and account resolution
internal/job/             workflow (service.go), commit + batch journal (commit.go), dry run, rollback,
                          mapping templates, SQL, HTTP handlers, Cloud Run Job trigger
internal/storage/         local disk / Cloud Storage drivers (put, open, delete)
internal/clients/         bulk POST with retries, payload shaping per endpoint
migrations/               import_jobs, import_rows, import_batches, mapping_templates, outbox
testdata/marg/            demo MargBooks exports: party master (.csv, HTML .xls), item master (.xlsx),
                          batch stock, bill-wise outstanding, sale register
testdata/tally/           demo Tally Prime masters + vouchers XML
```

All sample data is invented (demo company "Shree Ganesh Traders"); GSTINs are made up with valid checksums.

## Deploy (Cloud Run)

`cloudbuild.yaml` tests, builds with `mool` as a build context, pushes, then deploys **two** things from
the same image:

1. service `aayaat` — ingress internal, IAM auth, `min-instances=0`, 2 GiB memory, `--no-cpu-throttling`,
   `STORAGE_DRIVER=gcs`, `GCS_BUCKET=${_BUCKET}`, `WORKER_JOB_NAME=aayaat-worker`
2. job `aayaat-worker` — 2 vCPU / 4 GiB, 1 h task timeout, no retries

Secrets: `aayaat-database-url`, `jwt-secret`, `service-secret`.
IAM for the `aayaat` service account: `roles/storage.objectAdmin` on the files bucket (upload, read, delete
rejected uploads), `roles/run.developer` on the `aayaat-worker` job (to start executions with overrides),
`roles/pubsub.publisher`, and `roles/run.invoker` on bahi and bhandar.

```bash
gcloud builds submit --config cloudbuild.yaml --substitutions=_ENV=dev,_BUCKET=maapan-dev-files,SHORT_SHA=$(git rev-parse --short HEAD)
```

## TODO

- Busy and Vyapar exports: both export Excel, which the generic path reads with manual mapping; add their
  header aliases and a saved template each once real exports are at hand.
- Tally outstanding as at the cut-over date: the XML gives *opening* bill allocations of the Tally books;
  for bills open on the migration date export Tally's *Bills Receivable / Payable* report to Excel and
  import it as `open_bills`.
- Invoice line items (per-item quantities) — history is kept at invoice level.
- Resumable/chunked uploads for files above 50 MB (signed GCS upload URL).
- An items row whose opening stock failed stays `failed` while the item exists in bhandar; rollback skips it.
