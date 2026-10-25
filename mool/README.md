# mool (मूल) — Shared Go library

"Mool" means *root*. Every Maapan Go service imports it so that config, logging, auth, database,
events and GST maths behave identically everywhere. It is a library: **not deployed**.

| Package | What it gives you |
|---|---|
| `boot` | One call wires config, logger, Postgres (+ migrations), Redis, JWT verifier, event publisher, outbox relay, health routes and graceful shutdown |
| `config` | `.env` loader + typed env helpers; `Base` settings shared by all services |
| `logx` | Structured JSON logs in Cloud Logging format, request-scoped logger |
| `httpx` | Handler adapter with error returns, JSON helpers, `Decode` with validation, pagination, standard middleware (request ID, recover, access log, CORS), `/healthz` `/readyz`, graceful `Serve` |
| `auth` | Access-token issue/parse (HS256), `Claims`, `Principal`, `User`/`Internal` middleware, `Require(perms...)`, **RBAC matrix** |
| `db` | pgx pool with retry, embedded SQL migration runner (advisory-locked), `WithTx`, unique-violation helpers, idempotent `MarkProcessed` |
| `cache` | Redis connect, fixed-window rate limiter, JSON get/set |
| `events` | Event envelope, type constants, **payload structs**, subscription matrix, publishers (Pub/Sub REST, direct HTTP, log), push-subscriber handler, **transactional outbox** + relay |
| `gst` | GSTIN checksum validation, state codes, CGST/SGST vs IGST engine (tax-inclusive pricing, cess, round-off, rate summary), amount in words (lakh/crore) |
| `money` | Paise arithmetic, half-up rounding, Indian number formatting `₹1,23,456.78` |
| `gcp` | Access & ID tokens from the Cloud Run metadata server (no Google SDK needed) |
| `svc` | Internal HTTP client: service secret, tenant/request headers, ID token for Cloud Run IAM |

## Using it from a service

Services reference it with a local replace so changes are picked up instantly:

```go
// go.mod
require github.com/maapan/mool v0.0.0
replace github.com/maapan/mool => ../mool
```

```go
app, err := boot.New(ctx, boot.Options{Name: "aadesh", DefaultPort: "7004", DB: true, Migrations: migrations.FS, Outbox: true})
mux := http.NewServeMux()
mux.Handle("POST /v1/things", app.Verifier.User(auth.Require(httpx.H(create), auth.PermInvoiceCreate)))
return app.Run(ctx, mux)
```

Docker builds pass this repo as a named build context (`--build-context mool=../mool`), and Cloud
Build clones it (branch/tag `_MOOL_REF`), so no private Go module proxy is needed.

## Develop

```bash
make test   # GST engine, money, JWT/RBAC tests
make vet
```

Changing anything exported here affects every service: run `go build ./...` in the services you
touch, and update `../sanchalan/docs/ARCHITECTURE.md` if a contract (event, payload, permission) changes.
Tag releases with `make release V=v0.x.y` and bump `_MOOL_REF` in service `cloudbuild.yaml` files when
you want deploys pinned.
