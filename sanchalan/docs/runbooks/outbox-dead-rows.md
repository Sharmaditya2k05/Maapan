# Runbook — outbox dead rows

Services that publish events (pehchaan, kar, aadesh, lenden, aayaat, bhandar, nirmaan) write each event
to an `outbox` table in the same transaction as the business change. A relay goroutine publishes it to
Pub/Sub (or, locally, POSTs it to each subscriber). A row that fails **20 times** (backoff 2 s … 10 min,
roughly two hours) is parked: `dead_at` is set and the relay stops trying.

Effects of a dead row: the business change happened (the invoice exists) but nobody downstream heard
of it — no ledger voucher, no stock movement, no WhatsApp bill. Treat it as urgent.

## Signals

- Log line at ERROR from the publisher:
  `"outbox event dead-lettered after max attempts; replay with Relay.ReplayDead"` with `event_id`,
  `event_type`, `tenant_id`, `error`.
- bhandar: `GET /internal/v1/outbox/dead` → `{"dead": N}`.
- Any service: the SQL count under *Verify* below. (`events.Relay.DeadCheck` exists in mool for
  `/readyz`, but no service registers it yet.)

## Diagnose

The `error` / `last_error` says why publishing failed:

| Error | Cause | Fix |
|---|---|---|
| `pubsub publish <type>: 403` | service account lacks `roles/pubsub.publisher` | `ENV=… ./scripts/gcp-bootstrap.sh` (grants it to every service in `PUBLISHER_SERVICES`); add the service to that list in `scripts/env.sh` if it is new |
| `pubsub publish <type>: 404` | topic missing | `ENV=… ./scripts/pubsub-setup.sh` |
| `id token` / metadata errors | running outside GCP with `EVENTS_DRIVER=pubsub` | fix the environment |
| Direct driver: `<svc>: 5xx / connection refused` (local, Render demo) | a subscriber was down | start it |

## Replay

Fix the cause first. Then put the dead rows back in the queue; the relay picks them up within seconds
(attempt counter reset, so they get 20 more tries).

**bhandar** has an endpoint:

```bash
curl -s -XPOST "$(run_url bhandar)/internal/v1/outbox/replay" -H "X-Service-Secret: $SECRET"          # all
curl -s -XPOST "$(run_url bhandar)/internal/v1/outbox/replay?id=<event_id>" -H "X-Service-Secret: $SECRET"
```

**Other services** — SQL in the service's schema (Supabase SQL editor, or `psql` as the service role):

```sql
SET search_path = aadesh;                    -- the publishing service
SELECT id, event_type, attempts, last_error, created_at, dead_at
FROM outbox WHERE dead_at IS NOT NULL ORDER BY created_at;

-- replay one
UPDATE outbox SET dead_at = NULL, attempts = 0, leased_until = NULL WHERE id = '<event_id>';
-- replay all
UPDATE outbox SET dead_at = NULL, attempts = 0, leased_until = NULL WHERE dead_at IS NOT NULL;
```

This is exactly what `events.Relay.ReplayDead` does. The relay polls every 5 seconds.

## Verify

```sql
SELECT count(*) FILTER (WHERE dead_at IS NOT NULL) AS dead,
       count(*) FILTER (WHERE published_at IS NULL AND dead_at IS NULL) AS pending
FROM outbox;
```

Both should drop to 0. Then spot-check one replayed event downstream: e.g. for
`aadesh.invoice.created`, the voucher exists in bahi (`GET /v1/vouchers?q=<invoice number>&from=…&to=…`)
and the stock movement in bhandar.

## Do not

- Delete outbox rows to make the alert go away — the event is then lost for good.
- Edit `payload`: subscribers trust it; fix the business record and let the service emit a new event.
- Old published rows (`published_at` not null) can be pruned after 30 days if the table grows:
  `DELETE FROM outbox WHERE published_at < now() - interval '30 days';`
