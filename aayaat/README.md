# aayaat (आयात) — Import & Migration Service

"Aayaat" means *import*. It moves a shop's existing books into Taraju — typically from **MargBooks**
Excel exports, but any `.xlsx` / `.csv` works:

| Kind | Goes to | Typical MargBooks export |
|---|---|---|
| `parties` | bahi `POST /internal/v1/bulk/parties` | Party / Ledger master with opening balance |
| `opening_balances` | bahi `POST /internal/v1/bulk/opening-balances` | Trial balance / ledger balances |
| `items` | bhandar `POST /internal/v1/bulk/items` | Item master (HSN, MRP, sale/purchase rate, GST %) |
| `opening_stock` | bhandar `POST /internal/v1/bulk/opening-stock` | Stock / batch-wise stock with expiry |

Flow: **upload → preview & auto-mapping → validate (row-level errors) → commit (batches of 500)**,
then `aayaat.import.completed` is published so `soochna` can tell the owner.

| | |
|---|---|
| Port (local) | `7007` |
| Database | `taraju_aayaat` |
| Publishes | `aayaat.import.completed` (via outbox) |
| Subscribes | — |
| Calls | `bahi` (bulk parties / opening balances), `bhandar` (bulk items / opening stock), Cloud Storage, Cloud Run Admin API |
| Gateway prefix | `https://api.taraju.in/api/aayaat/...` |
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
make test
```

`commit` needs `bahi` (parties, balances) or `bhandar` (items, stock) running. Upload, preview,
mapping and validation work on their own.

### Walk-through

```bash
TOKEN=...   # access token of a super_admin or accountant (import:run)
H="Authorization: Bearer $TOKEN"

# 0. (optional) blank template
curl -s localhost:7007/v1/imports/templates/parties -H "$H" -o parties-template.csv

# 1. upload
curl -s localhost:7007/v1/imports -H "$H" -F kind=parties -F file=@PartyMaster.xlsx -F as_of=2026-04-01
#   → {"id":"<job>","status":"uploaded",...}

# 2. preview: sheets, detected header row (title rows skipped), 20 sample rows, suggested mapping
curl -s localhost:7007/v1/imports/<job>/preview -H "$H"

# 3. confirm / adjust the mapping (field → column header)
curl -s -X PUT localhost:7007/v1/imports/<job>/mapping -H "$H" -d '{
  "sheet": "Sheet1",
  "mapping": {"name":"Party Name","gstin":"GSTIN No","phone":"Mobile","opening_balance":"Opening Bal","dr_cr":"Dr/Cr","type":"Group"},
  "params": {"as_of":"2026-04-01","notify_phone":"+919876543210"}
}'

# 4. validate → counts, Dr/Cr reconciliation, first 20 errors
curl -s -X POST localhost:7007/v1/imports/<job>/validate -H "$H"
curl -s "localhost:7007/v1/imports/<job>/errors?format=csv" -H "$H" -o errors.csv   # fix in Excel, re-upload

# 5. commit (202) and poll
curl -s -X POST localhost:7007/v1/imports/<job>/commit -H "$H"
curl -s localhost:7007/v1/imports/<job> -H "$H"     # status running → completed, imported / failed counts
```

## API

All routes need `Authorization: Bearer <access token>` with **`import:run`** (super_admin, accountant);
via the gateway prefix them with `/api/aayaat`.

| Method & path | Purpose |
|---|---|
| `POST /v1/imports` | multipart: `file` (.xlsx/.csv, ≤ 50 MB), `kind`, optional `as_of`, `store_id` (opening_stock), `notify_phone` |
| `GET /v1/imports?kind=&limit=&offset=` | Jobs, newest first |
| `GET /v1/imports/{id}` | Job with counts and (parties / balances) Dr-Cr reconciliation |
| `GET /v1/imports/{id}/preview?sheet=` | Sheets, header row, headers, 20 rows, `suggested_mapping`, field list |
| `PUT /v1/imports/{id}/mapping` | `{sheet, header_row (0=auto), mapping:{field:header}, params}` — clears earlier validation |
| `POST /v1/imports/{id}/validate` | Validate every row → `{job, sample_errors}` |
| `GET /v1/imports/{id}/errors[?format=csv]` | Invalid + failed rows with messages and original values |
| `GET /v1/imports/{id}/rows?status=valid\|invalid\|imported\|failed` | Stored rows |
| `POST /v1/imports/{id}/commit` | 202; pushes valid rows (also resumes a `failed` job) |
| `GET /v1/imports/templates/{kind}` | CSV template with headers and an example row |
| `GET /healthz`, `GET /readyz` | Probes |

### Job states

`uploaded → mapped → validated → running (or queued → running) → completed | failed`.
A failed or interrupted commit keeps already-imported rows; committing again continues with the rest
(bahi/bhandar upsert on `external_ref`, so a re-sent row updates instead of duplicating).

### What validation checks

- **Header detection**: MargBooks puts company name and report title above the table; the row with the most recognised headers in the first 15 is used.
- **Auto-mapping**: fuzzy match (aliases, prefixes, edit distance) of headers such as *Party Name, GSTIN No, Opening Bal, Dr/Cr, Item Name, HSN Code, MRP, Sale Rate, Purc Rate, GST %, Batch, Expiry, Qty*.
- **Numbers**: Indian grouping `1,23,456.78`, `₹`/`Rs.` prefixes, `(1,200.00)` and `1200-` negatives, Excel scientific notation — parsed straight to paise without float error.
- **Dr/Cr**: `1,200.50 Dr`, `Cr 300`, a separate Dr/Cr column, or a negative number (= Cr). Unspecified: Dr for customers, Cr for suppliers.
- **GSTIN** checksum (`mool/gst`), state must match the GSTIN's state, phone → `+91XXXXXXXXXX`, HSN 4/6/8 digits, GST % must be a GST slab, sale rate ≤ MRP, dates `dd/mm/yyyy`, Excel serials and MargBooks expiry `MM/YY` (→ month end).
- **Duplicates within the file**: parties by name / GSTIN / code, items by name / barcode / code, stock by item + batch.
- **Reconciliation**: total Dr vs Cr of opening balances and the difference (a non-zero difference usually means capital, cash or stock ledgers are missing).

## Large imports — `aayaat-worker` Cloud Run Job

Up to `LARGE_IMPORT_ROWS` (50,000) valid rows the API instance commits in a background goroutine
(the service runs with `--no-cpu-throttling`). Above that, `commit` sets the job to `queued` and starts
an execution of the **`aayaat-worker` Cloud Run Job** (same image) with:

```
/app -job <job-id> -tenant <tenant-id>
```

Worker mode commits that one job, publishes `aayaat.import.completed` and exits. Run it locally the same way:

```bash
go run ./cmd/server -job <job-id> -tenant <tenant-id>
```

If an API instance dies mid-commit, the job is marked `failed` ("interrupted") on the next start after
15 minutes without progress; commit again to resume.

## Code layout

```
cmd/server/main.go        HTTP server, or worker mode with -job/-tenant
internal/sheet/           xlsx (excelize streaming rows) + csv reader; Indian number/date/Dr-Cr parsing
internal/mapping/         target fields per kind, header aliases, fuzzy auto-mapping, header-row detection
internal/validate/        row → bulk record + errors, duplicate tracking
internal/job/             job workflow, SQL, HTTP handlers, Cloud Run Job trigger
internal/storage/         local disk / Cloud Storage drivers
internal/clients/         bulk POST with retries
migrations/               import_jobs, import_rows, outbox
```

## Deploy (Cloud Run)

`cloudbuild.yaml` tests, builds with `mool` as a build context, pushes, then deploys **two** things from
the same image:

1. service `aayaat` — ingress internal, IAM auth, `min-instances=0`, 2 GiB memory, `--no-cpu-throttling`,
   `STORAGE_DRIVER=gcs`, `GCS_BUCKET=${_BUCKET}`, `WORKER_JOB_NAME=aayaat-worker`
2. job `aayaat-worker` — 2 vCPU / 4 GiB, 1 h task timeout, no retries

Secrets: `aayaat-database-url`, `jwt-secret`, `service-secret`.
IAM for the `aayaat` service account: `roles/storage.objectAdmin` on the files bucket, `roles/run.developer`
on the `aayaat-worker` job (to start executions with overrides), `roles/pubsub.publisher`, and
`roles/run.invoker` on bahi and bhandar.

```bash
gcloud builds submit --config cloudbuild.yaml --substitutions=_ENV=dev,_BUCKET=taraju-dev-files,SHORT_SHA=$(git rev-parse --short HEAD)
```

## TODO

- Invoice history import (sales/purchase registers into aadesh) — needs an aadesh bulk endpoint.
- Legacy `.xls` (BIFF) files: currently rejected with "save as .xlsx".
- Resumable/chunked uploads for files above 50 MB (signed GCS upload URL).
