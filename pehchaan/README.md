# pehchaan (पहचान) — Identity Service

"Pehchaan" means *identity*. It decides **who you are** and **which business/store you act in**:

- **Passwordless OTP login** over SMS or WhatsApp (sent through `soochna`)
- **JWT access tokens** (15 min, HS256, the `mool/auth` claims) with a `kid` header and two live secrets for rotation (`JWT_SECRET` signs, `JWT_SECRET_PREVIOUS` still verifies) + **rotating refresh tokens** (30 days, Redis, reuse detection revokes the whole family)
- **OTP hashes** keyed by an HKDF-derived key (from `OTP_SECRET`, else `JWT_SECRET`) — never the JWT secret itself; wrong-attempt counting is one atomic Lua script
- **User invites** by phone with role + stores, **POS device registration** (device_id → store, revocable) and an **audit log** of every role/store/membership change (before → after)
- **Tenants** (businesses), **stores** (locations/godowns), **users** (one per phone) and **memberships** (role + allowed stores per business)
- **GST registrations**: every GSTIN of a business (or its unregistered identity) — exactly one primary per business, every store bills under exactly one (multi-state / multi-GSTIN businesses)
- **Onboarding**: a new phone creates its business, primary registration, first store and super_admin membership in one call
- **RBAC roles**: `super_admin`, `store_manager`, `cashier`, `accountant` (permission matrix lives in `mool/auth/rbac.go`)
- Audit log of logins and every user/store/tenant change

| | |
|---|---|
| Port (local) | `7001` |
| Database | `maapan_pehchaan` + Redis |
| Publishes | `pehchaan.tenant.created` (→ bahi, bhandar, kar seed defaults) |
| Subscribes | – |
| Calls | `soochna` `POST /internal/v1/messages` (OTP) |
| Gateway prefix | `https://api.maapan.in/api/pehchaan/...` |
| First deployed | **Week 3** (OTP Auth & RBAC Security) |

## Run locally

Prerequisites: Go 1.25+, Docker, the `mool` repo next to this one.

```bash
cd ../sanchalan && docker compose up -d postgres redis && cd ../pehchaan
make db-create
cp .env.example .env         # OTP_DEV_FIXED=123456 → every OTP is 123456 and is logged
make run                     # → http://localhost:7001/healthz
```

`soochna` is optional locally: if it is down the OTP is only logged (local env only — in cloud the request fails).

### Log in end-to-end

```bash
# 1. request OTP
curl -s localhost:7001/v1/auth/otp/request -d '{"phone":"9876543210","channel":"sms"}'

# 2. verify — a brand-new phone gets needs_onboarding + onboarding_token
curl -s localhost:7001/v1/auth/otp/verify -d '{"phone":"9876543210","otp":"123456"}'

# 3. create the business (Authorization = onboarding_token)
curl -s localhost:7001/v1/onboarding -H "Authorization: Bearer $ONBOARDING_TOKEN" -d '{
  "business_name":"Sharma General Store","gstin":"27AAPFU0939F1ZV","state_code":"27",
  "store_name":"Andheri Counter","owner_name":"Kartik Sharma",
  "address":"12 SV Road","city":"Mumbai","pin":"400053"}'
# → access_token, refresh_token, store_id, permissions
# city + pin go on the primary registration: the IRP rejects e-invoices without them

# 4. refresh before the 15-minute access token expires
curl -s localhost:7001/v1/auth/refresh -d '{"refresh_token":"..."}'
```

## API

Via the gateway prefix routes with `/api/pehchaan`. Auth routes and onboarding are **public** on `dwaar`.

| Method & path | Auth / permission | Purpose |
|---|---|---|
| `POST /v1/auth/otp/request` | public | `{phone, channel: sms\|whatsapp}` → 202. 3/min per phone, 20/10 min per IP |
| `POST /v1/auth/otp/verify` | public | `{phone, otp, tenant_id?}` → session, or `needs_onboarding` + `onboarding_token`. 5 wrong tries → 15 min lock |
| `POST /v1/auth/refresh` | public | `{refresh_token}` → new pair (old token is burned; replay revokes the family) |
| `POST /v1/auth/logout` | public | `{refresh_token}` → 204 |
| `POST /v1/auth/switch` | JWT | `{tenant_id, store_id?}` → session for another business / store |
| `POST /v1/onboarding` | onboarding or access token | Create business + primary registration + first store + super_admin. Optional `address, city, pin` (6 digits) |
| `GET /v1/me` | JWT | user, tenant, role, permissions, memberships |
| `GET /v1/tenant` · `PUT /v1/tenant` | JWT · `settings:manage` | Business profile (GSTIN validated). GSTIN/state/address are carried to the primary registration |
| `GET /v1/registrations` · `GET /v1/registrations/{id}` | JWT | GST registrations (primary first, `store_count` each). `?gstin=` filters |
| `POST /v1/registrations` | `settings:manage` | `{gstin?, legal_name?, trade_name?, state_code?, address?, city?, pin?, phone?, email?, composition?, einvoice_applicable?, is_primary?}` — GSTIN checksum, state = GSTIN state, PIN 6 digits; `is_primary:true` demotes the old primary |
| `PUT /v1/registrations/{id}` | `settings:manage` | Same body (omitted `is_primary`/`status` keep their value). The primary cannot be demoted (promote another) or deactivated; `status:"inactive"` is refused (409) while stores bill under it. Primary changes are copied to `tenants` |
| `DELETE /v1/registrations/{id}` | `settings:manage` | Soft delete = deactivate (same rules); invoices keep pointing at it |
| `GET /v1/stores` · `GET /v1/stores/{id}` | JWT (scoped roles see their stores) | Stores (with `registration_id`) |
| `POST /v1/stores` · `PUT /v1/stores/{id}` | `settings:manage` | Create/update store. `registration_id` (must be this business's, active) — on create defaults to the registration of the store's `gstin`, else the primary; on update empty keeps it. A store's `gstin` must be empty or its registration's; `state_code` defaults to the registration's and must equal it for a registered GSTIN |
| `GET /v1/users` | `user:manage` | Members of the business |
| `POST /v1/users` | `user:manage` | Invite `{phone, name, role, store_ids}` (store-scoped roles need ≥ 1 store of this business) — the invitee logs in with OTP on that phone and lands in this business. 409 if already an active member; a removed member is re-activated with the new role/stores |
| `PATCH /v1/users/{id}` | `user:manage` | `{name?, role?, store_ids?, active?}` (cannot demote the last super_admin or yourself) |
| `DELETE /v1/users/{id}` | `user:manage` | Deactivate membership |
| `GET /v1/users/{id}/audit` | `user:manage` | That member's `user.*` audit entries, newest first |
| `GET /v1/audit?target=&action=&actor=&before=&limit=` | `user:manage` | Audit log of the business, newest first. `action` exact or a prefix ending in `.` (`user.`, `device.`); `before` = the `next_before` of the previous page; limit ≤ 200 (default 50). Entry: `{id, actor_id, actor_name, action, target, details, ip, created_at}` |
| `GET /v1/devices?store_id=&status=` | `settings:manage`, or store_manager (own stores) | Registered POS terminals |
| `GET /v1/devices/{id}` | same | One terminal |
| `POST /v1/devices` | same | Register `{device_id, store_id, name?, platform?}` (`device_id` 1–128 of `A-Z a-z 0-9 - _ . :`; platform `android\|ios\|windows\|linux\|macos\|web\|other`). 409 while the device is active in this business — revoke first to move it |
| `DELETE /v1/devices/{id}` | same | Revoke (row kept as history; the device_id can be registered again) |
| `GET /internal/v1/devices/{device_id}` | service secret + `X-Tenant-ID` | The tenant's **active** registration of that device (`store_id`…), 404 if unknown/revoked; records `last_seen_at` (aadesh/POS check) |
| `GET /internal/v1/stores/{id}` | service secret | `{id, tenant_id, name, code, state_code, gstin, address, phone, active, registration_id, legal_name, trade_name, city, pin, composition, einvoice_applicable, registration_primary}` — `gstin`/`state_code`/`address` come from the registration when the store has none (aadesh, kar, bhandar…) |
| `GET /internal/v1/registrations/{id}` | service secret | One registration (an `X-Tenant-ID`, when sent, must own it) |
| `GET /internal/v1/registrations?tenant_id=&gstin=` | service secret | `{items:[registration]}`; tenant from the query or `X-Tenant-ID` (kar returns per GSTIN) |
| `GET /internal/v1/tenants/{id}` | service secret + `X-Tenant-ID` = `{id}` | `{id, business_name, gstin, state_code, owner_phone}` (soochna, kar, buddhi). Another tenant → 404; no header → 403 (`INTERNAL_TENANT_HEADER=optional` tolerates a missing header, never a mismatch). `mool/svc` sends the header from the caller's principal |
| `GET /healthz`, `/readyz` | none | Probes (readyz pings Postgres + Redis) |

Role/store changes take effect on the user's next refresh (≤ 15 minutes) — refresh re-reads the membership.

## Redis keys

| Key | TTL | Holds |
|---|---|---|
| `pehchaan:otp:<phone>` | 5 min | HMAC(HKDF-SHA256(`OTP_SECRET` or `JWT_SECRET`, "otp-hmac/v1"), phone\|otp) |
| `pehchaan:otp:att:<phone>` / `:lock:<phone>` | 5 / 15 min | wrong-attempt counter / lock. Check + count + lock is one Lua script (`EVALSHA`); on a server without scripting (Garnet without `--lua`) the attempt is counted with `INCR` *before* comparing — equally race-free |
| `pehchaan:rl:*` | 1–10 min | rate-limit windows |
| `pehchaan:rt:<sha256>` | 30 days | refresh token → session JSON |
| `pehchaan:rtu:<sha256>` | 30 days | burned token → family (reuse detection) |
| `pehchaan:rtf:<family>` | 30 days | family alive marker (delete = revoke all) |

## Keys and rotation

| Variable | Use |
|---|---|
| `JWT_SECRET` | Signs access and onboarding tokens; `kid` = first 16 hex of SHA-256("maapan/jwt-kid/v1:" + secret). Claims, issuer and lifetimes are exactly `mool/auth.Issue`'s, and `mool/auth.Parse` ignores `kid`, so every service keeps verifying unchanged |
| `JWT_SECRET_PREVIOUS` | Still accepted by pehchaan (and dwaar) during a rotation; picked by `kid`, else both are tried (tokens other services minted without `kid`) |
| `OTP_SECRET` | Root of the OTP HMAC key (HKDF). Empty = derived from `JWT_SECRET`; set it in cloud so a JWT rotation does not void OTPs in flight |
| `INTERNAL_TENANT_HEADER` | `required` (default) or `optional` for `/internal/v1/tenants/{id}` |

Rotation: `JWT_SECRET_PREVIOUS=<old>`, `JWT_SECRET=<new>` on pehchaan and dwaar (then the other services), wait 15 minutes
(access-token lifetime; onboarding tokens 30), remove `JWT_SECRET_PREVIOUS`. Services on plain `mool/auth` accept only the
current secret, so for those 15 minutes they 401 old tokens and the app refreshes.

Demo mode is unchanged: with `APP_ENV=local` and `OTP_DEV_FIXED=123456` every OTP is that code.

## Audit actions

`login`, `onboarding`, `user.invite`, `user.reactivate`, `user.role_change`, `user.update` (stores/name), `user.deactivate`
(details: `before`/`after` `{role, store_ids, active}` and `changes`), `device.register`, `device.revoke`, plus the
existing store/registration/tenant entries.

## Code layout

```
cmd/server/main.go            wiring
internal/identity/            model, SQL, service (login, onboarding, users, stores), handlers
internal/identity/registrations.go  GST registrations, store ↔ registration rules, internal store lookup
internal/identity/otpcheck.go atomic OTP check (Lua script + no-script fallback)
internal/identity/devices.go  POS device registration
internal/identity/audit.go    audit log reads, membership before/after
internal/identity/authmw.go   user auth middleware on the keyring
internal/otp/                 phone normalisation, OTP generate/hash, HKDF key (pure, tested)
internal/tokens/              refresh-token store with rotation, onboarding JWT, keyring (kid + previous secret) (tested)
migrations/                   tenants, stores, users, memberships, audit_log, outbox;
                              0002 registrations (backfill: one primary per tenant from tenants,
                              one more per distinct store GSTIN, every store linked);
                              0003 devices + audit_log (tenant, target) index
```

Tests: `TEST_DATABASE_URL` runs the Postgres tests, `TEST_REDIS_URL` (Redis or Garnet, with or without `--lua`) the OTP
script and demo-login tests; both skip when unset.

## Deploy (Cloud Run)

`cloudbuild.yaml`: test → image (with `mool` build context) → `gcloud run deploy`:

- ingress **internal**, IAM auth; `min-instances=1` (login must be instant), `--no-cpu-throttling` for the outbox relay
- Direct VPC egress to reach Memorystore Redis and Cloud SQL private IP
- secrets: `pehchaan-database-url`, `redis-url`, `jwt-secret`, `service-secret` (add `otp-secret` → `OTP_SECRET`, and `jwt-secret-previous` → `JWT_SECRET_PREVIOUS` only while rotating)
- **Never** set `OTP_DEV_FIXED` outside local (it is ignored unless `APP_ENV=local` anyway)

Deploy order: `soochna` → `pehchaan` → `dwaar` (Week 3).
