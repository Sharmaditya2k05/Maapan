# dwaar (द्वार) — API Gateway / Middleware

"Dwaar" means *gateway*. It is the **only public backend** in Maapan. Every browser and POS
request goes `https://api.maapan.in/api/<service>/<path>` → dwaar → the internal service.

What it does, in order, for every request:

1. **Request ID** (`X-Request-ID`), panic recovery, access log, **CORS** (`CORS_ORIGINS`)
2. **Security headers** (HSTS, nosniff, frame deny, no-store) and a **body size limit** (`MAX_BODY_MB`)
3. **Header hygiene** — strips client-sent `X-Service-Secret`, `X-Tenant-ID`, `X-User-ID`, `X-Role`, `X-Serverless-Authorization` (no spoofing)
4. **Route check** — `/api/{service}/...` for known services only; `/internal/*` and `/events` are never reachable from outside (404)
5. **Auth** — verifies the HS256 access token with `mool/auth` (except the public routes below)
6. **Store check** — `X-Store-ID` must be one of the cashier's/manager's stores (403 otherwise)
7. **Rate limiting** — per IP on public routes (default 20/min), per user elsewhere (default 600/min); Redis-backed, in-memory when `REDIS_URL` is empty
8. **Proxy** — forwards `Authorization` and adds `X-Tenant-ID`, `X-User-ID`, `X-Role`, `X-Store-ID`, `X-Forwarded-*`; on Cloud Run adds a Google ID token as `X-Serverless-Authorization`. Streams responses (SSE from buddhi)

| | |
|---|---|
| Port (local) | `8000` (7000 is taken by macOS AirPlay) |
| Database | none (Redis for rate limits) |
| Publishes / subscribes | none |
| Calls | every service: pehchaan, bahi, kar, aadesh, lenden, soochna, aayaat, bhandar, nirmaan, buddhi |
| Public URL | `https://api.maapan.in` (HTTPS load balancer + Cloud Armor → Cloud Run) |
| First deployed | **Week 3** (with pehchaan OTP auth) |

### Public routes (no JWT)

| Route | Why |
|---|---|
| `POST /api/pehchaan/v1/auth/otp/request` | start OTP login |
| `POST /api/pehchaan/v1/auth/otp/verify` | finish OTP login |
| `POST /api/pehchaan/v1/auth/refresh` | rotate refresh token |
| `POST /api/pehchaan/v1/auth/logout` | revoke refresh token |
| `POST /api/lenden/v1/webhooks/razorpay` | Razorpay webhook (lenden verifies the signature) |

## Run locally

```bash
# Redis (optional — without it limits are per-process)
cd ../sanchalan && docker compose up -d redis && cd ../dwaar

cp .env.example .env     # upstream URLs point at localhost:7001..7010
make run                 # → http://localhost:8000/healthz
curl -s localhost:8000/api | jq   # which services are configured
```

Run whichever services you need; unconfigured or stopped ones answer `503`/`502`. The frontend
(`dukaan`) uses `NEXT_PUBLIC_API_URL=http://localhost:8000/api`.

```bash
curl -s -X POST localhost:8000/api/pehchaan/v1/auth/otp/request -H 'Content-Type: application/json' -d '{"phone":"+919999999999"}'
curl -s localhost:8000/api/aadesh/v1/invoices -H "Authorization: Bearer $TOKEN" -H "X-Store-ID: $STORE"
```

## API

| Method & path | Auth | Purpose |
|---|---|---|
| `ANY /api/{service}/{rest}` | JWT (except public routes) | Proxied to `{SERVICE}_URL/{rest}` |
| `GET /api` | none | Lists services and whether each is configured |
| `GET /healthz` | none | Liveness |
| `GET /readyz` | none | Redis ping (when configured) |

Errors use the platform shape `{"error":{"code","message"}}`: `401 unauthorized`, `403 forbidden`,
`404 not_found`, `413 body_too_large`, `429 rate_limited` (+ `Retry-After`), `502 upstream_error`,
`503 unavailable`, `504 upstream_timeout`.

## Environment

See `.env.example`. Key variables: `JWT_SECRET`, `SERVICE_SECRET`, `CORS_ORIGINS`, `REDIS_URL`,
`RATE_LIMIT_PUBLIC_PER_MIN`, `RATE_LIMIT_USER_PER_MIN`, `MAX_BODY_MB`, `UPSTREAM_TIMEOUT`,
`USE_ID_TOKENS`, and one `<SERVICE>_URL` per upstream.

## Code layout

```
cmd/server/main.go            wiring (boot, limiter, upstreams)
internal/gateway/routes.go    service list, public routes, path parsing, blocked paths, header stripping
internal/gateway/gateway.go   auth → store check → rate limit → reverse proxy
internal/ratelimit/           Redis and in-memory fixed-window limiters
```

## Deploy (Cloud Run)

`cloudbuild.yaml` tests, builds (with `mool` as a build context), pushes and deploys:

- `--allow-unauthenticated` (dwaar checks JWTs itself); ingress `all` until the HTTPS load
  balancer exists, then set `_INGRESS=internal-and-cloud-load-balancing` so Cloud Armor cannot be bypassed
- `--vpc-egress=all-traffic` — upstreams have **internal** ingress and IAM auth, so dwaar reaches
  them through the VPC with an ID token (`USE_ID_TOKENS=true`); its service account needs
  `roles/run.invoker` on every upstream
- `min-instances=1`, concurrency 250, 256 MiB
- secrets: `jwt-secret`, `service-secret`, `redis-url`

```bash
gcloud builds submit --config cloudbuild.yaml --substitutions=_ENV=dev,SHORT_SHA=$(git rev-parse --short HEAD)
```
