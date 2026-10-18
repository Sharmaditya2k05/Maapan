package events

import (
	"context"
	"encoding/json"
	"log/slog"
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

// Relay publishes outbox rows. Call Kick after a commit for low latency; Run
// also polls so nothing is stranded if an instance dies between commit and publish.
type Relay struct {
	Pool *pgxpool.Pool
	Pub  Publisher
	Log  *slog.Logger
	kick chan struct{}
}

// NewRelay builds a relay.
func NewRelay(pool *pgxpool.Pool, pub Publisher, log *slog.Logger) *Relay {
	return &Relay{Pool: pool, Pub: pub, Log: log, kick: make(chan struct{}, 1)}
}

// Kick asks the relay to flush now (non-blocking).
func (r *Relay) Kick() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// Run flushes on every kick and every interval until ctx ends.
func (r *Relay) Run(ctx context.Context, interval time.Duration) {
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

// Flush publishes up to 100 pending rows, oldest first. Rows are locked with
// SKIP LOCKED so several instances can relay concurrently.
func (r *Relay) Flush(ctx context.Context) error {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	rows, err := tx.Query(ctx, `SELECT id, payload FROM outbox
		WHERE published_at IS NULL AND attempts < 20
		ORDER BY created_at LIMIT 100 FOR UPDATE SKIP LOCKED`)
	if err != nil {
		return err
	}
	type item struct {
		id  string
		env Envelope
	}
	var items []item
	for rows.Next() {
		var it item
		var payload []byte
		if err := rows.Scan(&it.id, &payload); err != nil {
			rows.Close()
			return err
		}
		if err := json.Unmarshal(payload, &it.env); err != nil {
			rows.Close()
			return err
		}
		items = append(items, it)
	}
	rows.Close()
	for _, it := range items {
		pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := r.Pub.Publish(pctx, it.env)
		cancel()
		if err != nil {
			_, _ = tx.Exec(ctx, `UPDATE outbox SET attempts=attempts+1, last_error=$2 WHERE id=$1`, it.id, err.Error())
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE outbox SET published_at=now() WHERE id=$1`, it.id); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
