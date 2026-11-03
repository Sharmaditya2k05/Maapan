package inventory

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/maapan/bhandar/internal/barcode"
	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/events"
	"github.com/maapan/mool/httpx"
	"github.com/maapan/mool/svc"
)

// httpClient serves s over HTTP for a tenant; internal=true sends the service secret.
type httpClient struct {
	t        *testing.T
	srv      *httptest.Server
	token    string
	tenantID string
}

func newHTTP(t *testing.T, s *Service, tenantID, storeID string, role string) *httpClient {
	t.Helper()
	secret := "test-jwt-secret-that-is-32-chars-long!!"
	v := auth.NewVerifier(secret, "svc-secret")
	mux := http.NewServeMux()
	s.Routes(mux, v, false)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	tok, err := auth.Issue([]byte(secret), auth.Claims{TenantID: tenantID, StoreID: storeID, Role: role, Stores: []string{storeID},
		RegisteredClaims: auth.RegisteredClaimsFor("u1")}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return &httpClient{t: t, srv: srv, token: tok, tenantID: tenantID}
}

func (c *httpClient) do(method, path string, body any, internal bool) (int, []byte) {
	c.t.Helper()
	var rdr io.Reader = strings.NewReader("")
	if body != nil {
		if s, ok := body.(string); ok {
			rdr = strings.NewReader(s)
		} else {
			b, _ := json.Marshal(body)
			rdr = strings.NewReader(string(b))
		}
	}
	req, _ := http.NewRequest(method, c.srv.URL+path, rdr)
	req.Header.Set("Content-Type", "application/json")
	if internal {
		req.Header.Set(auth.HeaderServiceSecret, "svc-secret")
		req.Header.Set(auth.HeaderTenantID, c.tenantID)
	} else {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b
}

func mustItem(t *testing.T, s *Service, tenantID string, in ItemInput) *Item {
	t.Helper()
	it, err := s.CreateItem(context.Background(), tenantID, in)
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func TestPricingQuote(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	tenantID, storeID, party := uuid.NewString(), uuid.NewString(), uuid.NewString()
	soap := mustItem(t, s, tenantID, ItemInput{Name: "Lux Soap", Unit: "PCS", SalePricePaise: 1000, MRPPaise: 1200, TaxRateBP: 1800,
		Category: "Soaps", AltUnits: []AltUnit{{Unit: "BOX", Factor: 12}}})
	oil := mustItem(t, s, tenantID, ItemInput{Name: "Mustard Oil 1L", Unit: "PCS", SalePricePaise: 18000, MRPPaise: 20000, TaxRateBP: 500})

	retail, err := s.SavePriceList(ctx, tenantID, "", PriceListInput{Name: "Retail", Kind: ListRetail, IsDefault: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetPriceListItems(ctx, tenantID, retail.ID, PriceListItemsInput{Items: []PriceListPrice{
		{ItemID: soap.ID, PricePaise: 950, ValidFrom: "2026-10-01"},
	}}); err != nil {
		t.Fatal(err)
	}
	whole, err := s.SavePriceList(ctx, tenantID, "", PriceListInput{Name: "Wholesale", Kind: ListWholesale, IsDefault: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetPriceListItems(ctx, tenantID, whole.ID, PriceListItemsInput{Items: []PriceListPrice{
		{ItemID: soap.ID, Unit: "BOX", PricePaise: 10200},
		{ItemID: soap.ID, PricePaise: 900},
		{ItemID: soap.ID, PricePaise: 880, MinQty: 10},
	}}); err != nil {
		t.Fatal(err)
	}
	// A price above MRP is refused.
	if _, err := s.SetPriceListItems(ctx, tenantID, whole.ID, PriceListItemsInput{Items: []PriceListPrice{{ItemID: soap.ID, PricePaise: 1300}}}); err == nil {
		t.Fatal("price above MRP must be refused")
	}
	partyList, err := s.SavePriceList(ctx, tenantID, "", PriceListInput{Name: "Sharma Traders", Kind: ListParty, PartyIDs: []string{party}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetPriceListItems(ctx, tenantID, partyList.ID, PriceListItemsInput{Items: []PriceListPrice{{ItemID: soap.ID, PricePaise: 870}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SavePriceList(ctx, tenantID, "", PriceListInput{Name: "Retail", Kind: ListRetail}); err == nil || httpx.AsError(err).Status != 409 {
		t.Fatalf("duplicate name: %v", err)
	}

	price := func(in QuoteInput) QuotedLine {
		t.Helper()
		q, err := s.Quote(ctx, tenantID, in)
		if err != nil {
			t.Fatal(err)
		}
		return q.Lines[0]
	}
	if l := price(QuoteInput{Lines: []QuoteLine{{ItemID: soap.ID, Qty: 1}}}); l.UnitPricePaise != 950 || l.PriceListName != "Retail" {
		t.Fatalf("retail: %+v", l)
	}
	if l := price(QuoteInput{Date: "2026-09-15", Lines: []QuoteLine{{ItemID: soap.ID, Qty: 1}}}); l.UnitPricePaise != 1000 || l.PriceSource != "item" {
		t.Fatalf("before the retail price is effective: %+v", l)
	}
	if l := price(QuoteInput{Channel: ListWholesale, Lines: []QuoteLine{{ItemID: soap.ID, Qty: 2, Unit: "BOX"}}}); l.UnitPricePaise != 10200 || l.BaseQty != 24 {
		t.Fatalf("wholesale per BOX: %+v", l)
	}
	if l := price(QuoteInput{Channel: ListWholesale, Lines: []QuoteLine{{ItemID: soap.ID, Qty: 12}}}); l.UnitPricePaise != 880 {
		t.Fatalf("wholesale slab ≥10: %+v", l)
	}
	if l := price(QuoteInput{PartyID: party, Lines: []QuoteLine{{ItemID: soap.ID, Qty: 1, Unit: "BOX"}}}); l.UnitPricePaise != 870*12 || l.PriceListID != partyList.ID {
		t.Fatalf("party list in BOX via base price: %+v", l)
	}
	if l := price(QuoteInput{Lines: []QuoteLine{{ItemID: oil.ID, Qty: 1}}}); l.UnitPricePaise != 18000 || l.PriceSource != "item" {
		t.Fatalf("no list price: %+v", l)
	}

	// Schemes: % slabs on the category, buy 10 get 1 on oil, an expired flat scheme.
	if _, err := s.SaveScheme(ctx, tenantID, "", SchemeInput{Name: "Soap fest", Kind: SchemePercent, Category: "Soaps", ValidFrom: "2026-10-01",
		ValidTo: "2026-10-31", Slabs: []Slab{{MinQty: 10, PercentBP: 500}, {MinQty: 24, PercentBP: 1000}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveScheme(ctx, tenantID, "", SchemeInput{Name: "Oil 10+1", Kind: SchemeFreeQty, ItemIDs: []string{oil.ID}, BuyQty: 10, FreeQty: 1,
		ValidFrom: "2026-10-01"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveScheme(ctx, tenantID, "", SchemeInput{Name: "Old flat", Kind: SchemeFlat, ItemIDs: []string{soap.ID}, ValidFrom: "2026-01-01",
		ValidTo: "2026-06-30", Slabs: []Slab{{MinQty: 1, OffPerUnitPaise: 500}}}); err != nil {
		t.Fatal(err)
	}
	vip, err := s.SaveScheme(ctx, tenantID, "", SchemeInput{Name: "Party flat", Kind: SchemeFlat, ItemIDs: []string{soap.ID}, ValidFrom: "2026-10-01",
		PartyIDs: []string{party}, Slabs: []Slab{{MinQty: 1, OffPerUnitPaise: 200}}})
	if err != nil {
		t.Fatal(err)
	}
	q, err := s.Quote(ctx, tenantID, QuoteInput{Lines: []QuoteLine{{ItemID: soap.ID, Qty: 2, Unit: "BOX"}, {ItemID: oil.ID, Qty: 25}}})
	if err != nil {
		t.Fatal(err)
	}
	if l := q.Lines[0]; l.GrossPaise != 22800 || l.DiscountPaise != 2280 || l.SchemeName != "Soap fest" || l.SchemePercentBP != 1000 {
		t.Fatalf("percent scheme on 24 base units: %+v", l)
	}
	if len(q.FreeItems) != 1 || q.FreeItems[0].Qty != 2 || q.FreeItems[0].ItemID != oil.ID || q.FreeItems[0].ForLineNo != 2 {
		t.Fatalf("free items: %+v", q.FreeItems)
	}
	if q.NetPaise != 22800-2280+25*18000 {
		t.Fatalf("net: %d", q.NetPaise)
	}
	// For the party the ₹2/unit flat (24 × 200 = 4800 on 870 × 24) beats 10 % (2088).
	q, err = s.Quote(ctx, tenantID, QuoteInput{PartyID: party, Lines: []QuoteLine{{ItemID: soap.ID, Qty: 24}}})
	if err != nil {
		t.Fatal(err)
	}
	if l := q.Lines[0]; l.SchemeID != vip.ID || l.DiscountPaise != 4800 {
		t.Fatalf("best scheme for the party: %+v", l)
	}
	if list, err := s.Schemes(ctx, tenantID, "2026-10-23"); err != nil || len(list) != 3 {
		t.Fatalf("schemes in force: %d %v", len(list), err)
	}

	// aadesh's call: internal quote with the service secret.
	c := newHTTP(t, s, tenantID, storeID, auth.RoleCashier)
	code, body := c.do("POST", "/internal/v1/pricing/quote", map[string]any{"party_id": party, "lines": []map[string]any{{"line_no": 7, "item_id": soap.ID, "qty": 1}}}, true)
	if code != 200 || !strings.Contains(string(body), `"unit_price_paise":870`) || !strings.Contains(string(body), `"line_no":7`) {
		t.Fatalf("internal quote: %d %s", code, body)
	}
	if code, _ = c.do("POST", "/v1/price-lists", PriceListInput{Name: "X"}, false); code != 403 {
		t.Fatalf("cashier creating a price list: %d", code)
	}
	if code, body = c.do("GET", "/v1/price-lists/"+whole.ID, nil, false); code != 200 || !strings.Contains(string(body), `"unit":"BOX"`) {
		t.Fatalf("get price list: %d %s", code, body)
	}
}

// purchaseWithSerials builds an invoice event whose lines carry serial_nos.
func invoiceWithSerials(t *testing.T, tenantID, storeID string, p invoiceEvent) events.Envelope {
	t.Helper()
	return envelope(t, events.InvoiceCreated, tenantID, storeID, p)
}

func TestSerialNumbers(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	tenantID, storeA, storeB, supplier, customer := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	router := s.Router()
	phone := mustItem(t, s, tenantID, ItemInput{Name: "Redmi 13", SalePricePaise: 1200000, MRPPaise: 1300000, TaxRateBP: 1800, TrackSerials: true})
	if !phone.TrackSerials {
		t.Fatal("track_serials not stored")
	}

	v, err := s.ValidateSerials(ctx, tenantID, SerialValidateInput{StoreID: storeA, DocType: "purchase", Lines: []SerialValidateLine{
		{LineNo: 1, ItemID: phone.ID, Qty: 2, SerialNos: []string{"imei-1"}},
	}})
	if err != nil || v.OK || len(v.Errors) != 1 || !strings.Contains(v.Errors[0].Message, "needs 2") {
		t.Fatalf("count mismatch: %+v %v", v, err)
	}

	purchaseID := uuid.NewString()
	pur := invoiceEvent{InvoicePayload: events.InvoicePayload{InvoiceID: purchaseID, StoreID: storeA, DocType: "purchase", Number: "PUR/1",
		InvoiceDate: "2026-10-20", PartyID: supplier}}
	pur.Lines = []invoiceLine{{InvoiceLine: events.InvoiceLine{LineNo: 1, ItemID: phone.ID, Qty: 2, TaxablePaise: 2000000}, SerialNos: []string{"imei-1", " IMEI-2 "}}}
	if err := router[events.InvoiceCreated](ctx, invoiceWithSerials(t, tenantID, storeA, pur)); err != nil {
		t.Fatal(err)
	}
	got, err := s.LookupSerial(ctx, tenantID, "imei-2")
	if err != nil || len(got) != 1 || got[0].Status != SerialInStock || got[0].StoreID != storeA || got[0].SupplierID != supplier || len(got[0].History) != 1 {
		t.Fatalf("after purchase: %+v %v", got, err)
	}
	// Supplier mapping recorded the last purchase price per unit.
	if m, err := s.SupplierItems(ctx, tenantID, phone.ID, ""); err != nil || len(m) != 1 || *m[0].LastPricePaise != 1000000 || m[0].LastInvoiceNo != "PUR/1" {
		t.Fatalf("supplier mapping: %+v %v", m, err)
	}

	// Validation of a sale.
	v, _ = s.ValidateSerials(ctx, tenantID, SerialValidateInput{StoreID: storeB, DocType: "sale", Lines: []SerialValidateLine{
		{LineNo: 1, ItemID: phone.ID, Qty: 1, SerialNos: []string{"IMEI-1"}}}})
	if v.OK || !strings.Contains(v.Errors[0].Message, "another store") {
		t.Fatalf("sale at the wrong store: %+v", v)
	}
	v, _ = s.ValidateSerials(ctx, tenantID, SerialValidateInput{StoreID: storeA, DocType: "sale", Lines: []SerialValidateLine{
		{LineNo: 1, ItemID: phone.ID, Qty: 2, SerialNos: []string{"IMEI-1", "IMEI-9"}}}})
	if v.OK || len(v.Errors) != 1 || v.Errors[0].SerialNo != "IMEI-9" {
		t.Fatalf("unknown serial: %+v", v)
	}
	v, _ = s.ValidateSerials(ctx, tenantID, SerialValidateInput{StoreID: storeA, DocType: "purchase", Lines: []SerialValidateLine{
		{LineNo: 1, ItemID: phone.ID, Qty: 1, SerialNos: []string{"IMEI-1"}}}})
	if v.OK {
		t.Fatal("purchasing a serial already in stock must fail validation")
	}

	// Sale, then its cancellation restores the serial.
	saleID := uuid.NewString()
	sale := invoiceEvent{InvoicePayload: events.InvoicePayload{InvoiceID: saleID, StoreID: storeA, DocType: "sale", Number: "INV/1", PartyID: customer}}
	sale.Lines = []invoiceLine{{InvoiceLine: events.InvoiceLine{LineNo: 1, ItemID: phone.ID, Qty: 1}, SerialNos: []string{"IMEI-1"}}}
	if err := router[events.InvoiceCreated](ctx, invoiceWithSerials(t, tenantID, storeA, sale)); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.LookupSerial(ctx, tenantID, "IMEI-1"); got[0].Status != SerialSold || got[0].PartyID != customer {
		t.Fatalf("after sale: %+v", got[0])
	}
	v, _ = s.ValidateSerials(ctx, tenantID, SerialValidateInput{StoreID: storeA, DocType: "sale_return", Lines: []SerialValidateLine{
		{LineNo: 1, ItemID: phone.ID, Qty: 1, SerialNos: []string{"IMEI-1"}}}})
	if !v.OK {
		t.Fatalf("sale return of a sold serial: %+v", v)
	}
	if list, total, err := s.ListSerials(ctx, tenantID, SerialFilter{ItemID: phone.ID, StoreID: storeA, Page: httpx.Page{Limit: 10}}); err != nil || total != 1 || list[0].SerialNo != "IMEI-2" {
		t.Fatalf("in stock: %+v %d %v", list, total, err)
	}
	if err := router[events.InvoiceCancelled](ctx, envelope(t, events.InvoiceCancelled, tenantID, storeA, events.InvoicePayload{InvoiceID: saleID, Number: "INV/1"})); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.LookupSerial(ctx, tenantID, "IMEI-1"); got[0].Status != SerialInStock || got[0].StoreID != storeA || !got[0].History[1].Reverted {
		t.Fatalf("after cancelling the sale: %+v", got[0])
	}
	// Cancelling the purchase removes serials it introduced.
	if err := router[events.InvoiceCancelled](ctx, envelope(t, events.InvoiceCancelled, tenantID, storeA, events.InvoicePayload{InvoiceID: purchaseID, Number: "PUR/1"})); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupSerial(ctx, tenantID, "IMEI-2"); httpx.AsError(err).Status != 404 {
		t.Fatalf("serial of a cancelled purchase must disappear: %v", err)
	}

	// Adjustment: a found unit comes in, then is written off.
	if _, err := s.Adjust(ctx, tenantID, storeA, "u1", AdjustmentInput{Note: "found in back room", Lines: []AdjustmentLine{
		{ItemID: phone.ID, Qty: 1, UnitCostPaise: 1000000, SerialNos: []string{"IMEI-3"}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Adjust(ctx, tenantID, storeA, "u1", AdjustmentInput{Note: "damaged", Lines: []AdjustmentLine{
		{ItemID: phone.ID, Qty: -1, SerialNos: []string{"imei-3"}}}}); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.LookupSerial(ctx, tenantID, "IMEI-3"); got[0].Status != SerialWrittenOff || len(got[0].History) != 2 {
		t.Fatalf("after write-off: %+v", got[0])
	}

	// Over HTTP: the internal validate and the lookup.
	c := newHTTP(t, s, tenantID, storeA, auth.RoleCashier)
	code, body := c.do("POST", "/internal/v1/serials/validate", map[string]any{"store_id": storeA, "doc_type": "purchase", // IMEI-1 left with its cancelled purchase
		"lines": []map[string]any{{"line_no": 1, "item_id": phone.ID, "qty": 1, "serial_nos": []string{"IMEI-1"}}}}, true)
	if code != 200 || !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("internal validate: %d %s", code, body)
	}
	if code, body = c.do("GET", "/v1/serials/imei-3", nil, false); code != 200 || !strings.Contains(string(body), "Redmi 13") {
		t.Fatalf("serial lookup: %d %s", code, body)
	}
}

func TestItemVariants(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	tenantID := uuid.NewString()
	tee := mustItem(t, s, tenantID, ItemInput{Name: "Polo T-shirt", HSN: "6105", SalePricePaise: 59900, MRPPaise: 79900, TaxRateBP: 500,
		Category: "Apparel", Brand: "Acme"})
	vs, err := s.CreateVariants(ctx, tenantID, tee.ID, VariantsInput{Variants: []VariantInput{
		{Attrs: Attrs{"size": "M", "colour": "Red"}, SKU: "POLO-R-M"},
		{Attrs: Attrs{"Size": "L", "Color": "Red"}, SalePricePaise: 64900},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 2 || vs[0].Name != "Polo T-shirt - Red / M" || vs[0].ParentID != tee.ID || vs[0].HSN != "6105" || vs[0].Barcode == "" ||
		vs[1].SalePricePaise != 64900 || vs[1].VariantAttrs["colour"] != "Red" || vs[0].SalePricePaise != 59900 {
		t.Fatalf("variants: %+v", vs)
	}
	if _, err := s.CreateVariants(ctx, tenantID, tee.ID, VariantsInput{Variants: []VariantInput{{Attrs: Attrs{"colour": "red", "size": "m"}}}}); err == nil || httpx.AsError(err).Status != 409 {
		t.Fatalf("duplicate variant: %v", err)
	}
	if _, err := s.CreateVariants(ctx, tenantID, vs[0].ID, VariantsInput{Variants: []VariantInput{{Attrs: Attrs{"fit": "slim"}}}}); err == nil {
		t.Fatal("a variant cannot have variants")
	}
	rows, err := s.Variants(ctx, tenantID, tee.ID, "")
	if err != nil || len(rows) != 2 {
		t.Fatalf("list variants: %+v %v", rows, err)
	}
	// The variant is a normal item: its own barcode scans to it.
	if lk, err := s.LookupByCode(ctx, tenantID, "POLO-R-M"); err != nil || lk.Item.ID != vs[0].ID {
		t.Fatalf("scan a variant: %+v %v", lk, err)
	}
}

func TestStockCountAndBins(t *testing.T) {
	s, pool := newTestService(t)
	ctx := context.Background()
	tenantID, storeID := uuid.NewString(), uuid.NewString()
	router := s.Router()
	rice := mustItem(t, s, tenantID, ItemInput{Name: "Basmati Rice", Unit: "KG", SalePricePaise: 12000, TaxRateBP: 500, PLU: "123", Category: "Grocery"})
	salt := mustItem(t, s, tenantID, ItemInput{Name: "Tata Salt", SalePricePaise: 2800, TaxRateBP: 0, Category: "Grocery"})
	soap := mustItem(t, s, tenantID, ItemInput{Name: "Lux", SalePricePaise: 1000, TaxRateBP: 1800, Category: "Soaps"})
	if _, err := s.OpeningStock(ctx, tenantID, OpeningInput{StoreID: storeID, Lines: []OpeningLine{
		{ItemID: rice.ID, Qty: 20, UnitCostPaise: 9000}, {ItemID: salt.ID, Qty: 10, UnitCostPaise: 2200}, {ItemID: soap.ID, Qty: 5, UnitCostPaise: 800},
	}}); err != nil {
		t.Fatal(err)
	}
	// Bins.
	if _, err := s.UpsertBin(ctx, tenantID, storeID, BinInput{Code: "a-01", Name: "Aisle A rack 1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetItemBin(ctx, tenantID, storeID, salt.ID, "A-01"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetItemBin(ctx, tenantID, storeID, soap.ID, "Z-99"); err == nil || httpx.AsError(err).Status != 422 {
		t.Fatalf("unknown bin: %v", err)
	}
	if bins, err := s.Bins(ctx, tenantID, storeID); err != nil || len(bins) != 1 || bins[0].Items != 1 {
		t.Fatalf("bins: %+v %v", bins, err)
	}

	// Cycle count of the Grocery category.
	c, err := s.CreateStockCount(ctx, tenantID, storeID, "u1", StockCountInput{Category: "Grocery"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Totals.Lines != 2 || c.Totals.Uncounted != 2 {
		t.Fatalf("snapshot: %+v", c.Totals)
	}
	if _, err := s.CreateStockCount(ctx, tenantID, storeID, "u1", StockCountInput{Category: "Grocery"}); err == nil {
		t.Fatal("a second open count with the same scope must be refused")
	}
	// Scans: salt by barcode three times, then keyed 8; rice by a weighing-scale label (2 kg 500 g).
	scale := withScaleCode(t, "27", "00123", "02500")
	res, err := s.EnterCounts(ctx, tenantID, c.ID, "u2", CountEntriesInput{Entries: []CountEntry{
		{Code: salt.Barcode}, {Code: salt.Barcode}, {Code: salt.Barcode},
		{Code: scale}, {ItemID: rice.ID, Qty: ptr(15.0)},
		{Code: "9999999999999"}, {Code: soap.Barcode},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied != 5 || len(res.Errors) != 2 || res.Errors[0].Index != 5 || !strings.Contains(res.Errors[1].Message, "outside") {
		t.Fatalf("entries: %+v", res)
	}
	if _, err := s.EnterCounts(ctx, tenantID, c.ID, "u2", CountEntriesInput{Entries: []CountEntry{{ItemID: salt.ID, Qty: ptr(8.0), Mode: "set"}}}); err != nil {
		t.Fatal(err)
	}
	// A sale of 2 salt while counting: posting must keep it (variance is counted − frozen).
	sale := events.InvoicePayload{InvoiceID: uuid.NewString(), StoreID: storeID, DocType: "sale", Number: "INV/9",
		Lines: []events.InvoiceLine{{LineNo: 1, ItemID: salt.ID, Qty: 2}}}
	if err := router[events.InvoiceCreated](ctx, envelope(t, events.InvoiceCreated, tenantID, storeID, sale)); err != nil {
		t.Fatal(err)
	}
	rep, err := s.GetStockCount(ctx, tenantID, c.ID, "variance")
	if err != nil {
		t.Fatal(err)
	}
	// salt 8 vs 10 → −2 × 2200; rice 17.5 vs 20 → −2.5 × 9000.
	if rep.Totals.WithVariance != 2 || rep.Totals.ShortagePaise != 4400+22500 || len(rep.Lines) != 2 {
		t.Fatalf("variance: %+v %+v", rep.Totals, rep.Lines)
	}
	for _, l := range rep.Lines {
		if l.ItemID == salt.ID && l.BinCode != "A-01" {
			t.Fatalf("bin on the count sheet: %+v", l)
		}
	}
	before := outboxCount(t, pool, events.StockMoved)
	posted, movs, err := s.PostStockCount(ctx, tenantID, c.ID, "u1", PostCountInput{Note: "October cycle count"})
	if err != nil {
		t.Fatal(err)
	}
	if posted.Status != CountPosted || len(movs) != 2 || movs[0].Source != SourceStockCount || movs[0].Reason != ReasonAdjustment {
		t.Fatalf("posted: %+v %+v", posted, movs)
	}
	if outboxCount(t, pool, events.StockMoved) != before+1 {
		t.Fatal("posting must emit one bhandar.stock.moved")
	}
	if b := balance(t, s, tenantID, storeID, salt.ID); b.Qty != 6 {
		t.Fatalf("salt after count: %+v (10 − 2 sold − 2 short)", b)
	}
	if b := balance(t, s, tenantID, storeID, rice.ID); b.Qty != 17.5 {
		t.Fatalf("rice after count: %+v", b)
	}
	if _, _, err := s.PostStockCount(ctx, tenantID, c.ID, "u1", PostCountInput{}); err == nil || httpx.AsError(err).Status != 409 {
		t.Fatalf("posting twice: %v", err)
	}

	// A full count of the soap bin-less store with uncounted=zero writes off what was not found.
	full, err := s.CreateStockCount(ctx, tenantID, storeID, "u1", StockCountInput{ItemIDs: []string{soap.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PostStockCount(ctx, tenantID, full.ID, "u1", PostCountInput{Uncounted: "zero"}); err != nil {
		t.Fatal(err)
	}
	if b := balance(t, s, tenantID, storeID, soap.ID); b.Qty != 0 {
		t.Fatalf("uncounted soap must be zeroed: %+v", b)
	}

	// Over HTTP: a cashier cannot count; a manager can scan.
	cashier := newHTTP(t, s, tenantID, storeID, auth.RoleCashier)
	if code, _ := cashier.do("POST", "/v1/stock-counts", map[string]any{}, false); code != 403 {
		t.Fatalf("cashier creating a count: %d", code)
	}
}

func withScaleCode(t *testing.T, prefix, plu, value string) string {
	t.Helper()
	first12 := prefix + plu + value
	c, err := barcode.EAN13CheckDigit(first12)
	if err != nil {
		t.Fatal(err)
	}
	return first12 + string(c)
}

func ptr[T any](v T) *T { return &v }

func TestScaleFormatsLookup(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	tenantID := uuid.NewString()
	apples := mustItem(t, s, tenantID, ItemInput{Name: "Apples", Unit: "KG", SalePricePaise: 20000, TaxRateBP: 0, PLU: "123"})
	sweets := mustItem(t, s, tenantID, ItemInput{Name: "Kaju Katli", Unit: "KG", SalePricePaise: 100000, TaxRateBP: 500, PLU: "000456"})

	// Default layout: 27 = weight in grams, 5-digit PLU (leading zeros ignored).
	lk, err := s.LookupByCode(ctx, tenantID, withScaleCode(t, "27", "00123", "01250"))
	if err != nil || lk.Item.ID != apples.ID || lk.Scale.Kind != "weight" || lk.Scale.Value != 1250 || lk.Scale.Qty != 1.25 {
		t.Fatalf("default weight: %+v %v", lk, err)
	}
	// Configure 22: 6-digit PLU, 4-digit price in rupees with 1 decimal.
	if _, err := s.SetScaleFormats(ctx, tenantID, ScaleFormatsInput{Formats: []barcode.ScaleFormat{
		{Prefix: "22", Kind: "price", PLULen: 6, ValueDecimals: 1},
		{Prefix: "27", Kind: "none"},
	}}); err != nil {
		t.Fatal(err)
	}
	lk, err = s.LookupByCode(ctx, tenantID, withScaleCode(t, "22", "000456", "2500"))
	if err != nil || lk.Item.ID != sweets.ID || lk.Scale.Kind != "price" || lk.Scale.Value != 25000 || lk.Scale.Qty != 0.25 {
		t.Fatalf("configured price layout: %+v %v", lk, err)
	}
	if _, err := s.LookupByCode(ctx, tenantID, withScaleCode(t, "27", "00123", "01250")); httpx.AsError(err).Status != 404 {
		t.Fatalf("prefix 27 switched off: %v", err)
	}
	rows, err := s.ScaleFormats(ctx, tenantID)
	if err != nil || len(rows) != 10 || !rows[2].Configured || rows[3].Configured || rows[3].Kind != "price" {
		t.Fatalf("formats: %+v %v", rows, err)
	}
	if _, err := s.SetScaleFormats(ctx, tenantID, ScaleFormatsInput{Formats: []barcode.ScaleFormat{{Prefix: "22", Kind: "price", PLULen: 6, CheckDigit: true, ValueDecimals: 2}}}); err == nil {
		t.Fatal("a layout with 3 value digits must be refused")
	}
}

func TestSupplierMappingAndReorderPrice(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	tenantID, storeID, sup := uuid.NewString(), uuid.NewString(), uuid.NewString()
	router := s.Router()
	it := mustItem(t, s, tenantID, ItemInput{Name: "Atta 5kg", SalePricePaise: 30000, PurchasePricePaise: 25000, TaxRateBP: 500,
		ReorderLevel: 20, ReorderQty: 40, AltUnits: []AltUnit{{Unit: "BAG", Factor: 10}}})
	post := func(date string, qty float64, unit string, taxable int64, no string) {
		t.Helper()
		p := events.InvoicePayload{InvoiceID: uuid.NewString(), StoreID: storeID, DocType: "purchase", Number: no, InvoiceDate: date, PartyID: sup,
			Lines: []events.InvoiceLine{{LineNo: 1, ItemID: it.ID, Unit: unit, Qty: qty, TaxablePaise: taxable}}}
		if err := router[events.InvoiceCreated](ctx, envelope(t, events.InvoiceCreated, tenantID, storeID, p)); err != nil {
			t.Fatal(err)
		}
	}
	post("2026-10-10", 1, "BAG", 240000, "P-1") // 10 × ₹240
	post("2026-10-01", 5, "", 125000, "P-0")    // older bill arriving late: ₹250, must not win
	m, err := s.SupplierItems(ctx, tenantID, it.ID, "")
	if err != nil || len(m) != 1 || *m[0].LastPricePaise != 24000 || m[0].LastInvoiceNo != "P-1" || m[0].LastPurchaseDate != "2026-10-10" ||
		m[0].Purchases != 2 || *m[0].LastQty != 10 {
		t.Fatalf("mapping: %+v %v", m, err)
	}
	lead := 3
	if _, err := s.SetSupplierItem(ctx, tenantID, it.ID, sup, SupplierItemInput{SupplierSKU: "ATTA-5", LeadTimeDays: &lead, Preferred: true}); err != nil {
		t.Fatal(err)
	}
	if rows, err := s.SupplierItems(ctx, tenantID, "", sup); err != nil || len(rows) != 1 || rows[0].SupplierSKU != "ATTA-5" || !rows[0].Preferred {
		t.Fatalf("supplier's items: %+v %v", rows, err)
	}
	sugg, err := s.Suggestions(ctx, tenantID, storeID)
	if err != nil || len(sugg) != 1 || sugg[0].PurchasePricePaise != 24000 || sugg[0].PreferredSupplierID != sup {
		t.Fatalf("re-order uses the supplier's last price: %+v %v", sugg, err)
	}
}

func TestHSNSuggest(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	tenantID := uuid.NewString()
	mustItem(t, s, tenantID, ItemInput{Name: "Parle-G Glucose Biscuit 100g", HSN: "19053100", TaxRateBP: 1800})
	mustItem(t, s, tenantID, ItemInput{Name: "Marie Biscuit", HSN: "19053100", TaxRateBP: 1800})

	// Without kar: suggestions from the tenant's own items.
	sg, err := s.SuggestHSN(ctx, tenantID, "Good Day Biscuit", "", "", 5)
	if err != nil || len(sg.Suggestions) != 1 || sg.Suggestions[0].Code != "19053100" || sg.Suggestions[0].Uses != 2 || sg.Kar != "unavailable" {
		t.Fatalf("local: %+v %v", sg, err)
	}

	// A fake kar: the dated code lookup exists, the search does not (yet).
	var gotTenant string
	kar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTenant = r.Header.Get(auth.HeaderTenantID)
		switch {
		case r.URL.Path == "/internal/v1/hsn/19053100":
			_, _ = w.Write([]byte(`{"code":"19053100","description":"Sweet biscuits","kind":"goods","rate_bp":500,"cess_bp":0,"valid_from":"2025-09-22","source":"seed","verified":true,"on":"2026-10-23","rates":[]}`))
		case strings.HasPrefix(r.URL.Path, "/internal/v1/hsn/"):
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"HSN/SAC code not in master"}}`))
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"not found"}}`))
		}
	}))
	defer kar.Close()
	s.Kar = svc.New(kar.URL, "svc-secret", false)
	sg, err = s.SuggestHSN(ctx, tenantID, "Good Day Biscuit", "", "", 5)
	if err != nil || sg.Kar != "search_unsupported" || len(sg.Suggestions) != 1 || sg.Suggestions[0].RateBP == nil || *sg.Suggestions[0].RateBP != 500 ||
		sg.Suggestions[0].Description != "Sweet biscuits" || gotTenant != tenantID {
		t.Fatalf("with kar: %+v %v (tenant %q)", sg, err, gotTenant)
	}
	// auto_hsn on create: HSN from the suggestion, rate from kar when sent as 0.
	it := mustItem(t, s, tenantID, ItemInput{Name: "Hide & Seek Biscuit", AutoHSN: true})
	if it.HSN != "19053100" || it.TaxRateBP != 500 {
		t.Fatalf("auto hsn: hsn %q rate %d", it.HSN, it.TaxRateBP)
	}
	// A code kar does not know is still suggested from the items, without a rate.
	if sg, err = s.SuggestHSN(ctx, tenantID, "", "1905", "", 5); err != nil || len(sg.Suggestions) != 1 {
		t.Fatalf("by code prefix: %+v %v", sg, err)
	}
}

func TestImportExtrasAndRollback(t *testing.T) {
	s, pool := newTestService(t)
	ctx := context.Background()
	tenantID, storeID := uuid.NewString(), uuid.NewString()
	mrp := int64(5500)
	existing := mustItem(t, s, tenantID, ItemInput{Name: "Old item", SKU: "OLD1", TaxRateBP: 500})

	res, err := s.BulkItems(ctx, tenantID, []BulkItem{
		{Name: "Dettol 125ml", SKU: "DET125", Unit: "PCS", SalePricePaise: 5000, MRPPaise: 5500, TaxRateBP: 1800, TrackBatches: true,
			ExternalRef: "T-1", AltUnits: []AltUnit{{Unit: "box", Factor: 24}}},
		{Name: "Old item renamed", SKU: "OLD1", Unit: "PCS", TaxRateBP: 500, ExternalRef: "T-2"},
	})
	if err != nil || res.Created != 1 || res.Updated != 1 || len(res.Results) != 2 || !res.Results[0].Created || res.Results[1].Created ||
		res.Results[1].ID != existing.ID {
		t.Fatalf("bulk items: %+v %v", res, err)
	}
	det, err := s.GetItem(ctx, tenantID, res.Results[0].ID)
	if err != nil || len(det.AltUnits) != 1 || det.AltUnits[0].Unit != "BOX" || det.AltUnits[0].Factor != 24 {
		t.Fatalf("alt units: %+v %v", det, err)
	}
	op, err := s.OpeningStock(ctx, tenantID, OpeningInput{StoreID: storeID, AsOf: "2026-04-01", Lines: []OpeningLine{
		{ItemExternalRef: "T-1", Qty: 48, UnitCostPaise: 4000, BatchNo: "B1", MfgDate: "2026-01-10", ExpiryDate: "2028-01-09", MRPPaise: &mrp},
	}})
	if err != nil || op.Created != 1 || len(op.Results) != 1 || !op.Results[0].Created {
		t.Fatalf("opening: %+v %v", op, err)
	}
	batches, _ := s.ItemBatches(ctx, tenantID, det.ID, storeID)
	if len(batches) != 1 || batches[0].MfgDate != "2026-01-10" || batches[0].MRPPaise == nil || *batches[0].MRPPaise != 5500 || batches[0].Qty != 48 {
		t.Fatalf("batch extras: %+v", batches)
	}
	if op, _ = s.OpeningStock(ctx, tenantID, OpeningInput{StoreID: storeID, Lines: []OpeningLine{{ItemExternalRef: "T-1", Qty: 48, BatchNo: "B1"}}}); op.Updated != 1 || op.Results[0].Created {
		t.Fatalf("re-sent opening is a no-op: %+v", op)
	}

	// Rollback over HTTP with exactly what aayaat sends: opening stock first, then the items.
	c := newHTTP(t, s, tenantID, storeID, auth.RoleCashier)
	tr, fa := true, false
	jobID := uuid.NewString()
	before := outboxCount(t, pool, events.StockMoved)
	code, body := c.do("POST", "/internal/v1/bulk/rollback", map[string]any{"kind": "opening_stock", "job_id": jobID, "as_of": "2026-04-01",
		"records": []map[string]any{{"item_external_ref": "T-1", "batch_no": "B1", "store_id": storeID, "qty": 48, "created": true}}}, true)
	if code != 200 || !strings.Contains(string(body), `"updated":1`) || !strings.Contains(string(body), `"errors":[]`) {
		t.Fatalf("rollback opening: %d %s", code, body)
	}
	if b := balance(t, s, tenantID, storeID, det.ID); b.Qty != 0 {
		t.Fatalf("opening must be reversed: %+v", b)
	}
	if outboxCount(t, pool, events.StockMoved) != before+1 {
		t.Fatal("the reversal must emit bhandar.stock.moved")
	}
	recs := []RollbackRecord{{ExternalRef: "T-1", Created: &tr}, {ExternalRef: "T-2", Created: &fa}, {ExternalRef: "T-404"}}
	r, err := s.Rollback(ctx, tenantID, RollbackInput{Kind: RollbackItems, JobID: jobID, Records: recs})
	if err != nil || r.Updated != 3 || len(r.Errors) != 0 {
		t.Fatalf("rollback items: %+v %v", r, err)
	}
	if it, _ := s.GetItem(ctx, tenantID, det.ID); it.Status != "inactive" {
		t.Fatalf("created item must be archived: %+v", it)
	}
	if it, _ := s.GetItem(ctx, tenantID, existing.ID); it.Status != "active" || it.Name != "Old item renamed" {
		t.Fatalf("an updated item is never reverted: %+v", it)
	}

	// Re-import revives the archived item (created again) and its opening stock posts again.
	res, _ = s.BulkItems(ctx, tenantID, []BulkItem{{Name: "Dettol 125ml", SKU: "DET125", Unit: "PCS", TaxRateBP: 1800, ExternalRef: "T-1"}})
	if res.Created != 1 || !res.Results[0].Created {
		t.Fatalf("revive: %+v", res)
	}
	if op, _ = s.OpeningStock(ctx, tenantID, OpeningInput{StoreID: storeID, Lines: []OpeningLine{{ItemExternalRef: "T-1", Qty: 30, BatchNo: "B1"}}}); op.Created != 1 {
		t.Fatalf("opening after rollback: %+v", op)
	}
	// Once the position moves, the opening can no longer be rolled back.
	if _, err := s.Adjust(ctx, tenantID, storeID, "u1", AdjustmentInput{Note: "breakage", Lines: []AdjustmentLine{{ItemID: det.ID, BatchNo: "B1", Qty: -1}}}); err != nil {
		t.Fatal(err)
	}
	r, err = s.Rollback(ctx, tenantID, RollbackInput{Kind: RollbackOpeningStock, JobID: jobID, Records: []RollbackRecord{{ItemExternalRef: "T-1", BatchNo: "B1", StoreID: storeID}}})
	if err != nil || r.Updated != 0 || len(r.Errors) != 1 || !strings.Contains(r.Errors[0].Message, "moved since") {
		t.Fatalf("rollback after movement: %+v %v", r, err)
	}
	// An item a user edited after the import is not archived.
	other, _ := s.BulkItems(ctx, tenantID, []BulkItem{{Name: "Harpic", Unit: "PCS", TaxRateBP: 1800, ExternalRef: "T-3"}})
	if _, err := s.UpdateItem(ctx, tenantID, other.Results[0].ID, ItemInput{Name: "Harpic 500ml", TaxRateBP: 1800}); err != nil {
		t.Fatal(err)
	}
	r, _ = s.Rollback(ctx, tenantID, RollbackInput{Kind: RollbackItems, JobID: jobID, Records: []RollbackRecord{{ExternalRef: "T-3"}}})
	if len(r.Errors) != 1 || !strings.Contains(r.Errors[0].Message, "edited") {
		t.Fatalf("edited item: %+v", r)
	}
	if code, _ = c.do("POST", "/internal/v1/bulk/rollback", map[string]any{"kind": "parties", "job_id": jobID, "records": []any{}}, true); code != 422 {
		t.Fatalf("bahi kinds are not bhandar's: %d", code)
	}
}

// TestAadeshInternalTwins covers what aadesh calls: internal batches, serial
// check, track_serials on items/lookup, and serial_nos on its wrapper payload.
func TestAadeshInternalTwins(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	tenantID, storeA, storeB := uuid.NewString(), uuid.NewString(), uuid.NewString()
	router := s.Router()
	med := mustItem(t, s, tenantID, ItemInput{Name: "Crocin", TaxRateBP: 500, TrackBatches: true})
	tv := mustItem(t, s, tenantID, ItemInput{Name: "LED TV", TaxRateBP: 1800, TrackSerials: true})
	if _, err := s.OpeningStock(ctx, tenantID, OpeningInput{StoreID: storeA, Lines: []OpeningLine{{ItemID: med.ID, Qty: 10, BatchNo: "B1", ExpiryDate: "2027-12-31"}}}); err != nil {
		t.Fatal(err)
	}
	// aadesh's wrapper payload, as raw JSON: lines[].serial_nos.
	raw := map[string]any{"invoice_id": uuid.NewString(), "store_id": storeA, "doc_type": "purchase", "number": "P-9",
		"lines": []map[string]any{{"line_no": 1, "item_id": tv.ID, "name": "LED TV", "unit": "PCS", "qty": 2, "taxable_paise": 4000000,
			"serial_nos": []string{"TV-1", "TV-2"}}}}
	if err := router[events.InvoiceCreated](ctx, envelope(t, events.InvoiceCreated, tenantID, storeA, raw)); err != nil {
		t.Fatal(err)
	}
	sale := map[string]any{"invoice_id": uuid.NewString(), "store_id": storeA, "doc_type": "sale", "number": "S-9",
		"lines": []map[string]any{{"line_no": 1, "item_id": tv.ID, "name": "LED TV", "unit": "PCS", "qty": 1, "serial_nos": []string{"TV-2"}}}}
	if err := router[events.InvoiceCreated](ctx, envelope(t, events.InvoiceCreated, tenantID, storeA, sale)); err != nil {
		t.Fatal(err)
	}

	c := newHTTP(t, s, tenantID, storeA, auth.RoleCashier)
	code, body := c.do("POST", "/internal/v1/serials/check", map[string]any{"item_id": tv.ID, "store_id": storeB, "serials": []string{"TV-1", "tv-2", "TV-3"}}, true)
	var chk struct {
		Serials []SerialStatus `json:"serials"`
	}
	if code != 200 || json.Unmarshal(body, &chk) != nil || len(chk.Serials) != 3 {
		t.Fatalf("serials/check: %d %s", code, body)
	}
	if x := chk.Serials[0]; x.SerialNo != "TV-1" || x.Status != "in_stock" || x.StoreID != storeA {
		t.Fatalf("TV-1: %+v", x)
	}
	if x := chk.Serials[1]; x.SerialNo != "tv-2" || x.Status != "sold" {
		t.Fatalf("tv-2 (keyed as sent): %+v", x)
	}
	if x := chk.Serials[2]; x.Status != "unknown" || x.StoreID != "" {
		t.Fatalf("TV-3: %+v", x)
	}
	if code, _ = c.do("POST", "/internal/v1/serials/check", map[string]any{"item_id": tv.ID, "serials": []string{"TV-1"}}, false); code != 401 {
		t.Fatalf("serials/check without the secret: %d", code)
	}

	code, body = c.do("GET", "/internal/v1/items/"+med.ID+"/batches?store_id="+storeA, nil, true)
	var bt struct {
		Batches []Batch `json:"batches"`
	}
	if code != 200 || json.Unmarshal(body, &bt) != nil || len(bt.Batches) != 1 || bt.Batches[0].BatchNo != "B1" || bt.Batches[0].Qty != 10 ||
		bt.Batches[0].ExpiryDate != "2027-12-31" {
		t.Fatalf("internal batches: %d %s", code, body)
	}
	_, userBody := c.do("GET", "/v1/items/"+med.ID+"/batches?store_id="+storeA, nil, false)
	if string(userBody) != string(body) {
		t.Fatalf("internal and user batches differ:\n%s\n%s", body, userBody)
	}

	code, body = c.do("POST", "/internal/v1/items/lookup", LookupInput{IDs: []string{tv.ID, med.ID}}, true)
	if code != 200 || strings.Count(string(body), `"track_serials":true`) != 1 || !strings.Contains(string(body), `"track_serials":false`) {
		t.Fatalf("items/lookup track_serials: %d %s", code, body)
	}
}
