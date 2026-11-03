package job

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/maapan/aayaat/internal/clients"
	"github.com/maapan/aayaat/internal/mapping"
	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/events"
	"github.com/maapan/mool/svc"
)

// batch is one planned bulk call.
type batch struct {
	phase   string // main | opening_stock
	path    string
	client  *svc.Client
	storeID string
	groups  [][]int           // import_rows.row_no per record (several when bills are summed per party)
	orig    []json.RawMessage // records as validated
	recs    []json.RawMessage // records as sent (shaped to what the endpoint accepts)
	body    any
}

func (b *batch) rows() []int {
	var out []int
	for _, g := range b.groups {
		out = append(out, g...)
	}
	return out
}

func (b *batch) span() (int, int) {
	r := b.rows()
	lo, hi := r[0], r[0]
	for _, n := range r {
		lo, hi = min(lo, n), max(hi, n)
	}
	return lo, hi
}

// mainPath is the endpoint that receives a kind's rows.
func (s *Service) mainPath(j *Job) (string, *svc.Client) {
	switch j.Kind {
	case mapping.KindParties:
		return clients.PathParties, s.Targets.Bahi
	case mapping.KindOpeningBalances:
		return clients.PathOpeningBalances, s.Targets.Bahi
	case mapping.KindOpenBills:
		if j.Params.BillsMode == BillsPartyTotals {
			return clients.PathOpeningBalances, s.Targets.Bahi
		}
		return clients.PathOpenBills, s.Targets.Bahi
	case mapping.KindInvoices:
		return clients.PathInvoiceHistory, s.Targets.Bahi
	case mapping.KindItems:
		return clients.PathItems, s.Targets.Bhandar
	default:
		return clients.PathOpeningStock, s.Targets.Bhandar
	}
}

func body(path, asOf, storeID string, recs []json.RawMessage) any {
	switch path {
	case clients.PathParties:
		return map[string]any{"parties": recs}
	case clients.PathOpeningBalances:
		return map[string]any{"as_of": asOf, "balances": recs}
	case clients.PathItems:
		return map[string]any{"items": recs}
	case clients.PathOpeningStock:
		return map[string]any{"store_id": storeID, "as_of": asOf, "lines": recs}
	case clients.PathOpenBills:
		return map[string]any{"as_of": asOf, "bills": recs}
	default:
		return map[string]any{"as_of": asOf, "invoices": recs}
	}
}

func (s *Service) newBatch(j *Job, phase, path string, client *svc.Client, storeID string, groups [][]int, orig []json.RawMessage) (*batch, error) {
	b := &batch{phase: phase, path: path, client: client, storeID: storeID, groups: groups, orig: orig}
	for _, r := range orig {
		shaped, err := clients.Shape(path, r, s.Targets.ExtrasFor(path))
		if err != nil {
			return nil, err
		}
		b.recs = append(b.recs, shaped)
	}
	b.body = body(path, j.Params.AsOf, storeID, b.recs)
	return b, nil
}

// plan walks the job's valid rows in commit order and hands each bulk call to
// fn. It reads by row-number cursor, so it works both while commit changes row
// statuses and for a dry run that changes nothing. zero receives bill groups
// that net to nothing (party_totals mode).
func (s *Service) plan(ctx context.Context, j *Job, fn func(*batch) error, zero func(rows []int) error) error {
	if j.Kind == mapping.KindOpenBills && j.Params.BillsMode == BillsPartyTotals {
		return s.planPartyTotals(ctx, j, fn, zero)
	}
	path, client := s.mainPath(j)
	last := 0
	for {
		rows, err := s.DB.Query(ctx, `SELECT row_no, record FROM import_rows
			WHERE tenant_id=$1 AND job_id=$2 AND status='valid' AND row_no > $3 ORDER BY row_no LIMIT $4`,
			j.tenantID, j.ID, last, s.batchSize())
		if err != nil {
			return err
		}
		var nos []int
		var recs []json.RawMessage
		for rows.Next() {
			var n int
			var rec json.RawMessage
			if err := rows.Scan(&n, &rec); err != nil {
				rows.Close()
				return err
			}
			nos, recs = append(nos, n), append(recs, rec)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(nos) == 0 {
			return nil
		}
		last = nos[len(nos)-1]

		if j.Kind != mapping.KindOpeningStock {
			groups := make([][]int, len(nos))
			for i, n := range nos {
				groups[i] = []int{n}
			}
			b, err := s.newBatch(j, "main", path, client, "", groups, recs)
			if err != nil {
				return err
			}
			if err := fn(b); err != nil {
				return err
			}
			continue
		}
		// Opening stock: bhandar takes one store per call; rows may name different stores.
		var order []string
		byStore := map[string]*struct {
			groups [][]int
			recs   []json.RawMessage
		}{}
		for i, rec := range recs {
			var r struct {
				StoreID string `json:"store_id"`
			}
			_ = json.Unmarshal(rec, &r)
			st := r.StoreID
			if st == "" {
				st = j.Params.StoreID
			}
			g, ok := byStore[st]
			if !ok {
				g = &struct {
					groups [][]int
					recs   []json.RawMessage
				}{}
				byStore[st] = g
				order = append(order, st)
			}
			g.groups = append(g.groups, []int{nos[i]})
			g.recs = append(g.recs, rec)
		}
		for _, st := range order {
			b, err := s.newBatch(j, "main", path, client, st, byStore[st].groups, byStore[st].recs)
			if err != nil {
				return err
			}
			if err := fn(b); err != nil {
				return err
			}
		}
	}
}

// planPartyTotals sums each party's open bills into one opening balance:
// today's bahi has no bill-wise opening endpoint, but the party ledgers (and
// so total receivables / payables) then match the old books.
func (s *Service) planPartyTotals(ctx context.Context, j *Job, fn func(*batch) error, zero func([]int) error) error {
	rows, err := s.DB.Query(ctx, `SELECT record->>'party_external_ref', array_agg(row_no ORDER BY row_no),
			sum(CASE WHEN record->>'type'='cr' THEN -(record->>'amount_paise')::bigint ELSE (record->>'amount_paise')::bigint END)
		FROM import_rows WHERE tenant_id=$1 AND job_id=$2 AND status='valid'
		GROUP BY 1 ORDER BY min(row_no)`, j.tenantID, j.ID)
	if err != nil {
		return err
	}
	type party struct {
		ref  string
		rows []int
		net  int64
	}
	var parties []party
	for rows.Next() {
		var p party
		var nos []int32
		if err := rows.Scan(&p.ref, &nos, &p.net); err != nil {
			rows.Close()
			return err
		}
		for _, n := range nos {
			p.rows = append(p.rows, int(n))
		}
		parties = append(parties, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	path, client := s.mainPath(j)
	var groups [][]int
	var recs []json.RawMessage
	flush := func() error {
		if len(groups) == 0 {
			return nil
		}
		b, err := s.newBatch(j, "main", path, client, "", groups, recs)
		if err != nil {
			return err
		}
		groups, recs = nil, nil
		return fn(b)
	}
	for _, p := range parties {
		if p.net == 0 {
			if zero != nil {
				if err := zero(p.rows); err != nil {
					return err
				}
			}
			continue
		}
		side, amt := "dr", p.net
		if amt < 0 {
			side, amt = "cr", -amt
		}
		rec, _ := json.Marshal(map[string]any{"party_external_ref": p.ref, "amount_paise": amt, "type": side,
			"external_ref": "ob:" + j.Params.AsOf + ":party:" + p.ref})
		groups, recs = append(groups, p.rows), append(recs, rec)
		if len(groups) >= s.batchSize() {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	return flush()
}

// RunCommit pushes every valid row in batches and records per-row results.
// It is used by the in-process goroutine and by `aayaat -job <id> -tenant <id>`.
//
// Crash safety: each bulk call is journalled in import_batches as 'sent'
// before the POST and turned 'recorded' in the same transaction as the row
// results. If the process dies in between, the rows are still 'valid'; the
// next commit marks the orphaned batch failed and re-sends them, and the
// downstream keys (party / item external_ref, opening line key, bill and
// invoice external_ref) make the second delivery a no-op instead of a
// double posting.
func (s *Service) RunCommit(ctx context.Context, tenantID, id string) (err error) {
	ctx = auth.WithPrincipal(ctx, &auth.Principal{Internal: true, TenantID: tenantID})
	j, err := s.Get(ctx, tenantID, id)
	if err != nil {
		return err
	}
	if j.Status != StatusRunning && j.Status != StatusQueued {
		return fmt.Errorf("job %s is %s, not running", id, j.Status)
	}
	if _, err := s.DB.Exec(ctx, `UPDATE import_jobs SET status='running', updated_at=now() WHERE tenant_id=$1 AND id=$2`, tenantID, id); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			s.fail(context.Background(), tenantID, id, err.Error())
		}
	}()
	if j.Params.AsOf == "" {
		j.Params.AsOf = defaultAsOf(s.Now())
	}
	if _, err := s.DB.Exec(ctx, `UPDATE import_batches SET status='failed', error='interrupted before the result was recorded; rows re-sent'
		WHERE tenant_id=$1 AND job_id=$2 AND status='sent'`, tenantID, id); err != nil {
		return err
	}
	err = s.plan(ctx, j, func(b *batch) error {
		imported, err := s.deliver(ctx, j, b)
		if err != nil {
			return err
		}
		if j.Kind == mapping.KindItems {
			return s.itemOpeningStock(ctx, j, b, imported)
		}
		return nil
	}, func(rows []int) error {
		_, err := s.DB.Exec(ctx, `UPDATE import_rows SET status='skipped',
				warnings = warnings || '["this party''s open bills net to zero; no opening balance posted"]'::jsonb
			WHERE tenant_id=$1 AND job_id=$2 AND row_no = ANY($3)`, tenantID, id, rows)
		return err
	})
	if err != nil {
		return err
	}
	return s.complete(ctx, tenantID, id)
}

// deliver sends one batch and records its outcome; it returns the indexes of
// records that were accepted.
func (s *Service) deliver(ctx context.Context, j *Job, b *batch) ([]int, error) {
	lo, hi := b.span()
	var batchNo int
	err := s.DB.QueryRow(ctx, `INSERT INTO import_batches (job_id, tenant_id, batch_no, phase, path, store_id, first_row, last_row, records, status)
		SELECT $1, $2, COALESCE(max(batch_no),0)+1, $3, $4, NULLIF($5,''), $6, $7, $8, 'sent' FROM import_batches WHERE job_id=$1
		RETURNING batch_no`, j.ID, j.tenantID, b.phase, b.path, b.storeID, lo, hi, len(b.recs)).Scan(&batchNo)
	if err != nil {
		return nil, err
	}
	res, err := clients.Bulk(ctx, b.client, b.path, b.body)
	if err != nil {
		msg := err.Error()
		if clients.IsMissingEndpoint(err) {
			msg = missingEndpointMessage(b.path)
			err = fmt.Errorf("%s", msg)
		} else {
			err = fmt.Errorf("rows %d-%d: %w", lo, hi, err)
		}
		_, _ = s.DB.Exec(context.Background(), `UPDATE import_batches SET status='failed', error=$3, recorded_at=now() WHERE job_id=$1 AND batch_no=$2`,
			j.ID, batchNo, msg)
		return nil, err
	}
	return s.recordBatch(ctx, j, b, batchNo, res)
}

func missingEndpointMessage(path string) string {
	switch path {
	case clients.PathOpenBills:
		return "bahi does not offer POST " + path + " yet. The validated bills are kept: set params.bills_mode=party_totals and validate again " +
			"to load each party's net outstanding as its opening balance now, or commit again once bahi ships bill-wise opening"
	case clients.PathInvoiceHistory:
		return "bahi does not offer POST " + path + " yet; the validated invoices are kept, commit again once it does"
	}
	return "the downstream service does not offer POST " + path
}

// recordBatch marks rows imported, or failed with the downstream message, and
// closes the batch journal entry, all in one transaction.
func (s *Service) recordBatch(ctx context.Context, j *Job, b *batch, batchNo int, res clients.BulkResult) ([]int, error) {
	failed := map[int]string{}
	for _, e := range res.Errors {
		if e.Index >= 0 && e.Index < len(b.groups) {
			failed[e.Index] = e.Message
		}
	}
	created := map[int]bool{}
	for _, r := range res.Results {
		created[r.Index] = r.Created
	}
	var okIdx []int
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var okRows []int
		failedRows := 0
		for i, g := range b.groups {
			if msg, bad := failed[i]; bad {
				errs, _ := json.Marshal([]string{msg})
				if _, err := tx.Exec(ctx, `UPDATE import_rows SET status='failed', errors=$4, batch_no=$5
					WHERE tenant_id=$1 AND job_id=$2 AND row_no = ANY($3)`, j.tenantID, j.ID, g, errs, batchNo); err != nil {
					return err
				}
				failedRows += len(g)
				continue
			}
			okIdx = append(okIdx, i)
			okRows = append(okRows, g...)
			if c, known := created[i]; known {
				if _, err := tx.Exec(ctx, `UPDATE import_rows SET created=$4 WHERE tenant_id=$1 AND job_id=$2 AND row_no = ANY($3)`,
					j.tenantID, j.ID, g, c); err != nil {
					return err
				}
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE import_rows SET status='imported', batch_no=$4 WHERE tenant_id=$1 AND job_id=$2 AND row_no = ANY($3)`,
			j.tenantID, j.ID, okRows, batchNo); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE import_jobs SET imported=imported+$3, failed=failed+$4, updated_at=now() WHERE tenant_id=$1 AND id=$2`,
			j.tenantID, j.ID, len(okRows), failedRows); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE import_batches SET status='recorded', created=$3, updated=$4, errors=$5, recorded_at=now()
			WHERE job_id=$1 AND batch_no=$2`, j.ID, batchNo, res.Created, res.Updated, len(failed))
		return err
	})
	return okIdx, err
}

// itemOpeningStock turns the opening quantity of freshly imported items into
// bhandar opening stock (one call per store). If that fails the row is marked
// failed; committing again re-sends both (item upsert and opening stock are
// idempotent).
func (s *Service) itemOpeningStock(ctx context.Context, j *Job, items *batch, okIdx []int) error {
	var order []string
	type group struct {
		groups [][]int
		recs   []json.RawMessage
	}
	byStore := map[string]*group{}
	for _, i := range okIdx {
		var it struct {
			ExternalRef      string  `json:"external_ref"`
			OpeningQty       float64 `json:"opening_qty"`
			OpeningCostPaise int64   `json:"opening_cost_paise"`
			StoreID          string  `json:"store_id"`
		}
		if err := json.Unmarshal(items.orig[i], &it); err != nil || it.OpeningQty <= 0 {
			continue
		}
		store := it.StoreID
		if store == "" {
			store = j.Params.StoreID
		}
		if store == "" {
			continue
		}
		line, _ := json.Marshal(map[string]any{"item_external_ref": it.ExternalRef, "qty": it.OpeningQty, "unit_cost_paise": it.OpeningCostPaise})
		g, ok := byStore[store]
		if !ok {
			g = &group{}
			byStore[store] = g
			order = append(order, store)
		}
		g.groups = append(g.groups, items.groups[i])
		g.recs = append(g.recs, line)
	}
	for _, store := range order {
		b, err := s.newBatch(j, "opening_stock", clients.PathOpeningStock, s.Targets.Bhandar, store, byStore[store].groups, byStore[store].recs)
		if err != nil {
			return err
		}
		if err := s.deliverOpening(ctx, j, b); err != nil {
			return err
		}
	}
	return nil
}

// deliverOpening sends the opening stock of imported items; failures turn the
// item row from imported into failed.
func (s *Service) deliverOpening(ctx context.Context, j *Job, b *batch) error {
	lo, hi := b.span()
	var batchNo int
	if err := s.DB.QueryRow(ctx, `INSERT INTO import_batches (job_id, tenant_id, batch_no, phase, path, store_id, first_row, last_row, records, status)
		SELECT $1, $2, COALESCE(max(batch_no),0)+1, $3, $4, NULLIF($5,''), $6, $7, $8, 'sent' FROM import_batches WHERE job_id=$1
		RETURNING batch_no`, j.ID, j.tenantID, b.phase, b.path, b.storeID, lo, hi, len(b.recs)).Scan(&batchNo); err != nil {
		return err
	}
	res, err := clients.Bulk(ctx, b.client, b.path, b.body)
	if err != nil {
		_, _ = s.DB.Exec(context.Background(), `UPDATE import_batches SET status='failed', error=$3, recorded_at=now() WHERE job_id=$1 AND batch_no=$2`,
			j.ID, batchNo, err.Error())
		return fmt.Errorf("opening stock for rows %d-%d: %w", lo, hi, err)
	}
	return db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		n := 0
		for _, e := range res.Errors {
			if e.Index < 0 || e.Index >= len(b.groups) {
				continue
			}
			errs, _ := json.Marshal([]string{"item imported; opening stock failed: " + e.Message})
			tag, err := tx.Exec(ctx, `UPDATE import_rows SET status='failed', errors=$4
				WHERE tenant_id=$1 AND job_id=$2 AND row_no = ANY($3) AND status='imported'`, j.tenantID, j.ID, b.groups[e.Index], errs)
			if err != nil {
				return err
			}
			n += int(tag.RowsAffected())
		}
		if _, err := tx.Exec(ctx, `UPDATE import_jobs SET imported=imported-$3, failed=failed+$3, updated_at=now() WHERE tenant_id=$1 AND id=$2`,
			j.tenantID, j.ID, n); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE import_batches SET status='recorded', created=$3, updated=$4, errors=$5, recorded_at=now()
			WHERE job_id=$1 AND batch_no=$2`, j.ID, batchNo, res.Created, res.Updated, len(res.Errors))
		return err
	})
}

func (s *Service) complete(ctx context.Context, tenantID, id string) error {
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var kind, by string
		var params []byte
		var total, errorRows, imported, failed int
		err := tx.QueryRow(ctx, `UPDATE import_jobs SET status='completed', finished_at=now(), updated_at=now()
			WHERE tenant_id=$1 AND id=$2
			RETURNING kind, created_by, params, total_rows, error_rows, imported, failed`, tenantID, id).
			Scan(&kind, &by, &params, &total, &errorRows, &imported, &failed)
		if err != nil {
			return err
		}
		var p Params
		_ = json.Unmarshal(params, &p)
		ev, err := events.New(events.ImportCompleted, "aayaat", tenantID, p.StoreID, events.ImportCompletedPayload{
			JobID: id, Kind: kind, Total: total, Imported: imported, Failed: failed + errorRows,
			RequestedBy: by, NotifyPhone: p.NotifyPhone,
		})
		if err != nil {
			return err
		}
		return events.Enqueue(ctx, tx, ev)
	})
	if err == nil && s.Relay != nil {
		s.Relay.Kick()
	}
	return err
}
