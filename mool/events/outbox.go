package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// OutboxDDL must be in the first migration of every service that publishes.
const OutboxDDL = `CREATE TABLE IF NOT EXISTS outbox (
	id           TEXT PRIMARY KEY,
	event_type   TEXT NOT NULL,
	payload      JSONB NOT NULL,
	attempts     INT NOT NULL DEFAULT 0,
	last_error   TEXT,
	created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
	published_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS outbox_pending_idx ON outbox (created_at) WHERE published_at IS NULL;`

// outboxUpgradeDDL adds the columns introduced after the first release. The
// relay applies it at start so every service picks them up without a new
// migration file (ADD COLUMN IF NOT EXISTS is idempotent).
const outboxUpgradeDDL = `ALTER TABLE outbox ADD COLUMN IF NOT EXISTS leased_until TIMESTAMPTZ;
ALTER TABLE outbox ADD COLUMN IF NOT EXISTS delivered JSONB NOT NULL DEFAULT '[]';
ALTER TABLE outbox ADD COLUMN IF NOT EXISTS dead_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS outbox_dead_idx ON outbox (dead_at) WHERE dead_at IS NOT NULL;`

// Relay tuning.
const (
	// MaxAttempts is how often a row is retried before it is parked as dead.
	MaxAttempts = 20
	// BatchSize is how many rows one Flush claims.
	BatchSize = 100
	// LeaseFor is how long a claimed row stays invisible to other instances.
	LeaseFor = 30 * time.Second
	// MaxBackoff caps the retry delay between attempts.
	MaxBackoff = 10 * time.Minute
)

// Enqueue writes e to the outbox inside the caller's transaction, so the event
// exists if and only if the business change commits.
func Enqueue(ctx context.Context, tx pgx.Tx, e Envelope) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox(id, event_type, payload) VALUES($1,$2,$3)`, e.ID, e.Type, b)
	return err
}

// PartialPublisher is implemented by drivers that deliver to several
// subscribers one by one (the local direct driver). Delivery is tracked per
// subscriber so a retry only re-sends to the ones that failed.
type PartialPublisher interface {
	Publisher
	// PublishExcept delivers e to every subscriber not in done and returns the
	// full list of subscribers that have now received it. err is non-nil when
	// at least one subscriber is still missing.
	PublishExcept(ctx context.Context, e Envelope, done []string) (delivered []string, err error)
}

// Relay publishes outbox rows. Call Kick after a commit for low latency; Run
// also polls so nothing is stranded if an instance dies between commit and publish.
//
// Delivery is at-least-once: a row is leased (not locked) while it is being
// published, so a crash between publish and mark-published republishes it after
// the lease expires. Consumers dedupe on the event id.
type Relay struct {
	Pool *pgxpool.Pool
	Pub  Publisher
	Log  *slog.Logger
	kick chan struct{}
	// Now is overridable in tests.
	Now func() time.Time
}

// NewRelay builds a relay.
func NewRelay(pool *pgxpool.Pool, pub Publisher, log *slog.Logger) *Relay {
	return &Relay{Pool: pool, Pub: pub, Log: log, kick: make(chan struct{}, 1), Now: time.Now}
}

// Kick asks the relay to flush now (non-blocking).
func (r *Relay) Kick() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// EnsureSchema adds the relay's columns to an outbox created by an older
// migration. Safe to call on every start.
func (r *Relay) EnsureSchema(ctx context.Context) error {
	_, err := r.Pool.Exec(ctx, outboxUpgradeDDL)
	return err
}

// Run upgrades the outbox schema, then flushes on every kick and every
// interval until ctx ends.
func (r *Relay) Run(ctx context.Context, interval time.Duration) {
	for {
		if err := r.EnsureSchema(ctx); err == nil {
			break
		} else if ctx.Err() != nil {
			return
		} else {
			r.Log.Warn("outbox schema upgrade", "error", err.Error())
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-r.kick:
		}
		if err := r.Flush(ctx); err != nil && ctx.Err() == nil {
			r.Log.Warn("outbox flush", "error", err.Error())
		}
	}
}

type outboxRow struct {
	id        string
	env       Envelope
	attempts  int
	delivered []string
}

// Flush claims up to BatchSize due rows, publishes them without holding any
// database lock, then marks each one published, retried (with exponential
// backoff) or dead. It returns the first publish error, after processing the
// whole batch.
func (r *Relay) Flush(ctx context.Context) error {
	rows, err := r.claim(ctx)
	if err != nil || len(rows) == 0 {
		return err
	}
	var first error
	for _, row := range rows {
		pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		delivered, perr := r.publish(pctx, row)
		cancel()
		if perr == nil {
			if err := r.markPublished(ctx, row.id, delivered); err != nil {
				return err
			}
			continue
		}
		if first == nil {
			first = perr
		}
		if err := r.markFailed(ctx, row, delivered, perr); err != nil {
			return err
		}
	}
	return first
}

func (r *Relay) claim(ctx context.Context) ([]outboxRow, error) {
	rows, err := r.Pool.Query(ctx, `UPDATE outbox SET leased_until = now() + $1::interval
		WHERE id IN (
			SELECT id FROM outbox
			WHERE published_at IS NULL AND dead_at IS NULL AND (leased_until IS NULL OR leased_until < now())
			ORDER BY created_at LIMIT $2 FOR UPDATE SKIP LOCKED)
		RETURNING id, payload, attempts, delivered`, LeaseFor.String(), BatchSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []outboxRow
	for rows.Next() {
		var it outboxRow
		var payload, delivered []byte
		if err := rows.Scan(&it.id, &payload, &it.attempts, &delivered); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(payload, &it.env); err != nil {
			return nil, fmt.Errorf("outbox %s: %w", it.id, err)
		}
		if len(delivered) > 0 {
			_ = json.Unmarshal(delivered, &it.delivered)
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (r *Relay) publish(ctx context.Context, row outboxRow) ([]string, error) {
	if pp, ok := r.Pub.(PartialPublisher); ok {
		return pp.PublishExcept(ctx, row.env, row.delivered)
	}
	return row.delivered, r.Pub.Publish(ctx, row.env)
}

func (r *Relay) markPublished(ctx context.Context, id string, delivered []string) error {
	d, _ := json.Marshal(nonNil(delivered))
	_, err := r.Pool.Exec(ctx, `UPDATE outbox SET published_at=now(), leased_until=NULL, delivered=$2 WHERE id=$1`, id, d)
	return err
}

func (r *Relay) markFailed(ctx context.Context, row outboxRow, delivered []string, perr error) error {
	attempts := row.attempts + 1
	d, _ := json.Marshal(nonNil(delivered))
	msg := perr.Error()
	if len(msg) > 2000 {
		msg = msg[:2000]
	}
	if attempts >= MaxAttempts {
		_, err := r.Pool.Exec(ctx, `UPDATE outbox SET attempts=$2, last_error=$3, delivered=$4, leased_until=NULL, dead_at=now() WHERE id=$1`,
			row.id, attempts, msg, d)
		if err == nil {
			r.Log.Error("outbox event dead-lettered after max attempts; replay with Relay.ReplayDead",
				"event_id", row.id, "event_type", row.env.Type, "tenant_id", row.env.TenantID, "error", msg)
		}
		return err
	}
	_, err := r.Pool.Exec(ctx, `UPDATE outbox SET attempts=$2, last_error=$3, delivered=$4, leased_until=now() + $5::interval WHERE id=$1`,
		row.id, attempts, msg, d, Backoff(attempts).String())
	return err
}

// Backoff returns the delay before retry number attempts (1-based): 2s, 4s,
// 8s … capped at MaxBackoff. Twenty attempts span roughly two hours.
func Backoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	if attempts > 30 {
		return MaxBackoff
	}
	d := time.Duration(math.Pow(2, float64(attempts))) * time.Second
	if d > MaxBackoff {
		return MaxBackoff
	}
	return d
}

// DeadCount returns how many rows are parked as dead (for /readyz and alerts).
func (r *Relay) DeadCount(ctx context.Context) (int, error) {
	var n int
	err := r.Pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE dead_at IS NOT NULL`).Scan(&n)
	return n, err
}

// ReplayDead puts dead rows back in the queue (all of them, or only id).
func (r *Relay) ReplayDead(ctx context.Context, id string) (int64, error) {
	q := `UPDATE outbox SET dead_at=NULL, attempts=0, leased_until=NULL WHERE dead_at IS NOT NULL`
	args := []any{}
	if id != "" {
		q += ` AND id=$1`
		args = append(args, id)
	}
	tag, err := r.Pool.Exec(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	r.Kick()
	return tag.RowsAffected(), nil
}

// DeadCheck is an httpx.Checker-shaped probe that fails when dead rows exist,
// so /readyz surfaces stuck events to Cloud Run monitoring.
func (r *Relay) DeadCheck(ctx context.Context) error {
	n, err := r.DeadCount(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return errors.New(fmt.Sprintf("%d dead outbox events", n))
	}
	return nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
