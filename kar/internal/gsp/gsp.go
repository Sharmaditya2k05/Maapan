// Package gsp is kar's provider-agnostic gateway to the GST systems: the GSTN
// taxpayer search, the e-Invoice IRP (IRN generate / cancel / get) and the NIC
// e-Way Bill system, plus the GSTR-2B download.
//
// Two drivers implement Client:
//
//   - Simulator: deterministic, offline, keeps its state in kar's database. It
//     follows the official IRN rule, signs the QR code as a JWS with a local key
//     and validates payloads the way the IRP does, answering with NIC-style
//     error codes. It is the default outside prod and is refused in prod.
//   - HTTP: a configurable driver for a GST Suvidha Provider (OAuth2 client
//     credentials + per-GSTIN API user headers). Paths and header names come
//     from configuration and MUST be verified against the chosen GSP's sandbox.
//
// Payloads are passed as values that marshal to the official JSON (the
// compliance package builds them), so this package does not depend on any
// builder and the simulator sees exactly what the IRP would see.
package gsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Credentials are one GSTIN's portal API users (stored encrypted by kar/internal/creds).
type Credentials struct {
	GSTIN            string
	EInvoiceUsername string
	EInvoicePassword string
	EWayUsername     string
	EWayPassword     string
}

// Taxpayer is the GSTN search result.
type Taxpayer struct {
	GSTIN            string `json:"gstin"`
	LegalName        string `json:"legal_name"`
	TradeName        string `json:"trade_name"`
	Status           string `json:"status"` // Active | Cancelled | Suspended | Inactive
	RegistrationType string `json:"registration_type"`
	StateCode        string `json:"state_code"`
	Address          string `json:"address"`
}

// IRNResult is what the IRP returns for a registered document.
type IRNResult struct {
	IRN           string    `json:"irn"`
	AckNo         string    `json:"ack_no"`
	AckDt         time.Time `json:"ack_dt"`
	SignedInvoice string    `json:"signed_invoice,omitempty"`
	SignedQR      string    `json:"signed_qr,omitempty"`
	Status        string    `json:"status"` // ACT (active) | CNL (cancelled)
	EWBNo         string    `json:"ewb_no,omitempty"`
	EWBDate       time.Time `json:"ewb_date,omitempty"`
	EWBValidUpto  time.Time `json:"ewb_valid_upto,omitempty"`
	CancelDate    time.Time `json:"cancel_date,omitempty"`
}

// IRN statuses.
const (
	IRNActive    = "ACT"
	IRNCancelled = "CNL"
)

// IRN cancellation reason codes (IRP): 1 duplicate, 2 data entry mistake,
// 3 order cancelled, 4 others.
var IRNCancelReasons = map[int]string{1: "Duplicate", 2: "Data entry mistake", 3: "Order cancelled", 4: "Others"}

// EWB cancellation reason codes (NIC): 1 duplicate, 2 order cancelled,
// 3 data entry mistake, 4 others.
var EWBCancelReasons = map[int]string{1: "Duplicate", 2: "Order cancelled", 3: "Data entry mistake", 4: "Others"}

// CancelIRNRequest cancels an IRN (allowed within 24 hours of AckDt).
type CancelIRNRequest struct {
	IRN        string `json:"Irn"`
	ReasonCode int    `json:"CnlRsn"`
	Remark     string `json:"CnlRem"`
}

// DocumentRef identifies a document for GetIRNByDocument.
type DocumentRef struct {
	DocType string // INV | CRN | DBN
	DocNo   string
	DocDate string // dd/mm/yyyy
}

// Address is a dispatch-from / ship-to address for EWB-by-IRN.
type Address struct {
	Name  string `json:"Nm,omitempty"`
	Addr1 string `json:"Addr1"`
	Addr2 string `json:"Addr2,omitempty"`
	Loc   string `json:"Loc"`
	Pin   int    `json:"Pin"`
	Stcd  string `json:"Stcd"`
}

// EWBByIRN is the IRP "generate e-way bill by IRN" request.
type EWBByIRN struct {
	IRN         string   `json:"Irn"`
	Distance    int      `json:"Distance"`
	TransMode   string   `json:"TransMode,omitempty"` // 1 road, 2 rail, 3 air, 4 ship
	TransID     string   `json:"TransId,omitempty"`
	TransName   string   `json:"TransName,omitempty"`
	TransDocDt  string   `json:"TransDocDt,omitempty"` // dd/mm/yyyy
	TransDocNo  string   `json:"TransDocNo,omitempty"`
	VehNo       string   `json:"VehNo,omitempty"`
	VehType     string   `json:"VehType,omitempty"` // R | O
	DispDtls    *Address `json:"DispDtls,omitempty"`
	ExpShipDtls *Address `json:"ExpShipDtls,omitempty"`
}

// EWBRequest generates an e-way bill either from an IRN or from a standalone
// NIC payload (one bill object of the NIC e-way JSON, as built by compliance).
type EWBRequest struct {
	ByIRN *EWBByIRN
	Bill  any
}

// EWBResult is the NIC answer for a generated, updated or extended bill.
type EWBResult struct {
	EWBNo     string    `json:"ewb_no"`
	EWBDate   time.Time `json:"ewb_date"`
	ValidUpto time.Time `json:"valid_upto,omitempty"` // zero while Part-B is missing
	Alert     string    `json:"alert,omitempty"`
}

// PartBRequest updates the vehicle / transport document (Part-B).
type PartBRequest struct {
	EWBNo        string `json:"ewbNo"`
	VehicleNo    string `json:"vehicleNo,omitempty"`
	FromPlace    string `json:"fromPlace"`
	FromState    int    `json:"fromState"`
	ReasonCode   string `json:"reasonCode"` // 1 breakdown, 2 transhipment, 3 others, 4 first time
	ReasonRem    string `json:"reasonRem"`
	TransDocNo   string `json:"transDocNo,omitempty"`
	TransDocDate string `json:"transDocDate,omitempty"`
	TransMode    string `json:"transMode"`
	VehicleType  string `json:"vehicleType,omitempty"`
}

// ExtendRequest extends validity (allowed 8 hours either side of expiry).
type ExtendRequest struct {
	EWBNo             string `json:"ewbNo"`
	VehicleNo         string `json:"vehicleNo,omitempty"`
	FromPlace         string `json:"fromPlace"`
	FromState         int    `json:"fromState"`
	FromPincode       int    `json:"fromPincode"`
	RemainingDistance int    `json:"remainingDistance"`
	TransDocNo        string `json:"transDocNo,omitempty"`
	TransDocDate      string `json:"transDocDate,omitempty"`
	TransMode         string `json:"transMode"`
	ExtnRsnCode       int    `json:"extnRsnCode"` // 1 natural calamity, 2 law and order, 4 transhipment, 5 accident, 99 others
	ExtnRemarks       string `json:"extnRemarks"`
	ConsignmentStatus string `json:"consignmentStatus"` // M in movement | T in transit
	TransitType       string `json:"transitType,omitempty"`
	ODC               bool   `json:"-"` // over-dimensional cargo (20 km per day)
}

// CancelEWBRequest cancels an e-way bill (within 24 hours of generation).
type CancelEWBRequest struct {
	EWBNo      string `json:"ewbNo"`
	ReasonCode int    `json:"cancelRsnCode"`
	Remark     string `json:"cancelRmrk"`
}

// EWBDetails is the NIC "get e-way bill" answer (the fields kar uses).
type EWBDetails struct {
	EWBNo     string          `json:"ewb_no"`
	Status    string          `json:"status"` // ACT | CNL
	EWBDate   time.Time       `json:"ewb_date"`
	ValidUpto time.Time       `json:"valid_upto,omitempty"`
	VehicleNo string          `json:"vehicle_no,omitempty"`
	GSTIN     string          `json:"gstin"`
	DocNo     string          `json:"doc_no"`
	IRN       string          `json:"irn,omitempty"`
	Raw       json.RawMessage `json:"raw,omitempty"`
}

// Client is everything kar asks of the GST systems.
type Client interface {
	Name() string
	SearchGSTIN(ctx context.Context, gstin string) (Taxpayer, error)

	GenerateIRN(ctx context.Context, c Credentials, payload any) (IRNResult, error)
	CancelIRN(ctx context.Context, c Credentials, req CancelIRNRequest) (IRNResult, error)
	GetIRNByDocument(ctx context.Context, c Credentials, ref DocumentRef) (IRNResult, error)

	GenerateEWB(ctx context.Context, c Credentials, req EWBRequest) (EWBResult, error)
	UpdatePartB(ctx context.Context, c Credentials, req PartBRequest) (EWBResult, error)
	ExtendValidity(ctx context.Context, c Credentials, req ExtendRequest) (EWBResult, error)
	CancelEWB(ctx context.Context, c Credentials, req CancelEWBRequest) (time.Time, error)
	GetEWB(ctx context.Context, c Credentials, ewbNo string) (EWBDetails, error)

	// FetchGSTR2B returns the portal's GSTR-2B JSON for gstin and period (MMYYYY).
	FetchGSTR2B(ctx context.Context, c Credentials, gstin, period string) (json.RawMessage, error)
}

// Kind classifies an error so callers can react without knowing every code.
type Kind string

const (
	KindValidation   Kind = "validation"    // the payload was rejected; fix the data
	KindDuplicate    Kind = "duplicate"     // already registered (2150 / 604); Existing may carry the original
	KindNotFound     Kind = "not_found"     // no such IRN / EWB / document
	KindNotAllowed   Kind = "not_allowed"   // state forbids it (cancel window over, already cancelled, …)
	KindAuth         Kind = "auth"          // credentials missing or rejected
	KindUnavailable  Kind = "unavailable"   // GSP/IRP down or timed out; retry later
	KindNotSupported Kind = "not_supported" // the driver cannot do this
)

// Error codes. Real IRP/NIC codes pass through verbatim from the HTTP driver;
// the simulator answers with this subset (modelled on the NIC error list —
// callers should switch on Kind, not on numbers).
const (
	CodeDuplicateIRN       = "2150" // Duplicate IRN
	CodeInvalidField       = "2172" // mandatory field missing / invalid (header, seller, buyer, values)
	CodeInvalidItem        = "2140" // item-level error (HSN, rate, amounts)
	CodeFutureDate         = "2163" // document date is in the future
	CodeSameGSTIN          = "2211" // seller and buyer GSTIN are the same
	CodeCancelWindow       = "2270" // allowed cancellation time (24 h) is over
	CodeIRNNotActive       = "9999" // IRN already cancelled / not active
	CodeIRNHasEWB          = "2230" // cannot cancel an IRN with an active e-way bill
	CodeIRNNotFound        = "2283" // IRN / document not found
	CodeDuplicateEWB       = "604"  // e-way bill already generated for this document
	CodeEWBInvalid         = "602"  // e-way payload invalid
	CodeEWBNotFound        = "325"  // e-way bill not found
	CodeEWBCancelWindow    = "315"  // e-way cancellation window (24 h) is over
	CodeEWBNotActive       = "312"  // e-way bill cancelled / not active
	CodeEWBExtendWindow    = "4002" // extension allowed only 8 h before/after expiry
	CodeEWBDocTooOld       = "4010" // document older than 180 days
	CodeAuthFailed         = "AUTH"
	CodeProviderDown       = "UNAVAILABLE"
	CodeNotSupportedDriver = "NOT_SUPPORTED"
)

// Error is a GSP/IRP/NIC failure.
type Error struct {
	Kind     Kind       `json:"kind"`
	Code     string     `json:"code"`
	Message  string     `json:"message"`
	Field    string     `json:"field,omitempty"`
	Details  []Detail   `json:"details,omitempty"`
	Existing *IRNResult `json:"existing,omitempty"` // for KindDuplicate on an IRN: the original registration
}

// Detail is one error of a multi-error IRP answer.
type Detail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("%s %s: %s (%s)", e.Kind, e.Code, e.Message, e.Field)
	}
	return fmt.Sprintf("%s %s: %s", e.Kind, e.Code, e.Message)
}

// Retryable reports whether trying again later can succeed.
func (e *Error) Retryable() bool { return e.Kind == KindUnavailable }

// AsError extracts a *Error.
func AsError(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// IsKind reports whether err is a *Error of kind k.
func IsKind(err error, k Kind) bool {
	e, ok := AsError(err)
	return ok && e.Kind == k
}

func errf(k Kind, code, field, format string, a ...any) *Error {
	return &Error{Kind: k, Code: code, Field: field, Message: fmt.Sprintf(format, a...)}
}

// IST is Indian Standard Time; every IRP/NIC timestamp is IST.
var IST = time.FixedZone("IST", 5*3600+30*60)

// NICTimeLayout is the IRP's "yyyy-MM-dd HH:mm:ss" timestamp format.
const NICTimeLayout = "2006-01-02 15:04:05"

// EWBTimeLayout is NIC e-way's "dd/MM/yyyy hh:mm:ss tt" timestamp format.
const EWBTimeLayout = "02/01/2006 03:04:05 PM"

// FinancialYear returns "2026-27" for any date from 1 Apr 2026 to 31 Mar 2027.
func FinancialYear(d time.Time) string {
	d = d.In(IST)
	y := d.Year()
	if d.Month() < time.April {
		y--
	}
	return fmt.Sprintf("%d-%02d", y, (y+1)%100)
}
