package invoice

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maapan/aadesh/internal/clients"
	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/events"
	"github.com/maapan/mool/gst"
	"github.com/maapan/mool/httpx"
)

// Service implements invoice use cases.
type Service struct {
	DB    *pgxpool.Pool
	Dir   *clients.Directory
	Relay *events.Relay
	Log   *slog.Logger
	Now   func() time.Time
}

// Block sizes a device may reserve for offline billing, by role.
var blockCap = map[string]int64{auth.RoleCashier: 100, auth.RoleStoreManager: 500, auth.RoleAccountant: 100, auth.RoleSuperAdmin: 1000}

// Create validates, prices, numbers and stores an invoice, and queues its event.
// created=false means client_uuid matched an existing invoice (safe retry / offline re-sync).
func (s *Service) Create(ctx context.Context, p *auth.Principal, in CreateInput) (inv *Invoice, created bool, err error) {
	storeID := in.StoreID
	if storeID == "" {
		storeID = p.StoreID
	}
	if storeID == "" {
		return nil, false, httpx.Validation("store_id is required")
	}
	if p.Claims != nil && !p.Claims.CanAccessStore(storeID) {
		return nil, false, httpx.Forbidden("no access to store")
	}

	// Fast path for retries: return the existing invoice before calling other services.
	if in.ClientUUID != "" {
		if existing, err := s.byClientUUID(ctx, p.TenantID, in.ClientUUID); err == nil {
			return existing, false, nil
		} else if !db.IsNotFound(err) {
			return nil, false, err
		}
	}

	store, err := s.Dir.Store(ctx, p.TenantID, storeID)
	if err != nil {
		return nil, false, err
	}

	// Notes copy party, rates and prices from the original document.
	var orig *Invoice
	if IsNote(in.DocType) {
		orig, err = s.loadOriginal(ctx, p.TenantID, storeID, &in)
		if err != nil {
			return nil, false, err
		}
	}

	party, err := s.resolveParty(ctx, in)
	if err != nil {
		return nil, false, err
	}
	pos := in.PlaceOfSupply
	if pos == "" {
		pos = party.StateCode
	}
	if pos == "" {
		pos = store.StateCode
	}

	now := s.Now()
	date, err := s.documentDate(in, now)
	if err != nil {
		return nil, false, err
	}

	if err := s.validateItems(ctx, &in); err != nil {
		return nil, false, err
	}

	lines := make([]gst.Line, len(in.Lines))
	for i, l := range in.Lines {
		lines[i] = l.Line
	}
	sum, err := gst.ComputeWith(lines, gst.Options{
		StoreState: store.StateCode, PlaceOfSupply: pos, RoundToRupee: in.RoundOff == nil || *in.RoundOff,
		SupplyCategory: in.SupplyCategory, ReverseCharge: in.ReverseCharge,
		InvoiceDiscountPaise: in.InvoiceDiscountPaise, InvoiceDiscountBP: in.InvoiceDiscountBP, TCSBP: in.TCSBP, On: date,
	})
	if err != nil {
		return nil, false, httpx.Validation("%s", err.Error())
	}

	paid, change, credit, err := Settle(sum.GrandPaise, in.Tenders)
	if err != nil {
		return nil, false, err
	}
	if in.DocType == DocSale && credit > 0 && party.ID != "" && party.CreditLimitPaise > 0 {
		exposure := party.BalancePaise + credit
		if exposure > party.CreditLimitPaise && !(in.OverrideCreditLimit && canOverrideCredit(p)) {
			return nil, false, httpx.Validation("credit limit exceeded for %s", party.Name).WithDetails(map[string]any{
				"credit_limit_paise": party.CreditLimitPaise, "balance_paise": party.BalancePaise, "bill_credit_paise": credit,
			})
		}
	}
	due := in.DueDate
	if due == "" && credit > 0 && party.CreditDays > 0 {
		due = date.AddDate(0, 0, party.CreditDays).Format(time.DateOnly)
	}

	inv = &Invoice{
		StoreID: storeID, DocType: in.DocType, InvoiceDate: date.Format(time.DateOnly), DueDate: nilIfEmpty(due),
		PartyName: party.Name, StoreState: store.StateCode, StoreGSTIN: nilIfEmpty(store.GSTIN), PlaceOfSupply: pos,
		RegistrationID: nilIfEmpty(store.RegistrationID),
		InterState:     sum.InterState, SupplyCategory: in.SupplyCategory, ReverseCharge: in.ReverseCharge,
		Status: "posted", PaymentStatus: paymentStatus(paid, sum.GrandPaise, 0),
		TaxablePaise: sum.TaxablePaise, DiscountPaise: sum.DiscountPaise, InvoiceDiscountPaise: sum.InvoiceDiscountPaise,
		CGSTPaise: sum.CGSTPaise, SGSTPaise: sum.SGSTPaise, IGSTPaise: sum.IGSTPaise, CessPaise: sum.CessPaise, TCSPaise: sum.TCSPaise,
		RoundOffPaise: sum.RoundOffPaise, GrandTotalPaise: sum.GrandPaise, PaidPaise: paid, ChangePaise: change,
		AmountInWords: gst.AmountInWords(sum.GrandPaise), CreatedBy: p.UserID(), CreatedAt: now,
		PartyID: nilIfEmpty(party.ID), PartyPhone: nilIfEmpty(party.Phone), PartyGSTIN: nilIfEmpty(party.GSTIN),
		PartyState: nilIfEmpty(party.StateCode), SupplierInvoiceNo: nilIfEmpty(strings.TrimSpace(in.SupplierInvoiceNo)),
		SupplierInvoiceDate: nilIfEmpty(in.SupplierInvoiceDate), RefInvoiceID: nilIfEmpty(in.RefInvoiceID),
		Notes: nilIfEmpty(in.Notes), ClientUUID: nilIfEmpty(in.ClientUUID), Tenders: in.Tenders,
	}
	inv.BalancePaise = balance(inv.GrandTotalPaise, 0, paid)
	for i, l := range in.Lines {
		lt := sum.Lines[i]
		unit := strings.ToUpper(strings.TrimSpace(l.Unit))
		if unit == "" {
			unit = "PCS"
		}
		itc := ""
		if IsPurchaseDoc(in.DocType) {
			itc = l.ITCEligibility
			if itc == "" {
				itc = ITCEligible
			}
		}
		inv.Lines = append(inv.Lines, Line{
			LineNo: i + 1, ItemID: nilIfEmpty(l.ItemID), Name: strings.TrimSpace(l.Name), HSN: nilIfEmpty(l.HSN),
			Unit: unit, BatchNo: nilIfEmpty(l.BatchNo), Qty: l.Qty, UnitPricePaise: l.UnitPricePaise,
			DiscountPaise: l.DiscountPaise, DiscountBP: l.DiscountBP, TaxRateBP: lt.RateBP, CessRateBP: l.CessRateBP,
			CessPerUnitPaise: l.CessPerUnitPaise, TaxCategory: lt.TaxCategory, ITCEligibility: itc, PriceIncludesTax: l.PriceIncludesTax,
			TaxablePaise: lt.TaxablePaise, CGSTPaise: lt.CGSTPaise, SGSTPaise: lt.SGSTPaise, IGSTPaise: lt.IGSTPaise,
			CessPaise: lt.CessPaise, TotalPaise: lt.TotalPaise,
		})
	}

	err = db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if orig != nil {
			if err := s.lockAndCheckReturn(ctx, tx, p.TenantID, orig, inv); err != nil {
				return err
			}
		}
		seq, prefix, fy, regKey, err := s.allocateNumber(ctx, tx, p.TenantID, store, storeID, in, FinancialYear(date))
		if err != nil {
			return err
		}
		inv.FY = fy
		inv.Number = FormatNumber(prefix, fy, seq)
		if len(inv.Number) > MaxNumberLen {
			return httpx.Conflict("invoice number %s exceeds GST's 16-character limit; shorten the series prefix", inv.Number)
		}
		if err := insertInvoice(ctx, tx, p.TenantID, seq, regKey, in, inv); err != nil {
			return err
		}
		if orig != nil {
			if err := creditOriginal(ctx, tx, p.TenantID, orig.ID, inv.GrandTotalPaise); err != nil {
				return err
			}
		}
		evType := events.InvoiceCreated
		if IsNote(in.DocType) {
			evType = events.NoteCreated
		}
		ev, err := events.New(evType, "aadesh", p.TenantID, storeID, toPayload(inv))
		if err != nil {
			return err
		}
		return events.Enqueue(ctx, tx, ev)
	})
	if err != nil {
		if db.IsUniqueViolation(err) && in.ClientUUID != "" {
			// A concurrent sync of the same bill won the race; return that one.
			if existing, e2 := s.byClientUUID(ctx, p.TenantID, in.ClientUUID); e2 == nil {
				return existing, false, nil
			}
		}
		if db.IsUniqueViolation(err) {
			if in.DocType == DocPurchase && in.SupplierInvoiceNo != "" {
				return nil, false, httpx.Conflict("supplier bill %s dated %s is already entered for this supplier", in.SupplierInvoiceNo, in.SupplierInvoiceDate)
			}
			return nil, false, httpx.Conflict("invoice number already used")
		}
		return nil, false, err
	}
	s.kick()
	return inv, true, nil
}

func canOverrideCredit(p *auth.Principal) bool {
	return p.Internal || (p.Claims != nil && (p.Claims.Role == auth.RoleSuperAdmin || p.Claims.Role == auth.RoleStoreManager || p.Claims.Role == auth.RoleAccountant))
}

// documentDate picks the bill date: the explicit date, else the moment the
// offline POS made the bill, else now — never a future date.
func (s *Service) documentDate(in CreateInput, now time.Time) (time.Time, error) {
	today := now.In(IST)
	today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, IST)
	var d time.Time
	switch {
	case in.InvoiceDate != "":
		d, _ = time.ParseInLocation(time.DateOnly, in.InvoiceDate, IST)
	case in.ClientCreatedAt != nil:
		t := in.ClientCreatedAt.In(IST)
		d = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, IST)
	default:
		d = today
	}
	if d.After(today) {
		return d, httpx.Validation("invoice_date cannot be in the future")
	}
	return d, nil
}

func (s *Service) kick() {
	if s.Relay != nil {
		s.Relay.Kick()
	}
}

func (s *Service) resolveParty(ctx context.Context, in CreateInput) (clients.Party, error) {
	if in.PartyID != "" {
		return s.Dir.Party(ctx, in.PartyID)
	}
	if in.Party != nil && strings.TrimSpace(in.Party.Name) != "" {
		state := in.Party.StateCode
		if state == "" && in.Party.GSTIN != "" {
			state = gst.StateCode(in.Party.GSTIN)
		}
		return clients.Party{Name: strings.TrimSpace(in.Party.Name), Phone: in.Party.Phone,
			GSTIN: gst.Normalize(in.Party.GSTIN), StateCode: state}, nil
	}
	return clients.Party{Name: "Cash Sale"}, nil
}

// validateItems resolves every item line against bhandar: the item must exist
// and be active, HSN and unit are filled from the master when missing, a sale
// must carry the item's tax classification, and a sale price may not exceed
// the MRP. Free-text lines (no item_id) are left alone.
func (s *Service) validateItems(ctx context.Context, in *CreateInput) error {
	var ids []string
	seen := map[string]bool{}
	for _, l := range in.Lines {
		if l.ItemID != "" && !seen[l.ItemID] {
			seen[l.ItemID] = true
			ids = append(ids, l.ItemID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	items, err := s.Dir.Items(ctx, ids)
	if err != nil {
		return err
	}
	for i := range in.Lines {
		l := &in.Lines[i]
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
			if in.DocType == DocSale || in.DocType == DocSaleReturn {
				return httpx.Validation("line %d: tax must match the item master (%s, %d bp)", i+1, it.Name, it.TaxRateBP)
			}
			// A supplier's bill may legitimately carry a different rate (rate change, wrong master); keep it but say so.
			s.Log.Warn("purchase line tax differs from item master", "item_id", it.ID, "line_rate_bp", l.TaxRateBP, "item_rate_bp", it.TaxRateBP)
		}
		if (in.DocType == DocSale || in.DocType == DocSaleReturn) && it.MRPPaise > 0 {
			perBase := l.UnitPricePaise
			if l.Unit != "" && !strings.EqualFold(l.Unit, it.Unit) {
				for _, a := range it.AltUnits {
					if strings.EqualFold(a.Unit, l.Unit) && a.Factor > 0 {
						perBase = int64(math.Round(float64(l.UnitPricePaise) / a.Factor))
					}
				}
			}
			if perBase > it.MRPPaise {
				return httpx.Validation("line %d: price exceeds MRP of %s", i+1, it.Name)
			}
		}
	}
	return nil
}

// loadOriginal loads the document a note refers to, checks it can be
// returned against, defaults the note's party and store from it, and copies
// rates and prices onto the note lines so a return never re-prices goods.
func (s *Service) loadOriginal(ctx context.Context, tenantID, storeID string, in *CreateInput) (*Invoice, error) {
	orig, err := getInvoice(ctx, s.DB, tenantID, in.RefInvoiceID)
	if db.IsNotFound(err) {
		return nil, httpx.Validation("ref_invoice_id not found")
	}
	if err != nil {
		return nil, err
	}
	want := DocSale
	if in.DocType == DocPurchaseReturn {
		want = DocPurchase
	}
	if orig.DocType != want || orig.Status != "posted" {
		return nil, httpx.Validation("ref_invoice_id must be a posted %s invoice", want)
	}
	if orig.StoreID != storeID {
		return nil, httpx.Validation("a return must be made at the store that issued the original document")
	}
	if orig.PartyID != nil {
		if in.PartyID == "" {
			in.PartyID = *orig.PartyID
		} else if in.PartyID != *orig.PartyID {
			return nil, httpx.Validation("party does not match the original document")
		}
	} else if in.PartyID == "" && in.Party == nil {
		in.Party = &PartyInput{Name: orig.PartyName, Phone: deref(orig.PartyPhone), GSTIN: deref(orig.PartyGSTIN), StateCode: deref(orig.PartyState)}
	}
	if in.PlaceOfSupply == "" {
		in.PlaceOfSupply = orig.PlaceOfSupply
	}
	returned, err := returnedQty(ctx, s.DB, tenantID, orig.ID)
	if err != nil {
		return nil, err
	}
	byKey := map[string]*Line{}
	for i := range orig.Lines {
		byKey[lineKey(deref(orig.Lines[i].ItemID), orig.Lines[i].Name)] = &orig.Lines[i]
	}
	for i := range in.Lines {
		l := &in.Lines[i]
		ol, ok := byKey[lineKey(l.ItemID, l.Name)]
		if !ok {
			return nil, httpx.Validation("line %d: %s is not on the original document", i+1, l.Name)
		}
		key := lineKey(deref(ol.ItemID), ol.Name)
		if l.Qty > ol.Qty-returned[key]+1e-9 {
			return nil, httpx.Validation("line %d: only %.3f %s of %s can still be returned", i+1, ol.Qty-returned[key], ol.Unit, ol.Name)
		}
		returned[key] += l.Qty
		if l.UnitPricePaise == 0 {
			l.UnitPricePaise = ol.UnitPricePaise
		}
		if l.UnitPricePaise > ol.UnitPricePaise {
			return nil, httpx.Validation("line %d: return price cannot exceed the original price", i+1)
		}
		l.TaxRateBP, l.CessRateBP, l.CessPerUnitPaise = ol.TaxRateBP, ol.CessRateBP, ol.CessPerUnitPaise
		l.TaxCategory, l.PriceIncludesTax = ol.TaxCategory, ol.PriceIncludesTax
		if l.ITCEligibility == "" && in.DocType == DocPurchaseReturn {
			l.ITCEligibility = ol.ITCEligibility
		}
		if l.HSN == "" {
			l.HSN = deref(ol.HSN)
		}
		if l.Unit == "" {
			l.Unit = ol.Unit
		}
		if l.ItemID == "" {
			l.ItemID = deref(ol.ItemID)
		}
	}
	return orig, nil
}

func lineKey(itemID, name string) string {
	if itemID != "" {
		return "id:" + itemID
	}
	return "name:" + strings.ToLower(strings.TrimSpace(name))
}

// returnedQty sums what posted notes have already returned per original line.
func returnedQty(ctx context.Context, q db.Querier, tenantID, refID string) (map[string]float64, error) {
	rows, err := q.Query(ctx, `SELECT COALESCE(l.item_id::text,''), l.name, l.qty::float8 FROM invoice_lines l
		JOIN invoices n ON n.id = l.invoice_id WHERE n.tenant_id=$1 AND n.ref_invoice_id=$2 AND n.status='posted'`, tenantID, refID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var id, name string
		var qty float64
		if err := rows.Scan(&id, &name, &qty); err != nil {
			return nil, err
		}
		out[lineKey(id, name)] += qty
	}
	return out, rows.Err()
}

// lockAndCheckReturn serialises notes against one original so two concurrent
// returns cannot both pass the quantity check, and makes sure the note does
// not credit more than the original is worth.
func (s *Service) lockAndCheckReturn(ctx context.Context, tx pgx.Tx, tenantID string, orig, note *Invoice) error {
	var status string
	var grand, credited int64
	if err := tx.QueryRow(ctx, `SELECT status, grand_total_paise, credited_paise FROM invoices WHERE tenant_id=$1 AND id=$2 FOR UPDATE`,
		tenantID, orig.ID).Scan(&status, &grand, &credited); err != nil {
		return err
	}
	if status != "posted" {
		return httpx.Conflict("the original document is no longer posted")
	}
	if credited+note.GrandTotalPaise > grand {
		return httpx.Validation("notes against this document would exceed its value (%d of %d paise already credited)", credited, grand)
	}
	return nil
}

// creditOriginal records a note against its original (delta may be negative on cancel).
func creditOriginal(ctx context.Context, tx pgx.Tx, tenantID, refID string, delta int64) error {
	_, err := tx.Exec(ctx, `UPDATE invoices SET credited_paise = GREATEST(0, credited_paise + $3),
		payment_status = CASE WHEN paid_paise >= grand_total_paise - GREATEST(0, credited_paise + $3) THEN 'paid'
		                      WHEN paid_paise > 0 THEN 'partial' ELSE 'unpaid' END
		WHERE tenant_id=$1 AND id=$2`, tenantID, refID, delta)
	return err
}

// registrationKey is the series owner of a store's registration: the nil UUID
// for the registration that owns the tenant-wide series (the primary — every
// document numbered before registrations existed is in it), else the
// registration id. The first primary registration to number a document claims
// the tenant-wide series, so a later change of primary never moves a GSTIN
// onto another GSTIN's series. A store from an older pehchaan (no
// registration_id) is on the tenant-wide series.
func registrationKey(ctx context.Context, tx pgx.Tx, tenantID string, store clients.Store) (string, error) {
	if store.RegistrationID == "" {
		return NilUUID, nil
	}
	if store.RegistrationPrimary {
		if _, err := tx.Exec(ctx, `INSERT INTO registration_series (tenant_id, registration_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`,
			tenantID, store.RegistrationID); err != nil {
			return "", err
		}
	}
	var owner string
	err := tx.QueryRow(ctx, `SELECT registration_id FROM registration_series WHERE tenant_id=$1`, tenantID).Scan(&owner)
	if err != nil && !db.IsNotFound(err) {
		return "", err
	}
	if strings.EqualFold(owner, store.RegistrationID) {
		return NilUUID, nil
	}
	return store.RegistrationID, nil
}

// numberSeries is where a document's number comes from.
type numberSeries struct {
	Key    string // number_series.store_id: the nil UUID, a registration id, or a store id (custom prefix)
	RegKey string // invoices.registration_key: numbers are unique per registration key
	Prefix string
	seed   func(ctx context.Context, tx pgx.Tx, fy string) (int64, error) // first number of a new series
}

// seriesFor resolves a store's series for a document type: the store's own when
// it has a custom prefix (numbering_settings), else its registration's.
func seriesFor(ctx context.Context, tx pgx.Tx, tenantID string, store clients.Store, storeID, docType string) (numberSeries, error) {
	regKey, err := registrationKey(ctx, tx, tenantID, store)
	if err != nil {
		return numberSeries{}, err
	}
	ns := numberSeries{Key: regKey, RegKey: regKey, Prefix: defaultPrefix[docType]}
	err = tx.QueryRow(ctx, `SELECT prefix FROM numbering_settings WHERE tenant_id=$1 AND store_id=$2 AND doc_type=$3`, tenantID, storeID, docType).Scan(&ns.Prefix)
	switch {
	case err == nil:
		ns.Key = storeID
	case !db.IsNotFound(err):
		return numberSeries{}, err
	case regKey != NilUUID && store.GSTIN != "":
		// A registration's own series starts after any number its GSTIN already
		// issued from the tenant-wide series (stores with their own GSTIN shared
		// it before registrations existed), so a GSTIN never repeats a number.
		gstin, prefix := store.GSTIN, ns.Prefix
		ns.seed = func(ctx context.Context, tx pgx.Tx, fy string) (int64, error) {
			var next int64
			err := tx.QueryRow(ctx, `SELECT COALESCE(max(seq_no), 0) + 1 FROM invoices
				WHERE tenant_id=$1 AND registration_key=$2 AND doc_type=$3 AND fy=$4 AND store_gstin=$5 AND number LIKE $6`,
				tenantID, NilUUID, docType, fy, gstin, prefix+"/%").Scan(&next)
			return next, err
		}
	}
	return ns, nil
}

// take reserves n consecutive numbers of the series in financial year fy and
// returns the first and the series prefix.
func (ns numberSeries) take(ctx context.Context, tx pgx.Tx, tenantID, docType, fy string, n int64) (int64, string, error) {
	start := int64(1)
	if ns.seed != nil {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM number_series WHERE tenant_id=$1 AND store_id=$2 AND doc_type=$3 AND fy=$4)`,
			tenantID, ns.Key, docType, fy).Scan(&exists); err != nil {
			return 0, "", err
		}
		if !exists {
			var err error
			if start, err = ns.seed(ctx, tx, fy); err != nil {
				return 0, "", err
			}
		}
	}
	var first int64
	prefix := ns.Prefix
	err := tx.QueryRow(ctx, `
		INSERT INTO number_series (tenant_id, store_id, doc_type, fy, prefix, next_no)
		VALUES ($1,$2,$3,$4,$5,$6::bigint+$7::bigint)
		ON CONFLICT (tenant_id, store_id, doc_type, fy) DO UPDATE SET next_no = number_series.next_no + $7::bigint
		RETURNING next_no - $7::bigint, prefix`,
		tenantID, ns.Key, docType, fy, prefix, start, n).Scan(&first, &prefix)
	return first, prefix, err
}

// allocateNumber returns the sequence number, prefix, financial year and
// registration key of the invoice. Online bills take the next number of their
// series; offline bills bring a number from a block reserved for their device
// and inherit the block's series and year, so a block reserved in March still
// syncs in April.
func (s *Service) allocateNumber(ctx context.Context, tx pgx.Tx, tenantID string, store clients.Store, storeID string, in CreateInput, fy string) (int64, string, string, string, error) {
	if in.Number > 0 {
		var prefix, blockFY, blockSeries string
		err := tx.QueryRow(ctx, `
			SELECT ns.prefix, b.fy, b.series_store_id FROM number_blocks b
			JOIN number_series ns ON ns.tenant_id=b.tenant_id AND ns.store_id=b.series_store_id AND ns.doc_type=b.doc_type AND ns.fy=b.fy
			WHERE b.tenant_id=$1 AND b.store_id=$2 AND b.doc_type=$3 AND b.device_id=$4
			  AND $5 BETWEEN b.start_no AND b.end_no AND (b.released_at IS NULL OR $5 <= COALESCE(b.last_used_no, 0))
			ORDER BY b.created_at DESC LIMIT 1`,
			tenantID, storeID, in.DocType, in.DeviceID, in.Number).Scan(&prefix, &blockFY, &blockSeries)
		if db.IsNotFound(err) {
			return 0, "", "", "", httpx.Validation("number %d is not in a block reserved for device %s", in.Number, in.DeviceID)
		}
		if err != nil {
			return 0, "", "", "", err
		}
		// The number belongs to the series the block was cut from.
		switch {
		case blockSeries == NilUUID:
			return in.Number, prefix, blockFY, NilUUID, nil
		case strings.EqualFold(blockSeries, store.RegistrationID):
			return in.Number, prefix, blockFY, store.RegistrationID, nil
		}
		regKey, err := registrationKey(ctx, tx, tenantID, store) // a store-prefix series
		return in.Number, prefix, blockFY, regKey, err
	}
	ns, err := seriesFor(ctx, tx, tenantID, store, storeID, in.DocType)
	if err != nil {
		return 0, "", "", "", err
	}
	next, prefix, err := ns.take(ctx, tx, tenantID, in.DocType, fy, 1)
	return next, prefix, fy, ns.RegKey, err
}

// ReserveBlock hands a device `size` consecutive numbers for offline billing.
func (s *Service) ReserveBlock(ctx context.Context, p *auth.Principal, storeID, docType, deviceID string, size int64) (*Block, error) {
	if storeID == "" {
		storeID = p.StoreID
	}
	if storeID == "" {
		return nil, httpx.Validation("store_id is required")
	}
	if p.Claims != nil && !p.Claims.CanAccessStore(storeID) {
		return nil, httpx.Forbidden("no access to store")
	}
	limit := int64(100)
	if p.Internal {
		limit = 1000
	} else if p.Claims != nil {
		if c, ok := blockCap[p.Claims.Role]; ok {
			limit = c
		}
	}
	if _, ok := defaultPrefix[docType]; !ok || deviceID == "" || size < 1 {
		return nil, httpx.Validation("need doc_type, device_id and a positive size")
	}
	if size > limit {
		return nil, httpx.Validation("a block for your role may hold at most %d numbers", limit)
	}
	store, err := s.Dir.Store(ctx, p.TenantID, storeID)
	if err != nil {
		return nil, err
	}
	fy := FinancialYear(s.Now().In(IST))
	b := &Block{DocType: docType, FY: fy, DeviceID: deviceID}
	err = db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		ns, err := seriesFor(ctx, tx, p.TenantID, store, storeID, docType)
		if err != nil {
			return err
		}
		if b.StartNo, b.Prefix, err = ns.take(ctx, tx, p.TenantID, docType, fy, size); err != nil {
			return err
		}
		b.EndNo = b.StartNo + size - 1
		return tx.QueryRow(ctx, `INSERT INTO number_blocks (tenant_id, store_id, series_store_id, doc_type, fy, device_id, start_no, end_no)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`, p.TenantID, storeID, ns.Key, docType, fy, deviceID, b.StartNo, b.EndNo).Scan(&b.ID)
	})
	if err != nil {
		return nil, err
	}
	return b, nil
}

// ReleaseBlock records how far a device used its block and hands the rest
// back. Unused numbers stay unissued (they are reported as gaps in GSTR-1
// Table 13); they are never reused.
func (s *Service) ReleaseBlock(ctx context.Context, p *auth.Principal, id string, lastUsed int64) (*Block, error) {
	var b Block
	var storeID, seriesKey string
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT id, store_id, series_store_id, doc_type, fy, device_id, start_no, end_no FROM number_blocks
			WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, p.TenantID, id).Scan(&b.ID, &storeID, &seriesKey, &b.DocType, &b.FY, &b.DeviceID, &b.StartNo, &b.EndNo)
		if db.IsNotFound(err) {
			return httpx.NotFound("block not found")
		}
		if err != nil {
			return err
		}
		if p.Claims != nil && !p.Claims.CanAccessStore(storeID) {
			return httpx.Forbidden("no access to store")
		}
		if lastUsed < b.StartNo-1 || lastUsed > b.EndNo {
			return httpx.Validation("last_used must be between %d and %d (or %d when nothing was used)", b.StartNo, b.EndNo, b.StartNo-1)
		}
		var released *time.Time
		if err := tx.QueryRow(ctx, `UPDATE number_blocks SET last_used_no=$3, released_at=COALESCE(released_at, now())
			WHERE tenant_id=$1 AND id=$2 RETURNING released_at`, p.TenantID, id, lastUsed).Scan(&released); err != nil {
			return err
		}
		b.LastUsedNo, b.ReleasedAt = &lastUsed, released
		// The block's own series (the one it was cut from), whatever the store uses now.
		err = tx.QueryRow(ctx, `SELECT prefix FROM number_series WHERE tenant_id=$1 AND store_id=$2 AND doc_type=$3 AND fy=$4`,
			p.TenantID, seriesKey, b.DocType, b.FY).Scan(&b.Prefix)
		if db.IsNotFound(err) {
			b.Prefix, err = defaultPrefix[b.DocType], nil
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// Numbering lists a tenant's custom prefixes.
func (s *Service) Numbering(ctx context.Context, tenantID string) ([]NumberingSetting, error) {
	rows, err := s.DB.Query(ctx, `SELECT store_id, doc_type, prefix FROM numbering_settings WHERE tenant_id=$1 ORDER BY store_id, doc_type`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NumberingSetting{}
	for rows.Next() {
		var n NumberingSetting
		if err := rows.Scan(&n.StoreID, &n.DocType, &n.Prefix); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// SetNumbering gives a store its own series for a document type. The prefix
// must be unique across the tenant's stores so numbers stay unique per GSTIN.
func (s *Service) SetNumbering(ctx context.Context, tenantID string, n NumberingSetting) error {
	return db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var clash string
		err := tx.QueryRow(ctx, `SELECT store_id FROM numbering_settings WHERE tenant_id=$1 AND doc_type=$2 AND prefix=$3 AND store_id<>$4`,
			tenantID, n.DocType, n.Prefix, n.StoreID).Scan(&clash)
		if err == nil {
			return httpx.Conflict("prefix %s is already used by another store", n.Prefix)
		}
		if !db.IsNotFound(err) {
			return err
		}
		if n.Prefix == defaultPrefix[n.DocType] {
			return httpx.Validation("%s is the shared series prefix; choose a store-specific one", n.Prefix)
		}
		_, err = tx.Exec(ctx, `INSERT INTO numbering_settings (tenant_id, store_id, doc_type, prefix) VALUES ($1,$2,$3,$4)
			ON CONFLICT (tenant_id, store_id, doc_type) DO UPDATE SET prefix = EXCLUDED.prefix`, tenantID, n.StoreID, n.DocType, n.Prefix)
		return err
	})
}

// Cancel marks a posted invoice cancelled and queues the reversal event. A
// document with posted notes or unrefunded online payments cannot be cancelled.
func (s *Service) Cancel(ctx context.Context, p *auth.Principal, id, reason string) (*Invoice, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, httpx.Validation("reason is required")
	}
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		inv, err := getInvoice(ctx, tx, p.TenantID, id)
		if db.IsNotFound(err) {
			return httpx.NotFound("invoice not found")
		}
		if err != nil {
			return err
		}
		if p.Claims != nil && !p.Claims.CanAccessStore(inv.StoreID) {
			return httpx.Forbidden("no access to store")
		}
		if inv.Status != "posted" {
			return httpx.Conflict("invoice is already cancelled")
		}
		if deref(inv.EInvoiceStatus) == EInvoiceGenerated {
			return httpx.Conflict("this invoice has an active e-invoice (IRN %s); cancel the IRN in kar first "+
				"(POST /api/kar/v1/einvoices/%s/cancel, within 24 hours of generation), then cancel the invoice", deref(inv.IRN), inv.ID).
				WithDetails(map[string]any{"irn": deref(inv.IRN), "kar_cancel": "/api/kar/v1/einvoices/" + inv.ID + "/cancel"})
		}
		var notes int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM invoices WHERE tenant_id=$1 AND ref_invoice_id=$2 AND status='posted'`, p.TenantID, id).Scan(&notes); err != nil {
			return err
		}
		if notes > 0 {
			return httpx.Conflict("cancel the %d credit/debit note(s) against this document first", notes)
		}
		var unrefunded int64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(amount_paise - refunded_paise),0) FROM invoice_receipts WHERE invoice_id=$1`, id).Scan(&unrefunded); err != nil {
			return err
		}
		if unrefunded > 0 {
			return httpx.Conflict("refund the online payment of %d paise before cancelling", unrefunded)
		}
		tag, err := tx.Exec(ctx, `UPDATE invoices SET status='cancelled', cancelled_at=now(), cancel_reason=$3
			WHERE tenant_id=$1 AND id=$2 AND status='posted'`, p.TenantID, id, reason)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.Conflict("invoice not found or already cancelled")
		}
		if IsNote(inv.DocType) && inv.RefInvoiceID != nil {
			if err := creditOriginal(ctx, tx, p.TenantID, *inv.RefInvoiceID, -inv.GrandTotalPaise); err != nil {
				return err
			}
		}
		inv.Status = "cancelled"
		ev, err := events.New(events.InvoiceCancelled, "aadesh", p.TenantID, inv.StoreID, toPayload(inv))
		if err != nil {
			return err
		}
		return events.Enqueue(ctx, tx, ev)
	})
	if err != nil {
		return nil, err
	}
	s.kick()
	return s.Get(ctx, p.TenantID, id)
}

// Get loads one invoice with lines and tenders.
func (s *Service) Get(ctx context.Context, tenantID, id string) (*Invoice, error) {
	inv, err := getInvoice(ctx, s.DB, tenantID, id)
	if db.IsNotFound(err) {
		return nil, httpx.NotFound("invoice not found")
	}
	return inv, err
}

func (s *Service) byClientUUID(ctx context.Context, tenantID, clientUUID string) (*Invoice, error) {
	var id string
	if err := s.DB.QueryRow(ctx, `SELECT id FROM invoices WHERE tenant_id=$1 AND client_uuid=$2`, tenantID, clientUUID).Scan(&id); err != nil {
		return nil, err
	}
	return getInvoice(ctx, s.DB, tenantID, id)
}

// PaymentRequest asks lenden for a counter UPI QR (or payment link) for what
// is still due on an invoice.
func (s *Service) PaymentRequest(ctx context.Context, p *auth.Principal, id, kind string) (*clients.PaymentRequest, error) {
	if kind == "" {
		kind = "upi_qr"
	}
	inv, err := s.Get(ctx, p.TenantID, id)
	if err != nil {
		return nil, err
	}
	if p.Claims != nil && !p.Claims.CanAccessStore(inv.StoreID) {
		return nil, httpx.NotFound("invoice not found")
	}
	if inv.Status != "posted" || inv.DocType != DocSale {
		return nil, httpx.Conflict("payments can only be requested on posted sales invoices")
	}
	if inv.BalancePaise <= 0 {
		return nil, httpx.Conflict("nothing is due on this invoice")
	}
	req, err := s.Dir.PaymentRequest(ctx, clients.PaymentRequestInput{
		InvoiceID: inv.ID, PartyID: deref(inv.PartyID), PartyPhone: deref(inv.PartyPhone), AmountPaise: inv.BalancePaise,
		Purpose: "invoice", Kind: kind, StoreID: inv.StoreID,
	})
	if err != nil {
		return nil, err
	}
	return &req, nil
}

// ApplyPayment records a lenden payment against an invoice (idempotent on
// payment id). A capture that answers a payment request already written as a
// counter tender is recorded but does not count twice; payments on cancelled
// documents or purchase bills are recorded for the refund trail only.
func (s *Service) ApplyPayment(ctx context.Context, e events.Envelope, refund bool) error {
	var pl events.PaymentPayload
	if err := e.Decode(&pl); err != nil {
		return nil // malformed: nothing to retry
	}
	if pl.InvoiceID == "" {
		return nil // on-account payment, nothing to update here
	}
	return db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		fresh, err := db.MarkProcessed(ctx, tx, e.ID, e.Type)
		if err != nil || !fresh {
			return err
		}
		var docType, status string
		err = tx.QueryRow(ctx, `SELECT doc_type, status FROM invoices WHERE tenant_id=$1 AND id=$2`, e.TenantID, pl.InvoiceID).Scan(&docType, &status)
		if db.IsNotFound(err) {
			return fmt.Errorf("payment for unknown invoice %s", pl.InvoiceID) // retry: the invoice may still be syncing
		}
		if err != nil {
			return err
		}
		countsTowardsBill := docType == DocSale && status == "posted"
		if docType == DocPurchase || docType == DocPurchaseReturn {
			s.Log.Warn("customer payment references a purchase document; recorded without effect", "invoice_id", pl.InvoiceID)
			countsTowardsBill = false
		}
		delta := pl.AmountPaise
		if refund {
			tag, err := tx.Exec(ctx, `UPDATE invoice_receipts SET refunded_paise = refunded_paise + $2
				WHERE payment_id=$1 AND refunded_paise + $2 <= amount_paise`, pl.PaymentID, pl.AmountPaise)
			if err != nil || tag.RowsAffected() == 0 {
				return err
			}
			delta = -delta
		} else {
			tag, err := tx.Exec(ctx, `INSERT INTO invoice_receipts (payment_id, invoice_id, amount_paise)
				SELECT $1, id, $3 FROM invoices WHERE tenant_id=$4 AND id=$2 ON CONFLICT DO NOTHING`,
				pl.PaymentID, pl.InvoiceID, pl.AmountPaise, e.TenantID)
			if err != nil || tag.RowsAffected() == 0 {
				return err
			}
			if pl.PaymentRequestID != "" {
				var n int
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM invoice_tenders WHERE invoice_id=$1 AND payment_request_id=$2`, pl.InvoiceID, pl.PaymentRequestID).Scan(&n); err != nil {
					return err
				}
				if n > 0 {
					countsTowardsBill = false // the cashier already recorded this payment as a tender
				}
			}
		}
		if !countsTowardsBill {
			return nil
		}
		_, err = tx.Exec(ctx, `UPDATE invoices SET
				paid_paise = GREATEST(0, paid_paise + $3),
				payment_status = CASE WHEN GREATEST(0, paid_paise + $3) >= grand_total_paise - credited_paise THEN 'paid'
				                      WHEN GREATEST(0, paid_paise + $3) > 0 THEN 'partial' ELSE 'unpaid' END
			WHERE tenant_id=$1 AND id=$2`, e.TenantID, pl.InvoiceID, delta)
		return err
	})
}

func paymentStatus(paid, total, credited int64) string {
	switch {
	case paid >= total-credited:
		return "paid"
	case paid > 0:
		return "partial"
	}
	return "unpaid"
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

func toPayload(inv *Invoice) events.InvoicePayload {
	p := events.InvoicePayload{
		InvoiceID: inv.ID, StoreID: inv.StoreID, StoreState: inv.StoreState, StoreGSTIN: deref(inv.StoreGSTIN),
		DocType: inv.DocType, Number: inv.Number, InvoiceDate: inv.InvoiceDate, DueDate: deref(inv.DueDate),
		RefInvoiceID: deref(inv.RefInvoiceID), SupplierInvoiceNo: deref(inv.SupplierInvoiceNo),
		SupplierInvoiceDate: deref(inv.SupplierInvoiceDate), PartyID: deref(inv.PartyID), PartyName: inv.PartyName,
		PartyPhone: deref(inv.PartyPhone), PartyGSTIN: deref(inv.PartyGSTIN), PlaceOfSupply: inv.PlaceOfSupply,
		InterState: inv.InterState, SupplyCategory: inv.SupplyCategory, ReverseCharge: inv.ReverseCharge,
		TaxablePaise: inv.TaxablePaise, DiscountPaise: inv.DiscountPaise, CGSTPaise: inv.CGSTPaise, SGSTPaise: inv.SGSTPaise,
		IGSTPaise: inv.IGSTPaise, CessPaise: inv.CessPaise, TCSPaise: inv.TCSPaise, RoundOffPaise: inv.RoundOffPaise,
		GrandTotalPaise: inv.GrandTotalPaise, IRN: deref(inv.IRN), CreatedBy: inv.CreatedBy,
	}
	for _, l := range inv.Lines {
		p.Lines = append(p.Lines, events.InvoiceLine{
			LineNo: l.LineNo, ItemID: deref(l.ItemID), Name: l.Name, HSN: deref(l.HSN), Unit: l.Unit,
			BatchNo: deref(l.BatchNo), Qty: l.Qty, UnitPricePaise: l.UnitPricePaise, DiscountPaise: l.DiscountPaise,
			PriceInclTax: l.PriceIncludesTax, TaxRateBP: l.TaxRateBP, CessRateBP: l.CessRateBP, CessPerUnitPaise: l.CessPerUnitPaise,
			TaxCategory: l.TaxCategory, TaxablePaise: l.TaxablePaise, CGSTPaise: l.CGSTPaise, SGSTPaise: l.SGSTPaise,
			IGSTPaise: l.IGSTPaise, CessPaise: l.CessPaise, TotalPaise: l.TotalPaise, ITCEligibility: l.ITCEligibility,
		})
	}
	for _, t := range inv.Tenders {
		p.Tenders = append(p.Tenders, events.Tender{Mode: t.Mode, AmountPaise: t.AmountPaise, Reference: t.Reference, PaymentRequestID: t.PaymentRequestID})
	}
	return p
}
