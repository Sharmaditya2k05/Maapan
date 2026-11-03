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
	"github.com/maapan/nirmaan/internal/units"
)

// Service implements BOM use cases.
type Service struct {
	DB        *pgxpool.Pool
	Inventory clients.Inventory
	Log       *slog.Logger
	Now       func() time.Time
}

// IST is the business time zone (dates, financial years).
var IST = time.FixedZone("IST", 5*3600+1800)

// Today is the IST date of now.
func Today(now time.Time) string { return now.In(IST).Format(time.DateOnly) }

const bomCols = `id, item_id, item_name, version, output_qty::float8, unit, status, scrap_method, yield_percent::float8,
	to_char(effective_from,'YYYY-MM-DD'), to_char(effective_to,'YYYY-MM-DD'), is_primary, alternate_name,
	notes, created_by, created_at, updated_at`

// Load reads one BOM with components and operations.
func Load(ctx context.Context, q db.Querier, tenantID, id string) (*BOM, error) {
	var b BOM
	err := q.QueryRow(ctx, `SELECT `+bomCols+` FROM boms WHERE tenant_id=$1 AND id=$2`, tenantID, id).Scan(
		&b.ID, &b.ItemID, &b.ItemName, &b.Version, &b.OutputQty, &b.Unit, &b.Status, &b.ScrapMethod, &b.YieldPercent,
		&b.EffectiveFrom, &b.EffectiveTo, &b.IsPrimary, &b.AlternateName, &b.Notes, &b.CreatedBy, &b.CreatedAt, &b.UpdatedAt)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	orows, err := q.Query(ctx, `SELECT line_no, name, COALESCE(work_center,''), hours::float8, rate_paise_per_hour, cost_paise
		FROM bom_operations WHERE bom_id=$1 ORDER BY line_no`, id)
	if err != nil {
		return nil, err
	}
	defer orows.Close()
	b.Operations = []Operation{}
	for orows.Next() {
		var o Operation
		if err := orows.Scan(&o.LineNo, &o.Name, &o.WorkCenter, &o.Hours, &o.RatePaisePerHour, &o.CostPaise); err != nil {
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
	on     string // YYYY-MM-DD: which BOM is effective
	cache  map[string]*BOM
}

// NewResolver builds a Resolver over q for one tenant, choosing the BOMs effective on date (YYYY-MM-DD).
func NewResolver(ctx context.Context, q db.Querier, tenantID, date string) Resolver {
	return &dbResolver{ctx: ctx, q: q, tenant: tenantID, on: date, cache: map[string]*BOM{}}
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
	id, err := EffectiveBOMID(r.ctx, r.q, r.tenant, itemID, r.on)
	if err != nil || id == "" {
		return nil, err
	}
	return r.BOM(id)
}

// EffectiveBOMID returns the item's primary active BOM effective on date ("" when none).
func EffectiveBOMID(ctx context.Context, q db.Querier, tenantID, itemID, date string) (string, error) {
	var id string
	err := q.QueryRow(ctx, `SELECT id FROM boms WHERE tenant_id=$1 AND item_id=$2 AND status='active' AND is_primary
		AND (effective_from IS NULL OR effective_from <= $3::date) AND (effective_to IS NULL OR effective_to >= $3::date)
		ORDER BY effective_from DESC NULLS LAST, version DESC LIMIT 1`, tenantID, itemID, date).Scan(&id)
	if db.IsNotFound(err) {
		return "", nil
	}
	return id, err
}

func writeLines(ctx context.Context, tx pgx.Tx, id string, in Input) error {
	if _, err := tx.Exec(ctx, `DELETE FROM bom_components WHERE bom_id=$1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM bom_operations WHERE bom_id=$1`, id); err != nil {
		return err
	}
	for i, c := range in.Components {
		var sub *string
		if c.IsSubAssembly && c.SubBOMID != nil && *c.SubBOMID != "" {
			sub = c.SubBOMID
		}
		if _, err := tx.Exec(ctx, `INSERT INTO bom_components (bom_id, line_no, item_id, item_name, qty, unit,
			scrap_percent, is_sub_assembly, sub_bom_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			id, i+1, c.ItemID, strings.TrimSpace(c.ItemName), Round3(c.Qty), unitOr(c.Unit), c.ScrapPercent, c.IsSubAssembly, sub); err != nil {
			return err
		}
	}
	for i, o := range in.Operations {
		if _, err := tx.Exec(ctx, `INSERT INTO bom_operations (bom_id, line_no, name, work_center, hours, rate_paise_per_hour, cost_paise)
			VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			id, i+1, strings.TrimSpace(o.Name), nilIfEmpty(o.WorkCenter), Round3(o.Hours), o.RatePaisePerHour, o.CostPaise); err != nil {
			return err
		}
	}
	return nil
}

func unitOr(u string) string {
	u = units.Norm(u)
	if u == "" {
		return "PCS"
	}
	return u
}

func lockItem(ctx context.Context, tx pgx.Tx, tenantID, itemID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1 || ':bom:' || $2))`, tenantID, itemID)
	return err
}

// Create saves a new draft BOM as the next version for the finished item.
func (s *Service) Create(ctx context.Context, tenantID, userID string, in Input) (*BOM, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	var id string
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := lockItem(ctx, tx, tenantID, in.ItemID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO boms (tenant_id, item_id, item_name, version, output_qty, unit, notes, created_by,
			scrap_method, yield_percent, effective_from, effective_to, is_primary, alternate_name)
			VALUES ($1,$2,$3,(SELECT COALESCE(max(version),0)+1 FROM boms WHERE tenant_id=$1 AND item_id=$2),$4,$5,$6,$7,
			$8,$9,$10::date,$11::date,$12,$13)
			RETURNING id`, tenantID, in.ItemID, in.ItemName, Round3(in.OutputQty), unitOr(in.Unit),
			nilIfEmpty(in.Notes), userID, in.ScrapMethod, in.YieldPercent, nilIfEmpty(in.EffectiveFrom), nilIfEmpty(in.EffectiveTo),
			*in.IsPrimary, nilIfEmpty(in.AlternateName)).Scan(&id); err != nil {
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
	if err := in.Validate(); err != nil {
		return nil, err
	}
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
		if _, err := tx.Exec(ctx, `UPDATE boms SET item_name=$3, output_qty=$4, unit=$5, notes=$6, scrap_method=$7,
			yield_percent=$8, effective_from=$9::date, effective_to=$10::date, is_primary=$11, alternate_name=$12, updated_at=now()
			WHERE tenant_id=$1 AND id=$2`, tenantID, id, in.ItemName, Round3(in.OutputQty), unitOr(in.Unit), nilIfEmpty(in.Notes),
			in.ScrapMethod, in.YieldPercent, nilIfEmpty(in.EffectiveFrom), nilIfEmpty(in.EffectiveTo), *in.IsPrimary,
			nilIfEmpty(in.AlternateName)); err != nil {
			return err
		}
		return writeLines(ctx, tx, id, in)
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return Load(ctx, s.DB, tenantID, id)
}

// Activate makes a BOM active after proving the multi-level structure explodes
// without cycles and its units convert to stock units.
//
// A primary BOM supersedes the item's other primary BOMs whose effective range
// overlaps: one that started earlier is end-dated the day before this one
// starts; one that starts on/after it is archived. Alternates (is_primary=false)
// are activated side by side and never retire anything.
func (s *Service) Activate(ctx context.Context, tenantID, id string) (*BOM, error) {
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		b, err := Load(ctx, tx, tenantID, id)
		if err != nil {
			return err
		}
		if b.Status == StatusActive {
			return nil
		}
		if b.Status == StatusArchived {
			return httpx.Conflict("an archived BOM cannot be re-activated; create a new version from it")
		}
		if err := lockItem(ctx, tx, tenantID, b.ItemID); err != nil {
			return err
		}
		if b.IsPrimary {
			if err := supersede(ctx, tx, tenantID, b); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE boms SET status='active', updated_at=now() WHERE tenant_id=$1 AND id=$2`, tenantID, id); err != nil {
			return err
		}
		on := Today(s.now())
		if b.EffectiveFrom != nil && *b.EffectiveFrom > on {
			on = *b.EffectiveFrom
		}
		cat := s.catalogFor(ctx, b)
		if issues := UnitIssues(b, cat); len(issues) > 0 {
			return httpx.Validation("units cannot be converted: %s", strings.Join(issues, "; ")).
				WithDetails(map[string]any{"unit_issues": issues})
		}
		if _, _, err := Explode(NewResolver(ctx, tx, tenantID, on), b, b.OutputQty, cat); err != nil {
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

// supersede end-dates or archives the other primary active BOMs of b's item that overlap b.
func supersede(ctx context.Context, tx pgx.Tx, tenantID string, b *BOM) error {
	from, to := deref(b.EffectiveFrom), deref(b.EffectiveTo)
	// overlapping = old.from <= new.to AND old.to >= new.from (NULL = unbounded)
	rows, err := tx.Query(ctx, `SELECT id, to_char(effective_from,'YYYY-MM-DD') FROM boms
		WHERE tenant_id=$1 AND item_id=$2 AND status='active' AND is_primary AND id<>$3
		AND ($5 = '' OR effective_from IS NULL OR effective_from <= $5::date)
		AND ($4 = '' OR effective_to IS NULL OR effective_to >= $4::date)`, tenantID, b.ItemID, b.ID, from, to)
	if err != nil {
		return err
	}
	type old struct {
		id   string
		from *string
	}
	var olds []old
	for rows.Next() {
		var o old
		if err := rows.Scan(&o.id, &o.from); err != nil {
			rows.Close()
			return err
		}
		olds = append(olds, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, o := range olds {
		startsEarlier := from != "" && (o.from == nil || *o.from < from)
		if startsEarlier {
			if _, err := tx.Exec(ctx, `UPDATE boms SET effective_to=$3::date - 1, updated_at=now() WHERE tenant_id=$1 AND id=$2`,
				tenantID, o.id, from); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE boms SET status='archived', updated_at=now() WHERE tenant_id=$1 AND id=$2`, tenantID, o.id); err != nil {
			return err
		}
	}
	return nil
}

// catalogFor looks up units for b's items; when bhandar is unreachable it logs
// and returns nil (no conversion check at activation; runs still check).
func (s *Service) catalogFor(ctx context.Context, b *BOM) units.Catalog {
	if s.Inventory == nil {
		return nil
	}
	ids := []string{b.ItemID}
	for _, c := range b.Components {
		ids = append(ids, c.ItemID)
	}
	items, err := s.Inventory.Lookup(ctx, ids)
	if err != nil {
		if s.Log != nil {
			s.Log.Warn("unit check skipped: bhandar lookup failed", "error", err.Error())
		}
		return nil
	}
	return clients.Catalog(items)
}

// Archive retires a BOM. The item's primary active BOM is refused (activate a
// replacement first, which retires it) unless force — a discontinued product.
func (s *Service) Archive(ctx context.Context, tenantID, id string, force bool) (*BOM, error) {
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var status string
		var primary bool
		err := tx.QueryRow(ctx, `SELECT status, is_primary FROM boms WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, id).Scan(&status, &primary)
		if db.IsNotFound(err) {
			return httpx.NotFound("bom not found")
		}
		if err != nil {
			return err
		}
		if status == StatusActive && primary && !force {
			return httpx.Conflict("this is the item's active BOM; activate a replacement version (it retires this one) or pass force=true for a discontinued product")
		}
		var open int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM production_runs WHERE tenant_id=$1 AND bom_id=$2 AND status IN ('planned','in_progress')`,
			tenantID, id).Scan(&open); err != nil {
			return err
		}
		if open > 0 && !force {
			return httpx.Conflict("%d open production run(s) use this BOM; finish or cancel them first", open)
		}
		_, err = tx.Exec(ctx, `UPDATE boms SET status='archived', updated_at=now() WHERE tenant_id=$1 AND id=$2`, tenantID, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return Load(ctx, s.DB, tenantID, id)
}

// NewVersion copies a BOM into a new draft version.
func (s *Service) NewVersion(ctx context.Context, tenantID, userID, id string) (*BOM, error) {
	b, err := Load(ctx, s.DB, tenantID, id)
	if err != nil {
		return nil, err
	}
	primary := b.IsPrimary
	return s.Create(ctx, tenantID, userID, Input{
		ItemID: b.ItemID, ItemName: b.ItemName, OutputQty: b.OutputQty, Unit: b.Unit, Notes: deref(b.Notes),
		ScrapMethod: b.ScrapMethod, YieldPercent: b.YieldPercent, IsPrimary: &primary, AlternateName: deref(b.AlternateName),
		Components: b.Components, Operations: b.Operations,
	})
}

// Summary is a BOM row in lists.
type Summary struct {
	ID            string    `json:"id"`
	ItemID        string    `json:"item_id"`
	ItemName      string    `json:"item_name"`
	Version       int       `json:"version"`
	OutputQty     float64   `json:"output_qty"`
	Unit          string    `json:"unit"`
	Status        string    `json:"status"`
	IsPrimary     bool      `json:"is_primary"`
	AlternateName *string   `json:"alternate_name,omitempty"`
	EffectiveFrom *string   `json:"effective_from,omitempty"`
	EffectiveTo   *string   `json:"effective_to,omitempty"`
	Components    int       `json:"components"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// List lists BOMs.
func (s *Service) List(ctx context.Context, tenantID, itemID, status, search string, page httpx.Page) ([]Summary, error) {
	rows, err := s.DB.Query(ctx, `SELECT b.id, b.item_id, b.item_name, b.version, b.output_qty::float8, b.unit, b.status,
		b.is_primary, b.alternate_name, to_char(b.effective_from,'YYYY-MM-DD'), to_char(b.effective_to,'YYYY-MM-DD'),
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
			&sm.IsPrimary, &sm.AlternateName, &sm.EffectiveFrom, &sm.EffectiveTo, &sm.Components, &sm.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, sm)
	}
	return out, rows.Err()
}

// ExplodeByID explodes a stored BOM (sub-assemblies as effective on date; "" = today).
func (s *Service) ExplodeByID(ctx context.Context, tenantID, id string, qty float64, date string) (Node, []Requirement, error) {
	if date == "" {
		date = Today(s.now())
	}
	r := NewResolver(ctx, s.DB, tenantID, date)
	b, err := r.BOM(id)
	if err != nil {
		return Node{}, nil, err
	}
	if qty <= 0 {
		qty = b.OutputQty
	}
	tree, flat, err := Explode(r, b, qty, s.catalogFor(ctx, b))
	return tree, flat, mapErr(err)
}

// CostOptions pick the price basis of a roll-up.
type CostOptions struct {
	StoreID string // weighted-average cost at this store first
	Basis   string // avg (default) | purchase
	Date    string // which sub-assembly BOMs are effective (default today)
}

// CostByID rolls up the cost of a stored BOM from bhandar's weighted-average cost.
func (s *Service) CostByID(ctx context.Context, tenantID, id string, qty float64, opt CostOptions) (CostResult, error) {
	if opt.Date == "" {
		opt.Date = Today(s.now())
	}
	r := NewResolver(ctx, s.DB, tenantID, opt.Date)
	b, err := r.BOM(id)
	if err != nil {
		return CostResult{}, err
	}
	if qty <= 0 {
		qty = b.OutputQty
	}
	pr, err := s.PricingFor(ctx, r, b, opt)
	if err != nil {
		return CostResult{}, err
	}
	res, err := Cost(r, b, qty, pr)
	return res, mapErr(err)
}

// PricingFor prices every item of b's tree (leaves, direct components and the
// output of each sub-BOM) and loads their units.
func (s *Service) PricingFor(ctx context.Context, r Resolver, b *BOM, opt CostOptions) (Pricing, error) {
	if s.Inventory == nil {
		return Pricing{}, httpx.Unavailable("bhandar is not configured")
	}
	ids := map[string]bool{}
	var walk func(x *BOM, path []string) error
	walk = func(x *BOM, path []string) error {
		if err := checkCycle(path, x.ID); err != nil {
			return err
		}
		ids[x.ItemID] = true
		for _, c := range x.Components {
			ids[c.ItemID] = true
			sb, err := subBOM(r, c)
			if err != nil {
				return err
			}
			if sb != nil {
				if err := walk(sb, append(path, x.ID)); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(b, nil); err != nil {
		return Pricing{}, mapErr(err)
	}
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	prices, items, err := clients.ResolvePrices(ctx, s.Inventory, list, opt.StoreID, opt.Basis)
	if err != nil {
		return Pricing{}, err
	}
	pr := Pricing{Units: clients.Catalog(items), Prices: map[string]int64{}, Sources: map[string]string{}}
	for id, p := range prices {
		pr.Prices[id], pr.Sources[id] = p.Paise, p.Source
	}
	return pr, nil
}

func (s *Service) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrCycle) {
		return httpx.Validation("%s", err.Error())
	}
	if db.IsUniqueViolation(err) {
		return httpx.Conflict("another BOM version conflicts (one open-ended primary BOM per item)")
	}
	return err
}

func nilIfEmpty(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	s = strings.TrimSpace(s)
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
