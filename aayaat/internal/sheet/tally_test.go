package sheet

import (
	"bytes"
	"encoding/binary"
	"os"
	"strings"
	"testing"
	"unicode/utf16"
)

func readTable(t *testing.T, wb Workbook, name string) []map[string]string {
	t.Helper()
	var head []string
	var out []map[string]string
	err := wb.Each(name, func(n int, c []string) error {
		if n == 1 {
			head = c
			return nil
		}
		m := map[string]string{}
		for i, h := range head {
			m[h] = Cell(c, i)
		}
		out = append(out, m)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func openTallySample(t *testing.T) Workbook {
	t.Helper()
	b, err := os.ReadFile("../../testdata/tally/Master-and-Vouchers.xml")
	if err != nil {
		t.Fatal(err)
	}
	wb, err := Open(bytes.NewReader(b), "Master-and-Vouchers.xml")
	if err != nil {
		t.Fatal(err)
	}
	return wb
}

func TestTallyMasters(t *testing.T) {
	wb := openTallySample(t)
	if ks, ok := wb.(KindSheets); !ok || ks.DefaultSheet("parties") != TallyPartyLedgers || ks.DefaultSheet("invoices") != TallyVouchers {
		t.Fatal("tally workbook must pick its sheet per kind")
	}
	if src, ok := wb.(Source); !ok || src.Source() != "tally" {
		t.Fatal("source")
	}

	parties := readTable(t, wb, TallyPartyLedgers)
	if len(parties) != 2 {
		t.Fatalf("party ledgers %v", parties)
	}
	sh := parties[0]
	// A custom group under Sundry Debtors is still a party; Tally's negative = Dr.
	want := map[string]string{"Party Name": "Sharma Agencies - Pune", "Group": "Sundry Debtors - Pune", "GSTIN No": "27AAKFS4821M1ZQ",
		"PAN": "AAKFS4821M", "Mobile": "9822011223", "Address": "Shop 4, Laxmi Road, Near Mandai, Pune", "Pincode": "411002",
		"State": "Maharashtra", "Opening Bal": "12450.00 Dr", "Credit Limit": "50000.00", "Credit Days": "30"}
	for k, v := range want {
		if sh[k] != v {
			t.Errorf("%s = %q, want %q", k, sh[k], v)
		}
	}
	pt := parties[1] // Tally Prime 3 mailing / GST registration blocks
	if pt["GSTIN No"] != "24AAHCM5521R1Z2" || pt["State"] != "Gujarat" || pt["Address"] != "Plot 12, GIDC Estate Phase 2, Vapi" ||
		pt["Opening Bal"] != "18900.00 Cr" || pt["Mobile"] != "0260-2431122" || pt["Credit Days"] != "45" {
		t.Errorf("prime party %v", pt)
	}

	others := readTable(t, wb, TallyOtherLedgers)
	byName := map[string]map[string]string{}
	for _, o := range others {
		byName[o["Ledger Name"]] = o
	}
	if byName["Cash"]["Opening Bal"] != "15000.00 Dr" || byName["Cash"]["Group"] != "Cash-in-Hand" ||
		byName["Proprietor's Capital"]["Opening Bal"] != "255950.00 Cr" || byName["Output CGST"]["Primary Group"] != "Current Liabilities" {
		t.Errorf("other ledgers %v", others)
	}
	if byName["Sales @ 18%"]["Opening Bal"] != "" {
		t.Errorf("empty opening stays empty: %v", byName["Sales @ 18%"])
	}

	bills := readTable(t, wb, TallyBills)
	if len(bills) != 3 || bills[0]["Bill No"] != "GT/2526/1041" || bills[0]["Amount"] != "8250.00 Dr" || bills[0]["Bill Date"] != "20260210" ||
		bills[0]["Credit Days"] != "30" || bills[2]["Party Name"] != "Patel Distributors" || bills[2]["Amount"] != "18900.00 Cr" {
		t.Errorf("bills %v", bills)
	}

	items := readTable(t, wb, TallyStockItems)
	if len(items) != 3 {
		t.Fatalf("items %v", items)
	}
	pg := items[0]
	for k, v := range map[string]string{"Item Name": "Parle-G 100g", "Item Code": "I0001", "Category": "Biscuits", "Brand": "Parle",
		"Unit": "Pcs", "Alt Unit": "Box", "Conversion": "48", "HSN Code": "1905", "GST %": "5", "MRP": "10.00", "Sale Rate": "10.00",
		"Purc Rate": "8.50", "Batch Wise": "Yes", "Opening Qty": "1248", "Opening Rate": "8.50"} {
		if pg[k] != v {
			t.Errorf("item %s = %q, want %q", k, pg[k], v)
		}
	}
	if items[1]["Alt Unit"] != "" || items[2]["GST %"] != "28" || items[2]["Cess %"] != "5" {
		t.Errorf("items %v", items[1:])
	}

	batches := readTable(t, wb, TallyStockBatches)
	if len(batches) != 3 {
		t.Fatalf("batches %v", batches)
	}
	if b := batches[0]; b["Item Code"] != "I0001" || b["Godown"] != "Main Location" || b["Batch"] != "PG2601" || b["Qty"] != "1200" ||
		b["Rate"] != "8.50" || b["Value"] != "10200.00" || b["Expiry"] != "31-Dec-2026" || b["Mfg Date"] != "1-Jan-2026" {
		t.Errorf("batch %v", b)
	}
	if b := batches[2]; b["Item Name"] != "Tata Salt 1kg" || b["Qty"] != "300" || b["Godown"] != "Main Location" {
		t.Errorf("item without batches: one line from its opening balance, got %v", b)
	}
}

func TestTallyVouchers(t *testing.T) {
	v := readTable(t, openTallySample(t), TallyVouchers)
	// Receipt and the cancelled sale are not invoice history.
	if len(v) != 4 {
		t.Fatalf("vouchers %v", v)
	}
	check := func(i int, want map[string]string) {
		t.Helper()
		for k, w := range want {
			if v[i][k] != w {
				t.Errorf("voucher %d %s = %q, want %q", i, k, v[i][k], w)
			}
		}
	}
	check(0, map[string]string{"Voucher Type": "sale", "Voucher No": "GT/2627/0001", "Date": "20260401", "Party Name": "Sharma Agencies - Pune",
		"Taxable Value": "10000.00", "CGST": "900.00", "SGST": "900.00", "IGST": "0.00", "Total": "11800.00", "Place of Supply": "Maharashtra"})
	// Cash sale: no party, round-off kept.
	check(1, map[string]string{"Party Name": "", "Taxable Value": "423.73", "Round Off": "0.09", "Total": "445.00"})
	check(2, map[string]string{"Voucher Type": "purchase", "Party Name": "Patel Distributors", "IGST": "900.00", "Taxable Value": "5000.00",
		"Reference": "PD/778", "Party GSTIN": "24AAHCM5521R1Z2"})
	check(3, map[string]string{"Voucher Type": "sale_return", "Taxable Value": "1000.00", "CGST": "90.00", "Total": "1180.00"})
}

func TestTallyUTF16AndControlChars(t *testing.T) {
	xml := `<ENVELOPE><BODY><DATA><TALLYMESSAGE>
<GROUP NAME="Sundry Debtors"><PARENT>&#4; Primary</PARENT></GROUP>
<LEDGER NAME="राम स्टोर्स"><PARENT>Sundry Debtors</PARENT><OPENINGBALANCE>-500.00</OPENINGBALANCE></LEDGER>
</TALLYMESSAGE></DATA></BODY></ENVELOPE>`
	u := utf16.Encode([]rune(xml))
	b := []byte{0xFF, 0xFE}
	for _, ch := range u {
		b = binary.LittleEndian.AppendUint16(b, ch)
	}
	wb, err := Open(bytes.NewReader(b), "Master.xml")
	if err != nil {
		t.Fatal(err)
	}
	p := readTable(t, wb, TallyPartyLedgers)
	if len(p) != 1 || p[0]["Party Name"] != "राम स्टोर्स" || p[0]["Opening Bal"] != "500.00 Dr" {
		t.Fatalf("%v", p)
	}
	if _, err := Open(strings.NewReader("<html>not tally</html>"), "x.xml"); err == nil {
		t.Fatal("non-Tally XML must be rejected")
	}
}
