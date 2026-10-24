// Package api is kar's HTTP surface: GSTIN, HSN master, tax maths, e-Invoice
// (IRN) and e-Way bill lifecycle, tax settings, GSP credentials and the event
// subscriber. GSTR-1 / GSTR-3B / 2B routes are registered by
// kar/internal/returns (returns.Register), not here.
package api

import (
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/taraju/kar/internal/clients"
	"github.com/taraju/kar/internal/compliance"
	"github.com/taraju/kar/internal/creds"
	"github.com/taraju/kar/internal/gstin"
	"github.com/taraju/kar/internal/hsn"
	"github.com/taraju/mool/auth"
	"github.com/taraju/mool/events"
	"github.com/taraju/mool/gst"
	"github.com/taraju/mool/httpx"
)

// Service holds kar's dependencies.
type Service struct {
	DB     *pgxpool.Pool
	Dir    *clients.Directory
	GSTIN  *gstin.Service
	Log    *slog.Logger
	Engine *compliance.Engine
	HSN    *hsn.Store
	Creds  *creds.Store
	// HSNImportAllowed decides whether a tenant's admin may import into the
	// shared HSN master via /v1/hsn/import (nil = nobody; ops use /internal).
	HSNImportAllowed func(tenantID string) bool
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
	mux.Handle("POST /v1/hsn/import", user(s.importHSN, auth.PermSettingsManage))
	mux.Handle("GET /internal/v1/hsn/{code}", internal(s.getHSN))
	mux.Handle("POST /internal/v1/hsn/import", internal(s.importHSN))

	mux.Handle("POST /v1/tax/compute", user(s.computeTax))
	mux.Handle("POST /internal/v1/tax/compute", internal(s.computeTax))

	mux.Handle("GET /v1/settings", user(s.getSettings, auth.PermReportRead))
	mux.Handle("PUT /v1/settings", user(s.putSettings, auth.PermSettingsManage))
	mux.Handle("GET /v1/settings/gsp-credentials", user(s.listGSPCredentials, auth.PermSettingsManage))
	mux.Handle("GET /v1/settings/gsp-credentials/{gstin}", user(s.getGSPCredentials, auth.PermSettingsManage))
	mux.Handle("PUT /v1/settings/gsp-credentials/{gstin}", user(s.putGSPCredentials, auth.PermSettingsManage))
	mux.Handle("DELETE /v1/settings/gsp-credentials/{gstin}", user(s.deleteGSPCredentials, auth.PermSettingsManage))

	mux.Handle("GET /v1/einvoices", user(s.listEInvoices, auth.PermReportRead))
	mux.Handle("POST /v1/einvoices/{invoice_id}", user(s.generateEInvoice, auth.PermGSTFile))
	mux.Handle("POST /v1/einvoices/{invoice_id}/cancel", user(s.cancelEInvoice, auth.PermGSTFile))
	mux.Handle("POST /v1/einvoices/{invoice_id}/json", user(s.einvoiceJSON, auth.PermGSTFile))
	mux.Handle("GET /v1/einvoices/{invoice_id}", user(s.getEInvoice, auth.PermInvoiceRead))
	mux.Handle("GET /internal/v1/einvoices/{invoice_id}", internal(s.getEInvoice))

	mux.Handle("GET /v1/eway-bills", user(s.listEWays, auth.PermReportRead))
	mux.Handle("POST /v1/eway-bills/{invoice_id}", user(s.generateEWay, auth.PermInvoiceRead))
	mux.Handle("PUT /v1/eway-bills/{invoice_id}/part-b", user(s.partBEWay, auth.PermInvoiceRead))
	mux.Handle("POST /v1/eway-bills/{invoice_id}/extend", user(s.extendEWay, auth.PermInvoiceRead))
	mux.Handle("POST /v1/eway-bills/{invoice_id}/cancel", user(s.cancelEWay, auth.PermInvoiceCancel))
	mux.Handle("POST /v1/eway-bills/{invoice_id}/json", user(s.ewayJSON, auth.PermInvoiceRead))
	mux.Handle("GET /v1/eway-bills/{invoice_id}", user(s.getEWay, auth.PermInvoiceRead))

	mux.Handle("POST /events", events.Handler(v, trustIAM, events.Router{
		events.TenantCreated:  s.onTenantCreated,
		events.InvoiceCreated: s.Engine.OnInvoiceEvent,
		events.NoteCreated:    s.Engine.OnInvoiceEvent,
	}))
}

// CanAccessStore reports whether the caller may see documents of storeID
// (store-scoped roles see only their stores; internal callers see all).
func CanAccessStore(p *auth.Principal, storeID string) bool {
	return p == nil || p.Claims == nil || storeID == "" || p.Claims.CanAccessStore(storeID)
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

func (s *Service) searchHSN(w http.ResponseWriter, r *http.Request) error {
	on, err := s.HSN.ParseOn(httpx.Query(r, "on"))
	if err != nil {
		return httpx.Validation("%s", err.Error())
	}
	limit, _ := strconv.Atoi(httpx.Query(r, "limit"))
	items, err := s.HSN.Search(r.Context(), httpx.Query(r, "q"), on, limit)
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"items": items, "on": on.Format("2006-01-02")})
}

func (s *Service) getHSN(w http.ResponseWriter, r *http.Request) error {
	on, err := s.HSN.ParseOn(httpx.Query(r, "on"))
	if err != nil {
		return httpx.Validation("%s", err.Error())
	}
	c, ok, err := s.HSN.Get(r.Context(), r.PathValue("code"), on)
	if err != nil {
		return err
	}
	if !ok {
		return httpx.NotFound("HSN/SAC code not in master on %s", on.Format("2006-01-02"))
	}
	return httpx.OK(w, c)
}

// importHSN accepts the CSV as the raw body (text/csv) or as the "file" part
// of a multipart form. ?source= labels the rows (default "import").
func (s *Service) importHSN(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	if !p.Internal && (s.HSNImportAllowed == nil || !s.HSNImportAllowed(p.TenantID)) {
		return httpx.Forbidden("the HSN master is shared by every business; imports are limited to the platform operator (HSN_IMPORT_TENANTS)")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 20<<20)
	var src io.Reader = r.Body
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt == "multipart/form-data" {
		f, _, err := r.FormFile("file")
		if err != nil {
			return httpx.Validation("multipart upload needs a \"file\" part with the CSV")
		}
		defer f.Close()
		src = f
	}
	source := httpx.Query(r, "source")
	if source == "" {
		source = "import:" + p.UserID()
	}
	rep, err := s.HSN.Import(r.Context(), src, source)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return httpx.Validation("CSV exceeds 20 MB")
		}
		if httpx.AsError(err).Status == 500 && !strings.HasPrefix(err.Error(), "line ") {
			return httpx.Validation("%s", err.Error())
		}
		return err
	}
	s.Log.Info("hsn import", "by", p.UserID(), "tenant_id", p.TenantID, "rows", rep.Rows, "inserted", rep.Inserted,
		"updated", rep.Updated, "closed", rep.Closed, "rejected", rep.Rejected)
	return httpx.OK(w, rep)
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
	if err := in.Validate(); err != nil {
		return err
	}
	st, err := s.updateSettings(r.Context(), auth.FromContext(r.Context()).TenantID, in)
	if err != nil {
		return err
	}
	return httpx.OK(w, st)
}

// ---------- e-Invoice ----------

// loadInvoice fetches the invoice and enforces store scoping.
func (s *Service) loadInvoice(r *http.Request) (clients.Invoice, error) {
	p := auth.FromContext(r.Context())
	inv, err := s.Dir.Invoice(r.Context(), r.PathValue("invoice_id"))
	if err != nil {
		return inv, err
	}
	if !CanAccessStore(p, inv.StoreID) {
		return inv, httpx.NotFound("invoice not found")
	}
	return inv, nil
}

type einvoiceInput struct {
	Export *compliance.EInvExport `json:"export,omitempty"` // shipping bill / port / country for exports
}

func decodeOptional(r *http.Request, dst any) error {
	if r.ContentLength == 0 {
		return nil
	}
	return httpx.Decode(r, dst)
}

func (s *Service) generateEInvoice(w http.ResponseWriter, r *http.Request) error {
	var in einvoiceInput
	if err := decodeOptional(r, &in); err != nil {
		return err
	}
	inv, err := s.loadInvoice(r)
	if err != nil {
		return err
	}
	p := auth.FromContext(r.Context())
	rec, err := s.Engine.GenerateEInvoice(r.Context(), compliance.GenerateRequest{TenantID: p.TenantID, Invoice: inv,
		By: p.UserID(), Trigger: "manual", Export: in.Export})
	var na *compliance.NotApplicableError
	if errors.As(err, &na) {
		return httpx.Validation("%s", na.Reason)
	}
	if err != nil {
		return err
	}
	return httpx.OK(w, rec)
}

type cancelInput struct {
	ReasonCode int    `json:"reason_code"`
	Remark     string `json:"remark"`
}

func (s *Service) cancelEInvoice(w http.ResponseWriter, r *http.Request) error {
	var in cancelInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p := auth.FromContext(r.Context())
	cur, ok, err := s.Engine.EInvoice(r.Context(), p.TenantID, r.PathValue("invoice_id"))
	if err != nil {
		return err
	}
	if !ok || !CanAccessStore(p, cur.StoreID) {
		return httpx.NotFound("no e-invoice for this invoice")
	}
	rec, err := s.Engine.CancelEInvoice(r.Context(), p.TenantID, cur.InvoiceID, in.ReasonCode, in.Remark, p.UserID())
	if err != nil {
		return err
	}
	return httpx.OK(w, rec)
}

func (s *Service) getEInvoice(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	rec, ok, err := s.Engine.EInvoice(r.Context(), p.TenantID, r.PathValue("invoice_id"))
	if err != nil {
		return err
	}
	if !ok || !CanAccessStore(p, rec.StoreID) {
		return httpx.NotFound("no e-invoice for this invoice yet")
	}
	return httpx.OK(w, rec)
}

func listFilter(r *http.Request) compliance.EInvoiceFilter {
	pg := httpx.ParsePage(r)
	f := compliance.EInvoiceFilter{Status: httpx.Query(r, "status"), From: httpx.Query(r, "from"), To: httpx.Query(r, "to"),
		GSTIN: httpx.Query(r, "gstin"), Limit: pg.Limit, Offset: pg.Offset}
	if p := auth.FromContext(r.Context()); p != nil && p.Claims != nil && auth.StoreScoped(p.Claims.Role) {
		f.StoreIDs = append([]string{}, p.Claims.Stores...)
	}
	return f
}

func validListDates(f compliance.EInvoiceFilter) error {
	for _, d := range []string{f.From, f.To} {
		if d != "" {
			if _, err := compliance.NICDate(d); err != nil {
				return httpx.Validation("from / to must be YYYY-MM-DD")
			}
		}
	}
	return nil
}

func (s *Service) listEInvoices(w http.ResponseWriter, r *http.Request) error {
	f := listFilter(r)
	if err := validListDates(f); err != nil {
		return err
	}
	items, total, err := s.Engine.ListEInvoices(r.Context(), auth.FromContext(r.Context()).TenantID, f)
	if err != nil {
		return err
	}
	return httpx.OK(w, httpx.List[compliance.EInvoiceRecord]{Items: items, Total: total, Limit: f.Limit, Offset: f.Offset})
}

// einvoiceJSON returns the IRP payload for download without registering it.
func (s *Service) einvoiceJSON(w http.ResponseWriter, r *http.Request) error {
	var in einvoiceInput
	if err := decodeOptional(r, &in); err != nil {
		return err
	}
	inv, err := s.loadInvoice(r)
	if err != nil {
		return err
	}
	p := auth.FromContext(r.Context())
	payload, seller, err := s.Engine.BuildFor(r.Context(), p.TenantID, inv, in.Export)
	if err != nil {
		return err
	}
	resp := map[string]any{"invoice_id": inv.ID, "payload": payload, "warnings": []string{}}
	if ok, reason := compliance.EInvoiceApplicable(seller.Applicable, inv); !ok {
		resp["warnings"] = []string{reason}
	}
	return httpx.OK(w, resp)
}

// ---------- e-Way bill ----------

func (s *Service) generateEWay(w http.ResponseWriter, r *http.Request) error {
	var t compliance.Transport
	if err := httpx.Decode(r, &t); err != nil {
		return err
	}
	inv, err := s.loadInvoice(r)
	if err != nil {
		return err
	}
	p := auth.FromContext(r.Context())
	rec, err := s.Engine.GenerateEWay(r.Context(), p.TenantID, inv, t, p.UserID())
	if err != nil {
		return err
	}
	return httpx.OK(w, rec)
}

// ewayForCaller loads the stored bill and enforces store scoping.
func (s *Service) ewayForCaller(r *http.Request) (compliance.EWayRecord, error) {
	p := auth.FromContext(r.Context())
	rec, ok, err := s.Engine.EWay(r.Context(), p.TenantID, r.PathValue("invoice_id"))
	if err != nil {
		return rec, err
	}
	if !ok || !CanAccessStore(p, rec.StoreID) {
		return rec, httpx.NotFound("no e-way bill for this invoice yet")
	}
	return rec, nil
}

func (s *Service) partBEWay(w http.ResponseWriter, r *http.Request) error {
	var in compliance.PartBInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	cur, err := s.ewayForCaller(r)
	if err != nil {
		return err
	}
	p := auth.FromContext(r.Context())
	rec, err := s.Engine.UpdatePartB(r.Context(), p.TenantID, cur.InvoiceID, in, p.UserID())
	if err != nil {
		return err
	}
	return httpx.OK(w, rec)
}

func (s *Service) extendEWay(w http.ResponseWriter, r *http.Request) error {
	var in compliance.ExtendInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	cur, err := s.ewayForCaller(r)
	if err != nil {
		return err
	}
	p := auth.FromContext(r.Context())
	rec, err := s.Engine.ExtendEWay(r.Context(), p.TenantID, cur.InvoiceID, in, p.UserID())
	if err != nil {
		return err
	}
	return httpx.OK(w, rec)
}

func (s *Service) cancelEWay(w http.ResponseWriter, r *http.Request) error {
	var in cancelInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	cur, err := s.ewayForCaller(r)
	if err != nil {
		return err
	}
	p := auth.FromContext(r.Context())
	rec, err := s.Engine.CancelEWay(r.Context(), p.TenantID, cur.InvoiceID, in.ReasonCode, in.Remark, p.UserID())
	if err != nil {
		return err
	}
	return httpx.OK(w, rec)
}

func (s *Service) getEWay(w http.ResponseWriter, r *http.Request) error {
	rec, err := s.ewayForCaller(r)
	if err != nil {
		return err
	}
	return httpx.OK(w, rec)
}

func (s *Service) listEWays(w http.ResponseWriter, r *http.Request) error {
	f := listFilter(r)
	if err := validListDates(f); err != nil {
		return err
	}
	items, total, err := s.Engine.ListEWays(r.Context(), auth.FromContext(r.Context()).TenantID, f)
	if err != nil {
		return err
	}
	return httpx.OK(w, httpx.List[compliance.EWayRecord]{Items: items, Total: total, Limit: f.Limit, Offset: f.Offset})
}

// ewayJSON returns the NIC bulk JSON for download (offline upload) without generating.
func (s *Service) ewayJSON(w http.ResponseWriter, r *http.Request) error {
	var t compliance.Transport
	if err := httpx.Decode(r, &t); err != nil {
		return err
	}
	inv, err := s.loadInvoice(r)
	if err != nil {
		return err
	}
	p := auth.FromContext(r.Context())
	file, warnings, below, _, err := s.Engine.BuildEWayFor(r.Context(), p.TenantID, inv, t)
	if err != nil {
		return err
	}
	if err := compliance.CheckDocAge(inv.InvoiceDate, s.Engine.Clock()); err != nil {
		warnings = append(warnings, err.Error())
	}
	return httpx.OK(w, map[string]any{"invoice_id": inv.ID, "below_threshold": below, "warnings": warnings, "payload": file})
}
