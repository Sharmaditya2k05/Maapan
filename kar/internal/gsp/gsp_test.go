package gsp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/taraju/mool/gst"
)

const chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"

// gstinWith completes a 14-character prefix with its valid check character.
func gstinWith(t *testing.T, first14 string) string {
	t.Helper()
	for _, c := range chars {
		g := first14 + string(c)
		if gst.ValidateGSTIN(g) == nil {
			return g
		}
	}
	t.Fatalf("no check digit for %s", first14)
	return ""
}

// now is 24 Oct 2026, 10:00 IST.
var now = time.Date(2026, 10, 24, 10, 0, 0, 0, IST)

func newSim() *Simulator {
	s := NewSimulator(NewMemStore(), []byte("test-key"))
	s.Now = func() time.Time { return now }
	return s
}

const seller = "27AAPFU0939F1ZV"

func buyer(t *testing.T) string { return gstinWith(t, "29AAACB1234C1Z") }

func invoiceJSON(t *testing.T, mut func(m map[string]any)) map[string]any {
	t.Helper()
	raw := `{
	 "Version":"1.1",
	 "TranDtls":{"TaxSch":"GST","SupTyp":"B2B","RegRev":"N","IgstOnIntra":"N"},
	 "DocDtls":{"Typ":"INV","No":"INV/26-27/00042","Dt":"05/10/2026"},
	 "SellerDtls":{"Gstin":"` + seller + `","LglNm":"Acme Traders","Addr1":"12 MG Road","Loc":"Pune","Pin":411001,"Stcd":"27","Ph":"9876543210"},
	 "BuyerDtls":{"Gstin":"` + buyer(t) + `","LglNm":"Bharat Stores","Pos":"29","Addr1":"4 Church St","Loc":"Bengaluru","Pin":560001,"Stcd":"29"},
	 "ItemList":[{"SlNo":"1","PrdDesc":"Steel Almirah","IsServc":"N","HsnCd":"940320","Qty":2,"Unit":"PCS","UnitPrice":30000,
	   "TotAmt":60000,"Discount":0,"AssAmt":60000,"GstRt":18,"IgstAmt":10800,"CgstAmt":0,"SgstAmt":0,"CesRt":0,"CesAmt":0,"TotItemVal":70800}],
	 "ValDtls":{"AssVal":60000,"IgstVal":10800,"CgstVal":0,"SgstVal":0,"CesVal":0,"Discount":0,"OthChrg":0,"RndOffAmt":0,"TotInvVal":70800}
	}`
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	if mut != nil {
		mut(m)
	}
	return m
}

func sub(m map[string]any, k string) map[string]any { return m[k].(map[string]any) }
func item(m map[string]any) map[string]any          { return m["ItemList"].([]any)[0].(map[string]any) }

func TestIRNRuleVector(t *testing.T) {
	// SHA-256("27AAPFU0939F1ZV" + "2026-27" + "INV" + "INV/26-27/00042"), computed independently with sha256sum.
	const want = "281c111889c3563e699ab4ca056546f54ca46eaeda82aa11e290f77620784cec"
	if got := IRNFor(seller, "2026-27", "INV", "INV/26-27/00042"); got != want {
		t.Fatalf("IRN = %s, want %s", got, want)
	}
	for d, fy := range map[time.Time]string{
		time.Date(2026, 4, 1, 0, 0, 0, 0, IST):  "2026-27",
		time.Date(2027, 3, 31, 0, 0, 0, 0, IST): "2026-27",
		time.Date(2026, 3, 31, 0, 0, 0, 0, IST): "2025-26",
		time.Date(2099, 12, 1, 0, 0, 0, 0, IST): "2099-00",
	} {
		if got := FinancialYear(d); got != fy {
			t.Errorf("FY(%s) = %s, want %s", d.Format(time.DateOnly), got, fy)
		}
	}
}

func TestSimulatorGenerateAndDuplicate(t *testing.T) {
	s := newSim()
	ctx := context.Background()
	res, err := s.GenerateIRN(ctx, Credentials{}, invoiceJSON(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if res.IRN != IRNFor(seller, "2026-27", "INV", "INV/26-27/00042") || len(res.IRN) != 64 || strings.ToLower(res.IRN) != res.IRN {
		t.Fatalf("irn %q", res.IRN)
	}
	if len(res.AckNo) != 15 || strings.Trim(res.AckNo, "0123456789") != "" {
		t.Fatalf("ack no %q must be 15 digits", res.AckNo)
	}
	if !res.AckDt.Equal(now) || res.Status != IRNActive {
		t.Fatalf("ack %v %s", res.AckDt, res.Status)
	}
	// Signed QR: compact JWS, verifiable with the local key, carrying the IRP fields.
	if _, err := VerifyJWS([]byte("test-key"), res.SignedQR); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyJWS([]byte("other"), res.SignedQR); err == nil {
		t.Fatal("wrong key must fail")
	}
	q, err := ParseSignedQR(res.SignedQR)
	if err != nil {
		t.Fatal(err)
	}
	if q.SellerGstin != seller || q.BuyerGstin != buyer(t) || q.DocNo != "INV/26-27/00042" || q.DocTyp != "INV" ||
		q.DocDt != "05/10/2026" || q.TotInvVal != 70800 || q.ItemCnt != 1 || q.MainHsnCode != "940320" || q.Irn != res.IRN ||
		q.IrnDt != "2026-10-24 10:00:00" {
		t.Fatalf("qr %+v", q)
	}
	if _, err := VerifyJWS([]byte("test-key"), res.SignedInvoice); err != nil {
		t.Fatal(err)
	}
	// The same document again: 2150 with the original details.
	s.Now = func() time.Time { return now.Add(time.Hour) }
	_, err = s.GenerateIRN(ctx, Credentials{}, invoiceJSON(t, nil))
	ge, ok := AsError(err)
	if !ok || ge.Kind != KindDuplicate || ge.Code != CodeDuplicateIRN || ge.Existing == nil || ge.Existing.IRN != res.IRN ||
		ge.Existing.AckNo != res.AckNo || !ge.Existing.AckDt.Equal(now) {
		t.Fatalf("duplicate: %v %+v", err, ge)
	}
	// Lookup by document.
	got, err := s.GetIRNByDocument(ctx, Credentials{GSTIN: seller}, DocumentRef{DocType: "INV", DocNo: "INV/26-27/00042", DocDate: "05/10/2026"})
	if err != nil || got.IRN != res.IRN {
		t.Fatalf("by doc: %v %+v", err, got)
	}
	if _, err := s.GetIRNByDocument(ctx, Credentials{GSTIN: seller}, DocumentRef{DocType: "INV", DocNo: "X1", DocDate: "05/10/2026"}); !IsKind(err, KindNotFound) {
		t.Fatalf("missing doc: %v", err)
	}
}

func TestSimulatorValidationRejects(t *testing.T) {
	s := newSim()
	cases := map[string]struct {
		mut  func(m map[string]any)
		code string
		msg  string
	}{
		"4-digit HSN": {func(m map[string]any) { item(m)["HsnCd"] = "9403" }, CodeInvalidItem, "6 or 8 digits"},
		"bad seller GSTIN": {func(m map[string]any) { sub(m, "SellerDtls")["Gstin"] = "27AAPFU0939F1ZA" },
			CodeInvalidField, "SellerDtls.Gstin"},
		"future date":     {func(m map[string]any) { sub(m, "DocDtls")["Dt"] = "25/10/2026" }, CodeFutureDate, "future"},
		"doc no starts 0": {func(m map[string]any) { sub(m, "DocDtls")["No"] = "0042" }, CodeInvalidField, "DocDtls.No"},
		"bad supply type": {func(m map[string]any) { sub(m, "TranDtls")["SupTyp"] = "B2C" }, CodeInvalidField, "SupTyp"},
		"phone with plus": {func(m map[string]any) { sub(m, "SellerDtls")["Ph"] = "+919876543210" }, CodeInvalidField, "Ph"},
		"addr over 100":   {func(m map[string]any) { sub(m, "SellerDtls")["Addr1"] = strings.Repeat("a", 101) }, CodeInvalidField, "Addr1"},
		"same GSTIN":      {func(m map[string]any) { sub(m, "BuyerDtls")["Gstin"] = seller; sub(m, "BuyerDtls")["Stcd"] = "27" }, CodeSameGSTIN, "same"},
		"12% after GST 2.0": {func(m map[string]any) { item(m)["GstRt"] = 12.0; item(m)["IgstAmt"] = 7200.0 },
			CodeInvalidItem, "not a GST rate"},
		"intra with IGST": {func(m map[string]any) {
			sub(m, "BuyerDtls")["Pos"] = "27"
		}, CodeInvalidItem, "intra-state"},
		"totals mismatch": {func(m map[string]any) { sub(m, "ValDtls")["TotInvVal"] = 70900.0 }, CodeInvalidField, "TotInvVal"},
		"service flag":    {func(m map[string]any) { item(m)["HsnCd"] = "998314" }, CodeInvalidItem, "IsServc"},
		"export buyer": {func(m map[string]any) { sub(m, "TranDtls")["SupTyp"] = "EXPWP" },
			CodeInvalidField, "URP"},
	}
	for name, c := range cases {
		_, err := s.GenerateIRN(context.Background(), Credentials{}, invoiceJSON(t, c.mut))
		ge, ok := AsError(err)
		if !ok || ge.Kind != KindValidation {
			t.Errorf("%s: want validation error, got %v", name, err)
			continue
		}
		found := false
		for _, d := range ge.Details {
			if d.Code == c.code && strings.Contains(d.Message, c.msg) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: want %s containing %q, got %+v", name, c.code, c.msg, ge.Details)
		}
	}
	// A valid export passes.
	_, err := s.GenerateIRN(context.Background(), Credentials{}, invoiceJSON(t, func(m map[string]any) {
		sub(m, "TranDtls")["SupTyp"] = "EXPWOP"
		b := sub(m, "BuyerDtls")
		b["Gstin"], b["Stcd"], b["Pos"], b["Pin"] = "URP", "96", "96", 999999
		it := item(m)
		it["IgstAmt"], it["TotItemVal"] = 0.0, 60000.0
		v := sub(m, "ValDtls")
		v["IgstVal"], v["TotInvVal"] = 0.0, 60000.0
	}))
	if err != nil {
		t.Fatalf("export: %v", err)
	}
}

func TestSimulatorCancelWindow(t *testing.T) {
	s := newSim()
	ctx := context.Background()
	res, err := s.GenerateIRN(ctx, Credentials{}, invoiceJSON(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelIRN(ctx, Credentials{}, CancelIRNRequest{IRN: res.IRN, ReasonCode: 7}); !IsKind(err, KindValidation) {
		t.Fatalf("bad reason: %v", err)
	}
	s.Now = func() time.Time { return now.Add(25 * time.Hour) }
	_, err = s.CancelIRN(ctx, Credentials{}, CancelIRNRequest{IRN: res.IRN, ReasonCode: 2, Remark: "typo"})
	if ge, ok := AsError(err); !ok || ge.Code != CodeCancelWindow {
		t.Fatalf("after 24h: %v", err)
	}
	s.Now = func() time.Time { return now.Add(23 * time.Hour) }
	c, err := s.CancelIRN(ctx, Credentials{}, CancelIRNRequest{IRN: res.IRN, ReasonCode: 2, Remark: "typo"})
	if err != nil || c.Status != IRNCancelled {
		t.Fatalf("cancel: %v %+v", err, c)
	}
	if _, err := s.CancelIRN(ctx, Credentials{}, CancelIRNRequest{IRN: res.IRN, ReasonCode: 2}); !IsKind(err, KindNotAllowed) {
		t.Fatalf("second cancel: %v", err)
	}
	// A cancelled document number stays taken.
	if _, err := s.GenerateIRN(ctx, Credentials{}, invoiceJSON(t, nil)); !IsKind(err, KindDuplicate) {
		t.Fatalf("re-register cancelled: %v", err)
	}
}

func TestSimulatorEWayLifecycle(t *testing.T) {
	s := newSim()
	ctx := context.Background()
	inv, err := s.GenerateIRN(ctx, Credentials{}, invoiceJSON(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GenerateEWB(ctx, Credentials{}, EWBRequest{ByIRN: &EWBByIRN{IRN: inv.IRN, Distance: 840, TransMode: "1", VehNo: "MH12AB12"}}); !IsKind(err, KindValidation) {
		t.Fatalf("bad vehicle: %v", err)
	}
	r, err := s.GenerateEWB(ctx, Credentials{}, EWBRequest{ByIRN: &EWBByIRN{IRN: inv.IRN, Distance: 840, TransMode: "1", VehNo: "22BH1234AA", VehType: "R"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.EWBNo) != 12 || strings.Trim(r.EWBNo, "0123456789") != "" {
		t.Fatalf("ewb no %q", r.EWBNo)
	}
	// 840 km → 5 days; generated 24 Oct → valid until 29 Oct 23:59:59 IST.
	if want := time.Date(2026, 10, 29, 23, 59, 59, 0, IST); !r.ValidUpto.Equal(want) {
		t.Fatalf("valid upto %v, want %v", r.ValidUpto, want)
	}
	if _, err := s.GenerateEWB(ctx, Credentials{}, EWBRequest{ByIRN: &EWBByIRN{IRN: inv.IRN, Distance: 10, TransMode: "1", VehNo: "MH12AB1234"}}); !IsKind(err, KindDuplicate) {
		t.Fatalf("duplicate ewb: %v", err)
	}
	// IRN with an active EWB cannot be cancelled.
	if _, err := s.CancelIRN(ctx, Credentials{}, CancelIRNRequest{IRN: inv.IRN, ReasonCode: 1}); err == nil || !strings.Contains(err.Error(), CodeIRNHasEWB) {
		t.Fatalf("cancel irn with ewb: %v", err)
	}
	pb, err := s.UpdatePartB(ctx, Credentials{}, PartBRequest{EWBNo: r.EWBNo, VehicleNo: "KA01AB1234", FromPlace: "Hubli", FromState: 29, ReasonCode: "1", ReasonRem: "breakdown", TransMode: "1"})
	if err != nil || !pb.ValidUpto.Equal(r.ValidUpto) {
		t.Fatalf("part-b keeps validity: %v %+v", err, pb)
	}
	ext := ExtendRequest{EWBNo: r.EWBNo, VehicleNo: "KA01AB1234", FromPlace: "Hubli", FromState: 29, FromPincode: 580020,
		RemainingDistance: 150, TransMode: "1", ExtnRsnCode: 5, ExtnRemarks: "accident", ConsignmentStatus: "M"}
	if _, err := s.ExtendValidity(ctx, Credentials{}, ext); !IsKind(err, KindNotAllowed) {
		t.Fatalf("extend too early: %v", err)
	}
	s.Now = func() time.Time { return r.ValidUpto.Add(-2 * time.Hour) }
	e, err := s.ExtendValidity(ctx, Credentials{}, ext)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 10, 30, 23, 59, 59, 0, IST); !e.ValidUpto.Equal(want) {
		t.Fatalf("extended to %v, want %v", e.ValidUpto, want)
	}
	if _, err := s.CancelEWB(ctx, Credentials{}, CancelEWBRequest{EWBNo: r.EWBNo, ReasonCode: 2, Remark: "x"}); !IsKind(err, KindNotAllowed) {
		t.Fatalf("cancel after 24h: %v", err)
	}
	s.Now = func() time.Time { return now.Add(time.Hour) }
	if _, err := s.CancelEWB(ctx, Credentials{}, CancelEWBRequest{EWBNo: r.EWBNo, ReasonCode: 2, Remark: "x"}); err != nil {
		t.Fatal(err)
	}
	d, err := s.GetEWB(ctx, Credentials{}, r.EWBNo)
	if err != nil || d.Status != IRNCancelled || d.IRN != inv.IRN {
		t.Fatalf("get: %v %+v", err, d)
	}
	// After cancelling the EWB the IRN can be cancelled.
	if _, err := s.CancelIRN(ctx, Credentials{}, CancelIRNRequest{IRN: inv.IRN, ReasonCode: 1}); err != nil {
		t.Fatal(err)
	}
}

func TestSimulatorStandaloneEWBAndOldDoc(t *testing.T) {
	s := newSim()
	bill := map[string]any{"userGstin": seller, "supplyType": "O", "subSupplyType": 1, "docType": "INV", "docNo": "INV/9",
		"docDate": "20/10/2026", "fromGstin": seller, "fromPincode": 411001, "fromStateCode": 27, "toGstin": "URP", "toPincode": 411002,
		"toStateCode": 27, "transactionType": 1, "totInvValue": 118000, "transMode": "", "transDistance": "12", "transporterId": seller,
		"itemList": []any{map[string]any{"hsnCode": 9403}}}
	r, err := s.GenerateEWB(context.Background(), Credentials{}, EWBRequest{Bill: bill})
	if err != nil {
		t.Fatal(err)
	}
	if !r.ValidUpto.IsZero() || r.Alert == "" {
		t.Fatalf("Part-A only has no validity: %+v", r)
	}
	bill["docNo"], bill["docDate"] = "INV/10", "20/04/2026" // 187 days before 24 Oct 2026
	_, err = s.GenerateEWB(context.Background(), Credentials{}, EWBRequest{Bill: bill})
	if ge, ok := AsError(err); !ok || ge.Code != CodeEWBDocTooOld {
		t.Fatalf("old doc: %v", err)
	}
}

func TestSearchGSTINStatuses(t *testing.T) {
	s := newSim()
	ctx := context.Background()
	tp, err := s.SearchGSTIN(ctx, seller)
	if err != nil || tp.Status != "Active" || tp.StateCode != "27" || tp.LegalName == "" {
		t.Fatalf("%v %+v", err, tp)
	}
	if tp, _ := s.SearchGSTIN(ctx, gstinWith(t, "27AAPFU0939F9Z")); tp.Status != "Cancelled" {
		t.Fatalf("want cancelled: %+v", tp)
	}
	if tp, _ := s.SearchGSTIN(ctx, gstinWith(t, "27AAPFU0939F8Z")); tp.Status != "Suspended" {
		t.Fatalf("want suspended: %+v", tp)
	}
	if _, err := s.SearchGSTIN(ctx, "27AAPFU0939F1ZA"); !IsKind(err, KindValidation) {
		t.Fatalf("bad checksum: %v", err)
	}
}

func TestFetchGSTR2B(t *testing.T) {
	raw, err := newSim().FetchGSTR2B(context.Background(), Credentials{}, seller, "092026")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Data struct {
			GSTIN   string                     `json:"gstin"`
			Rtnprd  string                     `json:"rtnprd"`
			Docdata map[string]json.RawMessage `json:"docdata"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Data.GSTIN != seller || doc.Data.Rtnprd != "092026" || string(doc.Data.Docdata["b2b"]) != "[]" {
		t.Fatalf("%v %s", err, raw)
	}
	if _, err := newSim().FetchGSTR2B(context.Background(), Credentials{}, seller, "2026-09"); err == nil {
		t.Fatal("bad period must fail")
	}
}

func TestValidityAndVehicles(t *testing.T) {
	for _, c := range []struct {
		km   int
		odc  bool
		days int
	}{{0, false, 1}, {1, false, 1}, {200, false, 1}, {201, false, 2}, {840, false, 5}, {4000, false, 20},
		{20, true, 1}, {21, true, 2}, {100, true, 5}} {
		if got := ValidityDays(c.km, c.odc); got != c.days {
			t.Errorf("ValidityDays(%d, odc=%v) = %d, want %d", c.km, c.odc, got, c.days)
		}
	}
	gen := time.Date(2026, 10, 24, 23, 30, 0, 0, IST)
	if got := ValidUpto(gen, 1); !got.Equal(time.Date(2026, 10, 25, 23, 59, 59, 0, IST)) {
		t.Fatalf("one day after 23:30 → next midnight: %v", got)
	}
	if !CanExtend(gen, gen.Add(-7*time.Hour)) || CanExtend(gen, gen.Add(-9*time.Hour)) || !CanExtend(gen, gen.Add(8*time.Hour)) || CanExtend(gen, gen.Add(9*time.Hour)) {
		t.Fatal("extension window is 8 h either side")
	}
	if !DocTooOld(time.Date(2026, 4, 26, 0, 0, 0, 0, IST), now) || DocTooOld(time.Date(2026, 4, 27, 0, 0, 0, 0, IST), now) {
		t.Fatal("180-day rule")
	}
	for v, ok := range map[string]bool{
		"MH12AB1234": true, "mh 12 ab 1234": true, "DL1CAB1234": true, "KA011234": true, "22BH1234AA": true, "22BH1234A": true,
		"TR22AB1234567": true, "TMXY1234": true, "ABC1234": true, "MH12AB12": false, "BH1234AA": false, "1234": false, "": false,
	} {
		if ValidVehicle(v) != ok {
			t.Errorf("ValidVehicle(%q) = %v, want %v", v, !ok, ok)
		}
	}
}

func TestHTTPDriver(t *testing.T) {
	var tokens, calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/oauth/token":
			atomic.AddInt32(&tokens, 1)
			_ = r.ParseForm()
			if r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("client_secret") != "cs" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"tok-1","expires_in":3600}`))
		case r.URL.Path == "/eicore/v1.03/Invoice":
			atomic.AddInt32(&calls, 1)
			if r.Header.Get("Authorization") != "Bearer tok-1" || r.Header.Get("gstin") != seller ||
				r.Header.Get("user_name") != "api_user" || r.Header.Get("password") != "secret-pw" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if atomic.LoadInt32(&calls) == 1 {
				_, _ = w.Write([]byte(`{"Status":1,"Data":"{\"AckNo\":112610000000001,\"AckDt\":\"2026-10-24 10:00:00\",\"Irn\":\"abc\",\"SignedQRCode\":\"q.r.s\",\"Status\":\"ACT\"}"}`))
				return
			}
			_, _ = w.Write([]byte(`{"Status":0,"ErrorDetails":[{"ErrorCode":"2150","ErrorMessage":"Duplicate IRN"}],
				"InfoDtls":[{"InfCd":"DUPIRN","Desc":{"AckNo":112610000000001,"AckDt":"2026-10-24 10:00:00","Irn":"abc"}}]}`))
		case strings.HasPrefix(r.URL.Path, "/commonapi"):
			if r.URL.Query().Get("gstin") != seller {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"data":{"gstin":"` + seller + `","lgnm":"ACME","tradeNam":"Acme","sts":"Cancelled"}}`))
		case r.URL.Path == "/eicore/v1.03/Invoice/Cancel":
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer srv.Close()
	h, err := NewHTTP(HTTPConfig{BaseURL: srv.URL, ClientID: "ci", ClientSecret: "cs"})
	if err != nil {
		t.Fatal(err)
	}
	c := Credentials{GSTIN: seller, EInvoiceUsername: "api_user", EInvoicePassword: "secret-pw"}
	ctx := context.Background()
	r, err := h.GenerateIRN(ctx, c, map[string]any{"Version": "1.1"})
	if err != nil || r.IRN != "abc" || r.AckNo != "112610000000001" || r.SignedQR != "q.r.s" || r.AckDt.IsZero() {
		t.Fatalf("generate: %v %+v", err, r)
	}
	_, err = h.GenerateIRN(ctx, c, map[string]any{"Version": "1.1"})
	ge, ok := AsError(err)
	if !ok || ge.Kind != KindDuplicate || ge.Code != "2150" || ge.Existing == nil || ge.Existing.IRN != "abc" {
		t.Fatalf("duplicate: %v %+v", err, ge)
	}
	if n := atomic.LoadInt32(&tokens); n != 1 {
		t.Fatalf("token must be cached, fetched %d times", n)
	}
	if _, err := h.GenerateIRN(ctx, Credentials{GSTIN: seller}, map[string]any{}); !IsKind(err, KindAuth) {
		t.Fatalf("missing API user: %v", err)
	}
	if _, err := h.CancelIRN(ctx, c, CancelIRNRequest{IRN: "abc", ReasonCode: 1}); !IsKind(err, KindUnavailable) {
		t.Fatalf("5xx is unavailable: %v", err)
	}
	tp, err := h.SearchGSTIN(ctx, seller)
	if err != nil || tp.Status != "Cancelled" || tp.LegalName != "ACME" {
		t.Fatalf("search: %v %+v", err, tp)
	}
	if red := Redact([]byte(`{"password":"p","nested":{"client_secret":"x","Irn":"1"}}`)); strings.Contains(red, `"p"`) || strings.Contains(red, `"x"`) || !strings.Contains(red, `"Irn":"1"`) {
		t.Fatalf("redact: %s", red)
	}
}
