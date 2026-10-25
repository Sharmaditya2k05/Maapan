package invoice

import (
	"testing"
	"time"

	"github.com/maapan/mool/httpx"
)

func TestFinancialYear(t *testing.T) {
	cases := map[string]string{"2026-03-31": "25-26", "2026-04-01": "26-27", "2027-01-15": "26-27", "2099-12-31": "99-00"}
	for in, want := range cases {
		d, _ := time.Parse(time.DateOnly, in)
		if got := FinancialYear(d); got != want {
			t.Errorf("FY(%s)=%s want %s", in, got, want)
		}
	}
}

func TestFormatNumber(t *testing.T) {
	if got := FormatNumber("INV", "26-27", 1); got != "INV/26-27/00001" || len(got) > MaxNumberLen {
		t.Fatalf("got %s", got)
	}
	if got := FormatNumber("MUM-", "26-27", 123456); got != "MUM-/26-27/123456" {
		t.Fatalf("got %s", got)
	}
}

func TestSettle(t *testing.T) {
	// Cash over the bill is change; nothing is owed.
	paid, change, credit, err := Settle(11800, []Tender{{Mode: "cash", AmountPaise: 12000}})
	if err != nil || paid != 11800 || change != 200 || credit != 0 {
		t.Fatalf("cash over: %d %d %d %v", paid, change, credit, err)
	}
	// Card + cash exactly.
	paid, change, credit, err = Settle(11800, []Tender{{Mode: "card", AmountPaise: 10000}, {Mode: "cash", AmountPaise: 1800}})
	if err != nil || paid != 11800 || change != 0 || credit != 0 {
		t.Fatalf("mixed: %d %d %d %v", paid, change, credit, err)
	}
	// Short payment is on account even without a credit tender.
	paid, change, credit, err = Settle(11800, []Tender{{Mode: "upi", AmountPaise: 5000}})
	if err != nil || paid != 5000 || change != 0 || credit != 6800 {
		t.Fatalf("short: %d %d %d %v", paid, change, credit, err)
	}
	// A card machine cannot give change.
	if _, _, _, err = Settle(11800, []Tender{{Mode: "card", AmountPaise: 12000}}); err == nil || httpx.AsError(err).Status != 422 {
		t.Fatalf("card over must fail: %v", err)
	}
	// Explicit credit tender larger than the remainder is clamped.
	paid, _, credit, err = Settle(11800, []Tender{{Mode: "cash", AmountPaise: 10000}, {Mode: "credit", AmountPaise: 5000}})
	if err != nil || paid != 10000 || credit != 1800 {
		t.Fatalf("credit clamp: %d %d %v", paid, credit, err)
	}
	// No tenders at all: everything is on account.
	if _, _, credit, _ = Settle(500, nil); credit != 500 {
		t.Fatalf("no tenders: credit %d", credit)
	}
}

func TestCreateInputValidate(t *testing.T) {
	ok := CreateInput{DocType: DocSale, Lines: []LineInput{{Name: "x"}}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := []CreateInput{
		{DocType: "quote", Lines: []LineInput{{Name: "x"}}},
		{DocType: DocPurchase, Lines: []LineInput{{Name: "x"}}},                                                         // supplier required
		{DocType: DocPurchase, PartyID: "p", SupplierInvoiceNo: "S1", Lines: []LineInput{{Name: "x"}}},                  // supplier date required
		{DocType: DocSale, SupplierInvoiceNo: "S1", SupplierInvoiceDate: "2026-10-01", Lines: []LineInput{{Name: "x"}}}, // not on sales
		{DocType: DocSaleReturn, Lines: []LineInput{{Name: "x"}}},                                                       // ref required
		{DocType: DocSale, InvoiceDate: "01-10-2026", Lines: []LineInput{{Name: "x"}}},
		{DocType: DocSale, SupplyCategory: "moon", Lines: []LineInput{{Name: "x"}}},
		{DocType: DocSale, Lines: []LineInput{{Name: "x"}}, Tenders: []Tender{{Mode: "cash", AmountPaise: 100, PaymentRequestID: "pr"}}},
		{DocType: DocSale, Lines: []LineInput{{Name: "x"}}, Tenders: []Tender{{Mode: "credit", AmountPaise: 1}, {Mode: "credit", AmountPaise: 1}}},
		{DocType: DocSale, Lines: []LineInput{{Name: "x"}}, Number: 5},
	}
	for i, b := range bad {
		if err := b.Validate(); err == nil {
			t.Errorf("case %d must fail: %+v", i, b)
		}
	}
	n := NumberingSetting{StoreID: "s", DocType: DocSale, Prefix: "mum"}
	if err := n.Validate(); err != nil || n.Prefix != "MUM" {
		t.Fatalf("numbering: %v %s", err, n.Prefix)
	}
	if err := (&NumberingSetting{StoreID: "s", DocType: DocSale, Prefix: "TOOLONG"}).Validate(); err == nil {
		t.Fatal("long prefix must fail")
	}
}

func TestDocumentDate(t *testing.T) {
	s := &Service{}
	now := time.Date(2026, 10, 24, 1, 0, 0, 0, time.UTC) // 06:30 IST on 24 Oct
	d, err := s.documentDate(CreateInput{}, now)
	if err != nil || d.Format(time.DateOnly) != "2026-10-24" {
		t.Fatalf("default: %v %v", d, err)
	}
	made := time.Date(2026, 10, 22, 20, 0, 0, 0, time.UTC) // 01:30 IST on 23 Oct
	d, err = s.documentDate(CreateInput{ClientCreatedAt: &made}, now)
	if err != nil || d.Format(time.DateOnly) != "2026-10-23" {
		t.Fatalf("offline bill keeps the day it was made (IST): %v %v", d, err)
	}
	if _, err = s.documentDate(CreateInput{InvoiceDate: "2026-10-25"}, now); err == nil {
		t.Fatal("future date must fail")
	}
}

func TestPaymentStatus(t *testing.T) {
	if paymentStatus(0, 100, 0) != "unpaid" || paymentStatus(40, 100, 0) != "partial" || paymentStatus(100, 100, 0) != "paid" || paymentStatus(60, 100, 40) != "paid" {
		t.Fatal("payment status")
	}
	if balance(100, 40, 60) != 0 || balance(100, 10, 20) != 70 {
		t.Fatal("balance")
	}
}
