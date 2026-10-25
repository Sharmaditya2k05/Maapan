package inventory

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/httpx"
)

// Line-number offsets keep the three movements of one transfer line distinct
// under the (source, source_id, line_no) idempotency key.
const (
	transferOutOffset    = 0
	transferInOffset     = 10000
	transferReturnOffset = 20000
)

// TransferLineInput is one line of POST /v1/transfers.
type TransferLineInput struct {
	ItemID  string  `json:"item_id"`
	BatchNo string  `json:"batch_no,omitempty"`
	Qty     float64 `json:"qty"`
	Unit    string  `json:"unit,omitempty"`
}

// TransferInput is the body of POST /v1/transfers.
type TransferInput struct {
	FromStoreID string              `json:"from_store_id"`
	ToStoreID   string              `json:"to_store_id"`
	Notes       string              `json:"notes,omitempty"`
	Lines       []TransferLineInput `json:"lines"`
}

// Validate checks a transfer request.
func (in *TransferInput) Validate() error {
	switch {
	case in.FromStoreID == "" || in.ToStoreID == "":
		return httpx.Validation("from_store_id and to_store_id are required")
	case in.FromStoreID == in.ToStoreID:
		return httpx.Validation("source and destination stores must differ")
	case len(in.Lines) == 0 || len(in.Lines) > 500:
		return httpx.Validation("a transfer needs 1 to 500 lines")
	}
	for i, l := range in.Lines {
		if l.ItemID == "" || l.Qty <= 0 {
			return httpx.Validation("line %d: item_id and a positive qty are required", i+1)
		}
	}
	return nil
}

// CreateTransfer saves a draft transfer. Quantities are stored in the item's base unit.
func (s *Service) CreateTransfer(ctx context.Context, p *auth.Principal, in TransferInput) (*Transfer, error) {
	if !canAccessStore(p, in.FromStoreID) {
		return nil, httpx.Forbidden("no access to store %s", in.FromStoreID)
	}
	var id string
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO transfers (tenant_id, from_store_id, to_store_id, notes, created_by) VALUES ($1,$2,$3,$4,$5) RETURNING id`,
			p.TenantID, in.FromStoreID, in.ToStoreID, nilIfEmpty(strings.TrimSpace(in.Notes)), p.UserID()).Scan(&id); err != nil {
			return err
		}
		for i, l := range in.Lines {
			it, err := loadItemLite(ctx, tx, p.TenantID, l.ItemID)
			if err != nil {
				return err
			}
			qty, _ := ToBaseUnit(l.Qty, l.Unit, it.Unit, it.Alts)
			var batchID *string
			if b := strings.TrimSpace(l.BatchNo); b != "" {
				var bid string
				err := tx.QueryRow(ctx, `SELECT id FROM batches WHERE tenant_id=$1 AND item_id=$2 AND batch_no=$3`, p.TenantID, it.ID, b).Scan(&bid)
				if db.IsNotFound(err) {
					return httpx.Validation("line %d: unknown batch %s for %s", i+1, b, it.Name)
				}
				if err != nil {
					return err
				}
				batchID = &bid
			}
			if _, err := tx.Exec(ctx, `INSERT INTO transfer_lines (transfer_id, line_no, item_id, batch_id, qty) VALUES ($1,$2,$3,$4,$5)`,
				id, i+1, it.ID, batchID, Q3(qty)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetTransfer(ctx, p.TenantID, id)
}

const transferCols = `id, from_store_id, to_store_id, status, notes, created_by, created_at, dispatched_at, received_at, cancelled_at`

func scanTransfer(row pgx.Row) (*Transfer, error) {
	var t Transfer
	var notes *string
	if err := row.Scan(&t.ID, &t.FromStoreID, &t.ToStoreID, &t.Status, &notes, &t.CreatedBy, &t.CreatedAt, &t.DispatchedAt, &t.ReceivedAt, &t.CancelledAt); err != nil {
		return nil, err
	}
	t.Notes = deref(notes)
	t.Lines = []TransferLine{}
	return &t, nil
}

func loadTransfer(ctx context.Context, q db.Querier, tenantID, id string, lock bool) (*Transfer, error) {
	sql := `SELECT ` + transferCols + ` FROM transfers WHERE tenant_id=$1 AND id=$2`
	if lock {
		sql += " FOR UPDATE"
	}
	t, err := scanTransfer(q.QueryRow(ctx, sql, tenantID, id))
	if err != nil {
		if db.IsNotFound(err) {
			return nil, httpx.NotFound("transfer not found")
		}
		return nil, err
	}
	rows, err := q.Query(ctx, `SELECT l.line_no, l.item_id, i.name, l.batch_id, COALESCE(bt.batch_no,''), l.qty::float8, l.unit_cost_paise
		FROM transfer_lines l JOIN items i ON i.id = l.item_id LEFT JOIN batches bt ON bt.id = l.batch_id
		WHERE l.transfer_id=$1 ORDER BY l.line_no`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var l TransferLine
		var batchID *string
		if err := rows.Scan(&l.LineNo, &l.ItemID, &l.ItemName, &batchID, &l.BatchNo, &l.Qty, &l.UnitCostPaise); err != nil {
			return nil, err
		}
		l.BatchID = deref(batchID)
		t.Lines = append(t.Lines, l)
	}
	return t, rows.Err()
}

// GetTransfer loads a transfer with its lines.
func (s *Service) GetTransfer(ctx context.Context, tenantID, id string) (*Transfer, error) {
	return loadTransfer(ctx, s.DB, tenantID, id, false)
}

// TransferFilter narrows GET /v1/transfers.
type TransferFilter struct {
	StoreID string // either side
	Status  string
	httpx.Page
}

// ListTransfers lists transfers newest first (without lines).
func (s *Service) ListTransfers(ctx context.Context, tenantID string, f TransferFilter) ([]Transfer, int, error) {
	where := []string{"tenant_id = $1"}
	args := []any{tenantID}
	if f.StoreID != "" {
		args = append(args, f.StoreID)
		where = append(where, fmt.Sprintf("(from_store_id = $%d OR to_store_id = $%d)", len(args), len(args)))
	}
	if f.Status != "" {
		args = append(args, f.Status)
		where = append(where, fmt.Sprintf("status = $%d", len(args)))
	}
	cond := strings.Join(where, " AND ")
	var total int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM transfers WHERE `+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, f.Limit, f.Offset)
	rows, err := s.DB.Query(ctx, fmt.Sprintf(`SELECT %s FROM transfers WHERE %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d`,
		transferCols, cond, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Transfer{}
	for rows.Next() {
		t, err := scanTransfer(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *t)
	}
	return out, total, rows.Err()
}

// Transition dispatches, receives or cancels a transfer, moving stock
// accordingly: dispatch takes goods out of the source store at its average
// cost, receive puts them into the destination at that dispatch cost, and
// cancelling a dispatched transfer returns them to the source.
func (s *Service) Transition(ctx context.Context, p *auth.Principal, id, action string) (*Transfer, error) {
	var out *Transfer
	posted := false
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		t, err := loadTransfer(ctx, tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		next, err := NextTransferStatus(t.Status, action)
		if err != nil {
			return httpx.Conflict("%s", err.Error())
		}
		var col string
		now := s.Now()
		switch action {
		case ActionDispatch:
			if !canAccessStore(p, t.FromStoreID) {
				return httpx.Forbidden("no access to the source store")
			}
			req := postRequest{TenantID: p.TenantID, StoreID: t.FromStoreID, Source: SourceTransfer, SourceID: t.ID, Reason: ReasonTransferOut,
				CreatedBy: p.UserID(), OccurredAt: now}
			for _, l := range t.Lines {
				req.Lines = append(req.Lines, MoveLine{LineNo: transferOutOffset + l.LineNo, ItemID: l.ItemID, BatchNo: l.BatchNo, Qty: -l.Qty, Note: "transfer to " + t.ToStoreID})
			}
			movs, err := s.post(ctx, tx, req)
			if err != nil {
				return err
			}
			for _, m := range movs {
				if _, err := tx.Exec(ctx, `UPDATE transfer_lines SET unit_cost_paise=$3 WHERE transfer_id=$1 AND line_no=$2`,
					t.ID, m.LineNo-transferOutOffset, m.UnitCostPaise); err != nil {
					return err
				}
			}
			posted = len(movs) > 0
			col = "dispatched_at"
		case ActionReceive:
			if !canAccessStore(p, t.ToStoreID) {
				return httpx.Forbidden("no access to the destination store")
			}
			req := postRequest{TenantID: p.TenantID, StoreID: t.ToStoreID, Source: SourceTransfer, SourceID: t.ID, Reason: ReasonTransferIn,
				CreatedBy: p.UserID(), OccurredAt: now}
			for _, l := range t.Lines {
				req.Lines = append(req.Lines, MoveLine{LineNo: transferInOffset + l.LineNo, ItemID: l.ItemID, BatchNo: l.BatchNo, Qty: l.Qty,
					UnitCostPaise: l.UnitCostPaise, Note: "transfer from " + t.FromStoreID})
			}
			movs, err := s.post(ctx, tx, req)
			if err != nil {
				return err
			}
			posted = len(movs) > 0
			col = "received_at"
		case ActionCancel:
			if !canAccessStore(p, t.FromStoreID) {
				return httpx.Forbidden("no access to the source store")
			}
			if t.Status == TransferDispatched {
				req := postRequest{TenantID: p.TenantID, StoreID: t.FromStoreID, Source: SourceTransfer, SourceID: t.ID, Reason: ReasonTransferIn,
					CreatedBy: p.UserID(), Reversal: true, OccurredAt: now}
				for _, l := range t.Lines {
					req.Lines = append(req.Lines, MoveLine{LineNo: transferReturnOffset + l.LineNo, ItemID: l.ItemID, BatchNo: l.BatchNo, Qty: l.Qty,
						UnitCostPaise: l.UnitCostPaise, Note: "transfer cancelled"})
				}
				movs, err := s.post(ctx, tx, req)
				if err != nil {
					return err
				}
				posted = len(movs) > 0
			}
			col = "cancelled_at"
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE transfers SET status=$3, %s=$4 WHERE tenant_id=$1 AND id=$2`, col),
			p.TenantID, t.ID, next, now.UTC()); err != nil {
			return err
		}
		out, err = loadTransfer(ctx, tx, p.TenantID, t.ID, false)
		return err
	})
	if err != nil {
		return nil, err
	}
	if posted {
		s.kick()
	}
	return out, nil
}
