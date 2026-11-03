# Runbook — reopen a locked accounting period

bahi's **period lock** (`locked_until`) rejects anything dated on or before that date: manual vouchers,
bank transactions, cheque clearances, reversals and opening balances get `409 period_locked`, and
event-driven vouchers (sales, purchases, payments, stock) are **quarantined** with reason `period locked`.
Reopening is a business decision first and an ops step second. This runbook covers both.

The API is described in `bahi/README.md` (*Period lock*, *Quarantine and retries*); re-check it if bahi has
changed since this runbook was written.

## 1. Decide whether to reopen at all

Ask the store owner / their accountant, and record the answer:

- **Why** — a missed purchase bill, a wrong ledger, an import into a closed year, a late-arriving event
  that is now quarantined?
- **Which dates** — reopen only as far back as the earliest document you must change.
- **GST impact** — if the period's GSTR-1 / GSTR-3B has been **filed**, reopening the books does not
  reopen the return. Since the July-2025 period, GSTR-3B outward liability is auto-populated and
  hard-locked; outward corrections go through **GSTR-1A** (same period, before 3B is filed) or as
  amendments / credit notes in a later period's GSTR-1. Purchase-side corrections flow through IMS / 2B
  in the period the supplier reports them. So for a filed period, prefer a correcting entry **dated in
  the current open period** (credit/debit note, journal) over back-dating — no reopen needed.
- **Audit** — if the books for that year have been audited or the ITR filed, do not reopen; post an
  adjustment in the current year.

If the answer is "post in the current period", stop here.

## 2. Reopen

Needs a user with `settings:manage` (owner / admin), through the gateway:

```bash
API=https://api.maapan.in/api/bahi
curl -s "$API/v1/settings/period-lock" -H "Authorization: Bearer $JWT"
# → {"locked_until":"2026-09-30","locked_by":"<user>","locked_at":"…"}   — write this down
```

Move the lock back to the day **before** the earliest date you need, or clear it:

```bash
curl -s -XPUT "$API/v1/settings/period-lock" -H "Authorization: Bearer $JWT" \
  -d '{"locked_until":"2026-08-31"}'      # September reopened, August still locked
# or fully open:  -d '{"locked_until":""}'
```

A future `locked_until` is refused. The lock row keeps the last `locked_by` / `locked_at` only — keep your
own note of the old and new values (ticket / incident log).

## 3. Make the corrections

1. **Quarantined events** waiting on the lock:

   ```bash
   curl -s "$API/v1/quarantine" -H "Authorization: Bearer $JWT" | jq '.items[] | select(.reason | startswith("period locked")) | .event_id'
   curl -s -XPOST "$API/v1/quarantine/<event_id>/retry" -H "Authorization: Bearer $JWT"   # 200 posted, 409 still failing
   ```

2. **Manual entries** (missed bills, journals) — post them now, dated inside the reopened window.
3. **Imports** into a closed year (aayaat opening balances) — run them now.
4. Re-run the reports the accountant signed off for that period (trial balance, P&L, GSTR-1/3B review in
   kar) and send them the differences.

## 4. Re-lock

As soon as the corrections are in — the same day:

```bash
curl -s -XPUT "$API/v1/settings/period-lock" -H "Authorization: Bearer $JWT" -d '{"locked_until":"2026-09-30"}'
```

Use the original `locked_until` (or later). Check that `GET /v1/quarantine` has no new `period locked`
rows and that the period's trial balance still balances.

## Bulk / support case

If a store cannot do it themselves, support acts with the owner's written consent, using an admin JWT for
that tenant (never by editing `period_locks` in SQL — that bypasses the API's checks). Note the ticket
number in the voucher narration of every correction you post.
