# Taraju — What goes to Cloud Run, and when

This is the deployment plan for the 26-week programme. It answers four questions for
every repo: **what** gets deployed, **as what** (Cloud Run service / job / nothing),
**when** (sprint week), and **what has to exist first**.

Region `asia-south1` (Mumbai). Three GCP projects: `taraju-dev`, `taraju-staging`, `taraju-prod`.
Every repo deploys itself through its own `cloudbuild.yaml`; `sanchalan/scripts/` builds the shared plumbing.

---

## 1. Deployable units at a glance

| Repo | Cloud Run type | Ingress | Auth | Min / max instances | CPU · RAM | First deploy | Why min-instances |
|---|---|---|---|---|---|---|---|
| `dukaan` (Next.js) | Service | all (via HTTPS LB) | public | 1 / 10 | 1 · 512Mi | **W3** | first paint at store opening |
| `dwaar` (gateway) | Service | all (via HTTPS LB + Cloud Armor) | public | 1 / 20 | 1 · 512Mi | **W3** | every request passes through it |
| `pehchaan` | Service | internal | IAM | 1 / 10 | 1 · 512Mi | **W3** | OTP login latency |
| `soochna` | Service | internal | IAM | 0 / 10 | 1 · 256Mi | **W3** | – (async) |
| `kar` | Service | internal | IAM | 0 / 5 | 1 · 256Mi | **W4** | – |
| `bahi` | Service | internal | IAM | 1 / 10 | 1 · 512Mi | **W4** | ledger events + reports |
| `bhandar` | Service | internal | IAM | 1 / 10 | 1 · 512Mi | **W5** (items lite) | POS catalogue |
| `aadesh` | Service | internal | IAM | 1 / 20 | 1 · 512Mi | **W5** | billing hot path |
| `aayaat` | Service **+ Job** `aayaat-worker` | internal | IAM | 0 / 3 | 2 · 2Gi | **W9** | – (big files go to the Job) |
| `lenden` | Service | internal (webhook arrives via dwaar) | IAM | 1 / 10 | 1 · 256Mi | **W10** | UPI QR at the counter |
| `nirmaan` | Service | internal | IAM | 0 / 5 | 1 · 256Mi | **W20** | – |
| `buddhi` | Service | internal | IAM | 0 / 5 | 1 · 1Gi | **W23** (AI pilot flag from W12) | – |
| `mool` | – (Go library, tagged `vX.Y.Z`) | – | – | – | – | W1 | – |
| `sanchalan` | – (scripts & docs) | – | – | – | – | W1 | – |

Services that publish events (`aadesh`, `lenden`, `bhandar`, `nirmaan`, `aayaat`, `pehchaan`) run with
`--no-cpu-throttling` so the outbox relay keeps flushing between requests.

## 2. Shared infrastructure — what must exist before any service

| Resource | Created by | Needed from |
|---|---|---|
| GCP projects `taraju-dev` (W1), `taraju-staging` (W11), `taraju-prod` (W12) | Console / org admin | W1 |
| APIs, Artifact Registry `taraju`, VPC `taraju-vpc` + subnet, private service access | `scripts/gcp-bootstrap.sh` | W1 |
| Cloud SQL Postgres 16 `taraju-pg` (private IP, PITR; REGIONAL HA in prod) + one database & user per service | `gcp-bootstrap.sh` | W1 |
| Memorystore Redis 7 (1 GB) | `gcp-bootstrap.sh` | W3 (OTP, rate limits) |
| Secret Manager: `jwt-secret`, `service-secret`, `<svc>-database-url`, `redis-url`, `github-token` | `gcp-bootstrap.sh` | W1 |
| Secrets with real values: `msg91-auth-key`, `gupshup-api-key` | you, before **W3** | W3 |
| `gsp-client-id/secret` (GST Suvidha Provider contract) | you, before **W4** | W4 |
| `razorpay-key-id/secret`, `razorpay-webhook-secret` | you, before **W10** | W10 |
| `anthropic-api-key` | you, before **W12** (pilot) | W12 |
| Cloud Storage `taraju-<env>-files` (imports, invoice PDFs, bill photos) | `gcp-bootstrap.sh` | W9 |
| Service accounts per service, `pubsub-pusher`, `scheduler` | `gcp-bootstrap.sh` | W1 |
| Cloud Build triggers per repo | `scripts/triggers-setup.sh` | W1 |
| run.invoker grants (who may call whom) | `scripts/iam-invokers.sh` (re-run whenever a service is added) | W3 |
| Pub/Sub topics + push subscriptions + dead-letter | `scripts/pubsub-setup.sh` (re-run when a subscriber is added) | W4 |
| Cloud Scheduler jobs | `scripts/scheduler-setup.sh` | W3 |
| HTTPS LB + managed certs for `app.taraju.in` → dukaan, `api.taraju.in` → dwaar, Cloud Armor policy | console / Terraform later | W3 (dev), W12 (prod) |

## 3. Week-by-week deployment calendar

"Deploy" means: merged to `main` → Cloud Build deploys to **dev** automatically. Tagging `vX.Y.Z` deploys
to **staging**. **Prod** is a manual promotion of the staging image (same digest) once UAT passes.

### Phase 1 — Financial core, ledger MVP, GST & pilot (W1–W13)

| Week | Sprint | Deploy to Cloud Run (dev) | Infra / config that week | Notes |
|---|---|---|---|---|
| W1 | Discovery & scaffolding | nothing yet — CI only (`go vet`, tests) | Create `taraju-dev`, run `gcp-bootstrap.sh`, connect GitHub to Cloud Build, `triggers-setup.sh`, tag `mool v0.1.0` | Repos live, architecture doc signed off |
| W2 | UI/UX design system | **dukaan** (static shell + design system pages, no API yet) | Domain `app.dev.taraju.in` | Figma → components |
| W3 | OTP auth & RBAC | **soochna**, **pehchaan**, **dwaar**; redeploy **dukaan** (login) | Redis, MSG91/Gupshup secrets, `iam-invokers.sh`, `scheduler-setup.sh` (soochna-retry), LB for `api.dev.taraju.in` | Order: soochna → pehchaan → dwaar → dukaan |
| W4 | Accounts master & GSTIN | **kar**, **bahi** | GSP secrets, `pubsub-setup.sh` (topic `pehchaan.tenant.created` → bahi, kar, bhandar) | New tenants get a seeded chart of accounts |
| W5 | POS sales billing engine | **bhandar** (item master lite + POS catalogue), **aadesh**; redeploy **dukaan** (/pos) | re-run `pubsub-setup.sh` (`aadesh.invoice.created` → bahi, bhandar) | First end-to-end bill → voucher posted in bahi |
| W6 | Purchase billing & returns | redeploy **aadesh**, **bahi**, **bhandar** | `aadesh.note.created` subscriptions | Debit/credit notes post to ledger & stock |
| W7 | Daily cashbook & banking | redeploy **bahi**, **dukaan** | – | Cashbook, bank, cheque reconciliation |
| W8 | Treasury & aging reports | redeploy **bahi**, **kar** (GSTR-1/3B, e-Way/e-Invoice JSON), **dukaan** | – | Reports v1 |
| W9 | MargBooks Excel importer | **aayaat** service + **aayaat-worker** Cloud Run **Job** | Bucket `taraju-dev-files`, aayaat → bahi/bhandar invoker grants | Tested on real MargBooks exports |
| W10 | Razorpay & WhatsApp alerts | **lenden**; redeploy **soochna** (WhatsApp templates), **dwaar** (public webhook route) | Razorpay secrets, webhook URL `https://api.dev.taraju.in/api/lenden/v1/webhooks/razorpay`, `lenden.payment.*` subscriptions, `lenden-reconcile` scheduler job | Sandbox keys only |
| W11 | Offline PWA & hardware QA | redeploy **dukaan** (service worker, IndexedDB queue), **aadesh** (sync & number blocks) | Create `taraju-staging`, bootstrap it, tag `v0.11.0` on every repo → staging | Thermal printer + scanner testing on staging |
| W12 | Pilot store onboarding | **all services to staging**; create `taraju-prod`, bootstrap, **first prod deploy of everything shipped so far** | Prod LB `app.taraju.in` / `api.taraju.in`, Cloud Armor, REGIONAL Cloud SQL, alerting policies, `anthropic-api-key` (buddhi pilot stays off) | Import pilot data with aayaat-worker in prod |
| W13 | Pilot go-live & review | prod hot-fixes only | Uptime checks + log-based alerts on 5xx, Pub/Sub dead-letter monitoring | Phase 1 MVP live in 2–3 stores |

### Phase 2 — Inventory, manufacturing BOM & scale (W14–W26)

| Week | Sprint | Deploy to Cloud Run | Infra / config that week | Notes |
|---|---|---|---|---|
| W14 | Inventory & multi-store schema | **bhandar** (stock ledger, batches, warehouses) | – | Migrations run automatically at start (advisory-locked) |
| W15 | Item master & barcode engine | **bhandar**, **dukaan** (labels/barcode printing) | – | |
| W16 | Stock movement & transfers | **bhandar**, **dukaan** | – | Inter-store transfers |
| W17 | Re-order & WhatsApp alerts | **bhandar**, **soochna** | `bhandar.stock.low` subscriptions, `bhandar-reorder-scan` job | |
| W18 | Expiry tracking | **bhandar**, **soochna** | `bhandar.batch.expiring` subscription, `bhandar-expiry-scan` job | |
| W19 | Dead stock analytics | **bhandar**, **dukaan** | – | |
| W20 | Manufacturing BOM data model | **nirmaan** (first deploy) | nirmaan → bhandar invoker grant | |
| W21 | BOM builder UI & costing | **nirmaan**, **dukaan** | – | |
| W22 | Production run & stock sync | **nirmaan**, **bhandar**, **bahi** | `nirmaan.production.completed` subscriptions | |
| W23 | Redis analytics dashboards | **buddhi** (first full deploy: dashboards, forecasts, AI features) , **dukaan** | buddhi subscriptions, `buddhi-forecast` + `buddhi-weekly-summary` jobs | AI features on per tenant (`ai_enabled`) |
| W24 | End-to-end system QA | everything → staging (`v1.0.0-rc.N`) | Load test dwaar/aadesh (k6), chaos: kill instances mid-sync | |
| W25 | Expanded store rollout | everything → **prod** (`v1.0.0`) | Raise max-instances, Cloud SQL tier if needed, read replica for reports | |
| W26 | Security audit & handover | remediation releases | Pen-test fixes, backup/restore drill, runbooks in `sanchalan/docs` | Handover |

## 4. Deploy order when standing up a fresh environment

Dependencies flow left → right. Deploying in this order means every service finds its callees.

```
soochna → pehchaan → dwaar → kar → bahi → bhandar → aadesh → lenden → aayaat → nirmaan → buddhi → dukaan
```

```bash
cd sanchalan
ENV=staging ./scripts/gcp-bootstrap.sh
ENV=staging ./scripts/deploy.sh all          # Cloud Build per repo, in the order above
ENV=staging ./scripts/iam-invokers.sh
ENV=staging ./scripts/pubsub-setup.sh
ENV=staging ./scripts/scheduler-setup.sh
```

## 5. Per-service Cloud Run settings (what each `cloudbuild.yaml` sets)

Common to every Go service:

```text
--region=asia-south1 --service-account=<svc>@<project>.iam.gserviceaccount.com
--network=taraju-vpc --subnet=taraju-run-subnet --vpc-egress=private-ranges-only
--update-env-vars=APP_ENV=<env>,EVENTS_DRIVER=pubsub,GCP_PROJECT=<project>,EVENTS_TRUST_IAM=true,USE_ID_TOKENS=true,AUTO_MIGRATE=true
--update-secrets=JWT_SECRET=jwt-secret:latest,SERVICE_SECRET=service-secret:latest[,DATABASE_URL=<svc>-database-url:latest][,REDIS_URL=redis-url:latest]
```

Service-to-service URLs are deterministic: `https://<service>-<PROJECT_NUMBER>.asia-south1.run.app`.

| Service | Extra env / secrets |
|---|---|
| dwaar | `*_URL` for all 10 services, `REDIS_URL`, `--ingress=all --allow-unauthenticated` |
| pehchaan | `SOOCHNA_URL`, `REDIS_URL` |
| soochna | `PEHCHAAN_URL`, `msg91-auth-key`, `gupshup-api-key`, `SMS_DRIVER=msg91`, `WHATSAPP_DRIVER=gupshup` |
| kar | `AADESH_URL`, `PEHCHAAN_URL`, `gsp-client-id`, `gsp-client-secret`, `GSTIN_DRIVER=gsp` |
| bahi | `KAR_URL` |
| bhandar | `EXPIRY_ALERT_DAYS=30` |
| aadesh | `PEHCHAAN_URL`, `BAHI_URL` |
| lenden | `razorpay-key-id`, `razorpay-key-secret`, `razorpay-webhook-secret` and/or `cashfree-client-id`, `cashfree-client-secret`; `PAYMENT_GATEWAY=razorpay` or `cashfree`, `CASHFREE_ENV` |
| aayaat | `BAHI_URL`, `BHANDAR_URL`, `STORAGE_DRIVER=gcs`, `GCS_BUCKET` |
| nirmaan | `BHANDAR_URL` |
| buddhi | `BAHI_URL`, `AADESH_URL`, `BHANDAR_URL`, `SOOCHNA_URL`, `anthropic-api-key`, `AI_MONTHLY_TOKEN_BUDGET` |
| dukaan | build arg `NEXT_PUBLIC_API_URL=https://api.<env>.taraju.in/api` |

## 6. Release & rollback

- Every image is tagged with the git short SHA. Cloud Run keeps old revisions.
- Rollback: `gcloud run services update-traffic <svc> --region=asia-south1 --to-revisions=<previous>=100`.
- Migrations are forward-only and additive (add columns/tables, never drop in the same release), so a rollback
  never meets a schema it can't read.
- Canary for prod: `gcloud run deploy ... --no-traffic --tag=canary`, then shift 10% → 50% → 100%.

## 7. Monthly cost guide (dev ≈ small, prod ≈ pilot of 3–10 stores)

| Item | dev | prod (pilot) |
|---|---|---|
| Cloud Run (min-instances on 6 services) | ₹4–6k | ₹10–15k |
| Cloud SQL (1 vCPU dev / 2 vCPU HA prod) | ₹4k | ₹14–18k |
| Memorystore Redis 1 GB | ₹3.5k | ₹3.5k |
| Pub/Sub, Scheduler, Storage, Logging | < ₹1k | ₹1–2k |
| HTTPS LB + Cloud Armor | ₹1.5k | ₹2–3k |

This is in line with the ₹23,500/month cloud line in the budget (which was sized for AWS).
Turn dev min-instances to 0 outside working hours to save ~40%.
