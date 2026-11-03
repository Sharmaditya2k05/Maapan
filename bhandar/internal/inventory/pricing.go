package inventory

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/maapan/mool/db"
	"github.com/maapan/mool/httpx"
)

// Price list kinds.
const (
	ListRetail    = "retail"
	ListWholesale = "wholesale"
	ListParty     = "party"
)

// Scheme kinds.
const (
	SchemeFreeQty = "free_qty" // buy X get Y free
	SchemePercent = "percent"  // % off by quantity slab
	SchemeFlat    = "flat"     // ₹ off per unit by quantity slab
)

// ---------- price lists ----------

// PriceList is a named set of item prices.
type PriceList struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	Kind      string           `json:"kind"` // retail | wholesale | party
	IsDefault bool             `json:"is_default"`
	Priority  int              `json:"priority"`
	ValidFrom string           `json:"valid_from,omitempty"`
	ValidTo   string           `json:"valid_to,omitempty"`
	Active    bool             `json:"active"`
	PartyIDs  []string         `json:"party_ids"`
	Items     []PriceListPrice `json:"items,omitempty"`
	ItemCount int              `json:"item_count"`
	UpdatedAt time.Time        `json:"updated_at"`
}

// PriceListPrice is one price row: per item, unit ("" = base unit), minimum
// quantity (in that unit) and effective dates. Prices follow the item's
// price_includes_tax convention, like sale_price_paise.
type PriceListPrice struct {
	ItemID     string  `json:"item_id"`
	ItemName   string  `json:"item_name,omitempty"`
	Unit       string  `json:"unit,omitempty"`
	MinQty     float64 `json:"min_qty,omitempty"`
	ValidFrom  string  `json:"valid_from,omitempty"`
	ValidTo    string  `json:"valid_to,omitempty"`
	PricePaise int64   `json:"price_paise"`
}

// PriceListInput is the body of POST /v1/price-lists and PUT /v1/price-lists/{id}.
type PriceListInput struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	IsDefault bool     `json:"is_default,omitempty"`
	Priority  int      `json:"priority,omitempty"`
	ValidFrom string   `json:"valid_from,omitempty"`
	ValidTo   string   `json:"valid_to,omitempty"`
	Active    *bool    `json:"active,omitempty"`
	PartyIDs  []string `json:"party_ids,omitempty"`
}

// Validate checks a price list header.
func (in *PriceListInput) Validate() error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 100 {
		return httpx.Validation("name is required (max 100 characters)")
	}
	switch in.Kind {
	case ListRetail, ListWholesale, ListParty:
	case "":
		in.Kind = ListRetail
	default:
		return httpx.Validation("kind must be retail, wholesale or party")
	}
	if in.Kind == ListParty && in.IsDefault {
		return httpx.Validation("a party price list cannot be a default")
	}
	from, to, err := parseRange(in.ValidFrom, in.ValidTo)
	if err != nil {
		return err
	}
	_, _ = from, to
	return nil
}

// parseRange parses optional YYYY-MM-DD dates and checks from ≤ to.
func parseRange(from, to string) (*time.Time, *time.Time, error) {
	f, err := parseDate(from, "valid_from")
	if err != nil {
		return nil, nil, err
	}
	t, err := parseDate(to, "valid_to")
	if err != nil {
		return nil, nil, err
	}
	if f != nil && t != nil && t.Before(*f) {
		return nil, nil, httpx.Validation("valid_to is before valid_from")
	}
	return f, t, nil
}

func parseDate(v, field string) (*time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	t, err := time.Parse(time.DateOnly, v)
	if err != nil {
		return nil, httpx.Validation("%s must be YYYY-MM-DD", field)
	}
	return &t, nil
}

// SavePriceList creates (id "") or updates a price list header and its parties.
func (s *Service) SavePriceList(ctx context.Context, tenantID, id string, in PriceListInput) (*PriceList, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	from, to, _ := parseRange(in.ValidFrom, in.ValidTo)
	active := in.Active == nil || *in.Active
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if in.IsDefault {
			if _, err := tx.Exec(ctx, `UPDATE price_lists SET is_default=false WHERE tenant_id=$1 AND kind=$2 AND is_default AND id::text <> $3`,
				tenantID, in.Kind, id); err != nil {
				return err
			}
		}
		var err error
		if id == "" {
			err = tx.QueryRow(ctx, `INSERT INTO price_lists (tenant_id, name, kind, is_default, priority, valid_from, valid_to, active)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`, tenantID, in.Name, in.Kind, in.IsDefault, in.Priority, from, to, active).Scan(&id)
		} else {
			var tag interface{ RowsAffected() int64 }
			tag, err = tx.Exec(ctx, `UPDATE price_lists SET name=$3, kind=$4, is_default=$5, priority=$6, valid_from=$7, valid_to=$8,
				active=$9, updated_at=now() WHERE tenant_id=$1 AND id=$2`, tenantID, id, in.Name, in.Kind, in.IsDefault, in.Priority, from, to, active)
			if err == nil && tag.RowsAffected() == 0 {
				return httpx.NotFound("price list not found")
			}
		}
		if err != nil {
			if httpx.AsError(conflict(err)).Status == 409 {
				return httpx.Conflict("a price list named %q already exists", in.Name)
			}
			return err
		}
		if in.PartyIDs != nil {
			return setListParties(ctx, tx, tenantID, id, in.PartyIDs)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetPriceList(ctx, tenantID, id, false)
}

func setListParties(ctx context.Context, tx pgx.Tx, tenantID, listID string, parties []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM price_list_parties WHERE price_list_id=$1`, listID); err != nil {
		return err
	}
	for _, p := range parties {
		if strings.TrimSpace(p) == "" {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO price_list_parties (tenant_id, price_list_id, party_id) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`,
			tenantID, listID, p); err != nil {
			return err
		}
	}
	return nil
}

const priceListCols = `l.id, l.name, l.kind, l.is_default, l.priority, l.valid_from, l.valid_to, l.active, l.updated_at,
	COALESCE((SELECT array_agg(p.party_id::text ORDER BY p.party_id) FROM price_list_parties p WHERE p.price_list_id=l.id), '{}'),
	(SELECT count(DISTINCT pi.item_id) FROM price_list_items pi WHERE pi.price_list_id=l.id)`

func scanPriceList(row pgx.Row) (*PriceList, error) {
	var l PriceList
	var from, to *time.Time
	if err := row.Scan(&l.ID, &l.Name, &l.Kind, &l.IsDefault, &l.Priority, &from, &to, &l.Active, &l.UpdatedAt, &l.PartyIDs, &l.ItemCount); err != nil {
		return nil, err
	}
	l.ValidFrom, l.ValidTo = dateString(from), dateString(to)
	return &l, nil
}

// PriceLists lists the tenant's price lists.
func (s *Service) PriceLists(ctx context.Context, tenantID string) ([]PriceList, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+priceListCols+` FROM price_lists l WHERE l.tenant_id=$1 ORDER BY l.kind, l.priority DESC, l.name`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PriceList{}
	for rows.Next() {
		l, err := scanPriceList(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

// GetPriceList loads a list, with its prices when withItems.
func (s *Service) GetPriceList(ctx context.Context, tenantID, id string, withItems bool) (*PriceList, error) {
	l, err := scanPriceList(s.DB.QueryRow(ctx, `SELECT `+priceListCols+` FROM price_lists l WHERE l.tenant_id=$1 AND l.id=$2`, tenantID, id))
	if err != nil {
		if db.IsNotFound(err) {
			return nil, httpx.NotFound("price list not found")
		}
		return nil, err
	}
	if !withItems {
		return l, nil
	}
	rows, err := s.DB.Query(ctx, `SELECT pi.item_id, i.name, pi.unit, pi.min_qty::float8, pi.valid_from, pi.valid_to, pi.price_paise
		FROM price_list_items pi JOIN items i ON i.id = pi.item_id WHERE pi.price_list_id=$1
		ORDER BY lower(i.name), pi.unit, pi.min_qty, pi.valid_from`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	l.Items = []PriceListPrice{}
	for rows.Next() {
		var p PriceListPrice
		var from time.Time
		var to *time.Time
		if err := rows.Scan(&p.ItemID, &p.ItemName, &p.Unit, &p.MinQty, &from, &to, &p.PricePaise); err != nil {
			return nil, err
		}
		p.ValidFrom, p.ValidTo = dateString(&from), dateString(to)
		l.Items = append(l.Items, p)
	}
	return l, rows.Err()
}

// PriceListItemsInput is the body of PUT /v1/price-lists/{id}/items.
type PriceListItemsInput struct {
	Items []PriceListPrice `json:"items"`
	// Replace deletes the list's other prices (a full upload); otherwise the
	// rows are upserted by (item, unit, min_qty, valid_from).
	Replace bool `json:"replace,omitempty"`
}

// Validate checks price rows.
func (in *PriceListItemsInput) Validate() error {
	if len(in.Items) > 5000 {
		return httpx.Validation("send up to 5000 prices per call")
	}
	for i := range in.Items {
		p := &in.Items[i]
		p.Unit = strings.ToUpper(strings.TrimSpace(p.Unit))
		switch {
		case p.ItemID == "":
			return httpx.Validation("price %d: item_id is required", i+1)
		case p.PricePaise < 0:
			return httpx.Validation("price %d: price cannot be negative", i+1)
		case p.MinQty < 0:
			return httpx.Validation("price %d: min_qty cannot be negative", i+1)
		}
		if _, _, err := parseRange(p.ValidFrom, p.ValidTo); err != nil {
			return httpx.Validation("price %d: %s", i+1, httpx.AsError(err).Message)
		}
	}
	return nil
}

// SetPriceListItems upserts (or replaces) a list's prices. A price above
// the item's MRP (per base unit, or MRP × factor for an alternate unit) is
// refused: MRP is the legal ceiling.
func (s *Service) SetPriceListItems(ctx context.Context, tenantID, listID string, in PriceListItemsInput) (*PriceList, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	if _, err := s.GetPriceList(ctx, tenantID, listID, false); err != nil {
		return nil, err
	}
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if in.Replace {
			if _, err := tx.Exec(ctx, `DELETE FROM price_list_items WHERE price_list_id=$1`, listID); err != nil {
				return err
			}
		}
		items := map[string]*Item{}
		for i, p := range in.Items {
			it := items[p.ItemID]
			if it == nil {
				var err error
				if it, err = getItem(ctx, tx, tenantID, p.ItemID); err != nil {
					return httpx.Validation("price %d: %s", i+1, httpx.AsError(err).Message)
				}
				items[p.ItemID] = it
			}
			unit := p.Unit
			if unit == it.Unit {
				unit = ""
			}
			factor := 1.0
			if unit != "" {
				f, ok := ToBaseUnit(1, unit, it.Unit, it.AltUnits)
				if !ok {
					return httpx.Validation("price %d: %s has no unit %s", i+1, it.Name, unit)
				}
				factor = f
			}
			if it.MRPPaise > 0 && float64(p.PricePaise) > math.Round(float64(it.MRPPaise)*factor) {
				return httpx.Validation("price %d: %s above MRP", i+1, it.Name)
			}
			from, to, _ := parseRange(p.ValidFrom, p.ValidTo)
			if from == nil {
				d := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
				from = &d
			}
			if _, err := tx.Exec(ctx, `INSERT INTO price_list_items (tenant_id, price_list_id, item_id, unit, min_qty, valid_from, valid_to, price_paise)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
				ON CONFLICT (price_list_id, item_id, unit, min_qty, valid_from) DO UPDATE SET valid_to=EXCLUDED.valid_to, price_paise=EXCLUDED.price_paise`,
				tenantID, listID, p.ItemID, unit, p.MinQty, from, to, p.PricePaise); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `UPDATE price_lists SET updated_at=now() WHERE id=$1`, listID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.GetPriceList(ctx, tenantID, listID, true)
}

// SetPriceListParties replaces the parties a list applies to.
func (s *Service) SetPriceListParties(ctx context.Context, tenantID, listID string, parties []string) (*PriceList, error) {
	if _, err := s.GetPriceList(ctx, tenantID, listID, false); err != nil {
		return nil, err
	}
	if err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error { return setListParties(ctx, tx, tenantID, listID, parties) }); err != nil {
		return nil, err
	}
	return s.GetPriceList(ctx, tenantID, listID, false)
}

// ---------- schemes ----------

// Slab is one quantity break of a percent / flat scheme. MinQty is in the
// item's base unit.
type Slab struct {
	MinQty          float64 `json:"min_qty"`
	PercentBP       int     `json:"percent_bp,omitempty"`         // percent: 1000 = 10 %
	OffPerUnitPaise int64   `json:"off_per_unit_paise,omitempty"` // flat: off each base unit
}

// Scheme is a promotional offer.
type Scheme struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Kind       string    `json:"kind"`
	BuyQty     float64   `json:"buy_qty,omitempty"`
	FreeQty    float64   `json:"free_qty,omitempty"`
	FreeItemID string    `json:"free_item_id,omitempty"` // "" = the same item
	Slabs      []Slab    `json:"slabs"`
	ItemIDs    []string  `json:"item_ids"`
	Category   string    `json:"category,omitempty"`
	PartyIDs   []string  `json:"party_ids,omitempty"` // empty = every party (and walk-in customers)
	ValidFrom  string    `json:"valid_from"`
	ValidTo    string    `json:"valid_to,omitempty"`
	Active     bool      `json:"active"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// SchemeInput is the body of POST /v1/schemes and PUT /v1/schemes/{id}.
type SchemeInput struct {
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	BuyQty     float64  `json:"buy_qty,omitempty"`
	FreeQty    float64  `json:"free_qty,omitempty"`
	FreeItemID string   `json:"free_item_id,omitempty"`
	Slabs      []Slab   `json:"slabs,omitempty"`
	ItemIDs    []string `json:"item_ids,omitempty"`
	Category   string   `json:"category,omitempty"`
	PartyIDs   []string `json:"party_ids,omitempty"`
	ValidFrom  string   `json:"valid_from"`
	ValidTo    string   `json:"valid_to,omitempty"`
	Active     *bool    `json:"active,omitempty"`
}

// Validate checks a scheme.
func (in *SchemeInput) Validate() error {
	in.Name, in.Category = strings.TrimSpace(in.Name), strings.TrimSpace(in.Category)
	if in.Name == "" || len(in.Name) > 100 {
		return httpx.Validation("name is required (max 100 characters)")
	}
	if len(in.ItemIDs) == 0 && in.Category == "" {
		return httpx.Validation("a scheme needs item_ids or a category")
	}
	if strings.TrimSpace(in.ValidFrom) == "" {
		return httpx.Validation("valid_from is required")
	}
	if _, _, err := parseRange(in.ValidFrom, in.ValidTo); err != nil {
		return err
	}
	switch in.Kind {
	case SchemeFreeQty:
		if in.BuyQty <= 0 || in.FreeQty <= 0 {
			return httpx.Validation("buy X get Y needs positive buy_qty and free_qty")
		}
		in.Slabs = nil
	case SchemePercent, SchemeFlat:
		if len(in.Slabs) == 0 || len(in.Slabs) > 20 {
			return httpx.Validation("a %s scheme needs 1 to 20 slabs", in.Kind)
		}
		in.BuyQty, in.FreeQty, in.FreeItemID = 0, 0, ""
		sort.Slice(in.Slabs, func(i, j int) bool { return in.Slabs[i].MinQty < in.Slabs[j].MinQty })
		for i, sl := range in.Slabs {
			switch {
			case sl.MinQty < 0:
				return httpx.Validation("slab %d: min_qty cannot be negative", i+1)
			case i > 0 && sl.MinQty == in.Slabs[i-1].MinQty:
				return httpx.Validation("slab %d repeats min_qty %v", i+1, sl.MinQty)
			case in.Kind == SchemePercent && (sl.PercentBP <= 0 || sl.PercentBP > 10000):
				return httpx.Validation("slab %d: percent_bp must be 1 to 10000", i+1)
			case in.Kind == SchemeFlat && sl.OffPerUnitPaise <= 0:
				return httpx.Validation("slab %d: off_per_unit_paise must be positive", i+1)
			}
		}
	default:
		return httpx.Validation("kind must be free_qty, percent or flat")
	}
	return nil
}

// SaveScheme creates (id "") or replaces a scheme.
func (s *Service) SaveScheme(ctx context.Context, tenantID, id string, in SchemeInput) (*Scheme, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	from, to, _ := parseRange(in.ValidFrom, in.ValidTo)
	active := in.Active == nil || *in.Active
	slabs := in.Slabs
	if slabs == nil {
		slabs = []Slab{}
	}
	var parties []string
	for _, p := range in.PartyIDs {
		if strings.TrimSpace(p) != "" {
			parties = append(parties, p)
		}
	}
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var err error
		if in.FreeItemID != "" {
			if _, err := getItem(ctx, tx, tenantID, in.FreeItemID); err != nil {
				return httpx.Validation("free item: %s", httpx.AsError(err).Message)
			}
		}
		if id == "" {
			err = tx.QueryRow(ctx, `INSERT INTO schemes (tenant_id, name, kind, buy_qty, free_qty, free_item_id, slabs, category, party_ids,
				valid_from, valid_to, active) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING id`,
				tenantID, in.Name, in.Kind, in.BuyQty, in.FreeQty, nilIfEmpty(in.FreeItemID), slabs, nilIfEmpty(in.Category), parties,
				from, to, active).Scan(&id)
		} else {
			var tag interface{ RowsAffected() int64 }
			tag, err = tx.Exec(ctx, `UPDATE schemes SET name=$3, kind=$4, buy_qty=$5, free_qty=$6, free_item_id=$7, slabs=$8, category=$9,
				party_ids=$10, valid_from=$11, valid_to=$12, active=$13, updated_at=now() WHERE tenant_id=$1 AND id=$2`,
				tenantID, id, in.Name, in.Kind, in.BuyQty, in.FreeQty, nilIfEmpty(in.FreeItemID), slabs, nilIfEmpty(in.Category), parties,
				from, to, active)
			if err == nil && tag.RowsAffected() == 0 {
				return httpx.NotFound("scheme not found")
			}
		}
		if err != nil {
			if httpx.AsError(conflict(err)).Status == 409 {
				return httpx.Conflict("a scheme named %q already exists", in.Name)
			}
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM scheme_items WHERE scheme_id=$1`, id); err != nil {
			return err
		}
		for _, itemID := range in.ItemIDs {
			if _, err := getItem(ctx, tx, tenantID, itemID); err != nil {
				return httpx.Validation("item %s: %s", itemID, httpx.AsError(err).Message)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO scheme_items (scheme_id, item_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, id, itemID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetScheme(ctx, tenantID, id)
}

const schemeCols = `s.id, s.name, s.kind, s.buy_qty::float8, s.free_qty::float8, s.free_item_id, s.slabs, s.category,
	COALESCE(s.party_ids::text[], '{}'), s.valid_from, s.valid_to, s.active, s.updated_at,
	COALESCE((SELECT array_agg(si.item_id::text ORDER BY si.item_id) FROM scheme_items si WHERE si.scheme_id=s.id), '{}')`

func scanScheme(row pgx.Row) (*Scheme, error) {
	var x Scheme
	var free, cat *string
	var from time.Time
	var to *time.Time
	if err := row.Scan(&x.ID, &x.Name, &x.Kind, &x.BuyQty, &x.FreeQty, &free, &x.Slabs, &cat, &x.PartyIDs, &from, &to, &x.Active,
		&x.UpdatedAt, &x.ItemIDs); err != nil {
		return nil, err
	}
	x.FreeItemID, x.Category = deref(free), deref(cat)
	x.ValidFrom, x.ValidTo = dateString(&from), dateString(to)
	if x.Slabs == nil {
		x.Slabs = []Slab{}
	}
	return &x, nil
}

// GetScheme loads one scheme.
func (s *Service) GetScheme(ctx context.Context, tenantID, id string) (*Scheme, error) {
	x, err := scanScheme(s.DB.QueryRow(ctx, `SELECT `+schemeCols+` FROM schemes s WHERE s.tenant_id=$1 AND s.id=$2`, tenantID, id))
	if db.IsNotFound(err) {
		return nil, httpx.NotFound("scheme not found")
	}
	return x, err
}

// Schemes lists schemes; activeOn (YYYY-MM-DD) keeps those in force that day.
func (s *Service) Schemes(ctx context.Context, tenantID, activeOn string) ([]Scheme, error) {
	on, err := parseDate(activeOn, "active_on")
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT `+schemeCols+` FROM schemes s WHERE s.tenant_id=$1
		AND ($2::date IS NULL OR (s.active AND s.valid_from <= $2 AND (s.valid_to IS NULL OR s.valid_to >= $2)))
		ORDER BY s.valid_from DESC, s.name`, tenantID, on)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Scheme{}
	for rows.Next() {
		x, err := scanScheme(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *x)
	}
	return out, rows.Err()
}

// SetSchemeActive switches a scheme on or off.
func (s *Service) SetSchemeActive(ctx context.Context, tenantID, id string, active bool) error {
	tag, err := s.DB.Exec(ctx, `UPDATE schemes SET active=$3, updated_at=now() WHERE tenant_id=$1 AND id=$2`, tenantID, id, active)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return httpx.NotFound("scheme not found")
	}
	return nil
}

// SchemeBenefit is what a scheme gives one line.
type SchemeBenefit struct {
	DiscountPaise int64   // off the line (percent / flat)
	FreeQty       float64 // base units of the free item (free_qty)
	ValuePaise    int64   // money value, to pick the best scheme
	Slab          *Slab
}

// EvaluateScheme computes a scheme's benefit for a line of baseQty base
// units worth grossPaise; freeUnitPaise prices one base unit of the free
// item (to value buy-X-get-Y offers). Pure: no I/O.
func EvaluateScheme(sc Scheme, baseQty float64, grossPaise, freeUnitPaise int64) SchemeBenefit {
	var b SchemeBenefit
	if baseQty <= 0 {
		return b
	}
	switch sc.Kind {
	case SchemeFreeQty:
		if sc.BuyQty <= 0 {
			return b
		}
		sets := math.Floor(Q3(baseQty/sc.BuyQty) + 1e-9)
		b.FreeQty = Q3(sets * sc.FreeQty)
		b.ValuePaise = int64(math.Round(b.FreeQty * float64(freeUnitPaise)))
	case SchemePercent, SchemeFlat:
		for i := range sc.Slabs {
			if sc.Slabs[i].MinQty <= baseQty+1e-9 && (b.Slab == nil || sc.Slabs[i].MinQty >= b.Slab.MinQty) {
				b.Slab = &sc.Slabs[i]
			}
		}
		if b.Slab == nil {
			return b
		}
		if sc.Kind == SchemePercent {
			b.DiscountPaise = int64(math.Round(float64(grossPaise) * float64(b.Slab.PercentBP) / 10000))
		} else {
			b.DiscountPaise = int64(math.Round(baseQty * float64(b.Slab.OffPerUnitPaise)))
		}
		if b.DiscountPaise > grossPaise {
			b.DiscountPaise = grossPaise
		}
		b.ValuePaise = b.DiscountPaise
	}
	return b
}

// ---------- quote ----------

// QuoteLine is one requested line.
type QuoteLine struct {
	LineNo int     `json:"line_no"`
	ItemID string  `json:"item_id"`
	Qty    float64 `json:"qty"`
	Unit   string  `json:"unit,omitempty"`
}

// QuoteInput is the body of POST /internal/v1/pricing/quote (and /v1/pricing/quote).
type QuoteInput struct {
	PartyID     string      `json:"party_id,omitempty"`
	PriceListID string      `json:"price_list_id,omitempty"` // force a list (still below the party's own lists)
	Channel     string      `json:"channel,omitempty"`       // retail (default) | wholesale: which default list applies
	Date        string      `json:"date,omitempty"`          // YYYY-MM-DD, default today (IST)
	StoreID     string      `json:"store_id,omitempty"`      // informational
	Lines       []QuoteLine `json:"lines"`
}

// Validate checks a quote request.
func (in *QuoteInput) Validate() error {
	if len(in.Lines) == 0 || len(in.Lines) > 500 {
		return httpx.Validation("quote 1 to 500 lines")
	}
	switch in.Channel {
	case "", ListRetail, ListWholesale:
	default:
		return httpx.Validation("channel must be retail or wholesale")
	}
	if _, err := parseDate(in.Date, "date"); err != nil {
		return err
	}
	for i := range in.Lines {
		l := &in.Lines[i]
		l.Unit = strings.ToUpper(strings.TrimSpace(l.Unit))
		if l.LineNo == 0 {
			l.LineNo = i + 1
		}
		if l.ItemID == "" || l.Qty <= 0 {
			return httpx.Validation("line %d: item_id and a positive qty are required", l.LineNo)
		}
	}
	return nil
}

// QuotedLine is the price and offer for one line. Amounts follow the item's
// price_includes_tax convention; GST is computed by aadesh/kar on the net.
type QuotedLine struct {
	LineNo          int     `json:"line_no"`
	ItemID          string  `json:"item_id"`
	ItemName        string  `json:"item_name"`
	Unit            string  `json:"unit"`
	Qty             float64 `json:"qty"`
	BaseQty         float64 `json:"base_qty"`
	MRPPaise        int64   `json:"mrp_paise"`        // per line unit
	StandardPaise   int64   `json:"standard_paise"`   // item sale price per line unit
	UnitPricePaise  int64   `json:"unit_price_paise"` // after the price list
	PriceSource     string  `json:"price_source"`     // price_list | item
	PriceListID     string  `json:"price_list_id,omitempty"`
	PriceListName   string  `json:"price_list_name,omitempty"`
	GrossPaise      int64   `json:"gross_paise"`    // unit price × qty
	DiscountPaise   int64   `json:"discount_paise"` // scheme discount on the line
	NetPaise        int64   `json:"net_paise"`      // gross − discount
	PriceInclTax    bool    `json:"price_includes_tax"`
	SchemeID        string  `json:"scheme_id,omitempty"`
	SchemeName      string  `json:"scheme_name,omitempty"`
	SchemeKind      string  `json:"scheme_kind,omitempty"`
	SchemePercentBP int     `json:"scheme_percent_bp,omitempty"`
}

// FreeLine is a free item a buy-X-get-Y scheme adds.
type FreeLine struct {
	ForLineNo  int     `json:"for_line_no"`
	ItemID     string  `json:"item_id"`
	ItemName   string  `json:"item_name"`
	Unit       string  `json:"unit"`
	Qty        float64 `json:"qty"` // base units
	ValuePaise int64   `json:"value_paise"`
	SchemeID   string  `json:"scheme_id"`
	SchemeName string  `json:"scheme_name"`
}

// Quote is the response of the pricing quote.
type Quote struct {
	Date          string       `json:"date"`
	PartyID       string       `json:"party_id,omitempty"`
	Lines         []QuotedLine `json:"lines"`
	FreeItems     []FreeLine   `json:"free_items"`
	GrossPaise    int64        `json:"gross_paise"`
	DiscountPaise int64        `json:"discount_paise"`
	NetPaise      int64        `json:"net_paise"`
}

type quoteList struct {
	id, name, kind string
}

// Quote prices lines for a party on a date. Per line the first list that has
// a price wins, in this order: lists assigned to the party (party lists before
// wholesale/retail ones, then by priority), the requested price_list_id, the
// default list of the channel (retail unless channel=wholesale); otherwise the
// item's sale price. Prices are matched in the line's unit first (largest
// min_qty ≤ qty, newest valid_from), then in the base unit × the unit factor.
// Then the single best scheme in force for the item (by value) applies:
// a percent / flat slab discount or free units (schemes do not stack).
func (s *Service) Quote(ctx context.Context, tenantID string, in QuoteInput) (*Quote, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	day := s.today()
	if d, _ := parseDate(in.Date, "date"); d != nil {
		day = *d
	}
	channel := in.Channel
	if channel == "" {
		channel = ListRetail
	}
	// Candidate lists in rank order.
	var lists []quoteList
	seen := map[string]bool{}
	addLists := func(query string, args ...any) error {
		rows, err := s.DB.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l quoteList
			if err := rows.Scan(&l.id, &l.name, &l.kind); err != nil {
				return err
			}
			if !seen[l.id] {
				seen[l.id] = true
				lists = append(lists, l)
			}
		}
		return rows.Err()
	}
	const inForce = ` l.active AND (l.valid_from IS NULL OR l.valid_from <= $2) AND (l.valid_to IS NULL OR l.valid_to >= $2)`
	if in.PartyID != "" {
		if err := addLists(`SELECT l.id, l.name, l.kind FROM price_lists l JOIN price_list_parties p ON p.price_list_id=l.id
			WHERE l.tenant_id=$1 AND p.party_id::text=$3 AND`+inForce+` ORDER BY (l.kind='party') DESC, l.priority DESC, l.name`,
			tenantID, day, in.PartyID); err != nil {
			return nil, err
		}
	}
	if in.PriceListID != "" {
		if err := addLists(`SELECT l.id, l.name, l.kind FROM price_lists l WHERE l.tenant_id=$1 AND l.id::text=$3 AND`+inForce,
			tenantID, day, in.PriceListID); err != nil {
			return nil, err
		}
	}
	if err := addLists(`SELECT l.id, l.name, l.kind FROM price_lists l WHERE l.tenant_id=$1 AND l.kind=$3 AND l.is_default AND`+inForce,
		tenantID, day, channel); err != nil {
		return nil, err
	}
	listIDs := make([]string, len(lists))
	for i, l := range lists {
		listIDs[i] = l.id
	}

	out := &Quote{Date: day.Format(time.DateOnly), PartyID: in.PartyID, Lines: []QuotedLine{}, FreeItems: []FreeLine{}}
	items := map[string]*Item{}
	load := func(id string) (*Item, error) {
		if it := items[id]; it != nil {
			return it, nil
		}
		it, err := getItem(ctx, s.DB, tenantID, id)
		if err != nil {
			return nil, err
		}
		items[id] = it
		return it, nil
	}
	for _, l := range in.Lines {
		it, err := load(l.ItemID)
		if err != nil {
			return nil, httpx.Validation("line %d: %s", l.LineNo, httpx.AsError(err).Message)
		}
		unit := l.Unit
		if unit == "" {
			unit = it.Unit
		}
		baseQty, ok := ToBaseUnit(l.Qty, unit, it.Unit, it.AltUnits)
		if !ok {
			return nil, httpx.Validation("line %d: %s has no unit %s", l.LineNo, it.Name, unit)
		}
		factor := baseQty / l.Qty
		q := QuotedLine{
			LineNo: l.LineNo, ItemID: it.ID, ItemName: it.Name, Unit: unit, Qty: l.Qty, BaseQty: baseQty,
			MRPPaise: int64(math.Round(float64(it.MRPPaise) * factor)), StandardPaise: int64(math.Round(float64(it.SalePricePaise) * factor)),
			PriceSource: "item", PriceInclTax: it.PriceIncludesTax,
		}
		q.UnitPricePaise = q.StandardPaise
		if len(listIDs) > 0 {
			price, listID, found, err := s.listPrice(ctx, tenantID, listIDs, it, unit, l.Qty, baseQty, factor, day)
			if err != nil {
				return nil, err
			}
			if found {
				q.UnitPricePaise, q.PriceSource, q.PriceListID = price, "price_list", listID
				for _, cand := range lists {
					if cand.id == listID {
						q.PriceListName = cand.name
					}
				}
			}
		}
		q.GrossPaise = int64(math.Round(float64(q.UnitPricePaise) * l.Qty))

		// Schemes.
		schemes, err := s.schemesFor(ctx, tenantID, it, in.PartyID, day)
		if err != nil {
			return nil, err
		}
		var best *Scheme
		var bestB SchemeBenefit
		for i := range schemes {
			sc := schemes[i]
			freeUnit := int64(math.Round(float64(q.UnitPricePaise) / factor))
			if sc.Kind == SchemeFreeQty && sc.FreeItemID != "" && sc.FreeItemID != it.ID {
				fi, err := load(sc.FreeItemID)
				if err != nil {
					continue
				}
				freeUnit = fi.SalePricePaise
			}
			b := EvaluateScheme(sc, baseQty, q.GrossPaise, freeUnit)
			if b.ValuePaise > bestB.ValuePaise {
				best, bestB = &schemes[i], b
			}
		}
		if best != nil {
			q.SchemeID, q.SchemeName, q.SchemeKind = best.ID, best.Name, best.Kind
			q.DiscountPaise = bestB.DiscountPaise
			if bestB.Slab != nil {
				q.SchemePercentBP = bestB.Slab.PercentBP
			}
			if bestB.FreeQty > 0 {
				fi := it
				if best.FreeItemID != "" {
					if fi, err = load(best.FreeItemID); err != nil {
						return nil, err
					}
				}
				out.FreeItems = append(out.FreeItems, FreeLine{ForLineNo: l.LineNo, ItemID: fi.ID, ItemName: fi.Name, Unit: fi.Unit,
					Qty: bestB.FreeQty, ValuePaise: bestB.ValuePaise, SchemeID: best.ID, SchemeName: best.Name})
			}
		}
		q.NetPaise = q.GrossPaise - q.DiscountPaise
		out.GrossPaise += q.GrossPaise
		out.DiscountPaise += q.DiscountPaise
		out.NetPaise += q.NetPaise
		out.Lines = append(out.Lines, q)
	}
	return out, nil
}

// listPrice finds the first list (in rank order) with a price for the item
// in force on day, in the line's unit, else in the base unit × factor.
func (s *Service) listPrice(ctx context.Context, tenantID string, listIDs []string, it *Item, unit string, qty, baseQty, factor float64,
	day time.Time) (int64, string, bool, error) {
	lineUnit := unit
	if lineUnit == it.Unit {
		lineUnit = ""
	}
	rows, err := s.DB.Query(ctx, `SELECT price_list_id, unit, min_qty::float8, price_paise FROM price_list_items
		WHERE tenant_id=$1 AND item_id=$2 AND price_list_id = ANY($3::uuid[]) AND unit IN ($4, '')
		AND valid_from <= $5 AND (valid_to IS NULL OR valid_to >= $5)
		ORDER BY min_qty DESC, valid_from DESC`, tenantID, it.ID, listIDs, lineUnit, day)
	if err != nil {
		return 0, "", false, err
	}
	type cand struct {
		unit  string
		min   float64
		price int64
	}
	byList := map[string][]cand{}
	for rows.Next() {
		var listID string
		var c cand
		if err := rows.Scan(&listID, &c.unit, &c.min, &c.price); err != nil {
			rows.Close()
			return 0, "", false, err
		}
		byList[listID] = append(byList[listID], c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, "", false, err
	}
	for _, id := range listIDs {
		cs := byList[id]
		// Line unit first (only differs from base for an alternate unit).
		if lineUnit != "" {
			for _, c := range cs {
				if c.unit == lineUnit && c.min <= qty+1e-9 {
					return c.price, id, true, nil
				}
			}
		}
		for _, c := range cs {
			if c.unit == "" && c.min <= baseQty+1e-9 {
				return int64(math.Round(float64(c.price) * factor)), id, true, nil
			}
		}
	}
	return 0, "", false, nil
}

// schemesFor returns the schemes in force on day for an item (directly or by
// category) and party.
func (s *Service) schemesFor(ctx context.Context, tenantID string, it *Item, partyID string, day time.Time) ([]Scheme, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+schemeCols+` FROM schemes s WHERE s.tenant_id=$1 AND s.active
		AND s.valid_from <= $2 AND (s.valid_to IS NULL OR s.valid_to >= $2)
		AND (EXISTS (SELECT 1 FROM scheme_items si WHERE si.scheme_id=s.id AND si.item_id=$3) OR (s.category IS NOT NULL AND s.category=$4))
		AND (s.party_ids IS NULL OR cardinality(s.party_ids)=0 OR ($5 <> '' AND $5 = ANY(s.party_ids::text[])))
		ORDER BY s.name`, tenantID, day, it.ID, it.Category, partyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Scheme
	for rows.Next() {
		x, err := scanScheme(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *x)
	}
	return out, rows.Err()
}
