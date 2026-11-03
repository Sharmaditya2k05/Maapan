package validate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maapan/aayaat/internal/mapping"
	"github.com/maapan/aayaat/internal/sheet"
)

// runSample pushes a testdata file through the same steps as a job — open,
// pick sheet, detect the header row, auto-map, validate every row — and
// returns the results keyed by the first mapped cell (name / code / number).
func runSample(t *testing.T, file, kind string, opts Options) (map[string]string, []Result) {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	wb, err := sheet.Open(f, file)
	if err != nil {
		t.Fatal(err)
	}
	sh := wb.Sheets()[0]
	if ks, ok := wb.(sheet.KindSheets); ok {
		sh = ks.DefaultSheet(kind)
	}
	var all [][]string
	if err := wb.Each(sh, func(_ int, c []string) error { all = append(all, c); return nil }); err != nil {
		t.Fatal(err)
	}
	hr := mapping.DetectHeaderRow(kind, all[:min(15, len(all))])
	m := mapping.Suggest(kind, all[hr-1])
	if miss := mapping.MissingRequired(kind, m); len(miss) > 0 {
		t.Fatalf("%s: required fields not auto-mapped: %v (mapping %v)", file, miss, m)
	}
	index, unknown := mapping.Resolve(m, all[hr-1])
	if len(unknown) > 0 {
		t.Fatal(unknown)
	}
	v := New(kind, index, opts)
	var out []Result
	for i, row := range all[hr:] {
		if sheet.IsBlank(row) {
			continue
		}
		out = append(out, v.Row(hr+i+1, row))
	}
	return m, out
}

func TestSampleMargPartyMaster(t *testing.T) {
	m, rows := runSample(t, "marg/PartyMaster.csv", mapping.KindParties, Options{AsOf: "2026-04-01"})
	for field, header := range map[string]string{"name": "Party Name", "code": "Code", "phone": "Mobile", "pan": "PAN",
		"credit_limit": "Credit Limit", "credit_days": "Credit Days", "address2": "Address 2", "pincode": "Pincode"} {
		if m[field] != header {
			t.Errorf("auto-map %s → %q, want %q", field, m[field], header)
		}
	}
	if len(rows) != 10 {
		t.Fatalf("rows %d", len(rows))
	}
	valid := 0
	for _, r := range rows {
		if len(r.Errors) == 0 {
			valid++
		}
	}
	// Branch ledgers with one GSTIN, landlines, two numbers in a cell and a
	// junk phone/email all import (with warnings); only the duplicate name and
	// the GSTIN with a bad checksum fail. A PAN that disagrees with the GSTIN
	// is a warning.
	if valid != 8 {
		for i, r := range rows {
			t.Logf("row %d: %v %v", i, r.Errors, r.Warnings)
		}
		t.Fatalf("valid rows = %d, want 8", valid)
	}
	nashik := rows[1].Record.(PartyRecord)
	if nashik.Phone != "+912532571234" || nashik.CreditDays != 30 || nashik.CreditLimitPaise != 2500000 || nashik.OpeningBalancePaise != 320050 {
		t.Fatalf("%+v", nashik)
	}
	if p := rows[3].Record.(PartyRecord); p.Type != "supplier" || p.OpeningType != "cr" || p.OpeningBalancePaise != 1890000 || p.StateCode != "24" {
		t.Fatalf("patel %+v", p)
	}
	has(t, rows[5].Warnings, "phone", "email")
	has(t, rows[7].Errors, "duplicate party name (same as row 7)")
	has(t, rows[8].Warnings, "differs from the PAN inside GSTIN")
	has(t, rows[9].Errors, "gstin:", "does not match GSTIN state")
}

func TestSampleMargItemMasterXLSX(t *testing.T) {
	store := "55555555-5555-5555-5555-555555555555"
	m, rows := runSample(t, "marg/ItemMaster.xlsx", mapping.KindItems, Options{StoreID: store})
	for field, header := range map[string]string{"brand": "Company", "category": "Item Group", "alt_unit": "Alt Unit", "alt_factor": "Conversion",
		"cess_rate": "Cess %", "track_batches": "Batch Wise", "reorder_level": "Min Stock", "opening_qty": "Cl. Stock"} {
		if m[field] != header {
			t.Errorf("auto-map %s → %q, want %q (all %v)", field, m[field], header, m)
		}
	}
	if len(rows) != 7 {
		t.Fatalf("rows %d", len(rows))
	}
	pg := rows[0].Record.(ItemRecord)
	if pg.ExternalRef != "I0001" || pg.Unit != "PCS" || len(pg.AltUnits) != 1 || pg.AltUnits[0].Factor != 48 || !pg.TrackBatches ||
		pg.OpeningQty != 1258 || pg.OpeningCostPaise != 850 || pg.StoreID != store || pg.TaxRateBP != 500 {
		t.Fatalf("%+v", pg)
	}
	if rows[1].Record.(ItemRecord).Unit != "PCS" { // "Nos"
		t.Fatal("Nos → PCS")
	}
	has(t, rows[2].Warnings, "above MRP")
	// A pre-September-2025 master still carries the retired 12 % slab.
	has(t, rows[2].Errors, "has not been a GST slab since 22 Sep 2025")
	if c := rows[4].Record.(ItemRecord); c.CessRateBP != 500 || c.TaxRateBP != 2800 {
		t.Fatalf("%+v", c)
	}
	has(t, rows[5].Errors, "hsn:")
	has(t, rows[6].Errors, "not a GST slab")
}

func TestSampleMargStockBatch(t *testing.T) {
	main, counter, cold := "66666666-6666-6666-6666-666666666666", "77777777-7777-7777-7777-777777777777", "88888888-8888-8888-8888-888888888888"
	_, rows := runSample(t, "marg/StockBatch.csv", mapping.KindOpeningStock, Options{
		StoreMap: map[string]string{"Main Godown": main, "Counter": counter, "Cold Room": cold}})
	if len(rows) != 8 {
		t.Fatalf("rows %d", len(rows))
	}
	a, b := rows[0].Record.(StockRecord), rows[1].Record.(StockRecord)
	if a.StoreID != main || b.StoreID != counter || a.Qty != 1200 || a.MfgDate != "2026-01-31" || a.ExpiryDate != "2026-12-31" || a.MRPPaise != 1000 {
		t.Fatalf("%+v %+v", a, b)
	}
	if s := rows[2].Record.(StockRecord); s.UnitCostPaise != 2500 || s.Qty != 300 { // cost from value ÷ qty
		t.Fatalf("salt %+v", s)
	}
	has(t, rows[4].Errors, "only a year")
	if !rows[5].Skip {
		t.Fatal("zero qty skipped")
	}
	has(t, rows[6].Errors, `store "Back Store" is not mapped`)
	has(t, rows[7].Errors, "duplicate store + item + batch")
}

func TestSampleMargOutstanding(t *testing.T) {
	_, rows := runSample(t, "marg/Outstanding.csv", mapping.KindOpenBills, Options{AsOf: "2026-04-01"})
	if len(rows) != 7 {
		t.Fatalf("rows %d", len(rows))
	}
	b := rows[0].Record.(BillRecord)
	if b.PartyExternalRef != "C0001" || b.BillNo != "GT/2526/1041" || b.AmountPaise != 825000 || b.BillAmountPaise != 1180000 || b.DueDate != "2026-03-12" {
		t.Fatalf("%+v", b)
	}
	if !rows[4].Skip {
		t.Fatal("settled bill skipped")
	}
	has(t, rows[5].Errors, "due_date is before bill_date")
	has(t, rows[6].Errors, "duplicate bill")
}

func TestSampleMargSaleRegister(t *testing.T) {
	_, rows := runSample(t, "marg/SaleRegister.csv", mapping.KindInvoices, Options{DocType: "sale", AsOf: "2026-04-01"})
	if len(rows) != 5 {
		t.Fatalf("rows %d", len(rows))
	}
	for i := 0; i < 3; i++ {
		if len(rows[i].Errors) != 0 {
			t.Fatalf("row %d: %v", i, rows[i].Errors)
		}
	}
	if r := rows[1].Record.(InvoiceRecord); r.RoundOffPaise != 9 || r.PartyExternalRef != "" {
		t.Fatalf("cash sale %+v", r)
	}
	if r := rows[2].Record.(InvoiceRecord); r.IGSTPaise != 90000 || r.PlaceOfSupply != "29" {
		t.Fatalf("igst %+v", r)
	}
	has(t, rows[3].Errors, "but total is")
	has(t, rows[4].Errors, "either IGST or CGST")
}

func TestSampleMargHTMLXLS(t *testing.T) {
	_, rows := runSample(t, "marg/PartyMaster-html.xls", mapping.KindParties, Options{})
	if len(rows) != 2 || len(rows[0].Errors)+len(rows[1].Errors) != 0 {
		t.Fatalf("%+v", rows)
	}
	if p := rows[1].Record.(PartyRecord); p.OpeningType != "cr" || p.OpeningBalancePaise != 750000 || p.Phone != "+912352220011" {
		t.Fatalf("%+v", p)
	}
}

func TestSampleTally(t *testing.T) {
	const file = "tally/Master-and-Vouchers.xml"
	store := "99999999-9999-9999-9999-999999999999"

	_, parties := runSample(t, file, mapping.KindParties, Options{AsOf: "2026-04-01"})
	if len(parties) != 2 {
		t.Fatalf("parties %d", len(parties))
	}
	for _, p := range parties {
		if len(p.Errors) != 0 {
			t.Fatal(p.Errors)
		}
	}
	sh := parties[0].Record.(PartyRecord)
	if sh.ExternalRef != "name:sharma agencies - pune" || sh.Type != "customer" || sh.OpeningType != "dr" || sh.OpeningBalancePaise != 1245000 ||
		sh.CreditLimitPaise != 5000000 || sh.CreditDays != 30 || sh.PAN != "AAKFS4821M" || sh.Address != "Shop 4, Laxmi Road, Near Mandai, Pune, 411002" {
		t.Fatalf("%+v", sh)
	}
	if pt := parties[1].Record.(PartyRecord); pt.Type != "supplier" || pt.OpeningType != "cr" || pt.StateCode != "24" || pt.Phone != "+912602431122" {
		t.Fatalf("%+v", pt)
	}

	_, ledgers := runSample(t, file, mapping.KindOpeningBalances, Options{AsOf: "2026-04-01", AccountMap: map[string]string{"Output CGST": "2210"}})
	var dr, cr int64
	codes := map[string]bool{}
	for _, l := range ledgers {
		if len(l.Errors) > 0 {
			t.Fatalf("ledger errors %v (%v)", l.Errors, l.Raw)
		}
		if l.Skip {
			continue
		}
		codes[l.Record.(BalanceRecord).AccountCode] = true
		if l.Side == "dr" {
			dr += l.Amount
		} else {
			cr += l.Amount
		}
	}
	if !codes["1100"] || !codes["1110"] || !codes["3100"] || !codes["2210"] || dr != 26500000 || cr != 25855000 {
		t.Fatalf("codes %v dr %d cr %d", codes, dr, cr)
	}

	_, items := runSample(t, file, mapping.KindItems, Options{})
	if len(items) != 3 || len(items[0].Errors) != 0 {
		t.Fatalf("%+v", items)
	}
	if it := items[0].Record.(ItemRecord); it.ExternalRef != "I0001" || it.AltUnits[0] != (AltUnit{"BOX", 48}) || it.MRPPaise != 1000 ||
		it.HSN != "1905" || it.Brand != "Parle" || it.Category != "Biscuits" {
		t.Fatalf("%+v", it)
	}
	if it := items[2].Record.(ItemRecord); it.TaxRateBP != 2800 || it.CessRateBP != 500 {
		t.Fatalf("%+v", it)
	}

	_, stock := runSample(t, file, mapping.KindOpeningStock, Options{StoreID: store})
	if len(stock) != 3 {
		t.Fatalf("stock %d", len(stock))
	}
	// One store: the godown column folds into params.store_id, so Main
	// Location and Counter of the same batch would collide — that is a real
	// duplicate the user must split with store_map.
	has(t, stock[1].Errors, "duplicate store + item + batch")
	if s := stock[0].Record.(StockRecord); s.ItemExternalRef != "I0001" || s.ExpiryDate != "2026-12-31" || s.MfgDate != "2026-01-01" || s.UnitCostPaise != 850 {
		t.Fatalf("%+v", s)
	}
	if s := stock[2].Record.(StockRecord); s.ItemExternalRef != "name:tata salt 1kg" || s.Qty != 300 {
		t.Fatalf("%+v", s)
	}
	_, split := runSample(t, file, mapping.KindOpeningStock, Options{StoreMap: map[string]string{"Main Location": store, "Counter": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}})
	for _, s := range split {
		if len(s.Errors) != 0 {
			t.Fatal(s.Errors)
		}
	}

	_, bills := runSample(t, file, mapping.KindOpenBills, Options{AsOf: "2026-04-01"})
	if len(bills) != 3 || bills[0].Record.(BillRecord).PartyExternalRef != "name:sharma agencies - pune" ||
		bills[0].Record.(BillRecord).DueDate != "2026-03-12" || bills[2].Record.(BillRecord).Type != "cr" {
		t.Fatalf("%+v", bills)
	}

	_, inv := runSample(t, file, mapping.KindInvoices, Options{AsOf: "2026-04-01"})
	if len(inv) != 4 {
		t.Fatalf("invoices %d", len(inv))
	}
	for _, r := range inv {
		if len(r.Errors) != 0 {
			t.Fatalf("%v %v", r.Errors, r.Raw)
		}
	}
	types := []string{}
	for _, r := range inv {
		types = append(types, r.Record.(InvoiceRecord).DocType)
	}
	if strings.Join(types, ",") != "sale,sale,purchase,sale_return" {
		t.Fatal(types)
	}
	if p := inv[2].Record.(InvoiceRecord); p.ExternalRef != "inv:purchase:P-0001:2026-04-02:name:patel distributors" || p.Reference != "PD/778" {
		t.Fatalf("%+v", p)
	}
}
