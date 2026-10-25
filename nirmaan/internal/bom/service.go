package bom

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maapan/mool/db"
	"github.com/maapan/mool/httpx"
	"github.com/maapan/nirmaan/internal/clients"
)

// Service implements BOM use cases.
type Service struct {
	DB      *pgxpool.Pool
	Bhandar *clients.Bhandar
	Log     *slog.Logger
	Now     func() time.Time
}

const bomCols = `id, item_id, item_name, version, output_qty::float8, unit, status, notes, created_by, created_at, updated_at`

// Load reads one BOM with components and operations.
func Load(ctx context.Context, q db.Querier, tenantID, id string) (*BOM, error) {
	var b BOM
	err := q.QueryRow(ctx, `SELECT `+bomCols+` FROM boms WHERE tenant_id=$1 AND id=$2`, tenantID, id).Scan(
		&b.ID, &b.ItemID, &b.ItemName, &b.Version, &b.OutputQty, &b.Unit, &b.Status, &b.Notes, &b.CreatedBy,
		&b.CreatedAt, &b.UpdatedAt)
	if db.IsNotFound(err) {
		return nil, httpx.NotFound("bom %s not found", id)
	}
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `SELECT line_no, item_id, item_name, qty::float8, unit, scrap_percent::float8,
		is_sub_assembly, sub_bom_id FROM bom_components WHERE bom_id=$1 ORDER BY line_no`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var c Component
		if err := rows.Scan(&c.LineNo, &c.ItemID, &c.ItemName, &c.Qty, &c.Unit, &c.ScrapPercent, &c.IsSubAssembly, &c.SubBOMID); err != nil {
			rows.Close()
			return nil, err
		}
		b.Components = append(b.Components, c)
	}
	rows.Close()
	orows, err := q.Query(ctx, `SELECT line_no, name, cost_paise FROM bom_operations WHERE bom_id=$1 ORDER BY line_no`, id)
	if err != nil {
		return nil, err
	}
	defer orows.Close()
	b.Operations = []Operation{}
	for orows.Next() {
		var o Operation
		if err := orows.Scan(&o.LineNo, &o.Name, &o.CostPaise); err != nil {
			return nil, err
		}
		b.Operations = append(b.Operations, o)
	}
	return &b, orows.Err()
}

// dbResolver resolves sub-assembly BOMs from Postgres, caching per request.
type dbResolver struct {
	ctx    context.Context
	q      db.Querier
	tenant string
	cache  map[string]*BOM
}

// NewResolver builds a Resolver over q for one tenant.
func NewResolver(ctx context.Context, q db.Querier, tenantID string) Resolver {
	return &dbResolver{ctx: ctx, q: q, tenant: tenantID, cache: map[string]*BOM{}}
}

func (r *dbResolver) BOM(id string) (*BOM, error) {
	if b, ok := r.cache[id]; ok {
		return b, nil
	}
	b, err := Load(r.ctx, r.q, r.tenant, id)
	if err != nil {
		return nil, err
	}
	r.cache[id] = b
	return b, nil
}

func (r *dbResolver) ActiveBOMFor(itemID string) (*BOM, error) {
	var id string
	err := r.q.QueryRow(r.ctx, `SELECT id FROM boms WHERE tenant_id=$1 AND item_id=$2 AND status='active'`, r.tenant, itemID).Scan(&id)
	if db.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return r.BOM(id)
}

func writeLines(ctx context.Context, tx pgx.Tx, id string, in Input) error {
	if _, err := tx.Exec(ctx, `DELETE FROM bom_components WHERE bom_id=$1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM bom_operations WHERE bom_id=$1`, id); err != nil {
		return err
	}
	for i, c := range in.Components {
		unit := strings.ToUpper(strings.TrimSpace(c.Unit))
		if unit == "" {
			unit = "PCS"
		}
		var sub *string
		if c.IsSubAssembly && c.SubBOMID != nil && *c.SubBOMID != "" {
			sub = c.SubBOMID
		}
		if _, err := tx.Exec(ctx, `INSERT INTO bom_components (bom_id, line_no, item_id, item_name, qty, unit,
			scrap_percent, is_sub_assembly, sub_bom_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			id, i+1, c.ItemID, strings.TrimSpace(c.ItemName), Round3(c.Qty), unit, c.ScrapPercent, c.IsSubAssembly, sub); err != nil {
			return err
		}
	}
	for i, o := range in.Operations {
		if _, err := tx.Exec(ctx, `INSERT INTO bom_operations (bom_id, line_no, name, cost_paise) VALUES ($1,$2,$3,$4)`,
			id, i+1, strings.TrimSpace(o.Name), o.CostPaise); err != nil {
			return err
		}
	}
	return nil
}

func unitOr(u string) string {
	u = strings.ToUpper(strings.TrimSpace(u))
	if u == "" {
		return "PCS"
	}
	return u
}

// Create saves a new draft BOM as the next version for the finished item.
func (s *Service) Create(ctx context.Context, tenantID, userID string, in Input) (*BOM, error) {
	var id string
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		// Serialise version numbering per item.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1 || ':' || $2))`, tenantID, in.ItemID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO boms (tenant_id, item_id, item_name, version, output_qty, unit, notes, created_by)
			VALUES ($1,$2,$3,(SELECT COALESCE(max(version),0)+1 FROM boms WHERE tenant_id=$1 AND item_id=$2),$4,$5,$6,$7)
			RETURNING id`, tenantID, in.ItemID, in.ItemName, Round3(in.OutputQty), unitOr(in.Unit),
			nilIfEmpty(in.Notes), userID).Scan(&id); err != nil {
			return err
		}
		return writeLines(ctx, tx, id, in)
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return Load(ctx, s.DB, tenantID, id)
}

// Update replaces a draft BOM. Active/archived BOMs are immutable: make a new version.
func (s *Service) Update(ctx context.Context, tenantID, id string, in Input) (*BOM, error) {
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var status, itemID string
		err := tx.QueryRow(ctx, `SELECT status, item_id FROM boms WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, id).Scan(&status, &itemID)
		if db.IsNotFound(err) {
			return httpx.NotFound("bom not found")
		}
		if err != nil {
			return err
		}
		if status != StatusDraft {
			return httpx.Conflict("only draft BOMs can be edited; create a new version")
		}
		if itemID != in.ItemID {
			return httpx.Validation("item_id cannot change; create a BOM for the other item")
		}
		if _, err := tx.Exec(ctx, `UPDATE boms SET item_name=$3, output_qty=$4, unit=$5, notes=$6, updated_at=now()
			WHERE tenant_id=$1 AND id=$2`, tenantID, id, in.ItemName, Round3(in.OutputQty), unitOr(in.Unit), nilIfEmpty(in.Notes)); err != nil {
			return err
		}
		return writeLines(ctx, tx, id, in)
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return Load(ctx, s.DB, tenantID, id)
}

// Activate makes a BOM the item's active version (archiving the previous one)
// after proving the multi-level structure explodes without cycles.
func (s *Service) Activate(ctx context.Context, tenantID, id string) (*BOM, error) {
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		b, err := Load(ctx, tx, tenantID, id)
		if err != nil {
			return err
		}
		if b.Status == StatusActive {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE boms SET status='archived', updated_at=now()
			WHERE tenant_id=$1 AND item_id=$2 AND status='active'`, tenantID, b.ItemID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE boms SET status='active', updated_at=now() WHERE tenant_id=$1 AND id=$2`, tenantID, id); err != nil {
			return err
		}
		if _, _, err := Explode(NewResolver(ctx, tx, tenantID), b, b.OutputQty); err != nil {
			if errors.Is(err, ErrCycle) {
				return httpx.Validation("activating would create a cycle: %s", err.Error())
			}
			return err
		}
		return nil
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return Load(ctx, s.DB, tenantID, id)
}

// Archive retires a BOM.
func (s *Service) Archive(ctx context.Context, tenantID, id string) (*BOM, error) {
	tag, err := s.DB.Exec(ctx, `UPDATE boms SET status='archived', updated_at=now() WHERE tenant_id=$1 AND id=$2`, tenantID, id)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, httpx.NotFound("bom not found")
	}
	return Load(ctx, s.DB, tenantID, id)
}

// NewVersion copies a BOM into a new draft version.
func (s *Service) NewVersion(ctx context.Context, tenantID, userID, id string) (*BOM, error) {
	b, err := Load(ctx, s.DB, tenantID, id)
	if err != nil {
		return nil, err
	}
	return s.Create(ctx, tenantID, userID, Input{
		ItemID: b.ItemID, ItemName: b.ItemName, OutputQty: b.OutputQty, Unit: b.Unit, Notes: deref(b.Notes),
		Components: b.Components, Operations: b.Operations,
	})
}

// Summary is a BOM row in lists.
type Summary struct {
	ID         string    `json:"id"`
	ItemID     string    `json:"item_id"`
	ItemName   string    `json:"item_name"`
	Version    int       `json:"version"`
	OutputQty  float64   `json:"output_qty"`
	Unit       string    `json:"unit"`
	Status     string    `json:"status"`
	Components int       `json:"components"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// List lists BOMs.
func (s *Service) List(ctx context.Context, tenantID, itemID, status, search string, page httpx.Page) ([]Summary, error) {
	rows, err := s.DB.Query(ctx, `SELECT b.id, b.item_id, b.item_name, b.version, b.output_qty::float8, b.unit, b.status,
		(SELECT count(*) FROM bom_components c WHERE c.bom_id=b.id)::int, b.updated_at
		FROM boms b WHERE b.tenant_id=$1 AND ($2 = '' OR b.item_id::text=$2) AND ($3 = '' OR b.status=$3)
		AND ($4 = '' OR b.item_name ILIKE '%' || $4 || '%')
		ORDER BY b.item_name, b.version DESC LIMIT $5 OFFSET $6`, tenantID, itemID, status, search, page.Limit, page.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Summary{}
	for rows.Next() {
		var sm Summary
		if err := rows.Scan(&sm.ID, &sm.ItemID, &sm.ItemName, &sm.Version, &sm.OutputQty, &sm.Unit, &sm.Status,
			&sm.Components, &sm.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, sm)
	}
	return out, rows.Err()
}

// ExplodeByID explodes a stored BOM.
func (s *Service) ExplodeByID(ctx context.Context, tenantID, id string, qty float64) (Node, []Requirement, error) {
	r := NewResolver(ctx, s.DB, tenantID)
	b, err := r.BOM(id)
	if err != nil {
		return Node{}, nil, err
	}
	if qty <= 0 {
		qty = b.OutputQty
	}
	tree, flat, err := Explode(r, b, qty)
	return tree, flat, mapErr(err)
}

// CostByID rolls up the cost of a stored BOM using bhandar purchase prices.
func (s *Service) CostByID(ctx context.Context, tenantID, id string, qty float64) (CostResult, error) {
	r := NewResolver(ctx, s.DB, tenantID)
	b, err := r.BOM(id)
	if err != nil {
		return CostResult{}, err
	}
	if qty <= 0 {
		qty = b.OutputQty
	}
	prices, err := s.LeafPrices(ctx, r, b)
	if err != nil {
		return CostResult{}, err
	}
	res, err := Cost(r, b, qty, prices)
	return res, mapErr(err)
}

// LeafPrices fetches purchase prices for every raw material (and every direct
// component, so sub-assemblies held in stock can also be priced) of b.
func (s *Service) LeafPrices(ctx context.Context, r Resolver, b *BOM) (map[string]int64, error) {
	_, flat, err := Explode(r, b, b.OutputQty)
	if err != nil {
		return nil, mapErr(err)
	}
	ids := make([]string, 0, len(flat)+len(b.Components))
	for _, rq := range flat {
		ids = append(ids, rq.ItemID)
	}
	for _, c := range b.Components {
		ids = append(ids, c.ItemID)
	}
	return s.Bhandar.Prices(ctx, ids)
}

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrCycle) {
		return httpx.Validation("%s", err.Error())
	}
	if db.IsUniqueViolation(err) {
		return httpx.Conflict("another BOM version conflicts (one active BOM per item)")
	}
	return err
}

func nilIfEmpty(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
