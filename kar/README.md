# kar (कर) — Tax Service

"Kar" means *tax*. It owns GST compliance for Taraju:

- **GSTIN verification**: checksum + state first, then a live taxpayer search through the GSP client
  (legal name, trade name, status, address), cached 7 days. A provider failure answers
  `source: "checksum_only"`, `portal_verified: false` — never "verified on the portal".
  Cancelled / Suspended GSTINs come back with `b2b_allowed: false`.
- **HSN/SAC master**, effective-dated (`hsn_rates`), with a CSV import and full-text search
- **Tax computation API** (`mool/gst`, the same engine `aadesh` runs in-process)
- **e-Invoice** (IRP INV-01 schema v1.1): IRN generation through a GSP — automatically on
  `aadesh.invoice.created` / `aadesh.note.created`, or on request — cancel within 24 h, signed QR
- **e-Way bill** (NIC): generate (from the IRN when there is one), Part-B update, extend, cancel;
  validity by distance, state-specific intra-state thresholds, 180-day rule
- **GSTR-1** per GSTIN and period (B2B, B2CL, B2CS, CDNR/CDNUR, exports, nil/exempt, HSN summary split
  B2B / B2C, document summary) as the offline-tool JSON or CSV; **GSTR-3B** with Rule 88A set-off and
  blocked credit shown in 4(A) and reversed in 4(B)(1)
- **GSTR-2B import / fetch, purchase matching and IMS** actions (accept / reject / pending)
- Per-tenant **tax settings** and per-GSTIN **GST portal API users** (AES-256-GCM)

| | |
|---|---|
| Port (local) | `7003` |
| Database | `taraju_kar` |
| Publishes (outbox) | `kar.einvoice.generated`, `kar.eway.generated` |
| Subscribes | `pehchaan.tenant.created` (seeds tax settings), `aadesh.invoice.created`, `aadesh.note.created` (auto e-invoice) |
| Calls | `aadesh` (invoices), `pehchaan` (store + registration, tenant), `bahi` (party address/PIN), GSP API |
| Gateway prefix | `https://api.taraju.in/api/kar/...` |

## Run locally

Prerequisites: Go 1.25+, Docker (for Postgres), the `mool` repo checked out next to this one.

```bash
# 1. Postgres (+ Redis) from the sanchalan repo
cd ../sanchalan && docker compose up -d postgres redis && cd ../kar

# 2. database + env
make db-create
cp .env.example .env          # GSP_PROVIDER=simulator works with no GSP contract

# 3. run (migrations apply automatically)
make run                      # → http://localhost:7003/healthz
make test                     # TEST_DATABASE_URL=postgres://…/taraju_test runs the DB tests too
```

e-Invoice, e-Way bill and returns read invoices from `aadesh` and store/registration details from
`pehchaan`, so run those too for the full flow. GSTIN, HSN and tax compute work standalone.

### GSP providers (`GSP_PROVIDER`)

| Value | What you get | Needs |
|---|---|---|
| `simulator` (default outside prod) | Deterministic, offline IRP / NIC / GSTN stand-in. State lives in kar's DB (`gsp_sim_*`), so restarts are consistent. **Refused when `APP_ENV=prod`** — nothing it issues is registered with the government. | nothing |
| `http` | A generic GST Suvidha Provider driver: OAuth2 client-credentials token (cached until expiry, refreshed once on 401), per-GSTIN API user headers, configurable base URL, paths and header names, request/response logging with secrets redacted (bodies only at `LOG_LEVEL=debug`). | a GSP contract and `GSP_BASE_URL`, `GSP_CLIENT_ID`, `GSP_CLIENT_SECRET`; the GSTIN's portal API users saved under `/v1/settings/gsp-credentials/{gstin}` |

> ⚠ **The `http` driver must be verified against your GSP's sandbox before production.** Every GSP
> (Masters India, ClearTax, Cygnet, IRIS, …) wraps the NIC APIs differently: the default paths in
> `.env.example` (`GSP_IRN_PATH`, `GSP_EWB_PATH`, …), the token endpoint, the header names
> (`GSP_HEADER_GSTIN/USERNAME/PASSWORD`) and the response envelope are modelled on the NIC API and
> on common GSP shapes (`{Status, Data, ErrorDetails, InfoDtls}`, `{data}`, `{results:{message}}`).
> Run the generate / duplicate (2150) / cancel / e-way flows in the sandbox and adjust the paths,
> headers and, if needed, `internal/gsp/http.go`'s envelope parser.

**Simulator behaviour** (what tests and local dev rely on):

- IRN = lowercase hex SHA-256 of `GSTIN + FY ("2026-27") + DocType (INV/CRN/DBN) + DocNo` (the official rule);
  AckNo is 15 digits; AckDt is the simulator clock (IST).
- SignedQR / SignedInvoice are compact JWS (HS256, key `GSP_SIM_KEY` or derived from `SERVICE_SECRET`) with the
  IRP's shape `{"data":"<json>","iss":…}`; the QR data has `SellerGstin, BuyerGstin, DocNo, DocTyp, DocDt,
  TotInvVal, ItemCnt, MainHsnCode, Irn, IrnDt`. Real IRP tokens are RS256-signed by NIC.
- Validates like the IRP: GSTIN checksums and state codes, 6/8-digit HSN, IsServc vs SAC, GST rate in force on the
  document date, CGST=SGST intra-state vs IGST inter-state, item and invoice totals (±₹1), 100-char addresses,
  3–50-char Loc, 6–12-digit phone, export buyer `URP/96/999999`, no future dates.
- The same document again → **2150** with the original IRN details; cancel after 24 h → **2270**; cancel with an
  active e-way bill → **2230**; duplicate e-way bill → **604**; document older than 180 days → **4010**.
  Codes are a subset modelled on the NIC list — callers switch on `gsp.Error.Kind`
  (`validation | duplicate | not_found | not_allowed | auth | unavailable`), not on numbers.
- E-way numbers are 12 digits; GSTIN search answers *Active*, or *Cancelled* when the 13th character is `9`,
  *Suspended* when it is `8`; `FetchGSTR2B` returns an empty but valid 2B JSON.

### GSTIN provider (`GSTIN_PROVIDER`)

| Value | What you get |
|---|---|
| `checksum` (default) | `valid`, `state_code`, `source: "checksum_only"`, `portal_verified: false`; names/status empty |
| `gsp` | taxpayer search through the `GSP_PROVIDER` client, cached 7 days; `portal_verified: true`, `status`, `b2b_allowed` |

### Try it

```bash
TOKEN=...   # access token from pehchaan

curl -s localhost:7003/v1/gstin/verify -H "Authorization: Bearer $TOKEN" -d '{"gstin":"27AAPFU0939F1ZV"}'
curl -s "localhost:7003/v1/hsn?q=biscuit&on=2025-09-01" -H "Authorization: Bearer $TOKEN"
curl -s -X POST localhost:7003/v1/einvoices/<invoice-id> -H "Authorization: Bearer $TOKEN"
curl -s localhost:7003/v1/eway-bills/<invoice-id> -H "Authorization: Bearer $TOKEN" \
  -d '{"mode":"1","distance_km":120,"vehicle_no":"MH12AB1234","transporter_name":"Speedy Roadways"}'
curl -s localhost:7003/v1/hsn/import -H "Authorization: Bearer $TOKEN" -H "Content-Type: text/csv" --data-binary @rates.csv
```

## API

All `/v1` routes need `Authorization: Bearer <access token>`; via the gateway prefix them with `/api/kar`.
Store-scoped roles (cashier, store manager) only see documents of their stores.

| Method & path | Permission | Purpose |
|---|---|---|
| `POST /v1/gstin/verify` | `party:manage` | `{"gstin"}` → verification result |
| `GET /v1/hsn?q=&on=YYYY-MM-DD&limit=` | any user | Search codes in force on `on` (today): digits → code prefix, words → full-text (GIN index) |
| `GET /v1/hsn/{code}?on=` | any user | One code with every rate in force on that date (`rates[]`, one per condition) |
| `POST /v1/hsn/import` | `settings:manage` + `HSN_IMPORT_TENANTS` | CSV upsert (see below) → `{rows, inserted, updated, closed, rejected, errors[]}` |
| `POST /v1/tax/compute` | any user | `{"store_state","place_of_supply","lines":[gst.Line]}` → GST summary |
| `GET /v1/settings` / `PUT /v1/settings` | `report:read` / `settings:manage` | Tenant tax settings (state, GSTIN, composition, `einvoice_enabled`, thresholds) |
| `GET /v1/settings/gsp-credentials` | `settings:manage` | GSTINs with saved portal API users (masked) |
| `GET\|PUT\|DELETE /v1/settings/gsp-credentials/{gstin}` | `settings:manage` | One GSTIN's users: PUT `{einvoice_username, einvoice_password, eway_username, eway_password}` (empty password keeps the stored one); GET is masked; the GSTIN must be one of the tenant's registrations |
| `POST /v1/einvoices/{invoice_id}` | `gst:file` | Generate the IRN now (idempotent; optional `{"export":{ShipBNo,ShipBDt,Port,ForCur,CntCode}}`) |
| `POST /v1/einvoices/{invoice_id}/cancel` | `gst:file` | `{reason_code 1-4, remark}` — only within 24 h of AckDt and with no active e-way bill |
| `GET /v1/einvoices/{invoice_id}` | `invoice:read` | Status, IRN, AckNo/AckDt, signed QR (+ decoded `qr`), error, payload |
| `GET /v1/einvoices?status=&from=&to=&gstin=&limit=&offset=` | `report:read` | GST centre list |
| `POST /v1/einvoices/{invoice_id}/json` | `gst:file` | The IRP JSON for download (not registered) |
| `POST /v1/eway-bills/{invoice_id}` | `invoice:read` | Generate: `{transporter_id, transporter_name, mode 1-4, vehicle_no, vehicle_type R\|O, distance_km, trans_doc_no, trans_doc_date, dispatch_from{…}, ship_to{…}}` — from the IRN when the invoice has one |
| `PUT /v1/eway-bills/{invoice_id}/part-b` | `invoice:read` | `{mode, vehicle_no, vehicle_type, trans_doc_no, trans_doc_date, from_place, from_state, reason_code 1-4, reason_remark}` |
| `POST /v1/eway-bills/{invoice_id}/extend` | `invoice:read` | `{from_place, from_state, from_pincode, remaining_distance_km, consignment_status M\|T, transit_type, reason_code, remark, mode, vehicle_no}` — 8 h either side of expiry |
| `POST /v1/eway-bills/{invoice_id}/cancel` | `invoice:cancel` | `{reason_code 1-4, remark}` — within 24 h of generation |
| `GET /v1/eway-bills/{invoice_id}` · `GET /v1/eway-bills?status=&from=&to=` | `invoice:read` · `report:read` | One bill (with history) / list |
| `POST /v1/eway-bills/{invoice_id}/json` | `invoice:read` | NIC bulk JSON for offline upload (warnings for threshold / 180 days) |
| `GET /v1/returns/gstr1?gstin=&period=MMYYYY&format=json\|csv&section=` | `report:read` | GSTR-1 for one registration (primary when `gstin` is empty) |
| `GET /v1/returns/gstr1/export?gstin=&period=` | `gst:file` | Clean offline-tool JSON for upload |
| `GET /v1/returns/gstr3b?gstin=&period=` | `report:read` | GSTR-3B tables with ITC set-off |
| `GET\|PUT /v1/returns/itc-opening` | `report:read` · `gst:file` | Opening electronic credit ledger balances |
| `POST /v1/returns/gstr2b/import` · `POST /v1/returns/gstr2b/fetch` | `gst:file` | Upload the portal's 2B JSON, or pull it through the GSP with the GSTIN's portal users |
| `GET /v1/returns/gstr2b?gstin=&period=&status=` | `report:read` | 2B documents matched to purchase bills |
| `PUT /v1/returns/ims/{doc_key}` · `POST /v1/returns/ims/accept-matched` | `gst:file` | IMS action on one document / accept every exact match |
| `GET /v1/returns/ims/export?gstin=&period=` | `gst:file` | IMS actions for upload |
| `POST /internal/v1/gstin/verify`, `POST /internal/v1/tax/compute` | service secret | For bahi / aadesh / aayaat |
| `GET /internal/v1/einvoices/{invoice_id}`, `GET /internal/v1/hsn/{code}`, `POST /internal/v1/hsn/import` | service secret | Internal reads; ops import |
| `POST /events` | service secret / IAM | Pub/Sub push endpoint |
| `GET /healthz`, `GET /readyz` | none | Probes |

### Events

- **Subscribes** `aadesh.invoice.created` and `aadesh.note.created`: a `sale` (INV) or `sale_return` (CRN) of a
  registration with `einvoice_applicable` whose buyer has a GSTIN — or any export / SEZ / deemed-export supply —
  is registered on the IRP. B2C, purchases and bills of supply are skipped. Purchase returns (DBN) are generated
  only on request. Idempotent through `processed_events`; the IRP's 2150 answer recovers the existing IRN.
  Validation / IRP rejections are stored as `failed` with the IRP code and message and are **never retried**;
  transient failures (GSP down, pehchaan unreachable) stay `pending` and are redelivered up to
  `EINVOICE_MAX_ATTEMPTS`, then fail.
- **Publishes** through the outbox, in the same transaction as the state change:
  `kar.einvoice.generated` (`events.EInvoicePayload`, status `generated | cancelled | failed`) and
  `kar.eway.generated` (`events.EWayPayload`, status `generated | updated | extended | cancelled | failed`).

### Rules encoded

- **Seller details** come from the store lookup's registration fields (`legal_name, trade_name, address, city,
  pin, state_code, gstin`); stores on a pre-registration pehchaan fall back to the tenant name and
  `tax_settings.einvoice_enabled`.
- **e-Invoice payload**: SupTyp from `supply_category` (`""`→B2B, `sez_with_payment`→SEZWP, `sez_without_payment`→SEZWOP,
  `export_with_payment`→EXPWP, `export_without_payment`→EXPWOP, `deemed_export`→DEXP); RegRev Y/N from
  `reverse_charge`; IgstOnIntra=Y for IGST on an intra-state (SEZ) supply; CRN/DBN carry `RefDtls` to the original
  invoice; HSN must be 6 or 8 digits (a 4-digit code is rejected with a message to fix the item master); phone is
  reduced to 6–12 digits; addresses split at 100 characters on a comma/space; every limit is counted in characters
  (Devanagari is never cut mid-character); `Loc` keeps house/sector numbers; exports use `URP / 96 / 999999`;
  specific cess goes to `CesNonAdvlAmt`; TCS to `OthChrg`.
- **e-Way bill**: outward sale `O/1/INV` (export `O/3`), purchase `I/1/INV`, sales return `I/7/CNT`, purchase return
  `O/8/CHL`; dispatch-from / ship-to set transaction type 2/3/4; vehicle numbers accept the standard format,
  Bharat series (`22BH1234AA`), temporary `TR…`/`TM…`, old `ABC1234`, defence and Bhutan/Nepal formats.
- **Validity** (Rule 138(10)): 1 day per 200 km or part (over-dimensional cargo: 20 km), each day ending at
  midnight IST of the day after generation; Part-A-only bills have no validity; extension 8 h either side of
  expiry, at most 360 days after generation; cancel within 24 h; documents older than **180 days** are refused.
- **Thresholds**: inter-state uses the tenant's `eway_threshold_paise` (₹50,000). Intra-state uses
  `compliance.IntraStateThresholdPaise` — **₹1,00,000 in Delhi (07), Rajasthan (08), Bihar (10), West Bengal (19),
  Maharashtra (27), Tamil Nadu (33)**, ₹50,000 elsewhere. ⚠ *Verify this table against each state's current
  notification before relying on it* (some limits apply to specific goods or within a city). The consignment value
  excludes exempt / nil / non-GST lines and services. Below the threshold the bill is optional (warning only).

### HSN master

`hsn_rates(code, description, kind, rate_bp, cess_bp, cess_per_unit_paise, valid_from, valid_to, condition, source)`.
A lookup on date *d* takes, per code and condition, the latest row with `valid_from ≤ d ≤ valid_to`.
The 69 Phase-1 starter rows were migrated **unchanged** as `source='seed-unverified'`, `valid_from=2017-07-01`
(`verified: false` in the API) — they are hints, not law. **kar never invents or edits a rate**: real data enters
only through the import:

```csv
code,description,rate_bp,cess_bp,cess_per_unit_paise,valid_from,valid_to,condition
190590,Biscuits and cookies,1800,0,0,2017-07-01,2025-09-21,
190590,Biscuits and cookies,500,0,0,2025-09-22,,
640399,Footwear with leather uppers,500,0,0,2025-09-22,,sale value up to Rs 2500 per pair
```

Codes must be 4, 6 or 8 digits; `rate_bp` must be a slab in force on `valid_from` (`mool/gst.ValidRate`, so 12 %
after 21 Sep 2025 or 40 % before 22 Sep 2025 are rejected). Rows upsert on `(code, valid_from, condition)`; a new
`valid_from` closes the previous open-ended row the day before. Bad rows are reported by line and the rest are
imported. The master is shared by all tenants, so in prod only `HSN_IMPORT_TENANTS` (or `/internal`) may import.
`hsn_codes` remains as a read-only view of today's unconditional rates.

## Code layout

```
cmd/server/main.go            wiring, GSP / GSTIN provider selection
internal/gsp/                 Client interface, typed NIC-style errors, simulator (+ DB/memory store), http driver, JWS
internal/compliance/          e-Invoice / e-Way builders, rule tables (pure) + Engine (lifecycle, events, outbox)
internal/hsn/                 effective-dated HSN master: search, dated lookup, CSV import
internal/creds/               AES-256-GCM sealed per-GSTIN portal API users (same scheme as lenden)
internal/gstin/               GSTIN verification over gsp.Client, with cache
internal/returns/, recon/     GSTR-1 / 3B builders, 2B matching and IMS
internal/clients/             aadesh / pehchaan / bahi calls
internal/api/                 HTTP handlers, tax settings, credentials, event router
migrations/                   schema + data migrations, embedded and applied at startup
```

`gstin_cache`, `hsn_rates` and the `gsp_sim_*` tables hold public / simulated reference data shared by all tenants
and therefore have no `tenant_id`; every other table is tenant-scoped.

## Deploy (Cloud Run)

`cloudbuild.yaml` tests, builds with `mool` as a build context, pushes to Artifact Registry and deploys:

- ingress **internal**, IAM auth required, `min-instances=0` (not on the POS hot path)
- secrets: `kar-database-url`, `jwt-secret`, `service-secret`, `gsp-client-id`, `gsp-client-secret`,
  `kar-credentials-key` (32 bytes: `openssl rand -hex 32`), `redis-url`
- `_GSP_PROVIDER` is `simulator` for dev; set `http` and `_GSP_BASE_URL` on staging/prod triggers once the GSP
  contract is signed and the sandbox verified (prod refuses to start with the simulator)

```bash
gcloud builds submit --config cloudbuild.yaml --substitutions=_ENV=dev,SHORT_SHA=$(git rev-parse --short HEAD)
```

Push subscriptions (`pehchaan.tenant.created`, `aadesh.invoice.created`, `aadesh.note.created` → kar) are created by
`sanchalan/scripts/pubsub-setup.sh`.

## TODO

- Verify the `http` driver against the chosen GSP's sandbox (paths, headers, envelope, IRP encryption handled by the GSP).
- Consolidated e-way bills, e-way bills for job work / SKD and B2C dynamic QR (> ₹500 crore) are not built.
- Applicability is the owner's `einvoice_applicable` flag; AATO is not computed from turnover yet.
- Load the full CBIC HSN schedule through the import (no rates ship with kar).
