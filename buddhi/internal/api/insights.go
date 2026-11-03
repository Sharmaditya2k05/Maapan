package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/maapan/buddhi/internal/clients"
	"github.com/maapan/buddhi/internal/gstinsight"
	"github.com/maapan/buddhi/internal/kpi"
	"github.com/maapan/buddhi/internal/scan"
	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/httpx"
	"github.com/maapan/mool/logx"
	"github.com/maapan/mool/svc"
)

// shortlistPerLine is how many name candidates per billed line get their full
// item-master record fetched for attribute matching.
const shortlistPerLine = 3

// itemMaster loads the store's item list from bhandar and enriches the items
// that could match the billed names (plus any barcode printed on a line) with
// HSN, unit, GST rate and last purchase price. Failures degrade to warnings.
func (a *API) itemMaster(r *http.Request, storeID string, names []string) ([]scan.CatalogItem, []string) {
	var warnings []string
	items, err := a.Clients.StockSummary(r.Context(), storeID)
	if err != nil {
		return nil, append(warnings, "Item list unavailable — items were not matched.")
	}
	catalog := make([]scan.CatalogItem, 0, len(items))
	for _, it := range items {
		catalog = append(catalog, scan.CatalogItem{ItemID: it.ItemID, Name: it.Name, Unit: it.Unit})
	}
	ids, codes := scan.Shortlist(names, catalog, shortlistPerLine)
	if len(ids)+len(codes) == 0 {
		return catalog, warnings
	}
	master, err := a.Clients.LookupItems(r.Context(), ids, codes)
	if err != nil {
		logx.From(r.Context()).Warn("bhandar item lookup failed", "error", err.Error())
		return catalog, append(warnings, "Item details unavailable — items were matched by name only.")
	}
	enriched := make([]scan.CatalogItem, 0, len(master))
	for _, m := range master {
		if m.Status != "" && m.Status != "active" {
			continue
		}
		enriched = append(enriched, masterToCatalog(m))
	}
	return scan.Merge(catalog, enriched), warnings
}

func masterToCatalog(m clients.MasterItem) scan.CatalogItem {
	return scan.CatalogItem{ItemID: m.ID, Name: m.Name, SKU: m.SKU, Barcode: m.Barcode, HSN: m.HSN, Unit: m.Unit,
		TaxRateBP: m.TaxRateBP, PurchasePricePaise: m.PurchasePricePaise}
}

type matchInput struct {
	StoreID string            `json:"store_id"`
	Lines   []scan.MatchInput `json:"lines"`
}

func (in *matchInput) Validate() error {
	if len(in.Lines) == 0 || len(in.Lines) > 200 {
		return httpx.Validation("send 1 to 200 lines")
	}
	for i, l := range in.Lines {
		if n := len([]rune(strings.TrimSpace(l.Name))); n == 0 || n > 300 {
			return httpx.Validation("line %d: name must be 1 to 300 characters", i+1)
		}
	}
	return nil
}

// matchItems is POST /v1/scan/match-items: re-match (edited) bill lines against
// bhandar's item master without calling the AI.
func (a *API) matchItems(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	var in matchInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := a.Guard.CheckFlag(r.Context(), p.TenantID); err != nil {
		return err
	}
	st := p.StoreID
	if in.StoreID != "" {
		s, err := storeScope(p, in.StoreID, false)
		if err != nil {
			return err
		}
		st = s
	}
	names := make([]string, 0, len(in.Lines))
	for _, l := range in.Lines {
		names = append(names, l.Name)
	}
	catalog, warnings := a.itemMaster(r, st, names)
	if warnings == nil {
		warnings = []string{}
	}
	return httpx.OK(w, map[string]any{"matches": scan.MatchLines(in.Lines, catalog), "warnings": warnings})
}

// gstInsights is GET /v1/insights/gst?gstin=&period=: kar's GSTR-2B matching
// summarised (mismatches, suppliers missing in 2B, ITC at risk). The result is
// cached for the dashboard card.
func (a *API) gstInsights(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	if err := a.Guard.CheckFlag(r.Context(), p.TenantID); err != nil {
		return err
	}
	if p.Claims != nil && auth.StoreScoped(p.Claims.Role) {
		return httpx.Forbidden("GST insights are for the whole business; your role sees only its own stores")
	}
	period := httpx.Query(r, "period")
	if period == "" {
		period = gstinsight.PreviousPeriod(a.Now())
	}
	if !gstinsight.ValidPeriod(period) {
		return httpx.Validation("period must be MMYYYY, e.g. 092026")
	}
	gstin := strings.ToUpper(strings.TrimSpace(httpx.Query(r, "gstin")))
	in, err := gstinsight.Refresh(r.Context(), a.DB, a.Clients.GSTR2B, p.TenantID, gstin, period, a.Now())
	if err != nil {
		return err
	}
	return httpx.OK(w, in)
}

// gstCard is the dashboard's GST card from the last cached insight; it is
// shown only to tenants with the AI pilot flag and never calls kar.
func (a *API) gstCard(r *http.Request, p *auth.Principal) []kpi.Card {
	if a.Guard.CheckFlag(r.Context(), p.TenantID) != nil {
		return nil
	}
	if p.Claims != nil && !p.Claims.Can(auth.PermAIUse) {
		return nil
	}
	in, err := gstinsight.Latest(r.Context(), a.DB, p.TenantID)
	if err != nil {
		logx.From(r.Context()).Warn("gst insight card", "error", err.Error())
		return nil
	}
	if in == nil {
		return nil
	}
	return []kpi.Card{{
		Kind: "gst_2b", Title: in.Card.Title, Value: in.Card.Value, Severity: in.Card.Severity, Text: in.Card.Text,
		Link: "/api/buddhi/v1/insights/gst?gstin=" + in.GSTIN + "&period=" + in.Period,
		Data: map[string]any{
			"gstin": in.GSTIN, "period": in.Period, "itc_at_risk_paise": in.ITCAtRiskPaise,
			"mismatches": in.Counts["mismatch"], "missing_in_2b": in.Counts["missing_in_2b"],
			"missing_in_books": in.Counts["missing_in_books"], "computed_at": in.ComputedAt,
		},
	}}
}

// jobGSTInsights refreshes last month's insight (primary GSTIN) for every
// AI-enabled tenant. Run daily after kar's 2B fetch; no LLM.
func (a *API) jobGSTInsights(w http.ResponseWriter, r *http.Request) error {
	tenants, err := a.Guard.EnabledTenants(r.Context())
	if err != nil {
		return err
	}
	period := httpx.Query(r, "period")
	if period == "" {
		period = gstinsight.PreviousPeriod(a.Now())
	}
	if !gstinsight.ValidPeriod(period) {
		return httpx.Validation("period must be MMYYYY")
	}
	res := map[string]any{"period": period, "tenants": len(tenants), "refreshed": 0, "errors": []string{}}
	refreshed, errs := 0, []string{}
	for _, t := range tenants {
		ctx := svc.ForTenant(context.WithoutCancel(r.Context()), t.TenantID)
		if _, err := gstinsight.Refresh(ctx, a.DB, a.Clients.GSTR2B, t.TenantID, "", period, a.Now()); err != nil {
			errs = append(errs, t.TenantID+": "+err.Error())
			continue
		}
		refreshed++
	}
	res["refreshed"], res["errors"] = refreshed, errs
	return httpx.OK(w, res)
}
