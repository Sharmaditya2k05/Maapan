package compliance

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/taraju/kar/internal/clients"
	"github.com/taraju/mool/money"
)

// EWayVersion is the NIC bulk-generation JSON version this builder targets.
const EWayVersion = "1.0.0621"

// Transport is the Part-B information supplied when generating the JSON.
type Transport struct {
	TransMode       string `json:"trans_mode"` // road | rail | air | ship (or 1-4)
	TransDistance   int    `json:"trans_distance"`
	TransporterID   string `json:"transporter_id,omitempty"`
	TransporterName string `json:"transporter_name,omitempty"`
	TransDocNo      string `json:"trans_doc_no,omitempty"`
	TransDocDate    string `json:"trans_doc_date,omitempty"` // YYYY-MM-DD
	VehicleNo       string `json:"vehicle_no,omitempty"`
	VehicleType     string `json:"vehicle_type,omitempty"` // R regular | O over-dimensional
	FromPincode     int    `json:"from_pincode,omitempty"`
	ToPincode       int    `json:"to_pincode,omitempty"`
}

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

// EWayBill is one entry of billLists.
type EWayBill struct {
	UserGstin         string     `json:"userGstin"`
	SupplyType        string     `json:"supplyType"` // O outward | I inward
	SubSupplyType     int        `json:"subSupplyType"`
	SubSupplyDesc     string     `json:"subSupplyDesc"`
	DocType           string     `json:"docType"` // INV | BIL | BOE | CHL | CNT | OTH
	DocNo             string     `json:"docNo"`
	DocDate           string     `json:"docDate"`
	FromGstin         string     `json:"fromGstin"`
	FromTrdName       string     `json:"fromTrdName"`
	FromAddr1         string     `json:"fromAddr1"`
	FromAddr2         string     `json:"fromAddr2"`
	FromPlace         string     `json:"fromPlace"`
	FromPincode       int        `json:"fromPincode"`
	ActFromStateCode  int        `json:"actFromStateCode"`
	FromStateCode     int        `json:"fromStateCode"`
	ToGstin           string     `json:"toGstin"`
	ToTrdName         string     `json:"toTrdName"`
	ToAddr1           string     `json:"toAddr1"`
	ToAddr2           string     `json:"toAddr2"`
	ToPlace           string     `json:"toPlace"`
	ToPincode         int        `json:"toPincode"`
	ActToStateCode    int        `json:"actToStateCode"`
	ToStateCode       int        `json:"toStateCode"`
	TransactionType   int        `json:"transactionType"` // 1 regular
	TotalValue        float64    `json:"totalValue"`
	CgstValue         float64    `json:"cgstValue"`
	SgstValue         float64    `json:"sgstValue"`
	IgstValue         float64    `json:"igstValue"`
	CessValue         float64    `json:"cessValue"`
	CessNonAdvolValue float64    `json:"cessNonAdvolValue"`
	OtherValue        float64    `json:"otherValue"`
	TotInvValue       float64    `json:"totInvValue"`
	TransMode         string     `json:"transMode"`
	TransDistance     string     `json:"transDistance"`
	TransporterName   string     `json:"transporterName"`
	TransporterID     string     `json:"transporterId"`
	TransDocNo        string     `json:"transDocNo"`
	TransDocDate      string     `json:"transDocDate"`
	VehicleNo         string     `json:"vehicleNo"`
	VehicleType       string     `json:"vehicleType"`
	ItemList          []EWayItem `json:"itemList"`
}

// EWayFile is the uploadable bulk JSON.
type EWayFile struct {
	Version   string     `json:"version"`
	BillLists []EWayBill `json:"billLists"`
}

var (
	vehiclePattern = regexp.MustCompile(`^[A-Z]{2}[0-9]{1,2}[A-Z]{0,3}[0-9]{4}$|^TM[0-9A-Z]{6}$`)
	modeCodes      = map[string]string{"road": "1", "rail": "2", "air": "3", "ship": "4", "1": "1", "2": "2", "3": "3", "4": "4"}
)

// BuildEWay maps an invoice to NIC e-Way Bill JSON. our is the store (seller for
// outward, recipient for inward); party is the counterparty. It returns
// warnings (non-blocking) and whether the value is below the threshold.
func BuildEWay(inv clients.Invoice, our, party Party, t Transport, thresholdPaise int64) (EWayFile, []string, bool, error) {
	var warnings []string
	if inv.Status != "posted" {
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
	if len(b.DocNo) > 16 {
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
	if from.Pincode < 100000 || to.Pincode < 100000 {
		return EWayFile{}, nil, false, errors.New("6-digit from and to PIN codes are required (pass from_pincode / to_pincode)")
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
			a2 = p.Address2
		}
		return gstin, name, a1, a2, p.Place, p.Pincode, stateInt(p.StateCode)
	}
	b.FromGstin, b.FromTrdName, b.FromAddr1, b.FromAddr2, b.FromPlace, b.FromPincode, b.FromStateCode = fill(from)
	b.ToGstin, b.ToTrdName, b.ToAddr1, b.ToAddr2, b.ToPlace, b.ToPincode, b.ToStateCode = fill(to)
	b.ActFromStateCode, b.ActToStateCode = b.FromStateCode, b.ToStateCode
	if b.FromStateCode == 0 || b.ToStateCode == 0 {
		return EWayFile{}, nil, false, errors.New("state codes of both parties are required")
	}

	b.TotalValue = Rupees(inv.TaxablePaise)
	b.CgstValue, b.SgstValue, b.IgstValue = Rupees(inv.CGSTPaise), Rupees(inv.SGSTPaise), Rupees(inv.IGSTPaise)
	b.CessValue = Rupees(inv.CessPaise)
	b.OtherValue = Rupees(inv.RoundOffPaise)
	b.TotInvValue = Rupees(inv.GrandTotalPaise)

	for _, l := range inv.Lines {
		if IsService(l.HSN) {
			continue // services do not move on an e-way bill
		}
		if l.HSN == "" {
			return EWayFile{}, nil, false, fmt.Errorf("line %d (%s) has no HSN code", l.LineNo, l.Name)
		}
		it := EWayItem{
			ItemNo: len(b.ItemList) + 1, ProductName: truncate(l.Name, 100), ProductDesc: truncate(l.Name, 100),
			HsnCode: hsnInt(l.HSN), Quantity: round(l.Qty, 3), QtyUnit: UQC(l.Unit),
			TaxableAmount: Rupees(l.TaxablePaise), CessRate: RateFromBP(l.CessRateBP),
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

	if err := applyTransport(&b, t); err != nil {
		return EWayFile{}, nil, false, err
	}
	below := inv.GrandTotalPaise < thresholdPaise
	if below {
		warnings = append(warnings, fmt.Sprintf("consignment value %s is below the e-way threshold %s; an e-way bill is optional",
			money.Format(inv.GrandTotalPaise), money.Format(thresholdPaise)))
	}
	return EWayFile{Version: EWayVersion, BillLists: []EWayBill{b}}, warnings, below, nil
}

func applyTransport(b *EWayBill, t Transport) error {
	if t.TransDistance < 0 || t.TransDistance > 4000 {
		return errors.New("trans_distance must be 0-4000 km (0 lets the portal compute it from PIN codes)")
	}
	b.TransDistance = fmt.Sprint(t.TransDistance)
	b.TransporterID = strings.ToUpper(strings.TrimSpace(t.TransporterID))
	b.TransporterName = t.TransporterName
	if t.TransMode == "" {
		// Part-A only: allowed when a transporter will fill Part-B.
		if b.TransporterID == "" {
			return errors.New("give trans_mode with vehicle/transport document, or a transporter_id for Part-A only")
		}
		return nil
	}
	mode, ok := modeCodes[strings.ToLower(t.TransMode)]
	if !ok {
		return errors.New("trans_mode must be road, rail, air or ship")
	}
	b.TransMode = mode
	if mode == "1" {
		v := strings.ToUpper(strings.NewReplacer(" ", "", "-", "").Replace(t.VehicleNo))
		if v == "" && b.TransporterID == "" {
			return errors.New("vehicle_no is required for road transport")
		}
		if v != "" && !vehiclePattern.MatchString(v) {
			return fmt.Errorf("vehicle_no %q is not a valid registration number", t.VehicleNo)
		}
		b.VehicleNo = v
		b.VehicleType = "R"
		if t.VehicleType == "O" {
			b.VehicleType = "O"
		}
	} else if t.TransDocNo == "" {
		return errors.New("trans_doc_no is required for rail, air and ship")
	}
	b.TransDocNo = t.TransDocNo
	if t.TransDocDate != "" {
		d, err := NICDate(t.TransDocDate)
		if err != nil {
			return errors.New("trans_doc_date must be YYYY-MM-DD")
		}
		b.TransDocDate = d
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
