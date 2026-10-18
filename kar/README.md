# kar (कर) — Tax Service

"Kar" means *tax*. It owns GST compliance for Taraju:

- **GSTIN verification**: checksum + state first, then a live GSP lookup (legal name, trade name, status, address), cached 7 days
- **HSN/SAC master** with default GST rates (seeded starter set for retail)
- **Tax computation API** (`mool/gst`, the same engine `aadesh` runs in-process)
- **e-Way Bill JSON** in NIC bulk format v1.0.0621, with the ₹50,000 threshold check
- **e-Invoice JSON** in IRP INV-01 schema v1.1 (B2B invoices and credit notes)
- **GSTR-1** (B2B, B2CL, B2CS, CDNR, HSN summary, document summary) and **GSTR-3B** (3.1, 3.2, 4, and an ITC set-off computation), as JSON in GSTN offline-tool structure or CSV
- Per-tenant **tax settings** (state, GSTIN, composition, thresholds)

| | |
|---|---|
| Port (local) | `7003` |
| Database | `taraju_kar` |
| Publishes | — |
| Subscribes | `pehchaan.tenant.created` (seeds tax settings) |
| Calls | `aadesh` (invoices), `pehchaan` (store, tenant), `bahi` (party address/PIN), GSP API |
| Gateway prefix | `https://api.taraju.in/api/kar/...` |
| First deployed | **Week 4** (GSTIN verification with the accounts master); e-Way bill, e-Invoice and returns by **Week 8** |

## Run locally

Prerequisites: Go 1.25+, Docker (for Postgres), the `mool` repo checked out next to this one.

```bash
# 1. Postgres (+ Redis) from the sanchalan repo
cd ../sanchalan && docker compose up -d postgres redis && cd ../kar

# 2. database + env
make db-create
cp .env.example .env          # GSTIN_PROVIDER=checksum works with no GSP contract

# 3. run (migrations + HSN seed apply automatically)
make run                      # → http://localhost:7003/healthz
make test
```

e-Way bill, e-Invoice and returns read invoices from `aadesh` and store/tenant details from
`pehchaan`, so run those too for the full flow. GSTIN, HSN and tax compute work standalone.

### GSTIN provider

| `GSTIN_PROVIDER` | What you get | Needs |
|---|---|---|
| `checksum` (default) | `valid`, `state_code`, `source: "checksum_only"`; names/status empty | nothing |
| `gsp` | full taxpayer details from the GST portal via a GSP, cached 7 days | a **GSP contract** (Masters India, ClearTax, Cygnet, …) and `GSP_BASE_URL`, `GSP_CLIENT_ID`, `GSP_CLIENT_SECRET` |

The GSP driver calls `GSP_BASE_URL + GSP_SEARCH_PATH` with `client_id` / `client_secret` headers and
parses the standard GSTN "search taxpayer" payload (top level or under `data` / `result`). If the GSP
is down, kar answers from the checksum so billing is never blocked.

### Try it

```bash
TOKEN=...   # access token from pehchaan

curl -s localhost:7003/v1/gstin/verify -H "Authorization: Bearer $TOKEN" -d '{"gstin":"27AAPFU0939F1ZV"}'
curl -s "localhost:7003/v1/hsn?q=biscuit" -H "Authorization: Bearer $TOKEN"
curl -s localhost:7003/v1/eway-bills/<invoice-id>/json -H "Authorization: Bearer $TOKEN" \
  -d '{"trans_mode":"road","trans_distance":120,"vehicle_no":"MH12AB1234","to_pincode":411002}'
curl -s "localhost:7003/v1/returns/gstr1?period=102026&format=csv" -H "Authorization: Bearer $TOKEN" -o gstr1.csv
```

## API

All `/v1` routes need `Authorization: Bearer <access token>`; via the gateway prefix them with `/api/kar`.

| Method & path | Permission | Purpose |
|---|---|---|
| `POST /v1/gstin/verify` | `party:manage` | `{"gstin"}` → verification result |
| `GET /v1/hsn?q=&limit=` | any user | Search HSN/SAC master by code prefix or description |
| `GET /v1/hsn/{code}` | any user | One HSN/SAC code |
| `POST /v1/tax/compute` | any user | `{"store_state","place_of_supply","lines":[gst.Line]}` → GST summary |
| `GET /v1/settings` | `report:read` | Tenant tax settings |
| `PUT /v1/settings` | `settings:manage` | Update state, GSTIN, composition, `einvoice_enabled`, thresholds |
| `POST /v1/eway-bills/{invoice_id}/json` | `invoice:read` | Build NIC JSON. Body: `trans_mode` (road/rail/air/ship), `trans_distance`, `vehicle_no`, `transporter_id`, `trans_doc_no`, `trans_doc_date`, `from_pincode`, `to_pincode`. Returns `payload`, `warnings`, `below_threshold` |
| `GET /v1/eway-bills/{invoice_id}` | `invoice:read` | Last generated e-way JSON |
| `POST /v1/einvoices/{invoice_id}/json` | `gst:file` | Build IRP JSON (B2B only). Optional `seller_pincode`, `buyer_pincode` |
| `GET /v1/einvoices/{invoice_id}` | `gst:file` | Last generated e-invoice JSON |
| `GET /v1/returns/gstr1?period=MMYYYY[&format=csv][&gstin=]` | `report:read` | GSTR-1 |
| `GET /v1/returns/gstr3b?period=MMYYYY[&format=csv][&gstin=]` | `report:read` | GSTR-3B with ITC set-off |
| `POST /internal/v1/gstin/verify` | service secret | For bahi / aadesh / aayaat |
| `POST /internal/v1/tax/compute` | service secret | GST maths for other services |
| `POST /events` | service secret / IAM | Pub/Sub push endpoint |
| `GET /healthz`, `GET /readyz` | none | Probes |

### Rules encoded

- **e-Way bill**: outward sale `O/1/INV`, purchase `I/1/INV` (supplier's bill number), sales return `I/7/CNT`, purchase return `O/8/CHL`. Unregistered party → `URP`. Services (SAC 99xx) are left out of `itemList`. PIN codes are taken from addresses (`…, Pune 411001`) or the request. Below the tenant's threshold you still get the JSON plus a warning.
- **e-Invoice**: only B2B `sale` (INV) and `sale_return` (CRN, with `RefDtls` to the original). Unit price is derived from the assessable value so tax-inclusive POS prices reconcile.
- **GSTR-1**: registered buyers → B2B; unregistered inter-state above `b2cl_threshold_paise` (default ₹1,00,000) → B2CL; other unregistered → B2CS by POS and rate; credit notes to registered buyers → CDNR, to unregistered buyers netted in B2CS (listed in `warnings`). HSN summary is split by HSN **and** rate. Document summary is per number series, counting cancelled invoices.
- **GSTR-3B**: 3.1(a) taxable outward, 3.1(c) nil-rated (0% lines), 3.2 inter-state to unregistered by POS, 4(A)(5) ITC from purchases net of purchase returns, and set-off in the statutory order (IGST credit first; CGST and SGST never cross).
- One return per GSTIN: invoices are filtered to stores in the registration's state (pass `gstin=` for a second registration).

HSN default rates follow the GST 2.0 slabs (Sep 2025). They are defaults for the item master —
verify against the latest CBIC notification.

## Code layout

```
cmd/server/main.go            wiring, provider selection
internal/gstin/               Provider interface, GSP + checksum drivers, cached Service
internal/compliance/          e-Way bill + e-Invoice builders, date/UQC/PIN helpers (pure)
internal/returns/             GSTR-1, GSTR-3B builders, CSV writers (pure)
internal/clients/             aadesh / pehchaan / bahi calls
internal/api/                 HTTP handlers, tax settings, tenant.created subscriber
migrations/                   schema + HSN seed, embedded and applied at startup
```

`gstin_cache` and `hsn_codes` hold public reference data shared by all tenants and therefore have no
`tenant_id`; every other table is tenant-scoped.

## Deploy (Cloud Run)

`cloudbuild.yaml` tests, builds with `mool` as a build context, pushes to Artifact Registry and deploys:

- ingress **internal**, IAM auth required, `min-instances=0` (not on the POS hot path)
- secrets: `kar-database-url`, `jwt-secret`, `service-secret`, `gsp-client-id`, `gsp-client-secret`, `redis-url`
- set `_GSP_BASE_URL` on the trigger once the GSP contract is signed (until then deploy with `GSTIN_PROVIDER=checksum`)

```bash
gcloud builds submit --config cloudbuild.yaml --substitutions=_ENV=dev,SHORT_SHA=$(git rev-parse --short HEAD)
```

The `pehchaan.tenant.created` push subscription is created by `sanchalan/scripts/pubsub-setup.sh`.

## TODO

- Direct NIC / IRP API submission (IRN, EWB number) through the GSP once credentials exist; tables already have `irn` / `ewb_no`.
- CDNUR (credit notes against B2CL invoices) and exports (EXP) sections of GSTR-1.
