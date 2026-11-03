# Runbook — dead-letter replay

Maapan has three places where work that kept failing is parked instead of lost. Find out which one
fired, fix the cause, then replay. Every consumer is idempotent (`processed_events`, idempotency keys,
`ON CONFLICT`), so replaying something that already succeeded is harmless.

| Where | Holds | Alert / signal | Replay with |
|---|---|---|---|
| Pub/Sub `maapan-dead-letter` topic (sub `maapan-dead-letter-sub`, 7-day retention) | events a subscriber rejected 15 times | subscription backlog > 0 | §1 — republish to the event's topic |
| soochna `dead_letters` table | SMS / WhatsApp / e-mail that failed 5 times, for 24 h, or because the provider is not configured | log line `alert="soochna.dead_letter"` | §2 — `POST /internal/v1/dead-letters/replay` |
| bahi `quarantined_events` | events bahi acknowledged but could not post (unknown party, period locked, unbalanced settlement …) | bahi ERROR log, `GET /v1/quarantine` | §3 — `POST /v1/quarantine/{event_id}/retry` |

Outbox rows that a *publisher* could not hand to Pub/Sub are a fourth case: see
[outbox-dead-rows.md](outbox-dead-rows.md).

Set up once per environment:

```bash
cd sanchalan && export ENV=dev && source scripts/env.sh
SECRET="$(gcloud secrets versions access latest --secret=service-secret)"
```

Services run with `--ingress=internal`, so calls to `/internal/...` must come from inside the VPC
(Cloud Shell does not qualify). Use a short-lived VM or Cloud Run job in `maapan-run-subnet`, or call
the user-facing `/v1/...` variants through the gateway with an admin JWT where they exist.

---

## 1. Pub/Sub dead-letter topic

**Look** (does not ack, messages stay):

```bash
gcloud pubsub subscriptions pull maapan-dead-letter-sub --limit=20 --format=json \
  | jq -r '.[] | [.message.attributes.CloudPubSubDeadLetterSourceSubscription,
                  .message.attributes.type, .message.attributes.event_id,
                  .message.attributes.CloudPubSubDeadLetterSourceDeliveryCount] | @tsv'
```

The source subscription is named `<subscriber>--<topic>`, e.g. `bahi--aadesh.invoice.created`.

**Diagnose**: read the subscriber's logs for that `event_id`
(`jsonPayload.event_id="<id>" AND severity>=ERROR`). Typical causes: a downstream service was down
for longer than the retry window, a bad deploy, a missing migration. Fix and deploy first — replaying
into the same bug just dead-letters again.

**Replay** — republish the original envelope to its topic. All subscribers of the topic receive it
again; the ones that already processed it skip it (`processed_events`).

```bash
gcloud pubsub subscriptions pull maapan-dead-letter-sub --limit=100 --format=json > dl.json
jq -c '.[]' dl.json | while read -r m; do
  topic="$(jq -r '.message.attributes.type' <<<"$m")"
  data="$(jq -r '.message.data' <<<"$m" | base64 -d)"
  attrs="$(jq -r '.message.attributes | to_entries
           | map(select(.key | startswith("CloudPubSub") | not)) | map("\(.key)=\(.value)") | join(",")' <<<"$m")"
  gcloud pubsub topics publish "$topic" --message="$data" --attribute="$attrs" >/dev/null && echo "replayed $topic"
done
# Ack only what you replayed:
gcloud pubsub subscriptions ack maapan-dead-letter-sub --ack-ids="$(jq -r 'map(.ackId) | join(",")' dl.json)"
rm dl.json
```

If only one subscriber needs it and you can reach the VPC, POST the decoded envelope straight to
`$(run_url <subscriber>)/events` with `X-Service-Secret: $SECRET` and an ID token for that audience;
`/events` accepts raw envelopes.

**Never** purge the subscription (`seek --time=now`) without saving the messages: it is the only copy.

---

## 2. soochna message dead letters

The alert line (Cloud Logging, `resource.labels.service_name="soochna"`):

```
jsonPayload.alert="soochna.dead_letter"
```

carries `message_id, tenant_id, channel, template, attempts, reason, error` (recipient masked). Create a
log-based metric on it and alert on rate > 0. `alert="soochna.otp_failed"` is the OTP equivalent; OTPs
are never replayed (the code is redacted and stale) — the user just asks for a new one.

**Look**:

```bash
URL="$(run_url soochna)"
curl -s "$URL/internal/v1/dead-letters?limit=50" -H "X-Service-Secret: $SECRET" | jq '.items[] | {id, tenant_id, template, channel, reason, last_error}'
# filters: tenant_id, template, channel, reason (max_attempts | expired | not_configured), all=true (include replayed)
```

**Diagnose by `reason`**:

| reason | Meaning | Fix before replay |
|---|---|---|
| `not_configured` | No MSG91/Gupshup template id for that template (or tenant template without its sender) | `gcloud run services update soochna --update-env-vars=GUPSHUP_TEMPLATE_<NAME>=…`, or fix the tenant's `/v1/settings/messaging` |
| `max_attempts` | Provider rejected 5 times (1 m, 5 m, 15 m, 1 h backoff) | Read `last_error`: wallet empty, DLT template mismatch, number not on WhatsApp, attachment URL 404 |
| `expired` | Still failing 24 h after it was created | Usually a long provider outage; decide whether the message is still worth sending (a 3-day-old bill is) |

**Replay** (sends now; a message that fails again restarts the normal retry cycle):

```bash
# specific rows (dead_letters.id or messages.id)
curl -s "$URL/internal/v1/dead-letters/replay" -H "X-Service-Secret: $SECRET" -H "X-User-ID: $(gcloud config get account)" \
  -d '{"ids":["<id>","<id>"]}'
# everything for one template after fixing its id
curl -s "$URL/internal/v1/dead-letters/replay" -H "X-Service-Secret: $SECRET" -H "X-User-ID: ops" \
  -d '{"template":"invoice","reason":"not_configured","limit":500}'
```

The response lists the new status per message. `suppressed` means the customer opted out in the
meantime — that is correct, not a failure. A store owner can do the same for their own messages in the
app (`GET /v1/dead-letters`, `POST /v1/dead-letters/replay`, permission `settings:manage`).

---

## 3. bahi quarantine

```bash
# as a user with ledger:read / ledger:write, through the gateway
curl -s https://api.maapan.in/api/bahi/v1/quarantine -H "Authorization: Bearer $JWT" | jq '.items[] | {event_id, event_type, reason}'
curl -s -XPOST https://api.maapan.in/api/bahi/v1/quarantine/<event_id>/retry -H "Authorization: Bearer $JWT"
# 200 = posted; 409 = still failing (reason in the body)
```

Reason `period locked …`: follow [period-reopen.md](period-reopen.md). Reason `unknown party`: create or
import the party first. Reason `unbalanced settlement`: compare with the gateway's settlement report;
lenden owns the numbers.

## After any replay

- The dead-letter subscription backlog is 0, `GET /internal/v1/dead-letters` is empty (or only rows
  you decided to drop), `GET /v1/quarantine` is empty.
- Write down what failed, why, and what you replayed in the incident log.
