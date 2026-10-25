package compliance

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maapan/kar/internal/clients"
	"github.com/maapan/kar/internal/gsp"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/events"
	"github.com/maapan/mool/gst"
	"github.com/maapan/mool/httpx"
)

// CredentialSource yields a GSTIN's portal API users (kar/internal/creds.Store).
type CredentialSource interface {
	Credentials(ctx context.Context, tenantID, gstin string) (gsp.Credentials, bool, error)
}

// Engine runs the e-invoice and e-way bill lifecycle: it builds payloads from
// aadesh invoices and pehchaan registrations, calls the gsp.Client, stores
// the outcome and publishes kar.einvoice.generated / kar.eway.generated
// through the outbox in the same transaction.
type Engine struct {
	DB    *pgxpool.Pool
	Dir   *clients.Directory
	GSP   gsp.Client
	Creds CredentialSource // optional; without it drivers get only the GSTIN
	Log   *slog.Logger
	Now   func() time.Time
	Kick  func() // optional: wake the outbox relay after a commit
	// MaxAttempts bounds retries of transient failures (GSP down, pehchaan
	// unreachable) before an event-driven document is marked failed. Default 5.
	MaxAttempts int
}

// Source is the outbox envelope source.
const Source = "kar"

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Engine) maxAttempts() int {
	if e.MaxAttempts > 0 {
		return e.MaxAttempts
	}
	return 5
}

func (e *Engine) log() *slog.Logger {
	if e.Log != nil {
		return e.Log
	}
	return slog.Default()
}

func (e *Engine) kick() {
	if e.Kick != nil {
		e.Kick()
	}
}

func (e *Engine) credentials(ctx context.Context, tenantID, gstin string) (gsp.Credentials, error) {
	c := gsp.Credentials{GSTIN: gstin}
	if e.Creds == nil {
		return c, nil
	}
	got, ok, err := e.Creds.Credentials(ctx, tenantID, gstin)
	if err != nil {
		return c, err
	}
	if ok {
		return got, nil
	}
	return c, nil
}

// taxSettings reads the tenant settings the engine needs (defaults when absent).
func (e *Engine) taxSettings(ctx context.Context, tenantID string) (einvoiceEnabled bool, ewayThreshold int64, err error) {
	err = e.DB.QueryRow(ctx, `SELECT einvoice_enabled, eway_threshold_paise FROM tax_settings WHERE tenant_id=$1`, tenantID).
		Scan(&einvoiceEnabled, &ewayThreshold)
	if db.IsNotFound(err) {
		return false, DefaultEWayThresholdPaise, nil
	}
	return einvoiceEnabled, ewayThreshold, err
}

// Seller describes the store's registration (seller / consignor).
type Seller struct {
	Party
	Store      clients.Store
	Applicable bool // e-invoice applicable for this registration
}

// seller loads the store with its registration fields. Stores served by a
// pre-Phase-3 pehchaan (no registration_id) fall back to the tenant tax
// settings for applicability and to the tenant's business name.
func (e *Engine) seller(ctx context.Context, tenantID, storeID string) (Seller, error) {
	st, err := e.Dir.Store(ctx, storeID)
	if err != nil {
		return Seller{}, err
	}
	s := Seller{Store: st}
	g := gst.Normalize(st.GSTIN)
	legal, trade := strings.TrimSpace(st.LegalName), strings.TrimSpace(st.TradeName)
	if legal == "" {
		if t, err := e.Dir.Tenant(ctx, tenantID); err == nil && t.BusinessName != "" {
			legal = t.BusinessName
			if g == "" {
				g = gst.Normalize(t.GSTIN)
			}
		} else {
			legal = st.Name
		}
	}
	if trade == "" {
		trade = st.Name
	}
	state := st.StateCode
	if state == "" && len(g) == 15 {
		state = g[:2]
	}
	pin, _ := strconv.Atoi(strings.TrimSpace(st.Pin))
	if pin == 0 {
		pin = PincodeFromAddress(st.Address)
	}
	place := strings.TrimSpace(st.City)
	if place == "" {
		place = PlaceFromAddress(st.Address)
	}
	s.Party = Party{GSTIN: g, LegalName: legal, TradeName: trade, Address1: st.Address, Place: place, Pincode: pin,
		StateCode: state, Phone: st.Phone}
	if st.RegistrationID != "" {
		s.Applicable = st.EInvoiceApplicable
	} else {
		enabled, _, err := e.taxSettings(ctx, tenantID)
		if err != nil {
			return Seller{}, err
		}
		s.Applicable = enabled
	}
	return s, nil
}

// Counterparty describes the invoice's party, enriched from bahi when possible.
func (e *Engine) Counterparty(ctx context.Context, inv clients.Invoice) Party {
	cp := Party{GSTIN: gst.Normalize(inv.PartyGSTIN), LegalName: inv.PartyName, StateCode: inv.PartyState, Phone: inv.PartyPhone}
	if cp.StateCode == "" {
		if len(cp.GSTIN) == 15 {
			cp.StateCode = cp.GSTIN[:2]
		} else {
			cp.StateCode = inv.PlaceOfSupply
		}
	}
	if inv.PartyID != "" && e.Dir != nil && e.Dir.Bahi != nil {
		if party, err := e.Dir.Party(ctx, inv.PartyID); err == nil {
			cp.Address1 = party.Address
			cp.Place = PlaceFromAddress(party.Address)
			cp.Pincode = PincodeFromAddress(party.Address)
			if party.StateCode != "" {
				cp.StateCode = party.StateCode
			}
			if cp.LegalName == "" {
				cp.LegalName = party.Name
			}
			if cp.Phone == "" {
				cp.Phone = party.Phone
			}
		} else {
			e.log().Warn("party lookup failed; using invoice snapshot", "party_id", inv.PartyID, "error", err.Error())
		}
	}
	return cp
}

// enqueue writes one event to the outbox inside tx.
func enqueue(ctx context.Context, tx pgx.Tx, typ, tenantID, storeID string, data any) error {
	env, err := events.New(typ, Source, tenantID, storeID, data)
	if err != nil {
		return err
	}
	return events.Enqueue(ctx, tx, env)
}

// gspHTTPError maps a gsp error to an HTTP error for the API.
func gspHTTPError(err error) error {
	ge, ok := gsp.AsError(err)
	if !ok {
		return err
	}
	msg := ge.Message
	if ge.Code != "" {
		msg = ge.Code + ": " + msg
	}
	switch ge.Kind {
	case gsp.KindValidation:
		return httpx.Validation("%s", msg).WithDetails(map[string]any{"code": ge.Code, "errors": ge.Details})
	case gsp.KindDuplicate, gsp.KindNotAllowed:
		return httpx.Conflict("%s", msg)
	case gsp.KindNotFound:
		return httpx.NotFound("%s", msg)
	case gsp.KindAuth:
		return httpx.Validation("%s (save the portal API user under Settings → GST portal credentials)", msg)
	case gsp.KindUnavailable:
		return httpx.Unavailable("%s", msg)
	}
	return httpx.Upstream("%s", msg)
}

// errorCode returns a stable code and message for storing a failure.
func errorCode(err error) (string, string) {
	if ge, ok := gsp.AsError(err); ok {
		code := ge.Code
		if code == "" {
			code = strings.ToUpper(string(ge.Kind))
		}
		return code, ge.Message
	}
	var he *httpx.Error
	if errors.As(err, &he) {
		return strings.ToUpper(he.Code), he.Message
	}
	return "ERROR", err.Error()
}

// transient reports whether err may succeed on retry.
func transient(err error) bool {
	if ge, ok := gsp.AsError(err); ok {
		return ge.Retryable()
	}
	var he *httpx.Error
	if errors.As(err, &he) {
		return he.Status >= 500 || he.Status == 429
	}
	return true // network / database errors
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func rfc3339(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// invoiceFromEvent turns an aadesh event payload into the invoice shape the builders use.
func invoiceFromEvent(p events.InvoicePayload) clients.Invoice {
	inv := clients.Invoice{
		ID: p.InvoiceID, StoreID: p.StoreID, DocType: p.DocType, Number: p.Number, InvoiceDate: p.InvoiceDate,
		SupplierInvoiceNo: p.SupplierInvoiceNo, SupplierInvoiceDate: p.SupplierInvoiceDate, RefInvoiceID: p.RefInvoiceID,
		PartyID: p.PartyID, PartyName: p.PartyName, PartyPhone: p.PartyPhone, PartyGSTIN: p.PartyGSTIN,
		StoreState: p.StoreState, StoreGSTIN: p.StoreGSTIN, PlaceOfSupply: p.PlaceOfSupply, InterState: p.InterState,
		SupplyCategory: p.SupplyCategory, ReverseCharge: p.ReverseCharge, BillOfSupply: p.BillOfSupply, Status: "posted",
		TaxablePaise: p.TaxablePaise, DiscountPaise: p.DiscountPaise, CGSTPaise: p.CGSTPaise, SGSTPaise: p.SGSTPaise,
		IGSTPaise: p.IGSTPaise, CessPaise: p.CessPaise, TCSPaise: p.TCSPaise, RoundOffPaise: p.RoundOffPaise,
		GrandTotalPaise: p.GrandTotalPaise, IRN: p.IRN,
	}
	for _, l := range p.Lines {
		inv.Lines = append(inv.Lines, clients.Line{
			LineNo: l.LineNo, ItemID: l.ItemID, Name: l.Name, HSN: l.HSN, Unit: l.Unit, Qty: l.Qty,
			UnitPricePaise: l.UnitPricePaise, DiscountPaise: l.DiscountPaise, TaxRateBP: l.TaxRateBP, CessRateBP: l.CessRateBP,
			CessPerUnitPaise: l.CessPerUnitPaise, TaxCategory: l.TaxCategory, TaxablePaise: l.TaxablePaise,
			CGSTPaise: l.CGSTPaise, SGSTPaise: l.SGSTPaise, IGSTPaise: l.IGSTPaise, CessPaise: l.CessPaise, TotalPaise: l.TotalPaise,
		})
	}
	return inv
}

// Clock returns the engine's current time (Now, or the wall clock).
func (e *Engine) Clock() time.Time { return e.now() }
