package inventory

import (
	"net/http"

	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/httpx"
)

// marketRoutes registers the Phase 5 API: pricing, serials, variants, stock
// counts, bins, supplier mapping, scale formats, HSN suggestions, import rollback.
func (s *Service) marketRoutes(mux *http.ServeMux, v *auth.Verifier) {
	read := func(h httpx.HandlerFunc) http.Handler { return v.User(anyOf(h, readPerms...)) }
	manage := func(h httpx.HandlerFunc) http.Handler { return v.User(anyOf(h, auth.PermItemManage)) }
	stock := func(h httpx.HandlerFunc) http.Handler { return v.User(anyOf(h, auth.PermStockManage)) }
	binPerm := func(h httpx.HandlerFunc) http.Handler {
		return v.User(anyOf(h, auth.PermStockManage, auth.PermItemManage))
	}
	buyer := func(h httpx.HandlerFunc) http.Handler {
		return v.User(anyOf(h, auth.PermPurchaseManage, auth.PermItemManage))
	}
	internal := func(h httpx.HandlerFunc) http.Handler { return v.Internal(httpx.H(h)) }

	// Pricing
	mux.Handle("GET /v1/price-lists", read(s.listPriceLists))
	mux.Handle("POST /v1/price-lists", manage(s.createPriceList))
	mux.Handle("GET /v1/price-lists/{id}", read(s.getPriceList))
	mux.Handle("PUT /v1/price-lists/{id}", manage(s.updatePriceList))
	mux.Handle("PUT /v1/price-lists/{id}/items", manage(s.setPriceListItems))
	mux.Handle("PUT /v1/price-lists/{id}/parties", manage(s.setPriceListParties))
	mux.Handle("GET /v1/schemes", read(s.listSchemes))
	mux.Handle("POST /v1/schemes", manage(s.createScheme))
	mux.Handle("GET /v1/schemes/{id}", read(s.getScheme))
	mux.Handle("PUT /v1/schemes/{id}", manage(s.updateScheme))
	mux.Handle("DELETE /v1/schemes/{id}", manage(s.deactivateScheme))
	mux.Handle("POST /v1/pricing/quote", read(s.quote))
	mux.Handle("POST /internal/v1/pricing/quote", internal(s.quote))

	// Serials
	mux.Handle("GET /v1/serials", read(s.listSerials))
	mux.Handle("GET /v1/serials/{serial}", read(s.lookupSerial))
	mux.Handle("POST /v1/serials/validate", read(s.validateSerials))
	mux.Handle("POST /internal/v1/serials/validate", internal(s.validateSerials))
	mux.Handle("POST /internal/v1/serials/check", internal(s.checkSerials))
	mux.Handle("GET /internal/v1/items/{id}/batches", internal(s.itemBatches))

	// Variants
	mux.Handle("GET /v1/items/{id}/variants", read(s.listVariants))
	mux.Handle("POST /v1/items/{id}/variants", manage(s.createVariants))

	// Stock take
	mux.Handle("GET /v1/stock-counts", read(s.listStockCounts))
	mux.Handle("POST /v1/stock-counts", stock(s.createStockCount))
	mux.Handle("GET /v1/stock-counts/{id}", read(s.getStockCount))
	mux.Handle("POST /v1/stock-counts/{id}/entries", stock(s.countEntries))
	mux.Handle("POST /v1/stock-counts/{id}/post", stock(s.postStockCount))
	mux.Handle("POST /v1/stock-counts/{id}/cancel", stock(s.cancelStockCount))

	// Bins
	mux.Handle("GET /v1/bins", read(s.listBins))
	mux.Handle("POST /v1/bins", binPerm(s.upsertBin))
	mux.Handle("GET /v1/items/{id}/bins", read(s.itemBins))
	mux.Handle("PUT /v1/items/{id}/bins", binPerm(s.setItemBin))

	// Suppliers
	mux.Handle("GET /v1/items/{id}/suppliers", read(s.itemSuppliers))
	mux.Handle("PUT /v1/items/{id}/suppliers/{supplier_id}", buyer(s.setItemSupplier))
	mux.Handle("GET /v1/suppliers/{id}/items", read(s.supplierItems))

	// Scale formats, HSN
	mux.Handle("GET /v1/scale-formats", read(s.scaleFormats))
	mux.Handle("PUT /v1/scale-formats", manage(s.setScaleFormats))
	mux.Handle("GET /v1/hsn/suggest", read(s.suggestHSN))

	// Imports
	mux.Handle("POST /internal/v1/bulk/rollback", internal(s.bulkRollback))
}

// ---- pricing ----

func (s *Service) listPriceLists(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	lists, err := s.PriceLists(r.Context(), t)
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"price_lists": lists})
}

func (s *Service) createPriceList(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	var in PriceListInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	l, err := s.SavePriceList(r.Context(), t, "", in)
	if err != nil {
		return err
	}
	return httpx.Created(w, l)
}

func (s *Service) getPriceList(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	l, err := s.GetPriceList(r.Context(), t, r.PathValue("id"), true)
	if err != nil {
		return err
	}
	return httpx.OK(w, l)
}

func (s *Service) updatePriceList(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	var in PriceListInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	l, err := s.SavePriceList(r.Context(), t, r.PathValue("id"), in)
	if err != nil {
		return err
	}
	return httpx.OK(w, l)
}

func (s *Service) setPriceListItems(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	var in PriceListItemsInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	l, err := s.SetPriceListItems(r.Context(), t, r.PathValue("id"), in)
	if err != nil {
		return err
	}
	return httpx.OK(w, l)
}

func (s *Service) setPriceListParties(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	var in struct {
		PartyIDs []string `json:"party_ids"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	l, err := s.SetPriceListParties(r.Context(), t, r.PathValue("id"), in.PartyIDs)
	if err != nil {
		return err
	}
	return httpx.OK(w, l)
}

func (s *Service) listSchemes(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	list, err := s.Schemes(r.Context(), t, httpx.Query(r, "active_on"))
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"schemes": list})
}

func (s *Service) createScheme(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	var in SchemeInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	x, err := s.SaveScheme(r.Context(), t, "", in)
	if err != nil {
		return err
	}
	return httpx.Created(w, x)
}

func (s *Service) getScheme(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	x, err := s.GetScheme(r.Context(), t, r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.OK(w, x)
}

func (s *Service) updateScheme(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	var in SchemeInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	x, err := s.SaveScheme(r.Context(), t, r.PathValue("id"), in)
	if err != nil {
		return err
	}
	return httpx.OK(w, x)
}

func (s *Service) deactivateScheme(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	if err := s.SetSchemeActive(r.Context(), t, r.PathValue("id"), false); err != nil {
		return err
	}
	return httpx.NoContent(w)
}

func (s *Service) quote(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	var in QuoteInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	q, err := s.Quote(r.Context(), t, in)
	if err != nil {
		return err
	}
	return httpx.OK(w, q)
}

// ---- serials ----

func (s *Service) listSerials(w http.ResponseWriter, r *http.Request) error {
	p, t, err := tenant(r)
	if err != nil {
		return err
	}
	store, err := optionalStore(r, p)
	if err != nil {
		return err
	}
	f := SerialFilter{ItemID: httpx.Query(r, "item_id"), StoreID: store, Status: httpx.Query(r, "status"), Page: httpx.ParsePage(r)}
	rows, total, err := s.ListSerials(r.Context(), t, f)
	if err != nil {
		return err
	}
	return httpx.OK(w, httpx.List[Serial]{Items: rows, Total: total, Limit: f.Limit, Offset: f.Offset})
}

func (s *Service) lookupSerial(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	rows, err := s.LookupSerial(r.Context(), t, r.PathValue("serial"))
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"serials": rows})
}

func (s *Service) validateSerials(w http.ResponseWriter, r *http.Request) error {
	p, t, err := tenant(r)
	if err != nil {
		return err
	}
	var in SerialValidateInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if !p.Internal {
		if in.StoreID, err = storeFor(p, in.StoreID); err != nil {
			return err
		}
	}
	res, err := s.ValidateSerials(r.Context(), t, in)
	if err != nil {
		return err
	}
	return httpx.OK(w, res)
}

// ---- variants ----

func (s *Service) listVariants(w http.ResponseWriter, r *http.Request) error {
	p, t, err := tenant(r)
	if err != nil {
		return err
	}
	store, err := optionalStore(r, p)
	if err != nil {
		return err
	}
	rows, err := s.Variants(r.Context(), t, r.PathValue("id"), store)
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"parent_id": r.PathValue("id"), "variants": rows})
}

func (s *Service) createVariants(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	var in VariantsInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	items, err := s.CreateVariants(r.Context(), t, r.PathValue("id"), in)
	if err != nil {
		return err
	}
	return httpx.Created(w, map[string]any{"variants": items})
}

// ---- stock counts ----

// countForUser loads a count and checks the principal can reach its store.
func (s *Service) countForUser(r *http.Request, p *auth.Principal, tenantID string) error {
	c, err := s.loadCount(r.Context(), s.DB, tenantID, r.PathValue("id"), false)
	if err != nil {
		return err
	}
	if !canAccessStore(p, c.StoreID) {
		return httpx.NotFound("stock count not found")
	}
	return nil
}

func (s *Service) listStockCounts(w http.ResponseWriter, r *http.Request) error {
	p, t, err := tenant(r)
	if err != nil {
		return err
	}
	store, err := optionalStore(r, p)
	if err != nil {
		return err
	}
	f := StockCountFilter{StoreID: store, Status: httpx.Query(r, "status"), Page: httpx.ParsePage(r)}
	rows, err := s.ListStockCounts(r.Context(), t, f)
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"stock_counts": rows})
}

func (s *Service) createStockCount(w http.ResponseWriter, r *http.Request) error {
	p, t, err := tenant(r)
	if err != nil {
		return err
	}
	var in StockCountInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	store, err := storeFor(p, in.StoreID)
	if err != nil {
		return err
	}
	c, err := s.CreateStockCount(r.Context(), t, store, p.UserID(), in)
	if err != nil {
		return err
	}
	return httpx.Created(w, c)
}

func (s *Service) getStockCount(w http.ResponseWriter, r *http.Request) error {
	p, t, err := tenant(r)
	if err != nil {
		return err
	}
	if err := s.countForUser(r, p, t); err != nil {
		return err
	}
	c, err := s.GetStockCount(r.Context(), t, r.PathValue("id"), httpx.Query(r, "only"))
	if err != nil {
		return err
	}
	return httpx.OK(w, c)
}

func (s *Service) countEntries(w http.ResponseWriter, r *http.Request) error {
	p, t, err := tenant(r)
	if err != nil {
		return err
	}
	if err := s.countForUser(r, p, t); err != nil {
		return err
	}
	var in CountEntriesInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	res, err := s.EnterCounts(r.Context(), t, r.PathValue("id"), p.UserID(), in)
	if err != nil {
		return err
	}
	return httpx.OK(w, res)
}

func (s *Service) postStockCount(w http.ResponseWriter, r *http.Request) error {
	p, t, err := tenant(r)
	if err != nil {
		return err
	}
	if err := s.countForUser(r, p, t); err != nil {
		return err
	}
	var in PostCountInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	c, movs, err := s.PostStockCount(r.Context(), t, r.PathValue("id"), p.UserID(), in)
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"stock_count": c, "movements": movs})
}

func (s *Service) cancelStockCount(w http.ResponseWriter, r *http.Request) error {
	p, t, err := tenant(r)
	if err != nil {
		return err
	}
	if err := s.countForUser(r, p, t); err != nil {
		return err
	}
	c, err := s.CancelStockCount(r.Context(), t, r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.OK(w, c)
}

// ---- bins ----

func (s *Service) listBins(w http.ResponseWriter, r *http.Request) error {
	p, t, err := tenant(r)
	if err != nil {
		return err
	}
	store, err := storeFromQuery(r, p)
	if err != nil {
		return err
	}
	bins, err := s.Bins(r.Context(), t, store)
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"store_id": store, "bins": bins})
}

func (s *Service) upsertBin(w http.ResponseWriter, r *http.Request) error {
	p, t, err := tenant(r)
	if err != nil {
		return err
	}
	var in BinInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	store, err := storeFor(p, in.StoreID)
	if err != nil {
		return err
	}
	b, err := s.UpsertBin(r.Context(), t, store, in)
	if err != nil {
		return err
	}
	return httpx.Created(w, b)
}

func (s *Service) itemBins(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	bins, err := s.ItemBins(r.Context(), t, r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"bins": bins})
}

func (s *Service) setItemBin(w http.ResponseWriter, r *http.Request) error {
	p, t, err := tenant(r)
	if err != nil {
		return err
	}
	var in struct {
		StoreID string `json:"store_id,omitempty"`
		BinCode string `json:"bin_code"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	store, err := storeFor(p, in.StoreID)
	if err != nil {
		return err
	}
	if err := s.SetItemBin(r.Context(), t, store, r.PathValue("id"), in.BinCode); err != nil {
		return err
	}
	bins, err := s.ItemBins(r.Context(), t, r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"bins": bins})
}

// ---- suppliers ----

func (s *Service) itemSuppliers(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	rows, err := s.SupplierItems(r.Context(), t, r.PathValue("id"), "")
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"suppliers": rows})
}

func (s *Service) supplierItems(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	rows, err := s.SupplierItems(r.Context(), t, "", r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"items": rows})
}

func (s *Service) setItemSupplier(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	var in SupplierItemInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	m, err := s.SetSupplierItem(r.Context(), t, r.PathValue("id"), r.PathValue("supplier_id"), in)
	if err != nil {
		return err
	}
	return httpx.OK(w, m)
}

// ---- scale formats, HSN ----

func (s *Service) scaleFormats(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	rows, err := s.ScaleFormats(r.Context(), t)
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"formats": rows})
}

func (s *Service) setScaleFormats(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	var in ScaleFormatsInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	rows, err := s.SetScaleFormats(r.Context(), t, in)
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"formats": rows})
}

func (s *Service) suggestHSN(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	res, err := s.SuggestHSN(r.Context(), t, httpx.Query(r, "name"), httpx.Query(r, "code"), httpx.Query(r, "category"), intQuery(r, "limit", 8))
	if err != nil {
		return err
	}
	return httpx.OK(w, res)
}

// ---- imports ----

func (s *Service) bulkRollback(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	var in RollbackInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	res, err := s.Rollback(r.Context(), t, in)
	if err != nil {
		return err
	}
	return httpx.OK(w, res)
}

func (s *Service) checkSerials(w http.ResponseWriter, r *http.Request) error {
	_, t, err := tenant(r)
	if err != nil {
		return err
	}
	var in SerialCheckInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	rows, err := s.CheckSerials(r.Context(), t, in)
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"serials": rows})
}
