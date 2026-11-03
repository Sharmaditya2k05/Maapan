package invoice

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/maapan/aadesh/internal/clients"
	"github.com/maapan/mool/gst"
	"github.com/maapan/mool/httpx"
)

// Which way goods go on a document, from the business's point of view.
const (
	dirIn  = 1  // purchase, sale_return, GRN
	dirOut = -1 // sale, purchase_return, delivery challan
)

// isSaleSide reports whether a document type prices like a sale (item tax and
// MRP are enforced): sales, credit notes and every sales-side order.
func isSaleSide(docType string) bool {
	switch docType {
	case DocSale, DocSaleReturn, KindQuotation, KindProforma, KindSalesOrder, KindDeliveryChallan:
		return true
	}
	return false
}

// goodsDirection is the stock direction of a document type (0 = none: quotes, orders).
func goodsDirection(docType string) int {
	switch docType {
	case DocSale, DocPurchaseReturn, KindDeliveryChallan:
		return dirOut
	case DocPurchase, DocSaleReturn, KindGRN:
		return dirIn
	}
	return 0
}

// validateItems is validateLines for an invoice/note.
func (s *Service) validateItems(ctx context.Context, in *CreateInput, storeID string, date time.Time) error {
	return s.validateLines(ctx, in.DocType, storeID, date, in.Lines)
}

// validateLines resolves item lines against bhandar: the item must exist and be
// active, HSN and unit are filled from the master when missing, the unit must be
// the item's base unit or one of its alternate units (BOX of 48 → factor 48), a
// sale-side line must carry the item's tax classification and stay within MRP
// (per base unit), batch-tracked items need a batch (a sale without one gets
// bhandar's FEFO batch), and serial-tracked items need one serial per base unit.
// Free-text lines (no item_id) keep factor 1 and are otherwise left alone.
func (s *Service) validateLines(ctx context.Context, docType, storeID string, date time.Time, lines []LineInput) error {
	for i := range lines {
		lines[i].factor = 1
		if err := checkSerialShape(i, &lines[i]); err != nil {
			return err
		}
	}
	if err := checkSerialsUnique(lines); err != nil {
		return err
	}
	var ids []string
	seen := map[string]bool{}
	for _, l := range lines {
		if l.ItemID != "" && !seen[l.ItemID] {
			seen[l.ItemID] = true
			ids = append(ids, l.ItemID)
		}
	}
	if len(ids) == 0 {
		return checkSerialCounts(lines)
	}
	items, err := s.Dir.Items(ctx, ids)
	if err != nil {
		return err
	}
	saleSide := isSaleSide(docType)
	for i := range lines {
		l := &lines[i]
		if l.ItemID == "" {
			continue
		}
		it, ok := items[l.ItemID]
		if !ok {
			return httpx.Validation("line %d: item %s not found", i+1, l.ItemID)
		}
		if it.Status != "" && it.Status != "active" {
			return httpx.Validation("line %d: item %s is inactive", i+1, it.Name)
		}
		if l.HSN == "" {
			l.HSN = it.HSN
		}
		if l.Unit == "" {
			l.Unit = it.Unit
		}
		factor, err := unitFactor(it, l.Unit)
		if err != nil {
			return httpx.Validation("line %d: %s", i+1, err.Error())
		}
		l.factor, l.baseUnit = factor, strings.ToUpper(it.Unit)
		cat := l.TaxCategory
		if cat == "" {
			cat = gst.CatTaxable
		}
		itemCat := it.TaxCategory
		if itemCat == "" {
			itemCat = gst.CatTaxable
		}
		sameTax := l.TaxRateBP == it.TaxRateBP && l.CessRateBP == it.CessRateBP && l.CessPerUnitPaise == it.CessPerUnitPaise && cat == itemCat
		if !sameTax {
			if saleSide {
				return httpx.Validation("line %d: tax must match the item master (%s, %d bp)", i+1, it.Name, it.TaxRateBP)
			}
			// A supplier's bill may legitimately carry a different rate (rate change, wrong master); keep it but say so.
			s.Log.Warn("purchase line tax differs from item master", "item_id", it.ID, "line_rate_bp", l.TaxRateBP, "item_rate_bp", it.TaxRateBP)
		}
		if saleSide && it.MRPPaise > 0 {
			perBase := int64(math.Round(float64(l.UnitPricePaise) / factor))
			if perBase > it.MRPPaise {
				return httpx.Validation("line %d: price exceeds MRP of %s", i+1, it.Name)
			}
		}
		if it.TrackSerials && len(l.Serials) == 0 && goodsDirection(docType) != 0 && !IsNote(docType) {
			return httpx.Validation("line %d: %s is serial-tracked; send one serial per unit in serials", i+1, it.Name)
		}
		if it.TrackBatches && goodsDirection(docType) != 0 && !IsNote(docType) {
			if err := s.checkBatch(ctx, docType, storeID, date, i, it, l); err != nil {
				return err
			}
		}
	}
	return checkSerialCounts(lines)
}

// unitFactor is how many base units one `unit` of the item holds.
func unitFactor(it clients.Item, unit string) (float64, error) {
	if unit == "" || strings.EqualFold(unit, it.Unit) {
		return 1, nil
	}
	known := []string{strings.ToUpper(it.Unit)}
	for _, a := range it.AltUnits {
		if strings.EqualFold(a.Unit, unit) {
			if a.Factor <= 0 {
				break
			}
			return a.Factor, nil
		}
		known = append(known, strings.ToUpper(a.Unit))
	}
	return 0, errors.New("unit " + strings.ToUpper(unit) + " is not defined for " + it.Name + " (use " + strings.Join(known, ", ") + ")")
}

// checkBatch makes sure a batch-tracked line names a usable batch. Purchases and
// GRNs may bring a new batch; outward lines must name an existing, unexpired
// one, and a sale without a batch takes bhandar's FEFO pick (earliest expiry
// with enough stock). An older bhandar without the internal batches route
// leaves batches unchecked (logged), but a batch is still required.
func (s *Service) checkBatch(ctx context.Context, docType, storeID string, date time.Time, i int, it clients.Item, l *LineInput) error {
	if goodsDirection(docType) == dirIn {
		if strings.TrimSpace(l.BatchNo) == "" {
			return httpx.Validation("line %d: %s is batch-tracked; batch_no is required", i+1, it.Name)
		}
		return nil
	}
	batches, err := s.Dir.Batches(ctx, it.ID, storeID)
	if errors.Is(err, clients.ErrNotSupported) {
		if l.BatchNo == "" {
			return httpx.Validation("line %d: %s is batch-tracked; batch_no is required", i+1, it.Name)
		}
		s.Log.Warn("bhandar has no internal batches route; batch not validated", "item_id", it.ID, "batch_no", l.BatchNo)
		return nil
	}
	if err != nil {
		return err
	}
	day := date.Format(time.DateOnly)
	if l.BatchNo == "" {
		need := l.Qty * l.factor
		if pick := fefo(batches, day, need); pick != "" {
			l.BatchNo = pick
			return nil
		}
		return httpx.Validation("line %d: %s is batch-tracked and no single unexpired batch holds %.3f %s; choose batch_no (or split the line)",
			i+1, it.Name, need, it.Unit).WithDetails(map[string]any{"batches": batches})
	}
	for _, b := range batches {
		if strings.EqualFold(b.BatchNo, l.BatchNo) {
			if b.ExpiryDate != "" && b.ExpiryDate < day {
				return httpx.Validation("line %d: batch %s of %s expired on %s", i+1, b.BatchNo, it.Name, b.ExpiryDate)
			}
			l.BatchNo = b.BatchNo
			return nil
		}
	}
	return httpx.Validation("line %d: batch %s of %s does not exist at this store", i+1, l.BatchNo, it.Name)
}

// fefo picks the unexpired batch that expires first and holds need base units.
func fefo(batches []clients.Batch, day string, need float64) string {
	cands := make([]clients.Batch, 0, len(batches))
	for _, b := range batches {
		if (b.ExpiryDate == "" || b.ExpiryDate >= day) && b.Qty+1e-9 >= need {
			cands = append(cands, b)
		}
	}
	sort.SliceStable(cands, func(a, b int) bool {
		ea, eb := cands[a].ExpiryDate, cands[b].ExpiryDate
		if ea == "" {
			ea = "9999-12-31"
		}
		if eb == "" {
			eb = "9999-12-31"
		}
		return ea < eb
	})
	if len(cands) == 0 {
		return ""
	}
	return cands[0].BatchNo
}

func checkSerialShape(i int, l *LineInput) error {
	if len(l.Serials) == 0 {
		return nil
	}
	if len(l.Serials) > 1000 {
		return httpx.Validation("line %d: at most 1000 serials per line", i+1)
	}
	for j, sn := range l.Serials {
		sn = strings.ToUpper(strings.TrimSpace(sn))
		if sn == "" || len(sn) > 64 {
			return httpx.Validation("line %d: serials must be 1-64 characters", i+1)
		}
		l.Serials[j] = sn
	}
	return nil
}

func checkSerialsUnique(lines []LineInput) error {
	seen := map[string]int{}
	for i, l := range lines {
		for _, sn := range l.Serials {
			k := lineKey(l.ItemID, l.Name) + "|" + sn
			if prev, ok := seen[k]; ok {
				return httpx.Validation("line %d: serial %s is already on line %d", i+1, sn, prev+1)
			}
			seen[k] = i
		}
	}
	return nil
}

// checkSerialCounts: a line with serials carries exactly one per base unit.
func checkSerialCounts(lines []LineInput) error {
	for i, l := range lines {
		if len(l.Serials) == 0 {
			continue
		}
		f := l.factor
		if f == 0 {
			f = 1
		}
		base := l.Qty * f
		if math.Abs(base-math.Round(base)) > 1e-9 || int(math.Round(base)) != len(l.Serials) {
			return httpx.Validation("line %d: %g base unit(s) need %g serial(s), got %d", i+1, base, math.Round(base), len(l.Serials))
		}
	}
	return nil
}

// serialMove is the last posted movement of a serial.
type serialMove struct {
	Direction int
	InvoiceID string
}

// lastSerialMoves returns the latest posted movement of each serial of an item.
func lastSerialMoves(ctx context.Context, tx pgx.Tx, tenantID, itemKey string, serials []string) (map[string]serialMove, error) {
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON (s.serial_no) s.serial_no, s.direction, s.invoice_id::text
		FROM invoice_serials s JOIN invoices i ON i.id = s.invoice_id AND i.status = 'posted'
		WHERE s.tenant_id=$1 AND s.item_key=$2 AND s.serial_no = ANY($3)
		ORDER BY s.serial_no, s.created_at DESC, s.id DESC`, tenantID, itemKey, serials)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]serialMove{}
	for rows.Next() {
		var sn string
		var m serialMove
		if err := rows.Scan(&sn, &m.Direction, &m.InvoiceID); err != nil {
			return nil, err
		}
		out[sn] = m
	}
	return out, rows.Err()
}

// postSerials checks every serial line of a document against the serial ledger
// and records it. A serial that last went out (sold) cannot be sold again until
// it comes back on a credit note; one that last came in cannot be bought again.
// A serial the ledger has never seen is accepted either way (opening stock). A
// note's serials must be on the original line. bhandar is asked too when it
// tracks serials; an older bhandar (no route) leaves the local ledger in charge.
func (s *Service) postSerials(ctx context.Context, tx pgx.Tx, tenantID string, inv *Invoice, orig *Invoice) error {
	dir := goodsDirection(inv.DocType)
	if dir == 0 {
		return nil
	}
	for _, l := range inv.Lines {
		if len(l.Serials) == 0 {
			continue
		}
		key := lineKey(deref(l.ItemID), l.Name)
		// Serialise documents moving the same item's serials.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, tenantID+"|serial|"+key); err != nil {
			return err
		}
		if orig != nil {
			onOrig := map[string]bool{}
			for _, ol := range orig.Lines {
				if lineKey(deref(ol.ItemID), ol.Name) == key {
					for _, sn := range ol.Serials {
						onOrig[sn] = true
					}
				}
			}
			for _, sn := range l.Serials {
				if !onOrig[sn] {
					return httpx.Validation("line %d: serial %s is not on the original document", l.LineNo, sn)
				}
			}
		}
		last, err := lastSerialMoves(ctx, tx, tenantID, key, l.Serials)
		if err != nil {
			return err
		}
		for _, sn := range l.Serials {
			m, seen := last[sn]
			if !seen {
				continue
			}
			if m.Direction == dir {
				if dir == dirOut {
					return httpx.Conflict("line %d: serial %s of %s is already sold or returned (document %s)", l.LineNo, sn, l.Name, m.InvoiceID)
				}
				return httpx.Conflict("line %d: serial %s of %s is already in stock (document %s)", l.LineNo, sn, l.Name, m.InvoiceID)
			}
		}
		if l.ItemID != nil && dir == dirOut && !IsNote(inv.DocType) {
			st, err := s.Dir.CheckSerials(ctx, *l.ItemID, inv.StoreID, l.Serials)
			switch {
			case errors.Is(err, clients.ErrNotSupported):
			case err != nil:
				return err
			default:
				for _, sn := range l.Serials {
					if v, ok := st[sn]; ok && (serialGone(v.Status) || (v.Status == "in_stock" && v.StoreID != "" && v.StoreID != inv.StoreID)) {
						return httpx.Conflict("line %d: bhandar says serial %s of %s is %s", l.LineNo, sn, l.Name, v.Status)
					}
				}
			}
		}
		batch := &pgx.Batch{}
		for _, sn := range l.Serials {
			batch.Queue(`INSERT INTO invoice_serials (tenant_id, invoice_id, line_no, item_key, serial_no, direction) VALUES ($1,$2,$3,$4,$5,$6)`,
				tenantID, inv.ID, l.LineNo, key, sn, dir)
		}
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return err
		}
	}
	return nil
}

// serialGone reports a bhandar serial status that can no longer be sold.
func serialGone(status string) bool {
	switch status {
	case "sold", "returned_to_vendor", "written_off":
		return true
	}
	return false
}
