# Taraju (तराजू) — GST Billing, Accounting & Inventory ERP

Cloud-native, multi-tenant ERP for Indian retail ("Beyond MargBooks"): POS billing, double-entry
ledgers, live GSTIN verification, e-Way bill / e-Invoice JSON, OTP login + RBAC, multi-store
inventory, manufacturing BOM, MargBooks migration and AI assistance.

**Backend:** Go microservices · **Frontend:** Next.js PWA · **Cloud:** Google Cloud Run

Each folder below is a **separate git repository**.

| Repo | हिन्दी | Role | Port | First on Cloud Run |
|---|---|---|---|---|
| [mool](mool/) | मूल | Shared Go library (auth, db, events, GST engine) | – | library |
| [dwaar](dwaar/) | द्वार | **Middleware / API gateway** | 8000 | W3 |
| [pehchaan](pehchaan/) | पहचान | Identity, OTP, JWT, RBAC, tenants & stores | 7001 | W3 |
| [bahi](bahi/) | बही | Ledger, accounts, cashbook, bank, reports | 7002 | W4 |
| [kar](kar/) | कर | GST: GSTIN verify, e-Way bill, e-Invoice, GSTR-1/3B | 7003 | W4 |
| [aadesh](aadesh/) | आदेश | **Orders**: POS sales, purchases, returns, offline sync | 7004 | W5 |
| [lenden](lenden/) | लेनदेन | **Payments**: Razorpay UPI QR, links, webhooks | 7005 | W10 |
| [soochna](soochna/) | सूचना | SMS & WhatsApp notifications | 7006 | W3 |
| [aayaat](aayaat/) | आयात | MargBooks Excel/CSV migration | 7007 | W9 |
| [bhandar](bhandar/) | भंडार | Inventory: items, barcodes, stock, batches, expiry | 7008 | W5 / W14 |
| [nirmaan](nirmaan/) | निर्माण | Manufacturing BOM & production runs | 7009 | W20 |
| [buddhi](buddhi/) | बुद्धि | **AI initiative**: bill scanning, ask-your-books, forecasts, dashboards | 7010 | W23 |
| [dukaan](dukaan/) | दुकान | Next.js PWA frontend (POS + back office) | 3000 | W3 |
| [sanchalan](sanchalan/) | संचालन | Ops: docker-compose, GCP scripts, docs | – | – |

## Start here

1. **Architecture & contracts:** [sanchalan/docs/ARCHITECTURE.md](sanchalan/docs/ARCHITECTURE.md)
2. **What to deploy on Cloud Run, and when:** [sanchalan/docs/DEPLOYMENT.md](sanchalan/docs/DEPLOYMENT.md)
3. **Run locally:** [sanchalan/README.md](sanchalan/README.md)

```bash
cd sanchalan && docker compose up -d postgres redis && ./scripts/run-local.sh
cd ../dukaan && npm install && npm run dev        # http://localhost:3000, OTP 123456
```

Every service repo has its own `README.md` (run steps, API, deploy notes), `.env.example`,
`Makefile`, `Dockerfile` and `cloudbuild.yaml`.
