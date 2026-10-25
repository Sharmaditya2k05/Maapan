# dukaan (दुकान) — Maapan Web App & Offline POS

"Dukaan" means *shop*. This is the Next.js frontend: the cashier's POS counter, invoices, purchases,
party ledgers, items & stock, GST reports, the MargBooks importer, manufacturing, AI Q&A and settings.
It is an installable **PWA** that keeps billing during internet outages.

| | |
|---|---|
| Port (local) | `3000` |
| Talks to | **only** the `dwaar` gateway — `NEXT_PUBLIC_API_URL` (default `http://localhost:8000/api`) |
| Stack | Next.js 15 (App Router) · React 19 · TypeScript · Tailwind CSS v4 · idb · recharts · lucide-react |
| First deployed | **Week 3** (login / OTP) · **Week 5** (POS billing) · grows each sprint |
| Cloud Run | public service `dukaan`, `--ingress=all`, `--min-instances=1`, behind the HTTPS load balancer |

Brand (from the original prototype, now retired: navy `#062C60`, green `#0EA673`, gold accent,
Outfit headings). Brand images were copied into `public/brand`.

## Run locally

```bash
npm install
cp .env.example .env.local     # already done once; points at http://localhost:8000/api
npm run dev                    # http://localhost:3000
```

You need the gateway and the services behind it running (see `../sanchalan`, `docker compose up`).
Without them the landing page and login screen render, but API calls fail with clear errors.

| Script | What it does |
|---|---|
| `npm run dev` | Dev server with HMR (service worker disabled in dev) |
| `npm run build` | Production build (`output: 'standalone'`) |
| `npm start` | Serve the production build on :3000 |
| `npm run lint` | ESLint + `tsc --noEmit` |
| `npm test` | Node's built-in test runner on `src/lib/*.test.ts` (GST engine parity with Go) |

## Environment

| Variable | Example | Notes |
|---|---|---|
| `NEXT_PUBLIC_API_URL` | `https://api.maapan.in/api` | dwaar base URL incl. `/api`. **Build-time** (baked into JS) |
| `NEXT_PUBLIC_APP_URL` | `https://app.maapan.in` | Used for OpenGraph `metadataBase` |
| `NEXT_PUBLIC_APP_ENV` | `dev` / `staging` / `prod` | Display only |
| `NEXT_PUBLIC_SENTRY_DSN` | – | Reserved for Sentry wiring (TODO) |
| `NEXT_PUBLIC_SUPPORT_PHONE` | – | Landing page call link; hidden when empty |
| `NEXT_PUBLIC_SUPPORT_WHATSAPP` | – | Landing page WhatsApp chat button (digits, e.g. `919876543210`); hidden when empty |

## Pages

| Route | Service(s) | Notes |
|---|---|---|
| `/` | – | Landing |
| `/login` | pehchaan | Phone → OTP (WhatsApp/SMS) → verify → onboarding form if `needs_onboarding` |
| `/dashboard` | buddhi | KPIs + sales trend + top items; graceful empty state |
| `/pos` | bhandar, aadesh, lenden | Keyboard-first counter, barcode scan, live GST, cash/UPI QR/card, thermal print, offline queue |
| `/invoices`, `/invoices/[id]` | aadesh, kar | List + filters; printable A4 tax invoice, 80 mm receipt, WhatsApp share, e-Way JSON, cancel |
| `/purchases` | bahi, aadesh, buddhi | Purchase bill entry; **Scan bill with AI** pre-fills from a photo |
| `/parties`, `/parties/[id]` | bahi, kar | Party master with live GSTIN verify; ledger statement + WhatsApp |
| `/items` | bhandar | Item master + stock summary with re-order flags |
| `/reports` | bahi, kar | Trial balance, P&L, balance sheet, aging, cash flow, GSTR-1, GSTR-3B; CSV export |
| `/imports` | aayaat | MargBooks upload → column mapping → validate → import |
| `/manufacturing` | nirmaan | BOM list + production runs |
| `/ask` | buddhi | "Ask your books" chat |
| `/settings` | pehchaan | Business profile, stores, staff & roles |

Navigation is role-aware: `src/lib/rbac.ts` is a copy of `mool/auth/rbac.go` and only hides UI —
the services enforce permissions.

## POS keyboard shortcuts

`F2` search · `Enter` add (type `3*<barcode>` for quantity 3) · `F4` customer/GSTIN · `F8` cash ·
`F9` UPI QR · `F10` card · `Esc` clear/close/new bill · `Alt+P` open POS from anywhere.

## Offline billing

1. On opening `/pos` the catalog is fetched from `GET /bhandar/v1/items/pos-catalog` and stored in IndexedDB
   (`maapan-pos` → `catalog`). Offline, the cached copy is used.
2. The device reserves a block of invoice numbers (`POST /aadesh/v1/number-blocks`, 200 at a time) and
   reserves the next block when less than 20% is left.
3. Every bill gets a `client_uuid`. Online it goes to `POST /aadesh/v1/invoices`. On a network error or
   5xx it takes the next number from the block, prints immediately, and is queued in IndexedDB.
4. The queue is uploaded to `POST /aadesh/v1/sync/invoices` on the `online` event and every 30 s. Replays
   come back `duplicate`, so retries are safe. The header pill and `/invoices` show per-bill status.
5. `public/sw.js` caches the app shell and `_next/static` (cache-first), pages (network-first with
   offline fallback to `/pos`) and the catalog GET. API writes are never intercepted. Bump `VERSION`
   in `sw.js` when caching rules change.

UPI QR needs internet; offline, take cash/card.

## Printing & hardware

- `src/lib/escpos.ts` builds ESC/POS bytes (48 cols for 80 mm, 32 for 58 mm), partial cut and cash
  drawer kick (`ESC p 0`). Transports: **WebUSB** (printer class) or **Web Serial** (USB-serial /
  Bluetooth SPP), Chrome/Edge only. Click "Connect thermal printer" on the POS once per session.
- Without a connected printer it falls back to `window.print()` of an 80 mm receipt layout
  (`body.print-thermal` print CSS). Invoice view prints A4 or thermal.
- USB/Bluetooth barcode scanners work as keyboards: the search box is always focused.

## Structure

```
src/
  app/
    (public)/page.tsx            landing
    (public)/login/page.tsx      OTP login + onboarding
    (app)/layout.tsx             auth guard, sidebar, store switcher, offline/sync pill
    (app)/{dashboard,pos,invoices,purchases,parties,items,reports,imports,manufacturing,ask,settings}
    globals.css layout.tsx
  components/  Logo, ui kit, nav (role-aware), DataView (generic report renderer), ThermalReceipt, RegisterSW
  lib/
    api.ts       gateway client: bearer token, X-Store-ID, refresh-on-401, error envelope → ApiError
    session.ts   tokens/user/active store (localStorage for MVP), device id
    gst.ts       TS port of mool/gst (CGST/SGST vs IGST, inclusive pricing, round-off, words) + tests
    money.ts     paise helpers, exact BigInt mulDiv, ₹ formatting
    offline.ts   IndexedDB: catalog, number blocks, offline queue + sync
    escpos.ts    receipt bytes + WebUSB/Web Serial + drawer kick
    rbac.ts      permission matrix (copy of mool)
    types.ts     API shapes (paise, basis points)
public/  sw.js, manifest.webmanifest, brand/, icons
```

## Deploy (Cloud Run)

`cloudbuild.yaml`: `npm ci && npm test && tsc` → `docker build --build-arg NEXT_PUBLIC_API_URL=…` →
push to `asia-south1-docker.pkg.dev/$PROJECT_ID/maapan/dukaan:$SHORT_SHA` → `gcloud run deploy dukaan`
(`--ingress=all --allow-unauthenticated --min-instances=1`). Because `NEXT_PUBLIC_*` is baked at build
time, each environment has its own trigger with its own `_API_URL`.

```bash
docker build --build-arg NEXT_PUBLIC_API_URL=http://localhost:8000/api -t dukaan .
docker run -p 3000:8080 dukaan
```

## API map

Every call goes through dwaar as `${NEXT_PUBLIC_API_URL}/<service>/<path>`. Shapes match the Go structs
in each service (money `*_paise`, tax `*_bp`, lists `{items,total,limit,offset}`, errors `{"error":{code,message}}`).

| Page | Endpoints |
|---|---|
| `/login` | pehchaan `POST /v1/auth/otp/request` → `POST /v1/auth/otp/verify` (Session, or `needs_onboarding` + `onboarding_token`) → `POST /v1/onboarding` (onboarding token in `Authorization`) |
| session (`lib/pehchaan.ts`) | pehchaan `GET /v1/me` + `GET /v1/stores` → `SessionUser`; `POST /v1/auth/refresh`, `POST /v1/auth/logout` |
| `/dashboard` | buddhi `GET /v1/dashboard?store_id=`; bahi `GET /v1/reports/summary` (dues) |
| `/pos` | bhandar `GET /v1/items/pos-catalog?store_id=`; aadesh `POST /v1/invoices`, `POST /v1/number-blocks`, `POST /v1/sync/invoices`; lenden `POST /v1/payment-requests`, `GET /v1/payment-requests/{id}` |
| `/invoices`, `/invoices/[id]` | aadesh `GET /v1/invoices`, `GET /v1/invoices/{id}`, `POST /v1/invoices/{id}/cancel`; kar `POST /v1/eway-bills/{id}/json` (transport body), `POST /v1/einvoices/{id}/json` |
| `/purchases` | bahi `GET /v1/parties?type=supplier`; bhandar `GET /v1/items`; buddhi `POST /v1/scan/purchase-bill` (multipart `file`); aadesh `POST /v1/invoices` (`doc_type: purchase`) |
| `/parties`, `/parties/[id]` | bahi `GET/POST /v1/parties`, `GET /v1/parties/{id}`, `GET /v1/parties/{id}/statement?from=&to=`; kar `POST /v1/gstin/verify` |
| `/items` | bhandar `GET/POST /v1/items`, `GET /v1/stock?store_id=` |
| `/reports` | bahi `GET /v1/reports/{trial-balance,profit-loss,balance-sheet,aging?kind=,cash-flow,daybook}`; kar `GET /v1/returns/{gstr1,gstr3b}?period=MMYYYY`; CSV via `format=csv` |
| `/imports` | aayaat `POST /v1/imports` (multipart `file`, `kind`, `as_of`, `store_id`), `GET /v1/imports/{id}/preview`, `PUT /v1/imports/{id}/mapping`, `POST /v1/imports/{id}/validate`, `POST /v1/imports/{id}/commit`, `GET /v1/imports/{id}/errors?format=csv`, `GET /v1/imports/templates/{kind}`, `GET /v1/imports` |
| `/manufacturing` | nirmaan `GET /v1/boms`, `GET /v1/production-runs` |
| `/ask` | buddhi `POST /v1/ask` `{question}` |
| `/settings` | pehchaan `GET/PUT /v1/tenant`, `GET/POST /v1/stores`, `GET/POST /v1/users` |

### Backend gaps the UI works around

- No server-side "send ledger statement" endpoint: the party page shares the balance through a `wa.me` link.
- No tenant invoice terms/footer field in pehchaan: the setting was removed from the UI.
- buddhi's bill scan can't resolve the supplier: the page matches it by GSTIN, then name, against bahi parties.
- BOM cost is per-BOM (`GET /v1/boms/{id}/cost`), so the list shows component counts, not cost.

## TODO

- Move the refresh token to an httpOnly, Secure, SameSite=Strict cookie set by a Next route handler
  (`/api/session`); keep only the 15-minute access token in memory.
- Wire Sentry (`NEXT_PUBLIC_SENTRY_DSN`).
- POS: batch/expiry picker for `track_batches` items, held bills, returns from the counter, customer lookup from bahi.
- BOM builder UI (W21), stock transfers (W16), e-Invoice IRN flow.
- Playwright end-to-end test of offline → online sync.

## Landing page and command palette

- `/` is the full marketing page ported from the original prototype (`src/components/landing/Landing.tsx`): hero with the animated product tour (`src/components/DemoPlayer.tsx`), features, how it works, GST section, pricing, segments, FAQ and CTA. The hero form passes the mobile number to `/login?phone=`.
- Press **⌘K / Ctrl+K** anywhere in the app (or the header search box) for the command palette (`src/components/CommandPalette.tsx`): role-aware actions and pages, plus live search across aadesh invoices, bahi parties and bhandar items (`?q=`).

