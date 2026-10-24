package returns

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata golden files")

// golden compares v (as indented JSON) with testdata/name.
func golden(t *testing.T, name string, v any) {
	t.Helper()
	got, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update once to create it)", err)
	}
	want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from golden file; got:\n%s", name, got)
	}
}

func buildFixtureGSTR1(t *testing.T) GSTR1 {
	t.Helper()
	src := &fakeSource{invs: fixture()}
	s := newService(Deps{Invoices: src, Registrations: regs})
	ctx := principalCtx()
	tg := target{reg: regs[0], period: "102026", from: "2026-10-01", to: "2026-10-31"}
	g, err := s.BuildGSTR1(ctx, tg)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestGSTR1Golden(t *testing.T) {
	g := buildFixtureGSTR1(t)
	golden(t, "gstr1_golden.json", g)
	golden(t, "gstr1_portal_golden.json", g.Portal())
}

func TestGSTR1Sections(t *testing.T) {
	g := buildFixtureGSTR1(t)
	js := func(v any) string { b, _ := json.Marshal(v); return string(b) }

	// B2B: 4A regular, 4B reverse charge, 6B SEZ without payment, 6C deemed export.
	if len(g.B2B) != 4 {
		t.Fatalf("b2b recipients %s", js(g.B2B))
	}
	byCtin := map[string]B2BInv{}
	for _, b := range g.B2B {
		if len(b.Inv) != 1 {
			t.Fatalf("b2b %s has %d invoices", b.Ctin, len(b.Inv))
		}
		byCtin[b.Ctin] = b.Inv[0]
	}
	if inv := byCtin["29AAACB1234C1Z5"]; inv.InvTyp != "R" || inv.Rchrg != "N" || inv.Val != 144000 || len(inv.Itms) != 2 ||
		inv.Itms[0].ItmDet.Rt != 5 || inv.Itms[1].ItmDet.Iamt != 18000 || inv.Idt != "10-10-2026" {
		t.Errorf("4A %s", js(inv))
	}
	if inv := byCtin["27AABCU9603R1ZM"]; inv.Rchrg != "Y" || inv.Itms[0].ItmDet.Camt != 5000 || inv.Val != 200000 {
		t.Errorf("4B reverse charge %s", js(inv))
	}
	if inv := byCtin["29AAGCS1234A1Z1"]; inv.InvTyp != "SEWOP" || inv.Itms[0].ItmDet.Iamt != 0 || inv.Itms[0].ItmDet.Rt != 18 {
		t.Errorf("6B SEZ %s", js(inv))
	}
	if inv := byCtin["24AAACD1234E1Z2"]; inv.InvTyp != "DE" || inv.Itms[0].ItmDet.Iamt != 54000 {
		t.Errorf("6C deemed export %s", js(inv))
	}
	if _, ok := byCtin["27AAACB1111C1Z1"]; ok {
		t.Error("an invoice with only nil/non-GST lines must not appear in B2B")
	}

	// B2CL: only the ₹1.77 lakh inter-state invoice.
	if len(g.B2CL) != 1 || g.B2CL[0].Pos != "07" || g.B2CL[0].Inv[0].Inum != "INV/26-27/00005" || g.B2CL[0].Inv[0].Itms[0].ItmDet.Iamt != 2700000 {
		t.Errorf("b2cl %s", js(g.B2CL))
	}

	// B2CS: credit notes to walk-in buyers net into the same POS/rate row.
	if len(g.B2CS) != 2 {
		t.Fatalf("b2cs %s", js(g.B2CS))
	}
	if r := g.B2CS[0]; r.Pos != "07" || r.SplyTy != "INTER" || r.Typ != "OE" || r.Txval != 40000 || r.Iamt != 7200 {
		t.Errorf("b2cs inter %s", js(r))
	}
	if r := g.B2CS[1]; r.Pos != "27" || r.SplyTy != "INTRA" || r.Txval != 6000 || r.Camt != 150 || r.Samt != 150 {
		t.Errorf("b2cs intra %s", js(r))
	}

	// CDNR
	if len(g.CDNR) != 1 || g.CDNR[0].Ctin != "29AAACB1234C1Z5" || g.CDNR[0].Nt[0].Ntty != "C" || g.CDNR[0].Nt[0].Val != 21000 {
		t.Errorf("cdnr %s", js(g.CDNR))
	}

	// CDNUR: against the B2CL invoice (decided by the original, not the note) and against an export.
	if len(g.CDNUR) != 2 || g.CDNUR[0].Typ != "B2CL" || g.CDNUR[0].Pos != "07" || g.CDNUR[0].Itms[0].ItmDet.Iamt != 180000 ||
		g.CDNUR[1].Typ != "EXPWP" || g.CDNUR[1].Pos != "" {
		t.Errorf("cdnur %s", js(g.CDNUR))
	}

	// EXP
	if len(g.EXP) != 2 || g.EXP[0].ExpTyp != "WOPAY" || g.EXP[1].ExpTyp != "WPAY" {
		t.Fatalf("exp %s", js(g.EXP))
	}
	if e := g.EXP[1].Inv[0]; e.Sbpcode != "INBOM4" || e.Sbnum != "1234567" || e.Sbdt != "12-10-2026" || e.Itms[0].Iamt != 20000 {
		t.Errorf("exp wpay %s", js(e))
	}
	if e := g.EXP[0].Inv[0]; e.Sbnum != "" || e.Itms[0].Txval != 300000 || e.Itms[0].Iamt != 0 {
		t.Errorf("exp wopay %s", js(e))
	}

	// Table 8
	want8 := map[string]NilRow{
		"INTRB2B":  {SplyTy: "INTRB2B", ExptAmt: 5000},
		"INTRAB2B": {SplyTy: "INTRAB2B", NilAmt: 50000, NgsupAmt: 30000},
		"INTRAB2C": {SplyTy: "INTRAB2C", ExptAmt: 7000},
	}
	if len(g.Nil.Inv) != len(want8) {
		t.Fatalf("table 8 %s", js(g.Nil))
	}
	for _, r := range g.Nil.Inv {
		if want8[r.SplyTy] != r {
			t.Errorf("table 8 row %s", js(r))
		}
	}

	// Table 12 split: registered recipients vs B2C (incl. exports).
	hsn := func(rows []HSNRow, code string) *HSNRow {
		for i := range rows {
			if rows[i].HsnSc == code {
				return &rows[i]
			}
		}
		return nil
	}
	if h := hsn(g.HSN.B2B, "1905"); h != nil {
		t.Errorf("B2B 1905 is fully reversed by the credit note and must be dropped: %s", js(h))
	}
	if h := hsn(g.HSN.B2B, "8471"); h == nil || h.Txval != 800000 || h.Iamt != 54000 || h.Qty != 2 || h.Uqc != "NOS" {
		t.Errorf("hsn_b2b 8471 %s", js(h))
	}
	if h := hsn(g.HSN.B2B, "9965"); h == nil || h.Uqc != "NA" || h.Qty != 0 || h.Camt != 5000 {
		t.Errorf("hsn_b2b service %s", js(h))
	}
	if h := hsn(g.HSN.B2B, "1006"); h == nil || h.Rt != 0 || h.Uqc != "KGS" || h.Txval != 50000 {
		t.Errorf("hsn_b2b nil-rated %s", js(h))
	}
	if h := hsn(g.HSN.B2C, "8528"); h == nil || h.Txval != 14040000 || h.Iamt != 2527200 || h.Qty != 0 {
		t.Errorf("hsn_b2c 8528 %s", js(h))
	}
	if h := hsn(g.HSN.B2C, "6109"); h == nil || h.Txval != 600000 || h.Iamt != 15000 || h.Qty != 1 {
		t.Errorf("hsn_b2c exports %s", js(h))
	}
	if h := hsn(g.HSN.B2C, "0401"); h == nil || h.Uqc != "LTR" {
		t.Errorf("hsn_b2c milk %s", js(h))
	}
	if h := hsn(g.HSN.B2C, "1905"); h == nil || h.Txval != 6000 || h.Camt != 150 {
		t.Errorf("hsn_b2c 1905 %s", js(h))
	}

	// Table 13: numeric range with the gap at 00009 counted as cancelled.
	dd := g.DocIssue.DocDet
	if len(dd) != 2 || dd[0].DocNum != 1 || dd[1].DocNum != 5 {
		t.Fatalf("docs %s", js(dd))
	}
	if r := dd[0].Docs[0]; r.From != "INV/26-27/00001" || r.To != "INV/26-27/00012" || r.Totnum != 12 || r.Cancel != 2 || r.NetIssue != 10 {
		t.Errorf("invoice series %s", js(r))
	}
	if r := dd[1].Docs[0]; r.From != "CN/26-27/00001" || r.To != "CN/26-27/00005" || r.Totnum != 5 || r.Cancel != 0 || r.NetIssue != 5 {
		t.Errorf("credit note series %s", js(r))
	}

	// Summary: the other GSTIN's invoice is excluded; liability excludes RCM.
	if g.Summary.Excluded != 1 || g.Summary.Cancelled != 1 || g.Summary.Liability.IGST != 2614200 || g.Summary.Liability.CGST != 150 {
		t.Errorf("summary %s", js(g.Summary))
	}
	all := strings.Join(g.Warnings, "\n")
	for _, w := range []string{"INV/26-27/00011 has no shipping bill", "1 number(s) between INV/26-27/00001 and INV/26-27/00012"} {
		if !strings.Contains(all, w) {
			t.Errorf("missing warning %q in\n%s", w, all)
		}
	}

	// The portal file has no UI fields.
	p := g.Portal()
	if _, ok := p["summary"]; ok {
		t.Error("portal JSON must not carry the summary")
	}
	if p["version"] != GSTNVersion || p["hash"] != "hash" || p["fp"] != "102026" {
		t.Errorf("portal header %v", p)
	}
}

func TestDocSeriesNumericCompare(t *testing.T) {
	// Lexically "INV/9" > "INV/10"; numerically it is the start of the range.
	b := NewGSTR1(GSTR1Config{GSTIN: ourGSTIN, FP: "102026", State: "27"})
	for _, n := range []string{"INV/10", "INV/9", "INV/11", "A7", "B"} {
		_ = b.Add(context.Background(), doc(n, n, "sale", "", "27", ls(line("1905", 500, 100, false))))
	}
	g := b.Build()
	var got []DocRange
	for _, d := range g.DocIssue.DocDet {
		got = append(got, d.Docs...)
	}
	if len(got) != 3 {
		t.Fatalf("series %+v", got)
	}
	found := false
	for _, r := range got {
		if r.From == "INV/9" && r.To == "INV/11" && r.Totnum == 3 && r.NetIssue == 3 {
			found = true
		}
	}
	if !found {
		t.Fatalf("numeric range not found: %+v", got)
	}
}

func TestGSTR1CSV(t *testing.T) {
	g := buildFixtureGSTR1(t)
	for _, sec := range GSTR1Sections {
		var buf bytes.Buffer
		if err := WriteGSTR1SectionCSV(&buf, g, sec); err != nil {
			t.Fatalf("%s: %v", sec, err)
		}
		if lines := strings.Count(buf.String(), "\n"); lines < 2 {
			t.Errorf("section %s has no rows:\n%s", sec, buf.String())
		}
	}
	var buf bytes.Buffer
	_ = WriteGSTR1SectionCSV(&buf, g, "b2b")
	if !strings.Contains(buf.String(), "29AAACB1234C1Z5,INV/26-27/00001,10-10-2026,1440.00,29,N,R,18,1000.00,180.00,0.00,0.00,0.00") {
		t.Errorf("b2b csv:\n%s", buf.String())
	}
	if err := WriteGSTR1SectionCSV(&buf, g, "nope"); err == nil {
		t.Error("unknown section must fail")
	}
	buf.Reset()
	if err := WriteGSTR1CSV(&buf, g); err != nil || !strings.Contains(buf.String(), "cdnur,B2CL 07,CN/26-27/00003") {
		t.Errorf("combined csv %v:\n%s", err, buf.String())
	}
}

func TestUQCAndAmounts(t *testing.T) {
	for in, want := range map[string]string{"PCS": "NOS", "pcs": "NOS", "KG": "KGS", "LTR": "LTR", "BOX": "BOX", "DOZ": "DOZ",
		"MTR": "MTR", "GM": "GMS", "ML": "MLT", "weird": "OTH"} {
		if got := UQC(in); got != want {
			t.Errorf("UQC(%q) = %s, want %s", in, got, want)
		}
	}
	b, _ := json.Marshal(struct{ A, B, C Rs }{12345, -5, 0})
	if string(b) != `{"A":123.45,"B":-0.05,"C":0.00}` {
		t.Errorf("rupee JSON %s", b)
	}
}
