# Phase 3 contracts — GST compliance and multi-GSTIN

Three tracks build in parallel. This file is the agreement between them; if a
track needs to change an interface here, it changes this file in the same
commit and says so in its report.

## 1. Registrations (pehchaan owns)

A **registration** is one GSTIN of a business (or the business's unregistered
identity). A tenant has one or more; every store belongs to exactly one.

```text
registrations
  id UUID PK, tenant_id UUID, gstin TEXT NULL (NULL = unregistered), legal_name TEXT, trade_name TEXT,
  state_code TEXT NOT NULL, address TEXT, city TEXT, pin TEXT (6 digits), phone TEXT, email TEXT,
  composition BOOLEAN, einvoice_applicable BOOLEAN,  -- AATO > ₹5 crore, set by the owner
  is_primary BOOLEAN (exactly one per tenant), status active|inactive, created_at, updated_at
  UNIQUE (tenant_id, gstin) WHERE gstin IS NOT NULL
stores.registration_id UUID NOT NULL REFERENCES registrations(id)
```

Migration: one primary registration per tenant from `tenants` (gstin, state, address);
any store whose own `gstin` differs from the tenant's gets an extra registration;
every store is linked.

Public API (gateway `/api/pehchaan`): `GET /v1/registrations`, `POST /v1/registrations`,
`GET|PUT /v1/registrations/{id}` (settings:manage), and `PUT /v1/stores/{id}` accepts
`registration_id`. Onboarding input gains `pin` and `city` (the IRP rejects e-invoices
without them).

Internal API — **the existing store lookup gains fields** (old fields keep their meaning;
`gstin`, `state_code`, `address` now come from the registration when the store has none):

```text
GET /internal/v1/stores/{id} → {id, tenant_id, name, code, state_code, gstin, address, phone,
   registration_id, legal_name, trade_name, city, pin, composition, einvoice_applicable, registration_primary}
GET /internal/v1/registrations/{id} → registration
GET /internal/v1/registrations?tenant_id= (or X-Tenant-ID) → {items:[registration]}
```

## 2. aadesh (owns invoices)

- **Numbering per registration.** The shared series key is the registration: a store whose
  registration is the tenant's primary keeps the existing tenant-wide series (key = nil UUID,
  so nothing renumbers); any other registration has its own series (key = registration_id).
  Store-specific prefixes (numbering_settings) still override. Invoice uniqueness becomes
  `(tenant_id, registration_key, doc_type, fy, number)`; two GSTINs may both issue INV/26-27/00001.
- `invoices.registration_id` stored on every document (from the store lookup); `store_gstin`
  already exists and is now the registration's GSTIN.
- Purchase lines accept `itc_eligibility`: `eligible` (default) | `ineligible_17_5` (blocked
  credit, Sec 17(5)) | `ineligible_other` (personal use, exempt supplies, …). Stored, returned
  on lines, and carried on event lines (`events.InvoiceLine` gets the field).
- Internal list `GET /internal/v1/invoices` gains filters `registration_id`, `store_gstin`
  and `include=lines` already exists. Lines carry `tax_category`, the invoice carries
  `supply_category`, `reverse_charge`, `party_gstin`, `place_of_supply`, `store_gstin`.
- Subscribes `kar.einvoice.generated` and `kar.eway.generated` (payloads in
  `mool/events/payloads.go`): store `irn`, `ack_no`, `ack_date`, `signed_qr`,
  `einvoice_status` (generated|cancelled|failed) + `einvoice_error`; `ewb_no`, `ewb_date`,
  `ewb_valid_upto`, `ewb_status`. All returned on `GET /v1/invoices/{id}`.
- An invoice with a generated IRN cannot be cancelled in aadesh until the IRN is cancelled
  (409 with a message pointing to kar's cancel endpoint).

## 3. kar compliance (track B)

- `kar/internal/gsp`: provider-agnostic `Client` interface (GSTIN search, IRN generate /
  cancel / get-by-doc, e-way generate / Part-B / extend / cancel / get, GSTR-2B fetch).
  Drivers: `simulator` (default outside prod; deterministic, follows the official IRN rule
  IRN = SHA-256 of `GSTIN + FY + DocType + DocNo`, signs the QR as a JWS with a local key,
  validates the payload like the IRP and returns NIC-style error codes) and `http` (a
  configurable GSP driver: OAuth client-credentials + per-GSTIN API user headers; paths
  configurable; marked "verify against your GSP's sandbox"). Selected by `GSP_PROVIDER`.
- Per-GSTIN portal API users (e-invoice / e-way usernames and passwords) stored AES-GCM
  encrypted in kar under `CREDENTIALS_KEY` (same rules as lenden).
- HSN master becomes effective-dated (`hsn_rates`: code, description, rate_bp, cess_bp,
  cess_per_unit_paise, valid_from, valid_to, condition, source). The 69 seed rows migrate
  with `source='seed-unverified'`; real data enters through `POST /v1/hsn/import` (CSV).
  Nobody invents rates.
- e-Invoice: schema 1.1 payload from the aadesh invoice + store registration; supply types
  B2B/SEZWP/SEZWOP/EXPWP/EXPWOP/DEXP from `supply_category`; RCM flag; applicability =
  registration `einvoice_applicable` and a registered buyer or export/SEZ. Auto-generated
  on `aadesh.invoice.created`/`aadesh.note.created`; manual generate/cancel (24 h) endpoints;
  publishes `kar.einvoice.generated`.
- e-Way: generate (from IRN when present, else standalone), Part-B update, extend, cancel;
  validity 1 day per 200 km (ODC 20 km), state-specific intra-state thresholds, 180-day rule,
  BH-series vehicle numbers; publishes `kar.eway.generated`.
- kar owns `kar/internal/api/handlers.go`; it **removes** the old `/v1/returns/gstr1` and
  `/v1/returns/gstr3b` routes and handlers so track C can register them.

## 4. kar returns (track C)

- Lives in `kar/internal/returns` and new `kar/internal/recon`; registers its routes from
  `kar/internal/returns/routes.go` via `returns.Register(mux, v, deps)` (main.go wiring is
  done after both tracks).
- Returns are **per GSTIN** (`?gstin=` must be one of the tenant's registrations; default
  primary) and read invoices filtered by `store_gstin`.
- GSTR-1: B2B (incl. RCM, SEZ, deemed export), B2CL, B2CS, CDNR, CDNUR, EXP (WPAY/WOPAY),
  Table 8 (nil/exempt/non-GST split inter/intra × registered/unregistered), HSN Table 12
  split B2B/B2C, Table 13 documents; output in the GSTN offline-tool JSON shape.
- GSTR-3B: 3.1(a)–(e), 3.2, 4 (A)(3) RCM, (A)(5) other ITC, (B) reversals incl. 17(5),
  (D) ineligible, 5 exempt/nil/non-GST inward, set-off per Rule 88A.
- GSTR-2B / IMS: import the portal's 2B JSON (and fetch through `gsp.Client` when a GSP is
  configured), match against purchase bills (supplier GSTIN + normalised invoice number +
  date + amounts with ₹1 tolerance), record IMS actions (accept / reject / pending) and
  export them in the IMS upload shape.
