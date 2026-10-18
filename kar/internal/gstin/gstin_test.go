package gstin

import (
	"context"
	"testing"
)

func TestChecksumOnly(t *testing.T) {
	r, _ := ChecksumOnly{}.Lookup(context.Background(), " 27aapfu0939f1zv ")
	if !r.Valid || r.StateCode != "27" || r.GSTIN != "27AAPFU0939F1ZV" || r.Source != "checksum_only" {
		t.Fatalf("unexpected %+v", r)
	}
	r, _ = ChecksumOnly{}.Lookup(context.Background(), "27AAPFU0939F1ZA")
	if r.Valid || r.Message == "" {
		t.Fatalf("expected invalid with message: %+v", r)
	}
}

func TestParseGSPResponseWrapped(t *testing.T) {
	body := []byte(`{"success":true,"data":{"gstin":"27AAPFU0939F1ZV","lgnm":"ACME TRADERS PVT LTD",
		"tradeNam":"Acme Mart","sts":"ACTIVE","dty":"Regular",
		"pradr":{"addr":{"bno":"12","st":"MG Road","loc":"Andheri","dst":"Mumbai","stcd":"Maharashtra","pncd":"400069"}}}}`)
	r, err := ParseGSPResponse("27AAPFU0939F1ZV", body)
	if err != nil {
		t.Fatal(err)
	}
	if r.LegalName != "ACME TRADERS PVT LTD" || r.TradeName != "Acme Mart" || r.Status != "Active" ||
		r.RegistrationType != "Regular" || r.StateCode != "27" || r.Source != "gsp" {
		t.Fatalf("unexpected %+v", r)
	}
	if r.Address != "12, MG Road, Andheri, Mumbai, Maharashtra, 400069" {
		t.Fatalf("address %q", r.Address)
	}
}

func TestParseGSPResponseNotFound(t *testing.T) {
	if _, err := ParseGSPResponse("27AAPFU0939F1ZV", []byte(`{"error":"No record found"}`)); err == nil {
		t.Fatal("expected error")
	}
}
