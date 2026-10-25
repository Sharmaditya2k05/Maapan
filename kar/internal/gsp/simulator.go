package gsp

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/maapan/mool/gst"
)

// Simulator is an offline, deterministic stand-in for the GSTN search, the
// e-Invoice IRP and the NIC e-way bill system. Never use it in production:
// nothing it issues is registered with the government.
type Simulator struct {
	Store SimStore
	Key   []byte           // HS256 key for the signed invoice / QR
	Now   func() time.Time // overridable in tests
}

// NewSimulator builds a simulator over store with signing key.
func NewSimulator(store SimStore, key []byte) *Simulator {
	return &Simulator{Store: store, Key: key, Now: time.Now}
}

func (s *Simulator) Name() string { return "simulator" }

func (s *Simulator) now() time.Time {
	if s.Now == nil {
		return time.Now().In(IST)
	}
	return s.Now().In(IST)
}

// IRNFor applies the official rule: IRN = SHA-256 (lowercase hex) of
// supplier GSTIN + financial year (YYYY-YY) + document type (INV/CRN/DBN) + document number.
func IRNFor(gstin, fy, docType, docNo string) string {
	sum := sha256.Sum256([]byte(gstin + fy + docType + docNo))
	return hex.EncodeToString(sum[:])
}

// ackNoFor derives a stable 15-digit acknowledgement number from the IRN.
func ackNoFor(irn string) string {
	sum := sha256.Sum256([]byte("ack:" + irn))
	n := binary.BigEndian.Uint64(sum[:8]) % 100000000000000 // 14 digits
	return fmt.Sprintf("1%014d", n)
}

// ---------- GSTIN search ----------

// SearchGSTIN returns a simulated taxpayer. The status is derived from the
// entity number (13th character): '9' → Cancelled, '8' → Suspended, anything
// else → Active, so both paths can be exercised offline.
func (s *Simulator) SearchGSTIN(_ context.Context, g string) (Taxpayer, error) {
	g = gst.Normalize(g)
	if err := gst.ValidateGSTIN(g); err != nil {
		return Taxpayer{}, errf(KindValidation, CodeInvalidField, "gstin", "%s", err.Error())
	}
	status := "Active"
	switch g[12] {
	case '9':
		status = "Cancelled"
	case '8':
		status = "Suspended"
	}
	pan := gst.PAN(g)
	return Taxpayer{
		GSTIN: g, LegalName: "SIMULATED TAXPAYER " + pan, TradeName: "Sim " + pan[:5], Status: status,
		RegistrationType: "Regular", StateCode: g[:2], Address: "Simulated address, " + gst.States[g[:2]],
	}, nil
}

// ---------- e-Invoice ----------

type simParty struct {
	Gstin string `json:"Gstin"`
	LglNm string `json:"LglNm"`
	TrdNm string `json:"TrdNm"`
	Pos   string `json:"Pos"`
	Addr1 string `json:"Addr1"`
	Addr2 string `json:"Addr2"`
	Loc   string `json:"Loc"`
	Pin   int    `json:"Pin"`
	Stcd  string `json:"Stcd"`
	Ph    string `json:"Ph"`
	Em    string `json:"Em"`
}

type simItem struct {
	SlNo          string  `json:"SlNo"`
	PrdDesc       string  `json:"PrdDesc"`
	IsServc       string  `json:"IsServc"`
	HsnCd         string  `json:"HsnCd"`
	Qty           float64 `json:"Qty"`
	Unit          string  `json:"Unit"`
	UnitPrice     float64 `json:"UnitPrice"`
	TotAmt        float64 `json:"TotAmt"`
	Discount      float64 `json:"Discount"`
	AssAmt        float64 `json:"AssAmt"`
	GstRt         float64 `json:"GstRt"`
	IgstAmt       float64 `json:"IgstAmt"`
	CgstAmt       float64 `json:"CgstAmt"`
	SgstAmt       float64 `json:"SgstAmt"`
	CesRt         float64 `json:"CesRt"`
	CesAmt        float64 `json:"CesAmt"`
	CesNonAdvlAmt float64 `json:"CesNonAdvlAmt"`
	StateCesAmt   float64 `json:"StateCesAmt"`
	OthChrg       float64 `json:"OthChrg"`
	TotItemVal    float64 `json:"TotItemVal"`
}

type simInvoice struct {
	Version  string `json:"Version"`
	TranDtls struct {
		TaxSch      string `json:"TaxSch"`
		SupTyp      string `json:"SupTyp"`
		RegRev      string `json:"RegRev"`
		IgstOnIntra string `json:"IgstOnIntra"`
	} `json:"TranDtls"`
	DocDtls struct {
		Typ string `json:"Typ"`
		No  string `json:"No"`
		Dt  string `json:"Dt"`
	} `json:"DocDtls"`
	SellerDtls simParty  `json:"SellerDtls"`
	BuyerDtls  simParty  `json:"BuyerDtls"`
	ItemList   []simItem `json:"ItemList"`
	ValDtls    struct {
		AssVal    float64 `json:"AssVal"`
		CgstVal   float64 `json:"CgstVal"`
		SgstVal   float64 `json:"SgstVal"`
		IgstVal   float64 `json:"IgstVal"`
		CesVal    float64 `json:"CesVal"`
		StCesVal  float64 `json:"StCesVal"`
		Discount  float64 `json:"Discount"`
		OthChrg   float64 `json:"OthChrg"`
		RndOffAmt float64 `json:"RndOffAmt"`
		TotInvVal float64 `json:"TotInvVal"`
	} `json:"ValDtls"`
}

var (
	docNoPattern = regexp.MustCompile(`^[A-Za-z1-9][A-Za-z0-9/-]{0,15}$`)
	hsnPattern   = regexp.MustCompile(`^([0-9]{6}|[0-9]{8})$`)
	digits       = regexp.MustCompile(`^[0-9]+$`)
	supplyTypes  = map[string]bool{"B2B": true, "SEZWP": true, "SEZWOP": true, "EXPWP": true, "EXPWOP": true, "DEXP": true}
)

// validator collects IRP-style errors; the first one decides the code.
type validator struct{ errs []Detail }

func (v *validator) add(code, format string, a ...any) {
	v.errs = append(v.errs, Detail{Code: code, Message: fmt.Sprintf(format, a...)})
}

func (v *validator) err() error {
	if len(v.errs) == 0 {
		return nil
	}
	e := &Error{Kind: KindValidation, Code: v.errs[0].Code, Message: v.errs[0].Message, Details: v.errs}
	if len(v.errs) > 1 {
		e.Message = fmt.Sprintf("%s (and %d more)", v.errs[0].Message, len(v.errs)-1)
	}
	return e
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }

func off(a, b float64) bool { return math.Abs(a-b) > 1.0+1e-9 }

func (v *validator) party(role string, p simParty, export bool) {
	if export && role == "Buyer" {
		if p.Gstin != "URP" {
			v.add(CodeInvalidField, "%sDtls.Gstin must be URP for exports", role)
		}
		if p.Stcd != "96" || p.Pos != "96" {
			v.add(CodeInvalidField, "%sDtls.Stcd and Pos must be 96 (other country) for exports", role)
		}
		if p.Pin != 999999 {
			v.add(CodeInvalidField, "%sDtls.Pin must be 999999 for exports", role)
		}
	} else {
		if err := gst.ValidateGSTIN(p.Gstin); err != nil {
			v.add(CodeInvalidField, "%sDtls.Gstin %q is invalid: %s", role, p.Gstin, err.Error())
		} else if p.Stcd != p.Gstin[:2] && !(role == "Buyer" && p.Stcd == "96") {
			v.add(CodeInvalidField, "%sDtls.Stcd %q does not match the GSTIN state %s", role, p.Stcd, p.Gstin[:2])
		}
		if p.Pin < 100000 || p.Pin > 999999 {
			v.add(CodeInvalidField, "%sDtls.Pin must be a 6-digit PIN code", role)
		}
	}
	if n := runeLen(p.LglNm); n < 3 || n > 100 {
		v.add(CodeInvalidField, "%sDtls.LglNm must be 3-100 characters", role)
	}
	if runeLen(p.TrdNm) > 100 {
		v.add(CodeInvalidField, "%sDtls.TrdNm exceeds 100 characters", role)
	}
	if n := runeLen(p.Addr1); n < 1 || n > 100 {
		v.add(CodeInvalidField, "%sDtls.Addr1 must be 1-100 characters", role)
	}
	if runeLen(p.Addr2) > 100 {
		v.add(CodeInvalidField, "%sDtls.Addr2 exceeds 100 characters", role)
	}
	if n := runeLen(p.Loc); n < 3 || n > 50 {
		v.add(CodeInvalidField, "%sDtls.Loc must be 3-50 characters", role)
	}
	if p.Ph != "" && (!digits.MatchString(p.Ph) || len(p.Ph) < 6 || len(p.Ph) > 12) {
		v.add(CodeInvalidField, "%sDtls.Ph must be 6-12 digits", role)
	}
}

// validateInvoice checks an INV-01 v1.1 payload roughly the way the IRP does.
func validateInvoice(in simInvoice, now time.Time) (time.Time, error) {
	v := &validator{}
	if in.Version != "1.1" {
		v.add(CodeInvalidField, "Version must be 1.1")
	}
	t := in.TranDtls
	if t.TaxSch != "GST" {
		v.add(CodeInvalidField, "TranDtls.TaxSch must be GST")
	}
	if !supplyTypes[t.SupTyp] {
		v.add(CodeInvalidField, "TranDtls.SupTyp %q is not one of B2B, SEZWP, SEZWOP, EXPWP, EXPWOP, DEXP", t.SupTyp)
	}
	if t.RegRev != "Y" && t.RegRev != "N" {
		v.add(CodeInvalidField, "TranDtls.RegRev must be Y or N")
	}
	if t.IgstOnIntra != "" && t.IgstOnIntra != "Y" && t.IgstOnIntra != "N" {
		v.add(CodeInvalidField, "TranDtls.IgstOnIntra must be Y or N")
	}
	export := strings.HasPrefix(t.SupTyp, "EXP")
	if export && t.RegRev == "Y" {
		v.add(CodeInvalidField, "reverse charge is not applicable to exports")
	}
	d := in.DocDtls
	if d.Typ != "INV" && d.Typ != "CRN" && d.Typ != "DBN" {
		v.add(CodeInvalidField, "DocDtls.Typ must be INV, CRN or DBN")
	}
	if !docNoPattern.MatchString(d.No) {
		v.add(CodeInvalidField, "DocDtls.No %q must be 1-16 characters (letters, digits, / and -) and not start with 0, / or -", d.No)
	}
	docDate, err := time.ParseInLocation("02/01/2006", d.Dt, IST)
	if err != nil {
		v.add(CodeInvalidField, "DocDtls.Dt must be dd/mm/yyyy")
	} else if docDate.After(now) {
		v.add(CodeFutureDate, "DocDtls.Dt %s is a future date", d.Dt)
	}
	v.party("Seller", in.SellerDtls, false)
	v.party("Buyer", in.BuyerDtls, export)
	if in.SellerDtls.Gstin != "" && in.SellerDtls.Gstin == in.BuyerDtls.Gstin {
		v.add(CodeSameGSTIN, "seller and buyer GSTIN cannot be the same")
	}
	pos := in.BuyerDtls.Pos
	if !export && !gst.ValidState(pos) {
		v.add(CodeInvalidField, "BuyerDtls.Pos %q is not a valid state code", pos)
	}
	if len(in.ItemList) == 0 || len(in.ItemList) > 1000 {
		v.add(CodeInvalidItem, "ItemList must have 1-1000 items")
	}
	zeroTax := t.SupTyp == "SEZWOP" || t.SupTyp == "EXPWOP"
	intra := !export && !strings.HasPrefix(t.SupTyp, "SEZ") && t.IgstOnIntra != "Y" && in.SellerDtls.Stcd == pos
	var ass, tot float64
	seen := map[string]bool{}
	for i, it := range in.ItemList {
		n := i + 1
		if it.SlNo == "" || seen[it.SlNo] {
			v.add(CodeInvalidItem, "item %d: SlNo must be present and unique", n)
		}
		seen[it.SlNo] = true
		if !hsnPattern.MatchString(it.HsnCd) {
			v.add(CodeInvalidItem, "item %d: HsnCd %q must be 6 or 8 digits", n, it.HsnCd)
		}
		if svc := strings.HasPrefix(it.HsnCd, "99"); (it.IsServc == "Y") != svc || (it.IsServc != "Y" && it.IsServc != "N") {
			v.add(CodeInvalidItem, "item %d: IsServc must be Y for SAC (99…) codes and N for goods", n)
		}
		if runeLen(it.PrdDesc) > 300 {
			v.add(CodeInvalidItem, "item %d: PrdDesc exceeds 300 characters", n)
		}
		if it.Qty < 0 || it.AssAmt < 0 {
			v.add(CodeInvalidItem, "item %d: quantity and amounts cannot be negative", n)
		}
		bp := int(math.Round(it.GstRt * 100))
		if err == nil && !gst.ValidRate(bp, docDate) {
			v.add(CodeInvalidItem, "item %d: GstRt %v %% is not a GST rate in force on %s", n, it.GstRt, d.Dt)
		}
		if off(it.AssAmt, it.TotAmt-it.Discount) {
			v.add(CodeInvalidItem, "item %d: AssAmt must equal TotAmt - Discount", n)
		}
		taxes := it.IgstAmt + it.CgstAmt + it.SgstAmt
		if off(it.TotItemVal, it.AssAmt+taxes+it.CesAmt+it.CesNonAdvlAmt+it.StateCesAmt+it.OthChrg) {
			v.add(CodeInvalidItem, "item %d: TotItemVal must equal AssAmt + taxes + cess + other charges", n)
		}
		switch {
		case zeroTax:
			if taxes != 0 {
				v.add(CodeInvalidItem, "item %d: no tax may be charged on %s supplies", n, t.SupTyp)
			}
		case intra:
			if it.IgstAmt != 0 {
				v.add(CodeInvalidItem, "item %d: intra-state supply must carry CGST+SGST, not IGST", n)
			}
			if off(it.CgstAmt, it.SgstAmt) {
				v.add(CodeInvalidItem, "item %d: CGST and SGST must be equal", n)
			}
		default:
			if it.CgstAmt != 0 || it.SgstAmt != 0 {
				v.add(CodeInvalidItem, "item %d: inter-state supply must carry IGST, not CGST/SGST", n)
			}
		}
		if !zeroTax && off(taxes, it.AssAmt*it.GstRt/100) {
			v.add(CodeInvalidItem, "item %d: tax amount does not match AssAmt × GstRt", n)
		}
		ass += it.AssAmt
		tot += it.TotItemVal
	}
	val := in.ValDtls
	if off(val.AssVal, ass) {
		v.add(CodeInvalidField, "ValDtls.AssVal must equal the sum of item AssAmt")
	}
	if math.Abs(val.RndOffAmt) >= 100 {
		v.add(CodeInvalidField, "ValDtls.RndOffAmt must be within ±99.99")
	}
	if off(val.TotInvVal, tot-val.Discount+val.OthChrg+val.RndOffAmt) {
		v.add(CodeInvalidField, "ValDtls.TotInvVal must equal the item totals - Discount + OthChrg + RndOffAmt")
	}
	return docDate, v.err()
}

// GenerateIRN validates the payload and registers it. A document already
// registered answers 2150 with the original IRN details in Error.Existing.
func (s *Simulator) GenerateIRN(ctx context.Context, _ Credentials, payload any) (IRNResult, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return IRNResult{}, errf(KindValidation, CodeInvalidField, "", "payload: %s", err.Error())
	}
	var in simInvoice
	if err := json.Unmarshal(raw, &in); err != nil {
		return IRNResult{}, errf(KindValidation, CodeInvalidField, "", "payload is not INV-01 JSON: %s", err.Error())
	}
	now := s.now()
	docDate, err := validateInvoice(in, now)
	if err != nil {
		return IRNResult{}, err
	}
	fy := FinancialYear(docDate)
	irn := IRNFor(in.SellerDtls.Gstin, fy, in.DocDtls.Typ, in.DocDtls.No)
	ackDt := now.Truncate(time.Second)

	mainHSN, best := "", -1.0
	for _, it := range in.ItemList {
		if it.AssAmt > best {
			mainHSN, best = it.HsnCd, it.AssAmt
		}
	}
	qr := QRData{SellerGstin: in.SellerDtls.Gstin, BuyerGstin: in.BuyerDtls.Gstin, DocNo: in.DocDtls.No,
		DocTyp: in.DocDtls.Typ, DocDt: in.DocDtls.Dt, TotInvVal: in.ValDtls.TotInvVal, ItemCnt: len(in.ItemList),
		MainHsnCode: mainHSN, Irn: irn, IrnDt: ackDt.Format(NICTimeLayout)}
	qrJSON, _ := json.Marshal(qr)
	signedQR, err := SignJWS(s.Key, signedClaims{Data: string(qrJSON), Iss: "NIC Sandbox (Maapan simulator)"})
	if err != nil {
		return IRNResult{}, err
	}
	var full map[string]any
	_ = json.Unmarshal(raw, &full)
	full["Irn"], full["AckNo"], full["AckDt"] = irn, ackNoFor(irn), ackDt.Format(NICTimeLayout)
	fullJSON, _ := json.Marshal(full)
	signedInv, err := SignJWS(s.Key, signedClaims{Data: string(fullJSON), Iss: "NIC Sandbox (Maapan simulator)"})
	if err != nil {
		return IRNResult{}, err
	}
	rec := SimIRN{IRN: irn, GSTIN: in.SellerDtls.Gstin, DocType: in.DocDtls.Typ, DocNo: in.DocDtls.No, DocDate: in.DocDtls.Dt,
		FY: fy, AckNo: ackNoFor(irn), AckDt: ackDt, SignedInvoice: signedInv, SignedQR: signedQR, Status: IRNActive}
	got, inserted, err := s.Store.InsertIRN(ctx, rec)
	if err != nil {
		return IRNResult{}, errf(KindUnavailable, CodeProviderDown, "", "simulator store: %s", err.Error())
	}
	if !inserted {
		ex := simResult(got)
		return IRNResult{}, &Error{Kind: KindDuplicate, Code: CodeDuplicateIRN, Message: "Duplicate IRN", Existing: &ex}
	}
	return simResult(got), nil
}

func simResult(r SimIRN) IRNResult {
	return IRNResult{IRN: r.IRN, AckNo: r.AckNo, AckDt: r.AckDt.In(IST), SignedInvoice: r.SignedInvoice, SignedQR: r.SignedQR,
		Status: r.Status, EWBNo: r.EWBNo, CancelDate: r.CancelledAt}
}

// CancelIRN cancels within 24 hours of the acknowledgement.
func (s *Simulator) CancelIRN(ctx context.Context, _ Credentials, req CancelIRNRequest) (IRNResult, error) {
	if _, ok := IRNCancelReasons[req.ReasonCode]; !ok {
		return IRNResult{}, errf(KindValidation, CodeInvalidField, "CnlRsn", "cancel reason must be 1 (duplicate), 2 (data entry mistake), 3 (order cancelled) or 4 (others)")
	}
	if runeLen(req.Remark) > 100 {
		return IRNResult{}, errf(KindValidation, CodeInvalidField, "CnlRem", "remark exceeds 100 characters")
	}
	r, ok, err := s.Store.GetIRN(ctx, req.IRN)
	if err != nil {
		return IRNResult{}, errf(KindUnavailable, CodeProviderDown, "", "simulator store: %s", err.Error())
	}
	if !ok {
		return IRNResult{}, errf(KindNotFound, CodeIRNNotFound, "Irn", "IRN not found")
	}
	if r.Status != IRNActive {
		return IRNResult{}, errf(KindNotAllowed, CodeIRNNotActive, "Irn", "IRN is already cancelled")
	}
	now := s.now()
	if now.Sub(r.AckDt) > 24*time.Hour {
		return IRNResult{}, errf(KindNotAllowed, CodeCancelWindow, "Irn", "the allowed cancellation time (24 hours from %s) is over", r.AckDt.In(IST).Format(NICTimeLayout))
	}
	if r.EWBNo != "" {
		if e, ok, _ := s.Store.GetEWB(ctx, r.EWBNo); ok && e.Status == IRNActive {
			return IRNResult{}, errf(KindNotAllowed, CodeIRNHasEWB, "Irn", "an active e-way bill %s exists; cancel it first", r.EWBNo)
		}
	}
	r.Status, r.CancelledAt, r.CancelReason, r.CancelRemark = IRNCancelled, now.Truncate(time.Second), req.ReasonCode, req.Remark
	if err := s.Store.UpdateIRN(ctx, r); err != nil {
		return IRNResult{}, errf(KindUnavailable, CodeProviderDown, "", "simulator store: %s", err.Error())
	}
	return simResult(r), nil
}

// GetIRNByDocument looks a document up by type, number and date.
func (s *Simulator) GetIRNByDocument(ctx context.Context, c Credentials, ref DocumentRef) (IRNResult, error) {
	d, err := time.ParseInLocation("02/01/2006", ref.DocDate, IST)
	if err != nil {
		return IRNResult{}, errf(KindValidation, CodeInvalidField, "docdate", "document date must be dd/mm/yyyy")
	}
	r, ok, err := s.Store.GetIRN(ctx, IRNFor(gst.Normalize(c.GSTIN), FinancialYear(d), ref.DocType, ref.DocNo))
	if err != nil {
		return IRNResult{}, errf(KindUnavailable, CodeProviderDown, "", "simulator store: %s", err.Error())
	}
	if !ok {
		return IRNResult{}, errf(KindNotFound, CodeIRNNotFound, "", "no IRN for %s %s dated %s", ref.DocType, ref.DocNo, ref.DocDate)
	}
	return simResult(r), nil
}

// ---------- e-Way bill ----------

type simBill struct {
	UserGstin       string  `json:"userGstin"`
	SupplyType      string  `json:"supplyType"`
	SubSupplyType   int     `json:"subSupplyType"`
	DocType         string  `json:"docType"`
	DocNo           string  `json:"docNo"`
	DocDate         string  `json:"docDate"`
	FromGstin       string  `json:"fromGstin"`
	FromPincode     int     `json:"fromPincode"`
	FromStateCode   int     `json:"fromStateCode"`
	FromPlace       string  `json:"fromPlace"`
	ToGstin         string  `json:"toGstin"`
	ToPincode       int     `json:"toPincode"`
	ToStateCode     int     `json:"toStateCode"`
	TransactionType int     `json:"transactionType"`
	TotInvValue     float64 `json:"totInvValue"`
	TransMode       string  `json:"transMode"`
	TransDistance   string  `json:"transDistance"`
	TransporterID   string  `json:"transporterId"`
	TransDocNo      string  `json:"transDocNo"`
	TransDocDate    string  `json:"transDocDate"`
	VehicleNo       string  `json:"vehicleNo"`
	VehicleType     string  `json:"vehicleType"`
	ItemList        []struct {
		HsnCode int `json:"hsnCode"`
	} `json:"itemList"`
}

func validGSTINOrURP(g string) bool { return g == "URP" || gst.ValidateGSTIN(g) == nil }

// partB validates the transport fields; it reports whether Part-B is present.
func partB(v *validator, mode, vehicle, vehType, transID, docNo, docDate string) bool {
	if mode == "" {
		if transID == "" {
			v.add(CodeEWBInvalid, "give a transport mode with vehicle / transport document, or a transporter id for Part-A only")
		}
		return false
	}
	switch mode {
	case "1":
		if vehicle == "" {
			if transID == "" {
				v.add(CodeEWBInvalid, "vehicle number is required for road transport")
			}
			return false
		}
		if !ValidVehicle(vehicle) {
			v.add(CodeEWBInvalid, "vehicle number %q is not in a format the e-way system accepts", vehicle)
		}
		if vehType != "" && vehType != "R" && vehType != "O" {
			v.add(CodeEWBInvalid, "vehicle type must be R (regular) or O (over-dimensional cargo)")
		}
	case "2", "3", "4":
		if docNo == "" || docDate == "" {
			v.add(CodeEWBInvalid, "transport document number and date are required for rail, air and ship")
		}
	default:
		v.add(CodeEWBInvalid, "transport mode must be 1 (road), 2 (rail), 3 (air) or 4 (ship)")
	}
	return true
}

// GenerateEWB issues a 12-digit e-way bill number, from an IRN or a standalone payload.
func (s *Simulator) GenerateEWB(ctx context.Context, c Credentials, req EWBRequest) (EWBResult, error) {
	now := s.now()
	v := &validator{}
	var rec SimEWB
	var docDate time.Time
	var hasB bool
	switch {
	case req.ByIRN != nil:
		b := req.ByIRN
		r, ok, err := s.Store.GetIRN(ctx, b.IRN)
		if err != nil {
			return EWBResult{}, errf(KindUnavailable, CodeProviderDown, "", "simulator store: %s", err.Error())
		}
		if !ok {
			return EWBResult{}, errf(KindNotFound, CodeIRNNotFound, "Irn", "IRN not found")
		}
		if r.Status != IRNActive {
			return EWBResult{}, errf(KindNotAllowed, CodeIRNNotActive, "Irn", "IRN is cancelled")
		}
		if b.Distance < 1 || b.Distance > MaxDistance {
			v.add(CodeEWBInvalid, "Distance must be 1-%d km", MaxDistance)
		}
		hasB = partB(v, b.TransMode, NormalizeVehicle(b.VehNo), b.VehType, b.TransID, b.TransDocNo, b.TransDocDt)
		if b.TransID != "" && gst.ValidateGSTIN(b.TransID) != nil && len(b.TransID) != 15 {
			v.add(CodeEWBInvalid, "TransId must be a 15-character GSTIN / transporter id")
		}
		docDate, _ = time.ParseInLocation("02/01/2006", r.DocDate, IST)
		rec = SimEWB{GSTIN: r.GSTIN, DocKey: r.GSTIN + "|" + r.DocType + "|" + r.DocNo, DocDate: r.DocDate, IRN: r.IRN,
			VehicleNo: NormalizeVehicle(b.VehNo), TransMode: b.TransMode, Distance: b.Distance, ODC: b.VehType == "O"}
		rec.Raw, _ = json.Marshal(b)
	case req.Bill != nil:
		raw, err := json.Marshal(req.Bill)
		if err != nil {
			return EWBResult{}, errf(KindValidation, CodeEWBInvalid, "", "payload: %s", err.Error())
		}
		var b simBill
		if err := json.Unmarshal(raw, &b); err != nil {
			return EWBResult{}, errf(KindValidation, CodeEWBInvalid, "", "payload is not NIC e-way JSON: %s", err.Error())
		}
		if gst.ValidateGSTIN(b.UserGstin) != nil {
			v.add(CodeEWBInvalid, "userGstin is invalid")
		}
		if b.SupplyType != "O" && b.SupplyType != "I" {
			v.add(CodeEWBInvalid, "supplyType must be O or I")
		}
		if !validGSTINOrURP(b.FromGstin) || !validGSTINOrURP(b.ToGstin) {
			v.add(CodeEWBInvalid, "fromGstin / toGstin must be a valid GSTIN or URP")
		}
		if (b.SupplyType == "O" && b.FromGstin != b.UserGstin) || (b.SupplyType == "I" && b.ToGstin != b.UserGstin) {
			v.add(CodeEWBInvalid, "the generating GSTIN must be the consignor (outward) or consignee (inward)")
		}
		if b.FromPincode < 100000 || b.FromPincode > 999999 || b.ToPincode < 100000 || b.ToPincode > 999999 {
			v.add(CodeEWBInvalid, "from and to PIN codes must be 6 digits")
		}
		if !gst.ValidState(fmt.Sprintf("%02d", b.FromStateCode)) || !gst.ValidState(fmt.Sprintf("%02d", b.ToStateCode)) {
			v.add(CodeEWBInvalid, "from and to state codes must be valid")
		}
		if b.DocNo == "" || runeLen(b.DocNo) > 16 {
			v.add(CodeEWBInvalid, "docNo must be 1-16 characters")
		}
		if docDate, err = time.ParseInLocation("02/01/2006", b.DocDate, IST); err != nil {
			v.add(CodeEWBInvalid, "docDate must be dd/mm/yyyy")
		} else if docDate.After(now) {
			v.add(CodeEWBInvalid, "docDate cannot be a future date")
		}
		if len(b.ItemList) == 0 {
			v.add(CodeEWBInvalid, "itemList is empty")
		}
		for i, it := range b.ItemList {
			if it.HsnCode < 1000 {
				v.add(CodeEWBInvalid, "item %d: hsnCode must have at least 4 digits", i+1)
			}
		}
		dist, err := strconv.Atoi(b.TransDistance)
		if err != nil || dist < 1 || dist > MaxDistance {
			v.add(CodeEWBInvalid, "transDistance must be 1-%d km", MaxDistance)
		}
		hasB = partB(v, b.TransMode, NormalizeVehicle(b.VehicleNo), b.VehicleType, b.TransporterID, b.TransDocNo, b.TransDocDate)
		rec = SimEWB{GSTIN: b.UserGstin, DocKey: b.UserGstin + "|" + b.DocType + "|" + b.DocNo, DocDate: b.DocDate,
			VehicleNo: NormalizeVehicle(b.VehicleNo), TransMode: b.TransMode, Distance: dist, ODC: b.VehicleType == "O", Raw: raw}
	default:
		return EWBResult{}, errf(KindValidation, CodeEWBInvalid, "", "give an IRN or a standalone e-way payload")
	}
	if !docDate.IsZero() && DocTooOld(docDate, now) {
		v.add(CodeEWBDocTooOld, "the document is older than %d days; an e-way bill can no longer be generated", MaxDocAgeDays)
	}
	if err := v.err(); err != nil {
		return EWBResult{}, err
	}
	if ex, ok, err := s.Store.ActiveEWBForDoc(ctx, rec.DocKey); err != nil {
		return EWBResult{}, errf(KindUnavailable, CodeProviderDown, "", "simulator store: %s", err.Error())
	} else if ok {
		return EWBResult{}, &Error{Kind: KindDuplicate, Code: CodeDuplicateEWB,
			Message: fmt.Sprintf("an e-way bill (%s) is already generated for this document", ex.EWBNo)}
	}
	seq, err := s.Store.NextEWBSeq(ctx)
	if err != nil {
		return EWBResult{}, errf(KindUnavailable, CodeProviderDown, "", "simulator store: %s", err.Error())
	}
	rec.EWBNo = fmt.Sprintf("1%011d", seq%100000000000)
	rec.Status, rec.EWBDate = IRNActive, now.Truncate(time.Second)
	if hasB {
		rec.ValidUpto = ValidUpto(now, ValidityDays(rec.Distance, rec.ODC))
	}
	ok, err := s.Store.InsertEWB(ctx, rec)
	if err != nil {
		return EWBResult{}, errf(KindUnavailable, CodeProviderDown, "", "simulator store: %s", err.Error())
	}
	if !ok {
		return EWBResult{}, &Error{Kind: KindDuplicate, Code: CodeDuplicateEWB, Message: "an e-way bill is already generated for this document"}
	}
	if rec.IRN != "" {
		if r, ok, _ := s.Store.GetIRN(ctx, rec.IRN); ok {
			r.EWBNo = rec.EWBNo
			_ = s.Store.UpdateIRN(ctx, r)
		}
	}
	res := EWBResult{EWBNo: rec.EWBNo, EWBDate: rec.EWBDate, ValidUpto: rec.ValidUpto}
	if !hasB {
		res.Alert = "Part-B is not entered; the e-way bill is not valid for movement until a vehicle or transport document is added"
	}
	return res, nil
}

func (s *Simulator) activeEWB(ctx context.Context, no string) (SimEWB, error) {
	e, ok, err := s.Store.GetEWB(ctx, no)
	if err != nil {
		return e, errf(KindUnavailable, CodeProviderDown, "", "simulator store: %s", err.Error())
	}
	if !ok {
		return e, errf(KindNotFound, CodeEWBNotFound, "ewbNo", "e-way bill %s not found", no)
	}
	if e.Status != IRNActive {
		return e, errf(KindNotAllowed, CodeEWBNotActive, "ewbNo", "e-way bill %s is cancelled", no)
	}
	return e, nil
}

// UpdatePartB records a vehicle / transport document. Validity starts with the
// first Part-B; later vehicle changes keep it.
func (s *Simulator) UpdatePartB(ctx context.Context, _ Credentials, req PartBRequest) (EWBResult, error) {
	e, err := s.activeEWB(ctx, req.EWBNo)
	if err != nil {
		return EWBResult{}, err
	}
	now := s.now()
	if !e.ValidUpto.IsZero() && now.After(e.ValidUpto) {
		return EWBResult{}, errf(KindNotAllowed, CodeEWBExtendWindow, "ewbNo", "the e-way bill expired at %s; extend it instead", e.ValidUpto.Format(NICTimeLayout))
	}
	v := &validator{}
	if !partB(v, req.TransMode, NormalizeVehicle(req.VehicleNo), req.VehicleType, "", req.TransDocNo, req.TransDocDate) {
		v.add(CodeEWBInvalid, "Part-B needs a transport mode and a vehicle number or transport document")
	}
	if !gst.ValidState(fmt.Sprintf("%02d", req.FromState)) || strings.TrimSpace(req.FromPlace) == "" {
		v.add(CodeEWBInvalid, "fromPlace and a valid fromState are required")
	}
	if !map[string]bool{"1": true, "2": true, "3": true, "4": true}[req.ReasonCode] {
		v.add(CodeEWBInvalid, "reasonCode must be 1 (breakdown), 2 (transhipment), 3 (others) or 4 (first time)")
	}
	if err := v.err(); err != nil {
		return EWBResult{}, err
	}
	e.VehicleNo, e.TransMode = NormalizeVehicle(req.VehicleNo), req.TransMode
	if req.VehicleType == "O" {
		e.ODC = true
	}
	if e.ValidUpto.IsZero() {
		e.ValidUpto = ValidUpto(now, ValidityDays(e.Distance, e.ODC))
	}
	if err := s.Store.UpdateEWB(ctx, e); err != nil {
		return EWBResult{}, errf(KindUnavailable, CodeProviderDown, "", "simulator store: %s", err.Error())
	}
	return EWBResult{EWBNo: e.EWBNo, EWBDate: e.EWBDate, ValidUpto: e.ValidUpto}, nil
}

var extendReasons = map[int]bool{1: true, 2: true, 4: true, 5: true, 99: true}

// ExtendValidity extends from 8 hours before to 8 hours after expiry, for the
// remaining distance (and at most 360 days after generation).
func (s *Simulator) ExtendValidity(ctx context.Context, _ Credentials, req ExtendRequest) (EWBResult, error) {
	e, err := s.activeEWB(ctx, req.EWBNo)
	if err != nil {
		return EWBResult{}, err
	}
	now := s.now()
	if e.ValidUpto.IsZero() {
		return EWBResult{}, errf(KindNotAllowed, CodeEWBExtendWindow, "ewbNo", "Part-B was never entered; update Part-B instead")
	}
	if !CanExtend(e.ValidUpto, now) {
		return EWBResult{}, errf(KindNotAllowed, CodeEWBExtendWindow, "ewbNo",
			"validity can be extended only from 8 hours before to 8 hours after expiry (%s)", e.ValidUpto.Format(NICTimeLayout))
	}
	v := &validator{}
	if !extendReasons[req.ExtnRsnCode] {
		v.add(CodeEWBInvalid, "extnRsnCode must be 1, 2, 4, 5 or 99")
	}
	if req.RemainingDistance < 1 || req.RemainingDistance > MaxDistance {
		v.add(CodeEWBInvalid, "remainingDistance must be 1-%d km", MaxDistance)
	}
	if req.ConsignmentStatus != "M" && req.ConsignmentStatus != "T" {
		v.add(CodeEWBInvalid, "consignmentStatus must be M (in movement) or T (in transit)")
	}
	if req.ConsignmentStatus == "M" {
		partB(v, req.TransMode, NormalizeVehicle(req.VehicleNo), "", "", req.TransDocNo, req.TransDocDate)
	}
	if req.FromPincode < 100000 || req.FromPincode > 999999 || !gst.ValidState(fmt.Sprintf("%02d", req.FromState)) {
		v.add(CodeEWBInvalid, "current fromPincode and fromState are required")
	}
	if err := v.err(); err != nil {
		return EWBResult{}, err
	}
	nv := ValidUpto(now, ValidityDays(req.RemainingDistance, req.ODC || e.ODC))
	if limit := e.EWBDate.AddDate(0, 0, 360); nv.After(limit) {
		return EWBResult{}, errf(KindNotAllowed, CodeEWBExtendWindow, "ewbNo", "an e-way bill cannot be extended beyond 360 days from generation")
	}
	e.ValidUpto, e.Extended = nv, e.Extended+1
	if req.VehicleNo != "" {
		e.VehicleNo = NormalizeVehicle(req.VehicleNo)
	}
	if err := s.Store.UpdateEWB(ctx, e); err != nil {
		return EWBResult{}, errf(KindUnavailable, CodeProviderDown, "", "simulator store: %s", err.Error())
	}
	return EWBResult{EWBNo: e.EWBNo, EWBDate: e.EWBDate, ValidUpto: e.ValidUpto}, nil
}

// CancelEWB cancels within 24 hours of generation.
func (s *Simulator) CancelEWB(ctx context.Context, _ Credentials, req CancelEWBRequest) (time.Time, error) {
	if _, ok := EWBCancelReasons[req.ReasonCode]; !ok {
		return time.Time{}, errf(KindValidation, CodeEWBInvalid, "cancelRsnCode", "cancel reason must be 1 (duplicate), 2 (order cancelled), 3 (data entry mistake) or 4 (others)")
	}
	e, err := s.activeEWB(ctx, req.EWBNo)
	if err != nil {
		return time.Time{}, err
	}
	now := s.now()
	if now.Sub(e.EWBDate) > 24*time.Hour {
		return time.Time{}, errf(KindNotAllowed, CodeEWBCancelWindow, "ewbNo", "an e-way bill can be cancelled only within 24 hours of generation")
	}
	e.Status, e.Cancelled = IRNCancelled, now.Truncate(time.Second)
	if err := s.Store.UpdateEWB(ctx, e); err != nil {
		return time.Time{}, errf(KindUnavailable, CodeProviderDown, "", "simulator store: %s", err.Error())
	}
	return e.Cancelled, nil
}

// GetEWB returns a bill's current state.
func (s *Simulator) GetEWB(ctx context.Context, _ Credentials, no string) (EWBDetails, error) {
	e, ok, err := s.Store.GetEWB(ctx, no)
	if err != nil {
		return EWBDetails{}, errf(KindUnavailable, CodeProviderDown, "", "simulator store: %s", err.Error())
	}
	if !ok {
		return EWBDetails{}, errf(KindNotFound, CodeEWBNotFound, "ewbNo", "e-way bill %s not found", no)
	}
	parts := strings.SplitN(e.DocKey, "|", 3)
	docNo := ""
	if len(parts) == 3 {
		docNo = parts[2]
	}
	return EWBDetails{EWBNo: e.EWBNo, Status: e.Status, EWBDate: e.EWBDate, ValidUpto: e.ValidUpto, VehicleNo: e.VehicleNo,
		GSTIN: e.GSTIN, DocNo: docNo, IRN: e.IRN, Raw: e.Raw}, nil
}

var periodPattern = regexp.MustCompile(`^(0[1-9]|1[0-2])20[0-9]{2}$`)

// FetchGSTR2B returns an empty but structurally valid GSTR-2B for the period.
func (s *Simulator) FetchGSTR2B(_ context.Context, _ Credentials, g, period string) (json.RawMessage, error) {
	g = gst.Normalize(g)
	if err := gst.ValidateGSTIN(g); err != nil {
		return nil, errf(KindValidation, CodeInvalidField, "gstin", "%s", err.Error())
	}
	if !periodPattern.MatchString(period) {
		return nil, errf(KindValidation, CodeInvalidField, "rtnprd", "period must be MMYYYY")
	}
	doc := map[string]any{
		"chksum": "",
		"data": map[string]any{
			"gstin": g, "rtnprd": period, "version": "1.0", "gendt": s.now().Format("02-01-2006"),
			"docdata": map[string]any{"b2b": []any{}, "b2ba": []any{}, "cdnr": []any{}, "cdnra": []any{},
				"isd": []any{}, "isda": []any{}, "impg": []any{}, "impgsez": []any{}},
			"itcsumm": map[string]any{},
			"cpsumm":  map[string]any{},
		},
	}
	b, _ := json.Marshal(doc)
	return b, nil
}

var _ Client = (*Simulator)(nil)
