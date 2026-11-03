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
	"github.com/maapan/mool/httpx"
	"github.com/maapan/mool/svc"
)

// RollbackResult is returned by POST /v1/imports/{id}/rollback.
type RollbackResult struct {
	Job        *Job        `json:"job"`
	RolledBack int         `json:"rolled_back"` // rows undone downstream
	Failed     []RowResult `json:"failed"`      // rows the downstream could not undo (still imported)
}

// rollbackRecord is what aayaat asks bahi / bhandar to undo. The downstream
// archives what the import created (created=true, or unknown → only records
// still untouched since the import) and reverses opening vouchers / opening
// stock movements by their keys; it never reverts an update of a record that
// existed before the import.
type rollbackRecord struct {
	ExternalRef      string  `json:"external_ref,omitempty"`
	AccountCode      string  `json:"account_code,omitempty"`
	PartyExternalRef string  `json:"party_external_ref,omitempty"`
	ItemExternalRef  string  `json:"item_external_ref,omitempty"`
	ItemID           string  `json:"item_id,omitempty"`
	BatchNo          string  `json:"batch_no,omitempty"`
	StoreID          string  `json:"store_id,omitempty"`
	Qty              float64 `json:"qty,omitempty"`
	Created          *bool   `json:"created,omitempty"`
}

// Rollback undoes what a job imported: the whole job, or one batch
// (import_batches.batch_no) when batchNo > 0. A job that never reached
// bahi/bhandar (validated, dry-run only, failed before any row) is simply
// discarded. Downstream undo goes through POST /internal/v1/bulk/rollback on
// bahi and bhandar; until they offer it the call answers 503 and nothing changes.
func (s *Service) Rollback(ctx context.Context, tenantID, id string, batchNo int) (*RollbackResult, error) {
	j, err := s.Get(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	switch j.Status {
	case StatusRunning, StatusQueued:
		return nil, httpx.Conflict("job is %s; wait for it to finish", j.Status)
	case StatusRolledBack:
		return nil, httpx.Conflict("job is already rolled back")
	}
	if j.Params.AsOf == "" {
		j.Params.AsOf = defaultAsOf(s.Now())
	}
	if j.Imported == 0 && batchNo == 0 {
		if _, err := s.DB.Exec(ctx, `UPDATE import_jobs SET status='rolled_back', rolled_back_at=now(), updated_at=now()
			WHERE tenant_id=$1 AND id=$2`, tenantID, id); err != nil {
			return nil, err
		}
		j, err = s.Get(ctx, tenantID, id)
		return &RollbackResult{Job: j, Failed: []RowResult{}}, err
	}
	if batchNo > 0 {
		var n int
		if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM import_batches WHERE tenant_id=$1 AND job_id=$2 AND batch_no=$3`,
			tenantID, id, batchNo).Scan(&n); err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, httpx.NotFound("batch %d not found", batchNo)
		}
	}
	ctx = auth.WithPrincipal(ctx, &auth.Principal{Internal: true, TenantID: tenantID})
	out := &RollbackResult{Failed: []RowResult{}}

	// Items with opening quantity: undo the opening stock before the item.
	if j.Kind == mapping.KindItems {
		if err := s.rollbackRows(ctx, j, batchNo, "opening_stock", out); err != nil {
			return nil, err
		}
	}
	if err := s.rollbackRows(ctx, j, batchNo, "main", out); err != nil {
		return nil, err
	}
	err = db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE import_jobs SET
				imported=(SELECT count(*) FROM import_rows WHERE tenant_id=$1 AND job_id=$2 AND status='imported'),
				rolled_back=(SELECT count(*) FROM import_rows WHERE tenant_id=$1 AND job_id=$2 AND status='rolled_back'),
				updated_at=now() WHERE tenant_id=$1 AND id=$2`, tenantID, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE import_batches b SET status='rolled_back' WHERE b.tenant_id=$1 AND b.job_id=$2
				AND b.status='recorded' AND ($3 = 0 OR b.batch_no=$3)
				AND NOT EXISTS (SELECT 1 FROM import_rows r WHERE r.job_id=b.job_id AND r.batch_no=b.batch_no AND r.status='imported')`,
			tenantID, id, batchNo); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE import_jobs SET status='rolled_back', rolled_back_at=now()
			WHERE tenant_id=$1 AND id=$2 AND imported=0`, tenantID, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	out.Job, err = s.Get(ctx, tenantID, id)
	return out, err
}

// rollbackRows sends the imported rows of a job (or batch) to the downstream
// rollback endpoint in chunks and marks the undone rows rolled_back.
func (s *Service) rollbackRows(ctx context.Context, j *Job, batchNo int, phase string, out *RollbackResult) error {
	kind := j.Kind
	client := s.Targets.Bahi
	switch {
	case phase == "opening_stock":
		kind, client = mapping.KindOpeningStock, s.Targets.Bhandar
	case j.Kind == mapping.KindItems || j.Kind == mapping.KindOpeningStock:
		client = s.Targets.Bhandar
	case j.Kind == mapping.KindOpenBills && j.Params.BillsMode == BillsPartyTotals:
		kind = mapping.KindOpeningBalances
	}
	last := 0
	for {
		rows, err := s.DB.Query(ctx, `SELECT row_no, record, created FROM import_rows
			WHERE tenant_id=$1 AND job_id=$2 AND status='imported' AND ($3 = 0 OR batch_no=$3) AND row_no > $4
			ORDER BY row_no LIMIT $5`, j.tenantID, j.ID, batchNo, last, s.batchSize())
		if err != nil {
			return err
		}
		var groups [][]int
		var recs []rollbackRecord
		byParty := map[string]int{}
		read := 0
		for rows.Next() {
			var n int
			var raw json.RawMessage
			var created *bool
			if err := rows.Scan(&n, &raw, &created); err != nil {
				rows.Close()
				return err
			}
			read++
			last = n
			rec, ok := rollbackOf(j, phase, raw)
			if !ok {
				continue
			}
			rec.Created = created
			if kind == mapping.KindOpeningBalances && j.Kind == mapping.KindOpenBills {
				// party_totals: one opening line per party
				if i, seen := byParty[rec.PartyExternalRef]; seen {
					groups[i] = append(groups[i], n)
					continue
				}
				byParty[rec.PartyExternalRef] = len(recs)
			}
			groups = append(groups, []int{n})
			recs = append(recs, rec)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if read == 0 {
			return nil
		}
		if len(recs) == 0 {
			continue
		}
		if err := s.sendRollback(ctx, j, client, kind, phase, groups, recs, out); err != nil {
			return err
		}
	}
}

// rollbackOf extracts the keys the downstream needs to undo one row.
func rollbackOf(j *Job, phase string, raw json.RawMessage) (rollbackRecord, bool) {
	var r struct {
		ExternalRef      string  `json:"external_ref"`
		AccountCode      string  `json:"account_code"`
		PartyExternalRef string  `json:"party_external_ref"`
		ItemExternalRef  string  `json:"item_external_ref"`
		ItemID           string  `json:"item_id"`
		BatchNo          string  `json:"batch_no"`
		StoreID          string  `json:"store_id"`
		Qty              float64 `json:"qty"`
		OpeningQty       float64 `json:"opening_qty"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return rollbackRecord{}, false
	}
	store := r.StoreID
	if store == "" {
		store = j.Params.StoreID
	}
	switch {
	case phase == "opening_stock": // item rows with an opening quantity
		if r.OpeningQty <= 0 || store == "" {
			return rollbackRecord{}, false
		}
		return rollbackRecord{ItemExternalRef: r.ExternalRef, StoreID: store, Qty: r.OpeningQty}, true
	case j.Kind == mapping.KindOpeningStock:
		return rollbackRecord{ItemExternalRef: r.ItemExternalRef, ItemID: r.ItemID, BatchNo: r.BatchNo, StoreID: store, Qty: r.Qty}, true
	case j.Kind == mapping.KindOpeningBalances:
		return rollbackRecord{AccountCode: r.AccountCode, PartyExternalRef: r.PartyExternalRef, ExternalRef: r.ExternalRef}, true
	case j.Kind == mapping.KindOpenBills && j.Params.BillsMode == BillsPartyTotals:
		return rollbackRecord{PartyExternalRef: r.PartyExternalRef, ExternalRef: "ob:" + j.Params.AsOf + ":party:" + r.PartyExternalRef}, true
	}
	return rollbackRecord{ExternalRef: r.ExternalRef, PartyExternalRef: r.PartyExternalRef}, true
}

func (s *Service) sendRollback(ctx context.Context, j *Job, client *svc.Client, kind, phase string, groups [][]int,
	recs []rollbackRecord, out *RollbackResult) error {
	req := map[string]any{"kind": kind, "job_id": j.ID, "as_of": j.Params.AsOf, "records": recs}
	res, err := clients.Bulk(ctx, client, clients.PathRollback, req)
	if err != nil {
		if clients.IsMissingEndpoint(err) {
			return httpx.Unavailable("%s cannot undo imports yet (needs POST %s); nothing was rolled back", serviceOfKind(kind), clients.PathRollback)
		}
		return httpx.Upstream("rollback: %s", err.Error())
	}
	failed := map[int]string{}
	for _, e := range res.Errors {
		failed[e.Index] = e.Message
	}
	var done []int
	for i, g := range groups {
		if msg, bad := failed[i]; bad {
			for _, n := range g {
				out.Failed = append(out.Failed, RowResult{RowNo: n, Status: RowImported, Errors: []string{"rollback failed: " + msg}})
			}
			continue
		}
		if phase == "main" {
			done = append(done, g...)
		}
	}
	if phase != "main" {
		return nil // the item rows themselves are rolled back in the main phase
	}
	if _, err := s.DB.Exec(ctx, `UPDATE import_rows SET status='rolled_back' WHERE tenant_id=$1 AND job_id=$2 AND row_no = ANY($3)`,
		j.tenantID, j.ID, done); err != nil {
		return fmt.Errorf("mark rolled back: %w", err)
	}
	out.RolledBack += len(done)
	return nil
}
