package compliance

import (
	"errors"
	"fmt"
	"strings"

	"github.com/maapan/kar/internal/clients"
	"github.com/maapan/kar/internal/gsp"
	"github.com/maapan/mool/money"
)

// EWayVersion is the NIC bulk-generation JSON version this builder targets.
const EWayVersion = "1.0.0621"

// Location overrides an address: dispatch-from (goods leave from somewhere
// other than the seller's address) or ship-to (delivered somewhere other than
// the buyer's address).
type Location struct {
	GSTIN     string `json:"gstin,omitempty"`
	Name      string `json:"name,omitempty"`
	Address1  string `json:"address1"`
	Address2  string `json:"address2,omitempty"`
	Place     string `json:"place"`
	Pincode   int    `json:"pincode"`
	StateCode string `json:"state_code"`
}

// Transport is the Part-B information (and optional address overrides).
// Both the Phase-1 names (trans_mode, trans_distance) and the Phase-3 names
// (mode, distance_km) are accepted.
type Transport struct {
	TransMode       string    `json:"trans_mode,omitempty"` // road | rail | air | ship (or 1-4)
	Mode            string    `json:"mode,omitempty"`       // 1 road, 2 rail, 3 air, 4 ship
	TransDistance   int       `json:"trans_distance,omitempty"`
	DistanceKM      int       `json:"distance_km,omitempty"`
	TransporterID   string    `json:"transporter_id,omitempty"`
	TransporterName string    `json:"transporter_name,omitempty"`
	TransDocNo      string    `json:"trans_doc_no,omitempty"`
	TransDocDate    string    `json:"trans_doc_date,omitempty"` // YYYY-MM-DD
	VehicleNo       string    `json:"vehicle_no,omitempty"`
	VehicleType     string    `json:"vehicle_type,omitempty"` // R regular | O over-dimensional cargo
	FromPincode     int       `json:"from_pincode,omitempty"`
	ToPincode       int       `json:"to_pincode,omitempty"`
	DispatchFrom    *Location `json:"dispatch_from,omitempty"`
	ShipTo          *Location `json:"ship_to,omitempty"`
}

// Normalize folds the alias fields and cleans the vehicle number.
func (t *Transport) Normalize() {
	if t.TransMode == "" {
		t.TransMode = t.Mode
	}
	if t.TransDistance == 0 {
		t.TransDistance = t.DistanceKM
	}
	t.VehicleNo = gsp.NormalizeVehicle(t.VehicleNo)
	t.TransporterID = strings.ToUpper(strings.TrimSpace(t.TransporterID))
	t.VehicleType = strings.ToUpper(strings.TrimSpace(t.VehicleType))
}

// ModeCode returns 1-4 for the transport mode ("" if none or unknown).
func (t Transport) ModeCode() string {
	return modeCodes[strings.ToLower(strings.TrimSpace(t.TransMode))]
}

// ODC reports over-dimensional cargo.
func (t Transport) ODC() bool { return t.VehicleType == "O" }

// EWayItem is one item in itemList.
type EWayItem struct {
	ItemNo        int     `json:"itemNo"`
	ProductName   string  `json:"productName"`
	ProductDesc   string  `json:"productDesc"`
	HsnCode       int     `json:"hsnCode"`
	Quantity      float64 `json:"quantity"`
	QtyUnit       string  `json:"qtyUnit"`
	TaxableAmount float64 `json:"taxableAmount"`
	SgstRate      float64 `json:"sgstRate"`
	CgstRate      float64 `json:"cgstRate"`
	IgstRate      float64 `json:"igstRate"`
	CessRate      float64 `json:"cessRate"`
	CessNonAdvol  float64 `json:"cessNonAdvol"`
}

// EWayBill is one entry of billLists (and the body of the NIC generate API).
type EWayBill struct {
	UserGstin             string     `json:"userGstin"`
	SupplyType            string     `json:"supplyType"` // O outward | I inward
	SubSupplyType         int        `json:"subSupplyType"`
	SubSupplyDesc         string     `json:"subSupplyDesc"`
	DocType               string     `json:"docType"` // INV | BIL | BOE | CHL | CNT | OTH
	DocNo                 string     `json:"docNo"`
	DocDate               string     `json:"docDate"`
	FromGstin             string     `json:"fromGstin"`
	FromTrdName           string     `json:"fromTrdName"`
	FromAddr1             string     `json:"fromAddr1"`
	FromAddr2             string     `json:"fromAddr2"`
	FromPlace             string     `json:"fromPlace"`
	FromPincode           int        `json:"fromPincode"`
	ActFromStateCode      int        `json:"actFromStateCode"`
	FromStateCode         int        `json:"fromStateCode"`
	DispatchFromGSTIN     string     `json:"dispatchFromGSTIN,omitempty"`
	DispatchFromTradeName string     `json:"dispatchFromTradeName,omitempty"`
	ToGstin               string     `json:"toGstin"`
	ToTrdName             string     `json:"toTrdName"`
	ToAddr1               string     `json:"toAddr1"`
	ToAddr2               string     `json:"toAddr2"`
	ToPlace               string     `json:"toPlace"`
	ToPincode             int        `json:"toPincode"`
	ActToStateCode        int        `json:"actToStateCode"`
	ToStateCode           int        `json:"toStateCode"`
	ShipToGSTIN           string     `json:"shipToGSTIN,omitempty"`
	ShipToTradeName       string     `json:"shipToTradeName,omitempty"`
	TransactionType       int        `json:"transactionType"` // 1 regular, 2 bill-to ship-to, 3 bill-from dispatch-from, 4 both
	TotalValue            float64    `json:"totalValue"`
	CgstValue             float64    `json:"cgstValue"`
	SgstValue             float64    `json:"sgstValue"`
	IgstValue             float64    `json:"igstValue"`
	CessValue             float64    `json:"cessValue"`
	CessNonAdvolValue     float64    `json:"cessNonAdvolValue"`
	OtherValue            float64    `json:"otherValue"`
	TotInvValue           float64    `json:"totInvValue"`
	TransMode             string     `json:"transMode"`
	TransDistance         string     `json:"transDistance"`
	TransporterName       string     `json:"transporterName"`
	TransporterID         string     `json:"transporterId"`
	TransDocNo            string     `json:"transDocNo"`
	TransDocDate          string     `json:"transDocDate"`
	VehicleNo             string     `json:"vehicleNo"`
	VehicleType           string     `json:"vehicleType"`
	ItemList              []EWayItem `json:"itemList"`
}

// EWayFile is the uploadable bulk JSON.
type EWayFile struct {
	Version   string     `json:"version"`
	BillLists []EWayBill `json:"billLists"`
}

var modeCodes = map[string]string{"road": "1", "rail": "2", "air": "3", "ship": "4", "1": "1", "2": "2", "3": "3", "4": "4"}

// BuildEWay maps an invoice to NIC e-Way Bill JSON. our is the store (seller for
// outward, recipient for inward); party is the counterparty. interThreshold is
// the tenant's inter-state threshold; intra-state movements use the state
// table (rules.go). It returns warnings (non-blocking) and whether the value is
// below the applicable threshold.
func BuildEWay(inv clients.Invoice, our, party Party, t Transport, interThreshold int64) (EWayFile, []string, bool, error) {
	t.Normalize()
	var warnings []string
	if inv.Status != "" && inv.Status != "posted" {
		return EWayFile{}, nil, false, errors.New("only posted invoices can move goods")
	}
	if our.GSTIN == "" {
		return EWayFile{}, nil, false, errors.New("store GSTIN is required for an e-way bill")
	}
	b := EWayBill{UserGstin: our.GSTIN, TransactionType: 1}
	from, to := our, party
	switch inv.DocType {
	case "sale":
		b.SupplyType, b.SubSupplyType, b.DocType = "O", 1, "INV"
		b.DocNo = inv.Number
		if strings.HasPrefix(inv.SupplyCategory, "export_") {
			b.SubSupplyType = 3 // Export
		}
	case "purchase":
		from, to = party, our
		b.SupplyType, b.SubSupplyType, b.DocType = "I", 1, "INV"
		b.DocNo = inv.SupplierInvoiceNo
		if b.DocNo == "" {
			b.DocNo = inv.Number
			warnings = append(warnings, "supplier invoice number missing; using our purchase number")
		}
	case "sale_return":
		from, to = party, our
		b.SupplyType, b.SubSupplyType, b.DocType = "I", 7, "CNT" // 7 = Sales Return
		b.DocNo = inv.Number
	case "purchase_return":
		b.SupplyType, b.SubSupplyType, b.SubSupplyDesc, b.DocType = "O", 8, "Purchase Return", "CHL" // 8 = Others
		b.DocNo = inv.Number
	default:
		return EWayFile{}, nil, false, fmt.Errorf("doc_type %s cannot carry an e-way bill", inv.DocType)
	}
	if utf8Len(b.DocNo) > 16 {
		return EWayFile{}, nil, false, fmt.Errorf("document number %q exceeds 16 characters", b.DocNo)
	}
	var err error
	if b.DocDate, err = NICDate(inv.InvoiceDate); err != nil {
		return EWayFile{}, nil, false, fmt.Errorf("invoice_date: %w", err)
	}

	if t.FromPincode != 0 {
		from.Pincode = t.FromPincode
	}
	if t.ToPincode != 0 {
		to.Pincode = t.ToPincode
	}
	fill := func(p Party) (gstin, name, a1, a2, place string, pin, state int) {
		gstin = p.GSTIN
		if gstin == "" {
			gstin = "URP" // unregistered person
		}
		name = p.TradeName
		if name == "" {
			name = p.LegalName
		}
		a1, a2 = splitAddress(p.Address1)
		if p.Address2 != "" {
			a2 = Truncate(p.Address2, AddrLimit)
		}
		place = strings.TrimSpace(p.Place)
		if place == "" {
			place = PlaceFromAddress(p.Address1)
		}
		return gstin, Truncate(name, 100), a1, a2, Truncate(place, 50), p.Pincode, stateInt(p.StateCode)
	}
	b.FromGstin, b.FromTrdName, b.FromAddr1, b.FromAddr2, b.FromPlace, b.FromPincode, b.FromStateCode = fill(from)
	b.ToGstin, b.ToTrdName, b.ToAddr1, b.ToAddr2, b.ToPlace, b.ToPincode, b.ToStateCode = fill(to)
	b.ActFromStateCode, b.ActToStateCode = b.FromStateCode, b.ToStateCode
	if strings.HasPrefix(inv.SupplyCategory, "export_") && b.SupplyType == "O" {
		b.ToGstin, b.ToStateCode = "URP", 99 // other country; actToStateCode stays the port's state
	}

	// Dispatch-from / ship-to: the actual movement addresses change, the bill-from / bill-to stay.
	if d := t.DispatchFrom; d != nil {
		b.DispatchFromGSTIN, b.DispatchFromTradeName = strings.ToUpper(d.GSTIN), Truncate(d.Name, 100)
		b.FromAddr1, b.FromAddr2 = splitAddress(d.Address1)
		if d.Address2 != "" {
			b.FromAddr2 = Truncate(d.Address2, AddrLimit)
		}
		b.FromPlace, b.FromPincode, b.ActFromStateCode = Truncate(d.Place, 50), d.Pincode, stateInt(d.StateCode)
		b.TransactionType = 3
	}
	if s := t.ShipTo; s != nil {
		b.ShipToGSTIN, b.ShipToTradeName = strings.ToUpper(s.GSTIN), Truncate(s.Name, 100)
		b.ToAddr1, b.ToAddr2 = splitAddress(s.Address1)
		if s.Address2 != "" {
			b.ToAddr2 = Truncate(s.Address2, AddrLimit)
		}
		b.ToPlace, b.ToPincode, b.ActToStateCode = Truncate(s.Place, 50), s.Pincode, stateInt(s.StateCode)
		if b.TransactionType == 3 {
			b.TransactionType = 4
		} else {
			b.TransactionType = 2
		}
	}
	if b.FromPincode < 100000 || b.ToPincode < 100000 || b.FromPincode > 999999 || b.ToPincode > 999999 {
		return EWayFile{}, nil, false, errors.New("6-digit from and to PIN codes are required (pass from_pincode / to_pincode or a dispatch/ship-to address)")
	}
	if b.FromStateCode == 0 || b.ToStateCode == 0 || b.ActFromStateCode == 0 || b.ActToStateCode == 0 {
		return EWayFile{}, nil, false, errors.New("state codes of both parties are required")
	}

	b.TotalValue = Rupees(inv.TaxablePaise)
	b.CgstValue, b.SgstValue, b.IgstValue = Rupees(inv.CGSTPaise), Rupees(inv.SGSTPaise), Rupees(inv.IGSTPaise)
	b.OtherValue = Rupees(inv.RoundOffPaise + inv.TCSPaise)
	b.TotInvValue = Rupees(inv.GrandTotalPaise)

	var cessNonAdvol int64
	for _, l := range inv.Lines {
		if IsService(l.HSN) {
			continue // services do not move on an e-way bill
		}
		if strings.TrimSpace(l.HSN) == "" {
			return EWayFile{}, nil, false, fmt.Errorf("line %d (%s) has no HSN code", l.LineNo, l.Name)
		}
		na := int64(float64(l.CessPerUnitPaise)*l.Qty + 0.5)
		if na > l.CessPaise {
			na = l.CessPaise
		}
		cessNonAdvol += na
		it := EWayItem{
			ItemNo: len(b.ItemList) + 1, ProductName: Truncate(l.Name, 100), ProductDesc: Truncate(l.Name, 100),
			HsnCode: hsnInt(l.HSN), Quantity: round(l.Qty, 3), QtyUnit: UQC(l.Unit),
			TaxableAmount: Rupees(l.TaxablePaise), CessRate: RateFromBP(l.CessRateBP), CessNonAdvol: Rupees(l.CessPerUnitPaise),
		}
		if l.IGSTPaise > 0 || inv.InterState {
			it.IgstRate = RateFromBP(l.TaxRateBP)
		} else {
			it.CgstRate = RateFromBP(l.TaxRateBP) / 2
			it.SgstRate = RateFromBP(l.TaxRateBP) / 2
		}
		b.ItemList = append(b.ItemList, it)
	}
	if len(b.ItemList) == 0 {
		return EWayFile{}, nil, false, errors.New("invoice has no goods lines")
	}
	b.CessNonAdvolValue = Rupees(cessNonAdvol)
	b.CessValue = Rupees(inv.CessPaise - cessNonAdvol)

	if err := applyTransport(&b, t); err != nil {
		return EWayFile{}, nil, false, err
	}
	fromState, toState := fmt.Sprintf("%02d", b.ActFromStateCode), fmt.Sprintf("%02d", b.ActToStateCode)
	threshold := EWayThreshold(fromState, toState, interThreshold)
	value := ConsignmentValuePaise(inv)
	below := value < threshold
	if below {
		kind := "inter-state"
		if fromState == toState {
			kind = "intra-state (" + fromState + ")"
		}
		warnings = append(warnings, fmt.Sprintf("consignment value %s is below the %s e-way threshold %s; an e-way bill is optional",
			money.Format(value), kind, money.Format(threshold)))
	}
	return EWayFile{Version: EWayVersion, BillLists: []EWayBill{b}}, warnings, below, nil
}

func applyTransport(b *EWayBill, t Transport) error {
	if t.TransDistance < 0 || t.TransDistance > gsp.MaxDistance {
		return fmt.Errorf("distance must be 0-%d km (0 lets the portal compute it from PIN codes)", gsp.MaxDistance)
	}
	b.TransDistance = fmt.Sprint(t.TransDistance)
	b.TransporterID = t.TransporterID
	b.TransporterName = Truncate(t.TransporterName, 100)
	if t.TransMode == "" {
		// Part-A only: allowed when a transporter will fill Part-B.
		if b.TransporterID == "" {
			return errors.New("give a transport mode with vehicle / transport document, or a transporter_id for Part-A only")
		}
		return nil
	}
	mode := t.ModeCode()
	if mode == "" {
		return errors.New("mode must be 1 (road), 2 (rail), 3 (air) or 4 (ship)")
	}
	b.TransMode = mode
	if mode == "1" {
		if t.VehicleNo == "" && b.TransporterID == "" {
			return errors.New("vehicle_no is required for road transport")
		}
		if t.VehicleNo != "" && !gsp.ValidVehicle(t.VehicleNo) {
			return fmt.Errorf("vehicle_no %q is not a valid registration number (e.g. MH12AB1234, 22BH1234AA or a TR temporary number)", t.VehicleNo)
		}
		b.VehicleNo = t.VehicleNo
		b.VehicleType = "R"
		if t.VehicleType == "O" {
			b.VehicleType = "O"
		} else if t.VehicleType != "" && t.VehicleType != "R" {
			return errors.New("vehicle_type must be R (regular) or O (over-dimensional cargo)")
		}
	} else if t.TransDocNo == "" {
		return errors.New("trans_doc_no is required for rail, air and ship")
	}
	b.TransDocNo = Truncate(t.TransDocNo, 15)
	if t.TransDocDate != "" {
		d, err := NICDate(t.TransDocDate)
		if err != nil {
			return errors.New("trans_doc_date must be YYYY-MM-DD")
		}
		b.TransDocDate = d
	}
	return nil
}
