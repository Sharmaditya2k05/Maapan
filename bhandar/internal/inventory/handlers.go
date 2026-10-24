package inventory

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/taraju/bhandar/internal/barcode"
	"github.com/taraju/mool/auth"
	"github.com/taraju/mool/events"
	"github.com/taraju/mool/httpx"
)

// readPerms: anyone who bills, buys, manages stock or reads reports may read the item master and stock.
var readPerms = []string{auth.PermItemManage, auth.PermInvoiceCreate, auth.PermInvoiceRead, auth.PermStockManage, auth.PermPurchaseManage, auth.PermReportRead}

// anyOf runs h when the principal holds at least one of perms.
func anyOf(h httpx.HandlerFunc, perms ...string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := auth.FromContext(r.Context())
		if p == nil {
			httpx.WriteError(w, r, httpx.Unauthorized("not authenticated"))
			return
		}
		for _, perm := range perms {
			if p.Can(perm) {
				httpx.H(h).ServeHTTP(w, r)
				return
			}
		}
		httpx.WriteError(w, r, httpx.Forbidden("missing permission (need one of %s)", strings.Join(perms, ", ")))
	})
}

// Routes registers bhandar's HTTP API on mux.
func (s *Service) Routes(mux *http.ServeMux, v *auth.Verifier, trustIAM bool) {
	user := func(h httpx.HandlerFunc, perms ...string) http.Handler { return v.User(anyOf(h, perms...)) }
	read := func(h httpx.HandlerFunc) http.Handler { return v.User(anyOf(h, readPerms...)) }
	manage := func(h httpx.HandlerFunc) http.Handler { return v.User(anyOf(h, auth.PermItemManage)) }
	stock := func(h httpx.HandlerFunc) http.Handler { return v.User(anyOf(h, auth.PermStockManage)) }
	buyer := func(h httpx.HandlerFunc) http.Handler {
		return v.User(anyOf(h, auth.PermStockManage, auth.PermPurchaseManage))
	}
	analyst := func(h httpx.HandlerFunc) http.Handler {
		return v.User(anyOf(h, auth.PermReportRead, auth.PermStockManage))
	}

	mux.Handle("GET /v1/items", read(s.listItems))
	mux.Handle("POST /v1/items", manage(s.createItem))
	mux.Handle("GET /v1/items/pos-catalog", read(s.posCatalog))
	mux.Handle("GET /v1/items/{id}", read(s.getItem))
	mux.Handle("PUT /v1/items/{id}", manage(s.updateItem))
	mux.Handle("DELETE /v1/items/{id}", manage(s.deactivateItem))
	mux.Handle("POST /v1/items/{id}/activate", manage(s.activateItem))
	mux.Handle("PUT /v1/items/{id}/reorder", user(s.setReorder, auth.PermItemManage, auth.PermStockManage))
	mux.Handle("GET /v1/items/{id}/barcode.svg", read(s.barcodeSVG))
	mux.Handle("GET /v1/items/{id}/ledger", read(s.itemLedger))
	mux.Handle("GET /v1/items/{id}/batches", read(s.itemBatches))
	mux.Handle("GET /v1/barcodes/{code}", read(s.lookupCode))
	mux.Handle("POST /v1/labels", read(s.labels))
	mux.Handle("GET /v1/units", read(s.units))
	mux.Handle("POST /v1/units", manage(s.createUnit))

	mux.Handle("GET /v1/stock", read(s.stock))
	mux.Handle("POST /v1/stock/adjustments", stock(s.adjust))

	mux.Handle("GET /v1/transfers", read(s.listTransfers))
	mux.Handle("POST /v1/transfers", stock(s.createTransfer))
	mux.Handle("GET /v1/transfers/{id}", read(s.getTransfer))
	mux.Handle("POST /v1/transfers/{id}/{action}", stock(s.transferAction))

	mux.Handle("GET /v1/reorder/suggestions", buyer(s.suggestions))
	mux.Handle("POST /v1/reorder/purchase-draft", buyer(s.purchaseDraft))
	mux.Handle("GET /v1/batches/expiring", read(s.expiring))
	mux.Handle("POST /v1/batches/{id}/return-to-vendor", stock(s.returnToVendor))

	mux.Handle("GET /v1/analytics/dead-stock", analyst(s.deadStock))
	mux.Handle("GET /v1/analytics/stock-aging", analyst(s.stockAging))
	mux.Handle("GET /v1/analytics/valuation", analyst(s.valuation))

	mux.Handle("POST /internal/v1/items/lookup", v.Internal(httpx.H(s.lookupItems)))
	mux.Handle("POST /internal/v1/bulk/items", v.Internal(httpx.H(s.bulkItems)))
	mux.Handle("POST /internal/v1/bulk/opening-stock", v.Internal(httpx.H(s.bulkOpeningStock)))
	mux.Handle("GET /internal/v1/stock/summary", v.Internal(httpx.H(s.summary)))
	mux.Handle("POST /internal/v1/jobs/expiry-scan", v.Internal(httpx.H(s.expiryScan)))
	mux.Handle("POST /internal/v1/jobs/reorder-scan", v.Internal(httpx.H(s.reorderScan)))
	mux.Handle("GET /internal/v1/outbox/dead", v.Internal(httpx.H(s.outboxDead)))
	mux.Handle("POST /internal/v1/outbox/replay", v.Internal(httpx.H(s.outboxReplay)))

	mux.Handle("POST /events", events.Handler(v, trustIAM, s.Router()))
}

func tenant(r *http.Request) (*auth.Principal, string, error) {
	p := auth.FromContext(r.Context())
	if p == nil || p.TenantID == "" {
		return nil, "", httpx.Unauthorized("tenant is required")
	}
	return p, p.TenantID, nil
}

// storeFromQuery resolves ?store_id= (or the active store) and checks access.
func storeFromQuery(r *http.Request, p *auth.Principal) (string, error) {
	return storeFor(p, httpx.Query(r, "store_id"))
}

// optionalStore returns ?store_id= when given (checked) or "" meaning all stores
// the principal may see; store-scoped roles always get their active store.
func optionalStore(r *http.Request, p *auth.Principal) (string, error) {
	id := httpx.Query(r, "store_id")
	if id == "" && p.Claims != nil && auth.StoreScoped(p.Claims.Role) {
		id = p.StoreID
	}
	if id != "" && !canAccessStore(p, id) {
		return "", httpx.Forbidden("no access to store %s", id)
	}
	return id, nil
}

func intQuery(r *http.Request, key string, def int) int {
	if v, err := strconv.Atoi(httpx.Query(r, key)); err == nil && v > 0 {
		return v
	}
	return def
}

// ---- items ----

func (s *Service) listItems(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	f := ItemFilter{Q: httpx.Query(r, "q"), Category: httpx.Query(r, "category"), Status: httpx.Query(r, "status"), Page: httpx.ParsePage(r)}
	items, total, err := s.ListItems(r.Context(), t, f)
	if err != nil {
		return err
	}
	return httpx.OK(w, httpx.List[Item]{Items: items, Total: total, Limit: f.Limit, Offset: f.Offset})
}

func (s *Service) createItem(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	var in ItemInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	it, err := s.CreateItem(r.Context(), t, in)
	if err != nil {
		return err
	}
	return httpx.Created(w, it)
}

func (s *Service) getItem(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	it, err := s.GetItem(r.Context(), t, r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.OK(w, it)
}

func (s *Service) updateItem(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	var in ItemInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	it, err := s.UpdateItem(r.Context(), t, r.PathValue("id"), in)
	if err != nil {
		return err
	}
	return httpx.OK(w, it)
}

func (s *Service) deactivateItem(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	if err := s.SetStatus(r.Context(), t, r.PathValue("id"), "inactive"); err != nil {
		return err
	}
	return httpx.NoContent(w)
}

func (s *Service) activateItem(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	if err := s.SetStatus(r.Context(), t, r.PathValue("id"), "active"); err != nil {
		return err
	}
	it, err := s.GetItem(r.Context(), t, r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.OK(w, it)
}

func (s *Service) setReorder(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	var in ReorderInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	it, err := s.SetReorder(r.Context(), t, r.PathValue("id"), in)
	if err != nil {
		return err
	}
	return httpx.OK(w, it)
}

func (s *Service) barcodeSVG(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	it, err := s.GetItem(r.Context(), t, r.PathValue("id"))
	if err != nil {
		return err
	}
	if it.Barcode == "" {
		return httpx.NotFound("item has no barcode")
	}
	o := barcode.DefaultOptions()
	o.Module = intQuery(r, "module", o.Module)
	o.Height = intQuery(r, "height", o.Height)
	svg, err := barcode.Render(it.Barcode, o)
	if err != nil {
		return httpx.Validation("%s", err.Error())
	}
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	_, err = w.Write([]byte(svg))
	return err
}

func (s *Service) itemLedger(w http.ResponseWriter, r *http.Request) error {
	p, t, err := tenant(r)
	if err != nil {
		return err
	}
	store, err := optionalStore(r, p)
	if err != nil {
		return err
	}
	f := LedgerFilter{StoreID: store, From: httpx.Query(r, "from"), To: httpx.Query(r, "to"), Page: httpx.ParsePage(r)}
	movs, total, err := s.ItemLedger(r.Context(), t, r.PathValue("id"), f)
	if err != nil {
		return err
	}
	return httpx.OK(w, httpx.List[Movement]{Items: movs, Total: total, Limit: f.Limit, Offset: f.Offset})
}

func (s *Service) itemBatches(w http.ResponseWriter, r *http.Request) error {
	p, t, err := tenant(r)
	if err != nil {
		return err
	}
	store, err := optionalStore(r, p)
	if err != nil {
		return err
	}
	batches, err := s.ItemBatches(r.Context(), t, r.PathValue("id"), store)
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"batches": batches})
}

func (s *Service) lookupCode(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	res, err := s.LookupByCode(r.Context(), t, r.PathValue("code"))
	if err != nil {
		return err
	}
	return httpx.OK(w, res)
}

func (s *Service) posCatalog(w http.ResponseWriter, r *http.Request) error {
	p, t, err := tenant(r)
	if err != nil {
		return err
	}
	store, err := storeFromQuery(r, p)
	if err != nil {
		return err
	}
	items, etag, err := s.PosCatalog(r.Context(), t, store)
	if err != nil {
		return err
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, no-cache")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return nil
	}
	return httpx.OK(w, map[string]any{"store_id": store, "items": items, "etag": etag})
}

// LabelRequest is the body of POST /v1/labels.
type LabelRequest struct {
	Items []struct {
		ItemID string `json:"item_id"`
		Copies int    `json:"copies"`
	} `json:"items"`
	ShowPrice bool `json:"show_price"`
	Module    int  `json:"module,omitempty"`
	Height    int  `json:"height,omitempty"`
}

// Validate checks a label request.
func (in *LabelRequest) Validate() error {
	if len(in.Items) == 0 || len(in.Items) > 200 {
		return httpx.Validation("send 1 to 200 items")
	}
	for i := range in.Items {
		if in.Items[i].ItemID == "" {
			return httpx.Validation("item %d: item_id is required", i+1)
		}
		if in.Items[i].Copies <= 0 {
			in.Items[i].Copies = 1
		}
		if in.Items[i].Copies > 500 {
			return httpx.Validation("item %d: at most 500 copies", i+1)
		}
	}
	return nil
}

// Label is one sticker's data.
type Label struct {
	ItemID         string `json:"item_id"`
	Name           string `json:"name"`
	Barcode        string `json:"barcode"`
	MRPPaise       int64  `json:"mrp_paise,omitempty"`
	SalePricePaise int64  `json:"sale_price_paise,omitempty"`
	Copies         int    `json:"copies"`
	SVG            string `json:"svg"`
}

func (s *Service) labels(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	var in LabelRequest
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	o := barcode.DefaultOptions()
	if in.Module > 0 {
		o.Module = in.Module
	}
	if in.Height > 0 {
		o.Height = in.Height
	}
	out := make([]Label, 0, len(in.Items))
	for _, li := range in.Items {
		it, err := s.GetItem(r.Context(), t, li.ItemID)
		if err != nil {
			return err
		}
		if it.Barcode == "" {
			return httpx.Validation("item %s has no barcode", it.Name)
		}
		svg, err := barcode.Render(it.Barcode, o)
		if err != nil {
			return httpx.Validation("%s: %s", it.Name, err.Error())
		}
		l := Label{ItemID: it.ID, Name: it.Name, Barcode: it.Barcode, Copies: li.Copies, SVG: svg}
		if in.ShowPrice {
			l.MRPPaise, l.SalePricePaise = it.MRPPaise, it.SalePricePaise
		}
		out = append(out, l)
	}
	return httpx.OK(w, map[string]any{"labels": out})
}

func (s *Service) units(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	units, err := s.Units(r.Context(), t)
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"units": units})
}

func (s *Service) createUnit(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	var u Unit
	if err := httpx.Decode(r, &u); err != nil {
		return err
	}
	if err := s.CreateUnit(r.Context(), t, u); err != nil {
		return err
	}
	return httpx.Created(w, u)
}

// ---- stock ----

func (s *Service) stock(w http.ResponseWriter, r *http.Request) error {
	p, t, err := tenant(r)
	if err != nil {
		return err
	}
	store, err := optionalStore(r, p)
	if err != nil {
		return err
	}
	f := StockFilter{StoreID: store, ItemID: httpx.Query(r, "item_id"), ByBatch: httpx.Query(r, "by_batch") == "true", Page: httpx.ParsePage(r)}
	rows, total, err := s.Stock(r.Context(), t, f)
	if err != nil {
		return err
	}
	return httpx.OK(w, httpx.List[Balance]{Items: rows, Total: total, Limit: f.Limit, Offset: f.Offset})
}

func (s *Service) adjust(w http.ResponseWriter, r *http.Request) error {
	p, t, err := tenant(r)
	if err != nil {
		return err
	}
	var in AdjustmentInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	store, err := storeFor(p, in.StoreID)
	if err != nil {
		return err
	}
	adj, err := s.Adjust(r.Context(), t, store, p.UserID(), in)
	if err != nil {
		return err
	}
	return httpx.Created(w, adj)
}

// ---- transfers ----

func (s *Service) listTransfers(w http.ResponseWriter, r *http.Request) error {
	p, t, err := tenant(r)
	if err != nil {
		return err
	}
	store, err := optionalStore(r, p)
	if err != nil {
		return err
	}
	f := TransferFilter{StoreID: store, Status: httpx.Query(r, "status"), Page: httpx.ParsePage(r)}
	rows, total, err := s.ListTransfers(r.Context(), t, f)
	if err != nil {
		return err
	}
	return httpx.OK(w, httpx.List[Transfer]{Items: rows, Total: total, Limit: f.Limit, Offset: f.Offset})
}

func (s *Service) createTransfer(w http.ResponseWriter, r *http.Request) error {
	p, _, err := tenant(r)
	if err != nil {
		return err
	}
	var in TransferInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	tr, err := s.CreateTransfer(r.Context(), p, in)
	if err != nil {
		return err
	}
	return httpx.Created(w, tr)
}

func (s *Service) getTransfer(w http.ResponseWriter, r *http.Request) error {
	p, t, err := tenant(r)
	if err != nil {
		return err
	}
	tr, err := s.GetTransfer(r.Context(), t, r.PathValue("id"))
	if err != nil {
		return err
	}
	if !canAccessStore(p, tr.FromStoreID) && !canAccessStore(p, tr.ToStoreID) {
		return httpx.NotFound("transfer not found")
	}
	return httpx.OK(w, tr)
}

func (s *Service) transferAction(w http.ResponseWriter, r *http.Request) error {
	p, _, err := tenant(r)
	if err != nil {
		return err
	}
	action := r.PathValue("action")
	if action != ActionDispatch && action != ActionReceive && action != ActionCancel {
		return httpx.NotFound("unknown action %s", action)
	}
	tr, err := s.Transition(r.Context(), p, r.PathValue("id"), action)
	if err != nil {
		return err
	}
	return httpx.OK(w, tr)
}

// ---- re-order, expiry ----

func (s *Service) suggestions(w http.ResponseWriter, r *http.Request) error {
	p, t, err := tenant(r)
	if err != nil {
		return err
	}
	store, err := storeFromQuery(r, p)
	if err != nil {
		return err
	}
	sugg, err := s.Suggestions(r.Context(), t, store)
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"store_id": store, "items": sugg})
}

func (s *Service) purchaseDraft(w http.ResponseWriter, r *http.Request) error {
	p, t, err := tenant(r)
	if err != nil {
		return err
	}
	var in PurchaseDraftInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	in.StoreID, err = storeFor(p, in.StoreID)
	if err != nil {
		return err
	}
	drafts, err := s.PurchaseDrafts(r.Context(), t, in)
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"drafts": drafts})
}

func (s *Service) expiring(w http.ResponseWriter, r *http.Request) error {
	p, t, err := tenant(r)
	if err != nil {
		return err
	}
	store, err := optionalStore(r, p)
	if err != nil {
		return err
	}
	days := intQuery(r, "days", 30)
	rows, err := s.Expiring(r.Context(), t, store, days)
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"days": days, "items": rows})
}

func (s *Service) returnToVendor(w http.ResponseWriter, r *http.Request) error {
	p, t, err := tenant(r)
	if err != nil {
		return err
	}
	var in ReturnToVendorInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	store, err := storeFor(p, in.StoreID)
	if err != nil {
		return err
	}
	body, err := s.ReturnToVendor(r.Context(), t, r.PathValue("id"), store, in)
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"draft": body})
}

// ---- analytics ----

func (s *Service) deadStock(w http.ResponseWriter, r *http.Request) error {
	p, t, err := tenant(r)
	if err != nil {
		return err
	}
	store, err := optionalStore(r, p)
	if err != nil {
		return err
	}
	days := intQuery(r, "days", 90)
	rows, err := s.DeadStock(r.Context(), t, store, days)
	if err != nil {
		return err
	}
	var value int64
	for _, x := range rows {
		value += x.ValuePaise
	}
	return httpx.OK(w, map[string]any{"days": days, "items": rows, "total_value_paise": value})
}

func (s *Service) stockAging(w http.ResponseWriter, r *http.Request) error {
	p, t, err := tenant(r)
	if err != nil {
		return err
	}
	store, err := optionalStore(r, p)
	if err != nil {
		return err
	}
	rows, err := s.StockAging(r.Context(), t, store)
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"buckets": rows})
}

func (s *Service) valuation(w http.ResponseWriter, r *http.Request) error {
	p, t, err := tenant(r)
	if err != nil {
		return err
	}
	store, err := optionalStore(r, p)
	if err != nil {
		return err
	}
	v, err := s.Valuate(r.Context(), t, store)
	if err != nil {
		return err
	}
	return httpx.OK(w, v)
}

// ---- internal ----

func (s *Service) lookupItems(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	var in LookupInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	items, err := s.LookupItems(r.Context(), t, in)
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"items": items})
}

type bulkItemsInput struct {
	Items []BulkItem `json:"items"`
}

func (s *Service) bulkItems(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	var in bulkItemsInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	res, err := s.BulkItems(r.Context(), t, in.Items)
	if err != nil {
		return err
	}
	return httpx.OK(w, res)
}

func (s *Service) bulkOpeningStock(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	var in OpeningInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	res, err := s.OpeningStock(r.Context(), t, in)
	if err != nil {
		return err
	}
	return httpx.OK(w, res)
}

func (s *Service) summary(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	items, err := s.Summary(r.Context(), t, httpx.Query(r, "store_id"))
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"items": items})
}

func (s *Service) expiryScan(w http.ResponseWriter, r *http.Request) error {
	n, err := s.ExpiryScan(r.Context())
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"alerts": n})
}

func (s *Service) reorderScan(w http.ResponseWriter, r *http.Request) error {
	n, err := s.ReorderScan(r.Context())
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"alerts": n})
}

func (s *Service) outboxDead(w http.ResponseWriter, r *http.Request) error {
	if s.Relay == nil {
		return httpx.OK(w, map[string]any{"dead": 0})
	}
	n, err := s.Relay.DeadCount(r.Context())
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"dead": n})
}

func (s *Service) outboxReplay(w http.ResponseWriter, r *http.Request) error {
	if s.Relay == nil {
		return httpx.OK(w, map[string]any{"replayed": 0})
	}
	n, err := s.Relay.ReplayDead(r.Context(), httpx.Query(r, "id"))
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"replayed": n})
}
