// Package api is kar's HTTP surface: GSTIN, HSN, tax maths, e-Way bill and
// e-Invoice JSON, GSTR-1/3B, tax settings and the event subscriber.
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/taraju/kar/internal/clients"
	"github.com/taraju/kar/internal/compliance"
	"github.com/taraju/kar/internal/gstin"
	"github.com/taraju/kar/internal/returns"
	"github.com/taraju/mool/auth"
	"github.com/taraju/mool/db"
	"github.com/taraju/mool/events"
	"github.com/taraju/mool/gst"
	"github.com/taraju/mool/httpx"
)

// Service holds kar's dependencies.
type Service struct {
	DB    *pgxpool.Pool
	Dir   *clients.Directory
	GSTIN *gstin.Service
	Log   *slog.Logger
}

// Routes registers every route.
func (s *Service) Routes(mux *http.ServeMux, v *auth.Verifier, trustIAM bool) {
	user := func(h httpx.HandlerFunc, perms ...string) http.Handler {
		return v.User(auth.Require(httpx.H(h), perms...))
	}
	internal := func(h httpx.HandlerFunc) http.Handler { return v.Internal(httpx.H(h)) }

	mux.Handle("POST /v1/gstin/verify", user(s.verifyGSTIN, auth.PermPartyManage))
	mux.Handle("POST /internal/v1/gstin/verify", internal(s.verifyGSTIN))

	mux.Handle("GET /v1/hsn", user(s.searchHSN))
	mux.Handle("GET /v1/hsn/{code}", user(s.getHSN))

	mux.Handle("POST /v1/tax/compute", user(s.computeTax))
	mux.Handle("POST /internal/v1/tax/compute", internal(s.computeTax))

	mux.Handle("GET /v1/settings", user(s.getSettings, auth.PermReportRead))
	mux.Handle("PUT /v1/settings", user(s.putSettings, auth.PermSettingsManage))

	mux.Handle("POST /v1/eway-bills/{invoice_id}/json", user(s.ewayJSON, auth.PermInvoiceRead))
	mux.Handle("GET /v1/eway-bills/{invoice_id}", user(s.getStored("eway_bills"), auth.PermInvoiceRead))
	mux.Handle("POST /v1/einvoices/{invoice_id}/json", user(s.einvoiceJSON, auth.PermGSTFile))
	mux.Handle("GET /v1/einvoices/{invoice_id}", user(s.getStored("einvoices"), auth.PermGSTFile))

	mux.Handle("GET /v1/returns/gstr1", user(s.gstr1, auth.PermReportRead))
	mux.Handle("GET /v1/returns/gstr3b", user(s.gstr3b, auth.PermReportRead))

	mux.Handle("POST /events", events.Handler(v, trustIAM, events.Router{
		events.TenantCreated: s.onTenantCreated,
	}))
}

// ---------- GSTIN ----------

type verifyInput struct {
	GSTIN string `json:"gstin"`
}

func (s *Service) verifyGSTIN(w http.ResponseWriter, r *http.Request) error {
	var in verifyInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if strings.TrimSpace(in.GSTIN) == "" {
		return httpx.Validation("gstin is required")
	}
	res, err := s.GSTIN.Verify(r.Context(), in.GSTIN)
	if err != nil {
		return err
	}
	return httpx.OK(w, res)
}

// ---------- HSN ----------

// HSN is one HSN/SAC master row.
type HSN struct {
	Code          string `json:"code"`
	Description   string `json:"description"`
	Kind          string `json:"kind"`
	DefaultRateBP int    `json:"default_rate_bp"`
	CessRateBP    int    `json:"cess_rate_bp"`
}

func (s *Service) searchHSN(w http.ResponseWriter, r *http.Request) error {
	q := httpx.Query(r, "q")
	limit := 20
	if n, err := strconv.Atoi(httpx.Query(r, "limit")); err == nil && n > 0 && n <= 100 {
		limit = n
	}
	rows, err := s.DB.Query(r.Context(), `SELECT code, description, kind, default_rate_bp, cess_rate_bp FROM hsn_codes
		WHERE $1 = '' OR code LIKE $1 || '%' OR description ILIKE '%' || $1 || '%'
		ORDER BY (code LIKE $1 || '%') DESC, code LIMIT $2`, q, limit)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []HSN{}
	for rows.Next() {
		var h HSN
		if err := rows.Scan(&h.Code, &h.Description, &h.Kind, &h.DefaultRateBP, &h.CessRateBP); err != nil {
			return err
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"items": out})
}

func (s *Service) getHSN(w http.ResponseWriter, r *http.Request) error {
	var h HSN
	err := s.DB.QueryRow(r.Context(), `SELECT code, description, kind, default_rate_bp, cess_rate_bp FROM hsn_codes WHERE code=$1`,
		r.PathValue("code")).Scan(&h.Code, &h.Description, &h.Kind, &h.DefaultRateBP, &h.CessRateBP)
	if db.IsNotFound(err) {
		return httpx.NotFound("HSN/SAC code not in master")
	}
	if err != nil {
		return err
	}
	return httpx.OK(w, h)
}

// ---------- tax maths ----------

type computeInput struct {
	StoreState    string     `json:"store_state"`
	PlaceOfSupply string     `json:"place_of_supply"`
	Lines         []gst.Line `json:"lines"`
	RoundOff      *bool      `json:"round_off,omitempty"`
}

func (s *Service) computeTax(w http.ResponseWriter, r *http.Request) error {
	var in computeInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if len(in.Lines) == 0 {
		return httpx.Validation("lines are required")
	}
	sum, err := gst.Compute(in.Lines, in.StoreState, in.PlaceOfSupply, in.RoundOff == nil || *in.RoundOff)
	if err != nil {
		return httpx.Validation("%s", err.Error())
	}
	return httpx.OK(w, sum)
}

// ---------- settings ----------

func (s *Service) getSettings(w http.ResponseWriter, r *http.Request) error {
	st, err := s.settings(r.Context(), auth.FromContext(r.Context()).TenantID)
	if err != nil {
		return err
	}
	return httpx.OK(w, st)
}

func (s *Service) putSettings(w http.ResponseWriter, r *http.Request) error {
	var in SettingsUpdate
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	st, err := s.updateSettings(r.Context(), auth.FromContext(r.Context()).TenantID, in)
	if err != nil {
		return err
	}
	return httpx.OK(w, st)
}

// ---------- e-Way bill / e-Invoice ----------

// loadInvoice fetches the invoice and enforces store scoping.
func (s *Service) loadInvoice(ctx context.Context, p *auth.Principal, id string) (clients.Invoice, error) {
	inv, err := s.Dir.Invoice(ctx, id)
	if err != nil {
		return inv, err
	}
	if p.Claims != nil && !p.Claims.CanAccessStore(inv.StoreID) {
		return inv, httpx.NotFound("invoice not found")
	}
	return inv, nil
}

// ourParty describes the store (seller/consignor) from pehchaan + tax settings.
func (s *Service) ourParty(ctx context.Context, p *auth.Principal, storeID string, st *Settings) (compliance.Party, error) {
	store, err := s.Dir.Store(ctx, storeID)
	if err != nil {
		return compliance.Party{}, err
	}
	legal := store.Name
	if t, err := s.Dir.Tenant(ctx, p.TenantID); err == nil && t.BusinessName != "" {
		legal = t.BusinessName
	}
	g := gst.Normalize(store.GSTIN)
	if g == "" {
		g = st.GSTIN
	}
	state := store.StateCode
	if state == "" {
		state = st.StateCode
	}
	return compliance.Party{GSTIN: g, LegalName: legal, TradeName: store.Name, Address1: store.Address,
		Place: placeFromAddress(store.Address), Pincode: compliance.PincodeFromAddress(store.Address),
		StateCode: state, Phone: store.Phone}, nil
}

// counterparty describes the invoice's party, enriched from bahi when possible.
func (s *Service) counterparty(ctx context.Context, inv clients.Invoice) compliance.Party {
	cp := compliance.Party{GSTIN: gst.Normalize(inv.PartyGSTIN), LegalName: inv.PartyName, StateCode: inv.PartyState, Phone: inv.PartyPhone}
	if cp.StateCode == "" {
		cp.StateCode = inv.PlaceOfSupply
	}
	if inv.PartyID != "" {
		if party, err := s.Dir.Party(ctx, inv.PartyID); err == nil {
			cp.Address1 = party.Address
			cp.Place = placeFromAddress(party.Address)
			cp.Pincode = compliance.PincodeFromAddress(party.Address)
			if party.StateCode != "" {
				cp.StateCode = party.StateCode
			}
		} else {
			s.Log.Warn("party lookup failed; using invoice snapshot", "party_id", inv.PartyID, "error", err.Error())
		}
	}
	return cp
}

func (s *Service) ewayJSON(w http.ResponseWriter, r *http.Request) error {
	var t compliance.Transport
	if err := httpx.Decode(r, &t); err != nil {
		return err
	}
	ctx, p := r.Context(), auth.FromContext(r.Context())
	inv, err := s.loadInvoice(ctx, p, r.PathValue("invoice_id"))
	if err != nil {
		return err
	}
	st, err := s.settings(ctx, p.TenantID)
	if err != nil {
		return err
	}
	our, err := s.ourParty(ctx, p, inv.StoreID, st)
	if err != nil {
		return err
	}
	file, warnings, below, err := compliance.BuildEWay(inv, our, s.counterparty(ctx, inv), t, st.EWayThresholdPaise)
	if err != nil {
		return httpx.Validation("%s", err.Error())
	}
	if warnings == nil {
		warnings = []string{}
	}
	payload, _ := json.Marshal(file)
	warn, _ := json.Marshal(warnings)
	if _, err := s.DB.Exec(ctx, `INSERT INTO eway_bills (tenant_id, invoice_id, invoice_number, payload, below_threshold, warnings, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (tenant_id, invoice_id) DO UPDATE SET payload=EXCLUDED.payload, below_threshold=EXCLUDED.below_threshold,
			warnings=EXCLUDED.warnings, created_by=EXCLUDED.created_by, created_at=now()`,
		p.TenantID, inv.ID, inv.Number, payload, below, warn, p.UserID()); err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"invoice_id": inv.ID, "below_threshold": below, "warnings": warnings, "payload": file})
}

type einvoiceInput struct {
	SellerPincode int `json:"seller_pincode,omitempty"`
	BuyerPincode  int `json:"buyer_pincode,omitempty"`
}

func (s *Service) einvoiceJSON(w http.ResponseWriter, r *http.Request) error {
	var in einvoiceInput
	if r.ContentLength != 0 {
		if err := httpx.Decode(r, &in); err != nil {
			return err
		}
	}
	ctx, p := r.Context(), auth.FromContext(r.Context())
	inv, err := s.loadInvoice(ctx, p, r.PathValue("invoice_id"))
	if err != nil {
		return err
	}
	st, err := s.settings(ctx, p.TenantID)
	if err != nil {
		return err
	}
	seller, err := s.ourParty(ctx, p, inv.StoreID, st)
	if err != nil {
		return err
	}
	buyer := s.counterparty(ctx, inv)
	if in.SellerPincode != 0 {
		seller.Pincode = in.SellerPincode
	}
	if in.BuyerPincode != 0 {
		buyer.Pincode = in.BuyerPincode
	}
	var orig *clients.Invoice
	if inv.RefInvoiceID != "" {
		if o, err := s.Dir.Invoice(ctx, inv.RefInvoiceID); err == nil {
			orig = &o
		}
	}
	e, err := compliance.BuildEInvoice(inv, seller, buyer, orig)
	if err != nil {
		return httpx.Validation("%s", err.Error())
	}
	payload, _ := json.Marshal(e)
	if _, err := s.DB.Exec(ctx, `INSERT INTO einvoices (tenant_id, invoice_id, invoice_number, payload, created_by)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (tenant_id, invoice_id) DO UPDATE SET payload=EXCLUDED.payload, created_by=EXCLUDED.created_by, created_at=now()`,
		p.TenantID, inv.ID, inv.Number, payload, p.UserID()); err != nil {
		return err
	}
	resp := map[string]any{"invoice_id": inv.ID, "payload": e}
	if !st.EInvoiceEnabled {
		resp["warnings"] = []string{"e-invoicing is not enabled in tax settings; generate only if your turnover requires it"}
	}
	return httpx.OK(w, resp)
}

// getStored returns the last generated payload from eway_bills or einvoices.
func (s *Service) getStored(table string) httpx.HandlerFunc {
	q := `SELECT invoice_number, payload, created_by, created_at FROM ` + table + ` WHERE tenant_id=$1 AND invoice_id=$2`
	return func(w http.ResponseWriter, r *http.Request) error {
		p := auth.FromContext(r.Context())
		var number, by string
		var payload json.RawMessage
		var at any
		err := s.DB.QueryRow(r.Context(), q, p.TenantID, r.PathValue("invoice_id")).Scan(&number, &payload, &by, &at)
		if db.IsNotFound(err) {
			return httpx.NotFound("nothing generated for this invoice yet")
		}
		if err != nil {
			return err
		}
		return httpx.OK(w, map[string]any{"invoice_number": number, "payload": payload, "created_by": by, "created_at": at})
	}
}

// ---------- returns ----------

func (s *Service) periodData(r *http.Request) (string, *Settings, []clients.Invoice, error) {
	period := httpx.Query(r, "period")
	from, to, err := returns.Period(period)
	if err != nil {
		return "", nil, nil, httpx.Validation("%s", err.Error())
	}
	ctx, p := r.Context(), auth.FromContext(r.Context())
	st, err := s.settings(ctx, p.TenantID)
	if err != nil {
		return "", nil, nil, err
	}
	invs, err := s.Dir.InvoicesBetween(ctx, from, to)
	if err != nil {
		return "", nil, nil, err
	}
	// One GSTIN = one return: a multi-state business files per registration.
	if g := gst.Normalize(httpx.Query(r, "gstin")); g != "" {
		st.GSTIN, st.StateCode = g, gst.StateCode(g)
	}
	filtered := invs[:0]
	for _, inv := range invs {
		if inv.StoreState == "" || inv.StoreState == st.StateCode {
			filtered = append(filtered, inv)
		}
	}
	return period, st, filtered, nil
}

func (s *Service) gstr1(w http.ResponseWriter, r *http.Request) error {
	period, st, invs, err := s.periodData(r)
	if err != nil {
		return err
	}
	g := returns.BuildGSTR1(st.GSTIN, period, st.StateCode, st.B2CLThresholdPaise, invs)
	if httpx.Query(r, "format") == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="GSTR1_`+period+`.csv"`)
		return returns.WriteGSTR1CSV(w, g)
	}
	return httpx.OK(w, g)
}

func (s *Service) gstr3b(w http.ResponseWriter, r *http.Request) error {
	period, st, invs, err := s.periodData(r)
	if err != nil {
		return err
	}
	g := returns.BuildGSTR3B(st.GSTIN, period, st.StateCode, invs)
	if httpx.Query(r, "format") == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="GSTR3B_`+period+`.csv"`)
		return returns.WriteGSTR3BCSV(w, g)
	}
	return httpx.OK(w, g)
}

// placeFromAddress guesses the city: the last comma-separated part that is not
// a PIN code or a state name.
func placeFromAddress(addr string) string {
	parts := strings.Split(addr, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		p := strings.TrimSpace(parts[i])
		if p == "" || compliance.PincodeFromAddress(p) != 0 && len(p) <= 8 || isStateName(p) {
			continue
		}
		return strings.TrimSpace(strings.TrimRight(p, "0123456789- "))
	}
	return ""
}

func isStateName(s string) bool {
	for _, n := range gst.States {
		if strings.EqualFold(n, s) {
			return true
		}
	}
	return false
}
