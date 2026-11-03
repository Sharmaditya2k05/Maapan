package inventory

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/maapan/mool/db"
	"github.com/maapan/mool/httpx"
	"github.com/maapan/mool/logx"
)

// Serial statuses.
const (
	SerialInStock          = "in_stock"
	SerialSold             = "sold"
	SerialReturnedToVendor = "returned_to_vendor"
	SerialWrittenOff       = "written_off"
)

// Serial is one serial-numbered unit of an item.
type Serial struct {
	ID         string        `json:"id"`
	ItemID     string        `json:"item_id"`
	ItemName   string        `json:"item_name,omitempty"`
	SerialNo   string        `json:"serial_no"`
	Status     string        `json:"status"`
	StoreID    string        `json:"store_id,omitempty"`
	BatchNo    string        `json:"batch_no,omitempty"`
	SupplierID string        `json:"supplier_id,omitempty"`
	PartyID    string        `json:"party_id,omitempty"`
	ReceivedAt *time.Time    `json:"received_at,omitempty"`
	SoldAt     *time.Time    `json:"sold_at,omitempty"`
	History    []SerialEvent `json:"history,omitempty"`
}

// SerialEvent is one step in a serial's life.
type SerialEvent struct {
	Event     string    `json:"event"`
	StoreID   string    `json:"store_id,omitempty"`
	Source    string    `json:"source"`
	SourceID  string    `json:"source_id"`
	Reverted  bool      `json:"reverted,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// NormalizeSerial trims and upper-cases a serial number.
func NormalizeSerial(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }

// serialMove is the serial change one document line causes.
type serialMove struct {
	LineNo    int
	ItemID    string
	BatchNo   string
	SerialNos []string
}

// serialTransition describes what a document does to a serial.
type serialTransition struct {
	Event     string // serial_events.event
	NewStatus string
	// Expect is the status the serial should have before (for warnings); "" = must not be in stock.
	Expect string
}

// transitionFor maps a document type (or adjustment sign) to its transition.
func transitionFor(docType string) (serialTransition, bool) {
	switch docType {
	case "purchase":
		return serialTransition{Event: "received", NewStatus: SerialInStock}, true
	case "sale":
		return serialTransition{Event: "sold", NewStatus: SerialSold, Expect: SerialInStock}, true
	case "sale_return":
		return serialTransition{Event: "returned", NewStatus: SerialInStock, Expect: SerialSold}, true
	case "purchase_return":
		return serialTransition{Event: "vendor_returned", NewStatus: SerialReturnedToVendor, Expect: SerialInStock}, true
	case "adjust_in":
		return serialTransition{Event: "adjusted_in", NewStatus: SerialInStock}, true
	case "adjust_out":
		return serialTransition{Event: "written_off", NewStatus: SerialWrittenOff, Expect: SerialInStock}, true
	}
	return serialTransition{}, false
}

// applySerials records the serials of a document inside tx. It never fails a
// document for an unexpected state (a POS must not refuse a sale because the
// books are behind): anomalies are logged and the serial follows the document.
// Each (serial, source, source_id) is applied once, so redeliveries are no-ops.
func applySerials(ctx context.Context, tx pgx.Tx, tenantID, storeID, source, sourceID, docType, partyID string, moves []serialMove) (int, error) {
	tr, ok := transitionFor(docType)
	if !ok {
		return 0, nil
	}
	n := 0
	for _, m := range moves {
		for _, raw := range m.SerialNos {
			sn := NormalizeSerial(raw)
			if sn == "" || m.ItemID == "" {
				continue
			}
			var id, status string
			var prevStore *string
			err := tx.QueryRow(ctx, `SELECT id, status, store_id FROM serials WHERE tenant_id=$1 AND item_id=$2 AND serial_no=$3 FOR UPDATE`,
				tenantID, m.ItemID, sn).Scan(&id, &status, &prevStore)
			var prevStatus *string
			switch {
			case db.IsNotFound(err):
				if tr.Expect != "" {
					logx.From(ctx).Warn("serial not known before this document", "serial_no", sn, "item_id", m.ItemID, "event", tr.Event, "source_id", sourceID)
				}
				err = tx.QueryRow(ctx, `INSERT INTO serials (tenant_id, item_id, serial_no, status, store_id, batch_no)
					VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`, tenantID, m.ItemID, sn, tr.NewStatus, storeID, nilIfEmpty(m.BatchNo)).Scan(&id)
				if err != nil {
					return n, err
				}
			case err != nil:
				return n, err
			default:
				prevStatus = &status
				if (tr.Expect != "" && status != tr.Expect) || (tr.Expect == "" && status == SerialInStock) {
					logx.From(ctx).Warn("serial in an unexpected state", "serial_no", sn, "status", status, "event", tr.Event, "source_id", sourceID)
				}
			}
			tag, err := tx.Exec(ctx, `INSERT INTO serial_events (tenant_id, serial_id, event, store_id, source, source_id, line_no, prev_status, prev_store)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT (tenant_id, serial_id, source, source_id) DO NOTHING`,
				tenantID, id, tr.Event, storeID, source, sourceID, m.LineNo, prevStatus, prevStore)
			if err != nil {
				return n, err
			}
			if tag.RowsAffected() == 0 {
				continue // this document already moved the serial
			}
			set := `status=$3, store_id=$4, updated_at=now()`
			args := []any{tenantID, id, tr.NewStatus, storeID}
			switch tr.Event {
			case "received":
				set += `, received_at=now(), supplier_id=$5, party_id=NULL, sold_at=NULL, batch_no=COALESCE($6, batch_no)`
				args = append(args, nilIfEmpty(partyID), nilIfEmpty(m.BatchNo))
			case "adjusted_in":
				set += `, received_at=COALESCE(received_at, now())`
			case "sold":
				set += `, sold_at=now(), party_id=$5`
				args = append(args, nilIfEmpty(partyID))
			}
			if _, err := tx.Exec(ctx, `UPDATE serials SET `+set+` WHERE tenant_id=$1 AND id=$2`, args...); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

// revertSerials undoes what a document did to its serials (invoice
// cancellation): each serial goes back to the status and store it had before.
// A serial the document itself introduced is removed again.
func revertSerials(ctx context.Context, tx pgx.Tx, tenantID string, sources []string, sourceID string) (int, error) {
	rows, err := tx.Query(ctx, `SELECT e.id, e.serial_id, e.prev_status, e.prev_store FROM serial_events e
		WHERE e.tenant_id=$1 AND e.source = ANY($2::text[]) AND e.source_id=$3 AND NOT e.reverted ORDER BY e.created_at DESC`,
		tenantID, sources, sourceID)
	if err != nil {
		return 0, err
	}
	type ev struct {
		id, serialID string
		prevStatus   *string
		prevStore    *string
	}
	var evs []ev
	for rows.Next() {
		var e ev
		if err := rows.Scan(&e.id, &e.serialID, &e.prevStatus, &e.prevStore); err != nil {
			rows.Close()
			return 0, err
		}
		evs = append(evs, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, e := range evs {
		if _, err := tx.Exec(ctx, `UPDATE serial_events SET reverted=true WHERE id=$1`, e.id); err != nil {
			return 0, err
		}
		if e.prevStatus == nil {
			var others int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM serial_events WHERE serial_id=$1 AND id<>$2 AND NOT reverted`, e.serialID, e.id).Scan(&others); err != nil {
				return 0, err
			}
			if others == 0 {
				if _, err := tx.Exec(ctx, `DELETE FROM serial_events WHERE serial_id=$1`, e.serialID); err != nil {
					return 0, err
				}
				if _, err := tx.Exec(ctx, `DELETE FROM serials WHERE id=$1`, e.serialID); err != nil {
					return 0, err
				}
				continue
			}
			if _, err := tx.Exec(ctx, `UPDATE serials SET status=$2, updated_at=now() WHERE id=$1`, e.serialID, SerialWrittenOff); err != nil {
				return 0, err
			}
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE serials SET status=$2, store_id=$3, updated_at=now() WHERE id=$1`, e.serialID, *e.prevStatus, e.prevStore); err != nil {
			return 0, err
		}
	}
	return len(evs), nil
}

const serialCols = `s.id, s.item_id, i.name, s.serial_no, s.status, s.store_id, s.batch_no, s.supplier_id, s.party_id, s.received_at, s.sold_at`

func scanSerial(row pgx.Row) (Serial, error) {
	var x Serial
	var store, batch, sup, party *string
	err := row.Scan(&x.ID, &x.ItemID, &x.ItemName, &x.SerialNo, &x.Status, &store, &batch, &sup, &party, &x.ReceivedAt, &x.SoldAt)
	x.StoreID, x.BatchNo, x.SupplierID, x.PartyID = deref(store), deref(batch), deref(sup), deref(party)
	return x, err
}

// LookupSerial finds a serial number (across items) with its history.
func (s *Service) LookupSerial(ctx context.Context, tenantID, serialNo string) ([]Serial, error) {
	sn := NormalizeSerial(serialNo)
	if sn == "" {
		return nil, httpx.Validation("serial number is required")
	}
	rows, err := s.DB.Query(ctx, `SELECT `+serialCols+` FROM serials s JOIN items i ON i.id = s.item_id
		WHERE s.tenant_id=$1 AND s.serial_no=$2 ORDER BY s.updated_at DESC`, tenantID, sn)
	if err != nil {
		return nil, err
	}
	var out []Serial
	for rows.Next() {
		x, err := scanSerial(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, httpx.NotFound("serial %s not found", sn)
	}
	for i := range out {
		h, err := s.DB.Query(ctx, `SELECT event, store_id, source, source_id, reverted, created_at FROM serial_events
			WHERE serial_id=$1 ORDER BY created_at, id`, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].History = []SerialEvent{}
		for h.Next() {
			var e SerialEvent
			var store *string
			if err := h.Scan(&e.Event, &store, &e.Source, &e.SourceID, &e.Reverted, &e.CreatedAt); err != nil {
				h.Close()
				return nil, err
			}
			e.StoreID = deref(store)
			out[i].History = append(out[i].History, e)
		}
		h.Close()
		if err := h.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// SerialFilter narrows GET /v1/serials.
type SerialFilter struct {
	ItemID  string
	StoreID string
	Status  string
	httpx.Page
}

// ListSerials lists serials (default: in stock).
func (s *Service) ListSerials(ctx context.Context, tenantID string, f SerialFilter) ([]Serial, int, error) {
	where := []string{"s.tenant_id = $1"}
	args := []any{tenantID}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if f.ItemID != "" {
		add("s.item_id = $%d", f.ItemID)
	}
	if f.StoreID != "" {
		add("s.store_id = $%d", f.StoreID)
	}
	switch f.Status {
	case "":
		add("s.status = $%d", SerialInStock)
	case "all":
	default:
		add("s.status = $%d", f.Status)
	}
	cond := strings.Join(where, " AND ")
	var total int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM serials s WHERE `+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, f.Limit, f.Offset)
	rows, err := s.DB.Query(ctx, fmt.Sprintf(`SELECT %s FROM serials s JOIN items i ON i.id = s.item_id WHERE %s
		ORDER BY lower(i.name), s.serial_no LIMIT $%d OFFSET $%d`, serialCols, cond, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Serial{}
	for rows.Next() {
		x, err := scanSerial(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, x)
	}
	return out, total, rows.Err()
}

// SerialValidateLine is one document line to check.
type SerialValidateLine struct {
	LineNo    int      `json:"line_no"`
	ItemID    string   `json:"item_id"`
	Qty       float64  `json:"qty"`
	Unit      string   `json:"unit,omitempty"`
	SerialNos []string `json:"serial_nos,omitempty"`
}

// SerialValidateInput is the body of POST /internal/v1/serials/validate.
type SerialValidateInput struct {
	StoreID string               `json:"store_id"`
	DocType string               `json:"doc_type"` // sale | purchase | sale_return | purchase_return
	Lines   []SerialValidateLine `json:"lines"`
}

// Validate checks the request shape.
func (in *SerialValidateInput) Validate() error {
	if _, ok := transitionFor(in.DocType); !ok || strings.HasPrefix(in.DocType, "adjust") {
		return httpx.Validation("doc_type must be sale, purchase, sale_return or purchase_return")
	}
	if len(in.Lines) > 1000 {
		return httpx.Validation("validate up to 1000 lines per call")
	}
	return nil
}

// SerialProblem is one reason a document's serials are not acceptable.
type SerialProblem struct {
	LineNo   int    `json:"line_no"`
	ItemID   string `json:"item_id,omitempty"`
	SerialNo string `json:"serial_no,omitempty"`
	Message  string `json:"message"`
}

// SerialValidation is the response of POST /internal/v1/serials/validate.
type SerialValidation struct {
	OK     bool            `json:"ok"`
	Errors []SerialProblem `json:"errors"`
}

// ValidateSerials checks that a document's serial numbers fit its type: a
// sale or purchase return needs each serial in stock at the store, a sale
// return needs it sold, a purchase needs it not already in stock; a
// serial-tracked item needs exactly one serial per base unit.
func (s *Service) ValidateSerials(ctx context.Context, tenantID string, in SerialValidateInput) (*SerialValidation, error) {
	out := &SerialValidation{Errors: []SerialProblem{}}
	bad := func(l SerialValidateLine, sn, format string, a ...any) {
		out.Errors = append(out.Errors, SerialProblem{LineNo: l.LineNo, ItemID: l.ItemID, SerialNo: sn, Message: fmt.Sprintf(format, a...)})
	}
	items := map[string]*itemLite{}
	seen := map[string]int{}
	for _, l := range in.Lines {
		if l.ItemID == "" {
			if len(l.SerialNos) > 0 {
				bad(l, "", "serial numbers need an item_id")
			}
			continue
		}
		it := items[l.ItemID]
		if it == nil {
			var err error
			it, err = loadItemLite(ctx, s.DB, tenantID, l.ItemID)
			if err != nil {
				if httpx.AsError(err).Status == 404 {
					bad(l, "", "item not found")
					continue
				}
				return nil, err
			}
			items[l.ItemID] = it
		}
		qty, _ := ToBaseUnit(l.Qty, l.Unit, it.Unit, it.Alts)
		if !it.TrackSerials {
			if len(l.SerialNos) > 0 {
				bad(l, "", "%s does not track serial numbers", it.Name)
			}
			continue
		}
		if qty != math.Trunc(qty) {
			bad(l, "", "%s is serial-tracked: quantity must be whole units", it.Name)
		}
		if int(qty) != len(l.SerialNos) {
			bad(l, "", "%s needs %d serial number(s), got %d", it.Name, int(qty), len(l.SerialNos))
		}
		for _, raw := range l.SerialNos {
			sn := NormalizeSerial(raw)
			if sn == "" {
				bad(l, raw, "empty serial number")
				continue
			}
			key := l.ItemID + "|" + sn
			if prev, dup := seen[key]; dup {
				bad(l, sn, "serial repeated (also on line %d)", prev)
				continue
			}
			seen[key] = l.LineNo
			var status string
			var store *string
			err := s.DB.QueryRow(ctx, `SELECT status, store_id FROM serials WHERE tenant_id=$1 AND item_id=$2 AND serial_no=$3`,
				tenantID, l.ItemID, sn).Scan(&status, &store)
			known := err == nil
			if err != nil && !db.IsNotFound(err) {
				return nil, err
			}
			atStore := known && deref(store) == in.StoreID
			switch in.DocType {
			case "purchase":
				if known && status == SerialInStock {
					bad(l, sn, "serial %s is already in stock", sn)
				}
			case "sale", "purchase_return":
				switch {
				case !known:
					bad(l, sn, "serial %s not found", sn)
				case status != SerialInStock:
					bad(l, sn, "serial %s is %s", sn, status)
				case in.StoreID != "" && !atStore:
					bad(l, sn, "serial %s is in another store", sn)
				}
			case "sale_return":
				if !known {
					bad(l, sn, "serial %s was never received", sn)
				} else if status != SerialSold {
					bad(l, sn, "serial %s is %s, not sold", sn, status)
				}
			}
		}
	}
	out.OK = len(out.Errors) == 0
	return out, nil
}

// SerialCheckInput is the body of POST /internal/v1/serials/check.
type SerialCheckInput struct {
	ItemID  string   `json:"item_id"`
	StoreID string   `json:"store_id,omitempty"`
	Serials []string `json:"serials"`
}

// Validate checks the request.
func (in *SerialCheckInput) Validate() error {
	if in.ItemID == "" {
		return httpx.Validation("item_id is required")
	}
	if len(in.Serials) > 1000 {
		return httpx.Validation("check up to 1000 serials per call")
	}
	return nil
}

// SerialStatus is where one serial is.
type SerialStatus struct {
	SerialNo string `json:"serial_no"`
	Status   string `json:"status"` // in_stock | sold | returned_to_vendor | written_off | unknown
	StoreID  string `json:"store_id,omitempty"`
}

// CheckSerials reports the status and store of each serial of an item, in
// request order ("unknown" when bhandar has never seen it). store_id is
// informational: a serial in stock elsewhere comes back in_stock with its store.
func (s *Service) CheckSerials(ctx context.Context, tenantID string, in SerialCheckInput) ([]SerialStatus, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	norm := make([]string, 0, len(in.Serials))
	for _, sn := range in.Serials {
		if n := NormalizeSerial(sn); n != "" {
			norm = append(norm, n)
		}
	}
	rows, err := s.DB.Query(ctx, `SELECT serial_no, status, store_id FROM serials WHERE tenant_id=$1 AND item_id=$2 AND serial_no = ANY($3::text[])`,
		tenantID, in.ItemID, norm)
	if err != nil {
		return nil, err
	}
	found := map[string]SerialStatus{}
	for rows.Next() {
		var x SerialStatus
		var store *string
		if err := rows.Scan(&x.SerialNo, &x.Status, &store); err != nil {
			rows.Close()
			return nil, err
		}
		x.StoreID = deref(store)
		found[x.SerialNo] = x
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]SerialStatus, 0, len(in.Serials))
	for _, raw := range in.Serials {
		sn := NormalizeSerial(raw)
		x, ok := found[sn]
		if !ok {
			x = SerialStatus{SerialNo: sn, Status: "unknown"}
		}
		if sn != raw {
			x.SerialNo = raw // aadesh keys the answer by what it sent
		}
		out = append(out, x)
	}
	return out, nil
}
