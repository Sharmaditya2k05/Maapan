package returns

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"

	"github.com/taraju/kar/internal/recon"
	"github.com/taraju/mool/auth"
)

const testTenant = "11111111-1111-1111-1111-111111111111"

func principalCtx() context.Context {
	return principalAs(auth.RoleAccountant)
}

func principalAs(role string) context.Context {
	c := &auth.Claims{TenantID: testTenant, Role: role, RegisteredClaims: jwt.RegisteredClaims{Subject: "user-1"}}
	return auth.WithPrincipal(context.Background(), &auth.Principal{Claims: c, TenantID: testTenant})
}

func buildFixtureGSTR3B(t *testing.T, opening Taxes, twoB *recon.ITCSummary) GSTR3B {
	t.Helper()
	b := NewGSTR3B(ourGSTIN, "102026", "27")
	for _, inv := range fixture() {
		b.Add(inv)
	}
	return b.Build(opening, twoB)
}

func TestGSTR3BGolden(t *testing.T) {
	g := buildFixtureGSTR3B(t, Taxes{}, nil)
	golden(t, "gstr3b_golden.json", g)
}

func TestGSTR3BTables(t *testing.T) {
	g := buildFixtureGSTR3B(t, Taxes{}, nil)
	js := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	sd := g.SupDetails
	if sd.OsupDet != (Head{Txval: 14446000, Iamt: 2599200, Camt: 150, Samt: 150}) {
		t.Errorf("3.1(a) %s", js(sd.OsupDet))
	}
	if sd.OsupZero != (Head{Txval: 1100000, Iamt: 15000}) {
		t.Errorf("3.1(b) %s", js(sd.OsupZero))
	}
	if sd.OsupNilExmp != (Head{Txval: 62000}) {
		t.Errorf("3.1(c) %s", js(sd.OsupNilExmp))
	}
	if sd.IsupRev != (Head{Txval: 200000, Camt: 5000, Samt: 5000}) {
		t.Errorf("3.1(d) %s", js(sd.IsupRev))
	}
	if sd.OsupNongst != (Head{Txval: 30000}) {
		t.Errorf("3.1(e) %s", js(sd.OsupNongst))
	}
	if u := g.InterSup.UnregDetails; len(u) != 1 || u[0] != (UnregSupply{Pos: "07", Txval: 14040000, Iamt: 2527200}) {
		t.Errorf("3.2 %s", js(u))
	}
	row := func(rows []ITCRow, ty string) TaxHead {
		for _, r := range rows {
			if r.Ty == ty {
				return r.TaxHead
			}
		}
		t.Fatalf("no %s row", ty)
		return TaxHead{}
	}
	ie := g.ItcElg
	if r := row(ie.ItcAvl, "ISRC"); r != (TaxHead{Camt: 5000, Samt: 5000}) {
		t.Errorf("4(A)(3) %s", js(r))
	}
	if r := row(ie.ItcAvl, "OTH"); r != (TaxHead{Iamt: 162000, Camt: 7000, Samt: 7000}) {
		t.Errorf("4(A)(5) %s", js(r))
	}
	if r := row(ie.ItcRev, "RUL"); r != (TaxHead{Camt: 4500, Samt: 4500}) {
		t.Errorf("4(B)(1) blocked credit %s", js(r))
	}
	if ie.ItcNet != (TaxHead{Iamt: 162000, Camt: 7500, Samt: 7500}) {
		t.Errorf("4(C) %s", js(ie.ItcNet))
	}
	if len(ie.ItcAvl) != 5 || len(ie.ItcRev) != 2 || len(ie.ItcInelg) != 2 {
		t.Errorf("table 4 rows %s", js(ie))
	}
	if g.ITCBooks.Ineligible175 != (TaxHead{Camt: 4500, Samt: 4500}) || g.ITCBooks.ReverseCharge != (TaxHead{Camt: 5000, Samt: 5000}) {
		t.Errorf("itc books %s", js(g.ITCBooks))
	}
	is := g.InwardSup.IsupDetails
	if is[0] != (IsupRow{Ty: "GST", Inter: 2000, Intra: 6000}) || is[1] != (IsupRow{Ty: "NONGST", Inter: 3000}) {
		t.Errorf("table 5 %s", js(is))
	}

	// Set-off: liability IGST 26142, CGST 1.50, SGST 1.50 against IGST 1620, CGST 75, SGST 75.
	c := g.Computation
	if c.Liability != (Taxes{IGST: 2614200, CGST: 150, SGST: 150}) {
		t.Errorf("liability %s", js(c.Liability))
	}
	if c.CashPayable != (Taxes{IGST: 2437500}) || c.ITCCarryFwd != (Taxes{}) {
		t.Errorf("cash %s carry %s", js(c.CashPayable), js(c.ITCCarryFwd))
	}
	if c.ReverseCharge != (Taxes{CGST: 5000, SGST: 5000}) || c.TotalCash != (Taxes{IGST: 2437500, CGST: 5000, SGST: 5000}) {
		t.Errorf("rcm cash %s total %s", js(c.ReverseCharge), js(c.TotalCash))
	}
	if g.Summary.OutwardRCM.Txval != 200000 || g.Summary.Excluded != 1 {
		t.Errorf("summary %s", js(g.Summary))
	}
	if g.ITC2B == nil || g.ITC2B.Imported {
		t.Errorf("2B comparison without import %s", js(g.ITC2B))
	}
	p := g.Portal()
	if _, ok := p["computation"]; ok || p["ret_period"] != "102026" {
		t.Errorf("portal %v", p)
	}
	var buf bytes.Buffer
	if err := WriteGSTR3BCSV(&buf, g); err != nil || !strings.Contains(buf.String(), `3.1(a),"Outward taxable supplies`) ||
		!strings.Contains(buf.String(), "6.1,Total cash payable,0.00,24375.00,50.00,50.00,0.00") {
		t.Errorf("csv %v\n%s", err, buf.String())
	}
}

func TestGSTR3BOpeningAnd2B(t *testing.T) {
	two := recon.ITCSummary{Available: recon.Heads{IGST: 150000, CGST: 7500, SGST: 7500}}
	g := buildFixtureGSTR3B(t, Taxes{IGST: 3000000}, &two)
	c := g.Computation
	// IGST credit 1620 + 30000 opening covers IGST 26142; the rest pays CGST/SGST 1.50 each.
	if c.CashPayable != (Taxes{}) || c.ITCCarryFwd.IGST != 3162000-2614200-300 || c.ITCCarryFwd.CGST != 7500 || c.OpeningITC.IGST != 3000000 {
		t.Errorf("with opening: %+v", c)
	}
	if g.ITC2B.Difference != (recon.Heads{IGST: 12000}) || g.ITC2B.Note == "" {
		t.Errorf("2B difference %+v", g.ITC2B)
	}
}

func TestSetOff(t *testing.T) {
	cases := []struct {
		name               string
		liab, cred         [4]int64
		cash, carry, usedC [4]int64
	}{
		{name: "CGST credit never pays SGST", liab: [4]int64{0, 10000, 10000, 0}, cred: [4]int64{0, 30000, 0, 0},
			cash: [4]int64{0, 0, 10000, 0}, carry: [4]int64{0, 20000, 0, 0}, usedC: [4]int64{0, 10000, 0, 0}},
		{name: "SGST credit pays IGST after SGST", liab: [4]int64{5000, 0, 1000, 0}, cred: [4]int64{0, 0, 4000, 0},
			cash: [4]int64{2000, 0, 0, 0}, usedC: [4]int64{0, 0, 4000, 0}},
		{name: "IGST credit goes where own credit falls short", liab: [4]int64{0, 10000, 50000, 0}, cred: [4]int64{30000, 30000, 0, 0},
			cash: [4]int64{0, 0, 20000, 0}, carry: [4]int64{0, 20000, 0, 0}, usedC: [4]int64{30000, 10000, 0, 0}},
		{name: "IGST credit is used up before CGST credit (Sec 49A)", liab: [4]int64{0, 10000, 0, 0}, cred: [4]int64{10000, 10000, 0, 0},
			carry: [4]int64{0, 10000, 0, 0}, usedC: [4]int64{10000, 0, 0, 0}},
		{name: "IGST first against IGST", liab: [4]int64{8000, 1000, 1000, 0}, cred: [4]int64{9000, 0, 0, 0},
			cash: [4]int64{0, 0, 1000, 0}, usedC: [4]int64{9000, 0, 0, 0}},
		{name: "cess only with cess", liab: [4]int64{0, 0, 0, 5000}, cred: [4]int64{10000, 0, 0, 2000},
			cash: [4]int64{0, 0, 0, 3000}, carry: [4]int64{10000, 0, 0, 0}, usedC: [4]int64{0, 0, 0, 2000}},
	}
	for _, tc := range cases {
		s := setOff(tc.liab, tc.cred)
		if s.CashPayable != taxes(tc.cash) || s.ITCCarryFwd != taxes(tc.carry) || s.ITCUtilised != taxes(tc.usedC) {
			t.Errorf("%s: cash %+v carry %+v used %+v", tc.name, s.CashPayable, s.ITCCarryFwd, s.ITCUtilised)
		}
		// The payment table balances per head.
		for h, r := range s.Payable {
			if r.PaidIGST+r.PaidCGST+r.PaidSGST+r.PaidCess+r.Cash != Rs(max(tc.liab[h], 0)) {
				t.Errorf("%s: row %s does not balance %+v", tc.name, r.Head, r)
			}
			if (r.Head == "cgst" && r.PaidSGST != 0) || (r.Head == "sgst" && r.PaidCGST != 0) {
				t.Errorf("%s: CGST/SGST crossed %+v", tc.name, r)
			}
		}
	}
}

// Tax on a forward-charge bill from a supplier without a GSTIN is never
// claimed: it stays out of Table 4 and is reported with a warning. A reverse
// charge bill from the same kind of supplier is still credit (ISRC).
func TestGSTR3BNoSupplierGSTIN(t *testing.T) {
	b := NewGSTR3B(ourGSTIN, "102026", "27")
	b.Add(doc("u1", "PUR/26-27/00009", "purchase", "", "27", ls(line("1905", 1800, 78729, false))))
	b.Add(doc("u2", "PUR/26-27/00010", "purchase", "", "27", ls(line("9965", 500, 200000, false)), rcm()))
	g := b.Build(Taxes{}, nil)
	var oth, isrc ITCRow
	for _, r := range g.ItcElg.ItcAvl {
		switch r.Ty {
		case "OTH":
			oth = r
		case "ISRC":
			isrc = r
		}
	}
	if oth.TaxHead != (TaxHead{}) {
		t.Errorf("4(A)(5) must not carry unregistered-supplier tax: %+v", oth)
	}
	if isrc.Camt != 5000 || isrc.Samt != 5000 {
		t.Errorf("RCM credit from an unregistered supplier is still ISRC: %+v", isrc)
	}
	if g.ITCBooks.NoGSTINBills != 1 || g.ITCBooks.NoSupplierGSTIN.Camt+g.ITCBooks.NoSupplierGSTIN.Samt == 0 {
		t.Errorf("no_supplier_gstin bucket: %+v", g.ITCBooks)
	}
	if g.ITCBooks.Eligible.Camt != 5000 {
		t.Errorf("eligible must hold only the RCM bill: %+v", g.ITCBooks.Eligible)
	}
	found := false
	for _, w := range g.Warnings {
		found = found || strings.Contains(w, "without a GSTIN")
	}
	if !found {
		t.Errorf("missing warning: %v", g.Warnings)
	}
}
