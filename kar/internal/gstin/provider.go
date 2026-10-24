// Package gstin verifies GSTINs: checksum first, then a live taxpayer search
// through the gsp.Client (simulator or a GST Suvidha Provider), with a 7-day cache.
package gstin

import (
	"context"

	"github.com/taraju/kar/internal/gsp"
	"github.com/taraju/mool/gst"
)

// Sources of a Result.
const (
	SourceGSP          = "gsp"           // live portal search
	SourceCache        = "cache"         // an earlier live search, still fresh
	SourceChecksumOnly = "checksum_only" // format and checksum only; NOT verified on the portal
)

// Result is the verification answer (shape fixed in ARCHITECTURE.md §6).
//
// Valid means the GSTIN is well-formed with a correct checksum. Only
// PortalVerified=true means the GST portal confirmed it; when the live lookup
// is not configured or fails, Source is "checksum_only" and PortalVerified is
// false — callers must show "format valid, not portal-verified". Status comes
// from the portal; B2BAllowed is false for Cancelled / Suspended / Inactive.
type Result struct {
	GSTIN            string `json:"gstin"`
	Valid            bool   `json:"valid"`
	PortalVerified   bool   `json:"portal_verified"`
	LegalName        string `json:"legal_name"`
	TradeName        string `json:"trade_name"`
	Status           string `json:"status"` // Active | Cancelled | Suspended | Inactive ("" when unknown)
	B2BAllowed       bool   `json:"b2b_allowed"`
	StateCode        string `json:"state_code"`
	Address          string `json:"address"`
	RegistrationType string `json:"registration_type"`
	Source           string `json:"source"` // gsp | cache | checksum_only
	Provider         string `json:"provider,omitempty"`
	Message          string `json:"message,omitempty"`
}

// Provider performs a live taxpayer lookup.
type Provider interface {
	Name() string
	Lookup(ctx context.Context, gstin string) (Result, error)
}

// ChecksumOnly needs no contract: it validates the checksum and derives the
// state from the first two digits. Names and status stay empty.
type ChecksumOnly struct{}

func (ChecksumOnly) Name() string { return SourceChecksumOnly }

func (ChecksumOnly) Lookup(_ context.Context, g string) (Result, error) {
	return checksumResult(g), nil
}

func checksumResult(g string) Result {
	g = gst.Normalize(g)
	r := Result{GSTIN: g, Source: SourceChecksumOnly}
	if err := gst.ValidateGSTIN(g); err != nil {
		r.Message = err.Error()
		return r
	}
	r.Valid = true
	r.B2BAllowed = true // unknown status does not block billing
	r.StateCode = gst.StateCode(g)
	r.Message = "format and checksum valid; not verified on the GST portal"
	return r
}

// GSP adapts a gsp.Client's taxpayer search to Provider.
type GSP struct{ Client gsp.Client }

func (p GSP) Name() string { return "gsp:" + p.Client.Name() }

func (p GSP) Lookup(ctx context.Context, g string) (Result, error) {
	tp, err := p.Client.SearchGSTIN(ctx, g)
	if err != nil {
		return Result{}, err
	}
	return FromTaxpayer(tp, p.Client.Name()), nil
}

// FromTaxpayer builds a portal-verified Result.
func FromTaxpayer(tp gsp.Taxpayer, provider string) Result {
	g := gst.Normalize(tp.GSTIN)
	r := Result{GSTIN: g, Valid: true, PortalVerified: true, LegalName: tp.LegalName, TradeName: tp.TradeName,
		Status: gsp.NormalizeStatus(tp.Status), StateCode: gst.StateCode(g), Address: tp.Address,
		RegistrationType: tp.RegistrationType, Source: SourceGSP, Provider: provider}
	applyStatus(&r)
	return r
}

// applyStatus derives B2BAllowed and the warning from Status.
func applyStatus(r *Result) {
	switch r.Status {
	case "Cancelled", "Suspended", "Inactive":
		r.B2BAllowed = false
		r.Message = "GSTIN is " + r.Status + " on the GST portal: do not bill B2B to it or claim ITC on its invoices"
	default:
		r.B2BAllowed = true
	}
}

// ParseGSPResponse maps a GSTN-style taxpayer search payload to a Result.
func ParseGSPResponse(g string, body []byte) (Result, error) {
	tp, err := gsp.ParseTaxpayer(g, body)
	if err != nil {
		return Result{}, err
	}
	return FromTaxpayer(tp, "http"), nil
}
