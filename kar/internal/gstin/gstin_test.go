package gstin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/maapan/kar/internal/gsp"
	"github.com/maapan/mool/gst"
)

func TestChecksumOnly(t *testing.T) {
	r, _ := ChecksumOnly{}.Lookup(context.Background(), " 27aapfu0939f1zv ")
	if !r.Valid || r.StateCode != "27" || r.GSTIN != "27AAPFU0939F1ZV" || r.Source != SourceChecksumOnly || r.PortalVerified {
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
		r.RegistrationType != "Regular" || r.StateCode != "27" || r.Source != SourceGSP || !r.PortalVerified || !r.B2BAllowed {
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

type failing struct{ gsp.Client }

func (failing) Name() string { return "down" }
func (failing) SearchGSTIN(context.Context, string) (gsp.Taxpayer, error) {
	return gsp.Taxpayer{}, &gsp.Error{Kind: gsp.KindUnavailable, Message: "timeout"}
}

type errClient struct{ gsp.Client }

func (errClient) Name() string { return "err" }
func (errClient) SearchGSTIN(context.Context, string) (gsp.Taxpayer, error) {
	return gsp.Taxpayer{}, errors.New("connection refused")
}

func cancelled(t *testing.T) string {
	for _, c := range "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ" {
		if g := "27AAPFU0939F9Z" + string(c); gst.ValidateGSTIN(g) == nil {
			return g
		}
	}
	t.Fatal("no cancelled test GSTIN")
	return ""
}

// The audit bug: a provider failure must never be reported as portal-verified.
func TestVerifyProviderFailureIsChecksumOnly(t *testing.T) {
	for _, c := range []gsp.Client{failing{}, errClient{}} {
		s := &Service{Provider: GSP{Client: c}, TTL: time.Hour}
		r, err := s.Verify(context.Background(), "27AAPFU0939F1ZV")
		if err != nil {
			t.Fatal(err)
		}
		if r.Source != SourceChecksumOnly || r.PortalVerified || !r.Valid || r.Message == "" {
			t.Fatalf("%s: %+v", c.Name(), r)
		}
	}
}

func TestVerifySurfacesCancelledAndSuspended(t *testing.T) {
	sim := gsp.NewSimulator(gsp.NewMemStore(), []byte("k"))
	s := &Service{Provider: GSP{Client: sim}, TTL: time.Hour}
	r, err := s.Verify(context.Background(), cancelled(t))
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "Cancelled" || r.B2BAllowed || !r.PortalVerified || r.Source != SourceGSP || r.Message == "" {
		t.Fatalf("cancelled must block B2B: %+v", r)
	}
	r, _ = s.Verify(context.Background(), "27AAPFU0939F1ZV")
	if r.Status != "Active" || !r.B2BAllowed || !r.PortalVerified {
		t.Fatalf("active: %+v", r)
	}
}
