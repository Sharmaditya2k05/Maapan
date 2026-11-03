package inventory

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/maapan/mool/db"
	"github.com/maapan/mool/httpx"
)

// Rollback kinds bhandar undoes (aayaat's job kinds).
const (
	RollbackItems        = "items"
	RollbackOpeningStock = "opening_stock"
)

// RollbackRecord is one record aayaat asks to undo. It carries every key
// aayaat may send (the decoder rejects unknown fields); bhandar reads the
// item and stock ones.
type RollbackRecord struct {
	ExternalRef      string  `json:"external_ref,omitempty"`       // items: the item's external_ref
	AccountCode      string  `json:"account_code,omitempty"`       // (bahi)
	PartyExternalRef string  `json:"party_external_ref,omitempty"` // (bahi)
	ItemExternalRef  string  `json:"item_external_ref,omitempty"`  // opening stock
	ItemID           string  `json:"item_id,omitempty"`
	BatchNo          string  `json:"batch_no,omitempty"`
	StoreID          string  `json:"store_id,omitempty"`
	Qty              float64 `json:"qty,omitempty"`
	Created          *bool   `json:"created,omitempty"` // from the bulk call's results; nil = unknown
}

// RollbackInput is the body of POST /internal/v1/bulk/rollback.
type RollbackInput struct {
	Kind    string           `json:"kind"`
	JobID   string           `json:"job_id"`
	AsOf    string           `json:"as_of,omitempty"`
	Records []RollbackRecord `json:"records"`
}

// Validate checks the request.
func (in *RollbackInput) Validate() error {
	if in.Kind != RollbackItems && in.Kind != RollbackOpeningStock {
		return httpx.Validation("bhandar rolls back kind items or opening_stock, not %q", in.Kind)
	}
	if strings.TrimSpace(in.JobID) == "" {
		return httpx.Validation("job_id is required")
	}
	if len(in.Records) > 1000 {
		return httpx.Validation("send up to 1000 records per call")
	}
	return nil
}

// Rollback undoes what an import created, record by record (one bad record
// does not stop the rest; it is reported in errors):
//
//   - opening_stock: the opening movement of store/item/batch is reversed by
//     an opening.rollback movement at the same cost (bhandar.stock.moved with
//     reversal=true), only while nothing else has moved that position since.
//     A record with no opening on file is already undone.
//   - items: an item the import created (created=true, or unknown and never
//     edited) is archived (inactive, archived_at) while untouched: never
//     edited by a user, no movement besides rolled-back opening stock, no
//     serials, variants or price-list rows. created=false is an update of an
//     existing item and is left alone. A later re-import revives the item.
//
// Updated counts the records undone (or already undone); results carries
// {index, id, created:false} for each.
func (s *Service) Rollback(ctx context.Context, tenantID string, in RollbackInput) (BulkResult, error) {
	res := BulkResult{Errors: []BulkError{}, Results: []BulkRecordResult{}}
	if err := in.Validate(); err != nil {
		return res, err
	}
	for i, r := range in.Records {
		var id string
		var err error
		if in.Kind == RollbackOpeningStock {
			id, err = s.rollbackOpening(ctx, tenantID, in.JobID, r)
		} else {
			id, err = s.rollbackItem(ctx, tenantID, in.JobID, r)
		}
		if err != nil {
			e := httpx.AsError(err)
			if e.Status >= 500 {
				return res, err
			}
			res.Errors = append(res.Errors, BulkError{Index: i, Message: e.Message})
			continue
		}
		res.Updated++
		res.Results = append(res.Results, BulkRecordResult{Index: i, ID: id})
	}
	return res, nil
}

func (s *Service) rollbackOpening(ctx context.Context, tenantID, jobID string, r RollbackRecord) (string, error) {
	if r.StoreID == "" {
		return "", httpx.Validation("store_id is required")
	}
	ref := r.ItemExternalRef
	if ref == "" {
		ref = r.ExternalRef
	}
	it, err := resolveItemRef(ctx, s.DB, tenantID, r.ItemID, ref)
	if err != nil {
		if httpx.AsError(err).Status == 404 {
			return "", nil // the item never arrived: nothing to undo
		}
		return "", err
	}
	key := openingKey(r.StoreID, it.ID, r.BatchNo)
	var moved bool
	err = db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		gen, err := openingGeneration(ctx, tx, tenantID, key)
		if err != nil {
			return err
		}
		var qty float64
		var cost int64
		var at time.Time
		var batchID *string
		err = tx.QueryRow(ctx, `SELECT qty::float8, unit_cost_paise, created_at, batch_id FROM stock_movements
			WHERE tenant_id=$1 AND source=$2 AND source_id=$3 AND line_no=$4`, tenantID, SourceOpening, key, gen).Scan(&qty, &cost, &at, &batchID)
		if db.IsNotFound(err) {
			return nil // never posted, or already rolled back
		}
		if err != nil {
			return err
		}
		// Untouched since: no other movement of this position after the opening.
		var others int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM stock_movements WHERE tenant_id=$1 AND store_id=$2 AND item_id=$3
			AND batch_id IS NOT DISTINCT FROM $4 AND created_at >= $5 AND NOT (source=$6 AND source_id=$7)`,
			tenantID, r.StoreID, it.ID, batchID, at, SourceOpening, key).Scan(&others); err != nil {
			return err
		}
		if others > 0 {
			return httpx.Conflict("%s has moved since the import (%d movement(s)); correct it with an adjustment", it.Name, others)
		}
		movs, err := s.post(ctx, tx, postRequest{
			TenantID: tenantID, StoreID: r.StoreID, Source: SourceOpeningRollback, SourceID: key, Reason: ReasonOpening,
			CreatedBy: "import", Reversal: true,
			Lines: []MoveLine{{LineNo: gen, ItemID: it.ID, BatchNo: r.BatchNo, Qty: -qty, UnitCostPaise: cost, Note: "import rollback " + jobID}},
		})
		moved = len(movs) > 0
		return err
	})
	if err == nil && moved {
		s.kick()
	}
	return it.ID, err
}

func (s *Service) rollbackItem(ctx context.Context, tenantID, jobID string, r RollbackRecord) (string, error) {
	ref := r.ExternalRef
	if ref == "" {
		ref = r.ItemExternalRef
	}
	if ref == "" && r.ItemID == "" {
		return "", httpx.Validation("external_ref is required")
	}
	if r.Created != nil && !*r.Created {
		return "", nil // the import updated an existing item: never reverted
	}
	var id string
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var edited, archived *time.Time
		var name string
		err := tx.QueryRow(ctx, `SELECT id, name, edited_at, archived_at FROM items WHERE tenant_id=$1
			AND (($2 <> '' AND external_ref=$2) OR ($3 <> '' AND id::text=$3)) LIMIT 1 FOR UPDATE`,
			tenantID, ref, r.ItemID).Scan(&id, &name, &edited, &archived)
		if db.IsNotFound(err) {
			return nil // never created
		}
		if err != nil {
			return err
		}
		if archived != nil {
			return nil // already rolled back
		}
		if edited != nil {
			return httpx.Conflict("%s was edited after the import; not archived", name)
		}
		var used string
		err = tx.QueryRow(ctx, `SELECT CASE
			WHEN EXISTS (SELECT 1 FROM stock_movements m WHERE m.tenant_id=$1 AND m.item_id=$2 AND m.source NOT IN ($3,$4)) THEN 'stock movements'
			WHEN COALESCE((SELECT sum(b.qty) FROM stock_balances b WHERE b.tenant_id=$1 AND b.item_id=$2), 0) <> 0 THEN 'opening stock (roll it back first)'
			WHEN EXISTS (SELECT 1 FROM serials x WHERE x.tenant_id=$1 AND x.item_id=$2) THEN 'serial numbers'
			WHEN EXISTS (SELECT 1 FROM items v WHERE v.tenant_id=$1 AND v.parent_id=$2) THEN 'variants'
			WHEN EXISTS (SELECT 1 FROM price_list_items p WHERE p.tenant_id=$1 AND p.item_id=$2) THEN 'price-list prices'
			WHEN EXISTS (SELECT 1 FROM scheme_items si WHERE si.item_id=$2) THEN 'schemes'
			WHEN EXISTS (SELECT 1 FROM supplier_items si WHERE si.tenant_id=$1 AND si.item_id=$2 AND si.purchases > 0) THEN 'purchases'
			ELSE '' END`, tenantID, id, SourceOpening, SourceOpeningRollback).Scan(&used)
		if err != nil {
			return err
		}
		if used != "" {
			return httpx.Conflict("%s has %s since the import; not archived", name, used)
		}
		_, err = tx.Exec(ctx, `UPDATE items SET status='inactive', archived_at=now(), archived_job_id=$3, updated_at=now()
			WHERE tenant_id=$1 AND id=$2`, tenantID, id, jobID)
		return err
	})
	if err != nil {
		return "", err
	}
	return id, nil
}
