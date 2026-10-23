# sanchalan (संचालन) — Operations

"Sanchalan" means *operation / running*. This repo holds everything needed to **run** Taraju
that does not belong to a single service: the local stack, Google Cloud setup scripts, the
architecture contract and the deployment plan.

| File | What |
|---|---|
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Service catalogue, ports, events, internal APIs, RBAC, money rules — **the contract** |
| [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md) | **What is deployed to Cloud Run and in which sprint week** |
| [docs/MATURITY-PLAN.md](docs/MATURITY-PLAN.md) | Per-service audit (Oct 2026), decisions, fixes and additions, and the phase order |
| `docker-compose.yml` | Postgres 16 + Redis 7 (and every service under the `apps` profile) |
| `local/init-databases.sql` | Creates one database per service |
| `scripts/run-local.sh` | Run Go services on your machine with `go run`, logs in `logs/` |
| `scripts/gcp-bootstrap.sh` | One-time GCP setup per environment (APIs, VPC, bucket, secrets, service accounts; Cloud SQL/Memorystore only if chosen) |
| `supabase/setup.sql` | Run in Supabase SQL Editor: one schema + login role per service |
| `scripts/supabase-secrets.sh` | Writes each service's Supabase connection string into Secret Manager |
| `scripts/iam-invokers.sh` | Cloud Run `run.invoker` grants — who may call whom |
| `scripts/pubsub-setup.sh` | Topics, push subscriptions, dead-letter |
| `scripts/scheduler-setup.sh` | Cloud Scheduler jobs |
| `scripts/triggers-setup.sh` | Cloud Build triggers per repo |
| `scripts/deploy.sh` | Manual deploy of one / all repos |

## Repos

All repos sit side by side in one folder:

```
taraju/
  mool/       shared Go library         dwaar/     API gateway (middleware)
  pehchaan/   identity & OTP            soochna/   SMS & WhatsApp
  bahi/       ledger & accounts         kar/       GST engine & returns
  aadesh/     orders & billing          lenden/    payments (Razorpay)
  bhandar/    inventory                 nirmaan/   manufacturing BOM
  aayaat/     MargBooks import          buddhi/    AI & analytics
  dukaan/     Next.js PWA frontend      sanchalan/ this repo
```

## Run everything locally

Prerequisites: Go 1.25+, Node 22+, Docker Desktop.

```bash
cd sanchalan

# Option A — services on the host (best for development)
docker compose up -d postgres redis
./scripts/run-local.sh                  # all Go services; or: ./scripts/run-local.sh pehchaan aadesh
cd ../dukaan && npm install && npm run dev   # http://localhost:3000
./scripts/run-local.sh stop

# Option B — everything in containers
docker compose --profile apps up -d --build
```

Login locally with any phone number and OTP `123456` (pehchaan runs with `OTP_DEV_FIXED`).

| Service | URL |
|---|---|
| dukaan (frontend) | http://localhost:3000 |
| dwaar (gateway) | http://localhost:8000/api/... |
| pehchaan … buddhi | http://localhost:7001 … 7010 |
| Postgres | `postgres://taraju:taraju@localhost:5432/taraju_<service>` |
| Redis | `redis://localhost:6379/0` |

Locally `EVENTS_DRIVER=direct`: a published event is POSTed straight to each subscriber's `/events`
using the `<SERVICE>_URL` variables, so the whole flow works without Pub/Sub.

## Stand up a cloud environment

```bash
gcloud auth login
ENV=dev ./scripts/gcp-bootstrap.sh       # once (Supabase + Upstash by default)
# Supabase: run supabase/setup.sql in the SQL Editor, save the result as creds.csv, then:
ENV=dev SUPABASE_REF=<ref> SUPABASE_POOLER_HOST=<host> ./scripts/supabase-secrets.sh creds.csv && rm -P creds.csv
ENV=dev ./scripts/triggers-setup.sh      # CI/CD on push to main
ENV=dev ./scripts/deploy.sh all          # or let the triggers do it
ENV=dev ./scripts/iam-invokers.sh
ENV=dev ./scripts/pubsub-setup.sh
ENV=dev ./scripts/scheduler-setup.sh
```

Then follow the week-by-week plan in [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md).
