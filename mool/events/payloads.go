package events

// Payload structs for every event type. Publishers fill them, subscribers
// decode them with Envelope.Decode. Add fields freely; never rename or remove
// one without updating every subscriber in ARCHITECTURE.md §5.

// InvoiceLine is one line of an invoice/note event.
type InvoiceLine struct {
	LineNo           int     `json:"line_no"`
	ItemID           string  `json:"item_id,omitempty"`
	Name             string  `json:"name"`
	HSN              string  `json:"hsn,omitempty"`
	Unit             string  `json:"unit"`
	BatchNo          string  `json:"batch_no,omitempty"`
	Qty              float64 `json:"qty"`
	UnitPricePaise   int64   `json:"unit_price_paise"`
	DiscountPaise    int64   `json:"discount_paise"`
	PriceInclTax     bool    `json:"price_includes_tax"`
	TaxRateBP        int     `json:"tax_rate_bp"`
	CessRateBP       int     `json:"cess_rate_bp,omitempty"`
	CessPerUnitPaise int64   `json:"cess_per_unit_paise,omitempty"`
	TaxCategory      string  `json:"tax_category,omitempty"` // taxable | exempt | nil_rated | non_gst
	TaxablePaise     int64   `json:"taxable_paise"`
	CGSTPaise        int64   `json:"cgst_paise"`
	SGSTPaise        int64   `json:"sgst_paise"`
	IGSTPaise        int64   `json:"igst_paise"`
	CessPaise        int64   `json:"cess_paise"`
	TotalPaise       int64   `json:"total_paise"`
	ITCEligibility   string  `json:"itc_eligibility,omitempty"` // purchase lines: eligible | ineligible_17_5 | ineligible_other
}

// Tender is a counter payment recorded on the invoice.
type Tender struct {
	Mode             string `json:"mode"` // cash | card | upi | bank | credit | other
	AmountPaise      int64  `json:"amount_paise"`
	Reference        string `json:"reference,omitempty"`
	PaymentRequestID string `json:"payment_request_id,omitempty"` // lenden request behind a counter UPI/card tender
}

// InvoicePayload is the data of aadesh.invoice.created / .cancelled and aadesh.note.created.
type InvoicePayload struct {
	InvoiceID           string        `json:"invoice_id"`
	StoreID             string        `json:"store_id"`
	StoreState          string        `json:"store_state,omitempty"`
	StoreGSTIN          string        `json:"store_gstin,omitempty"`
	DocType             string        `json:"doc_type"` // sale | purchase | sale_return | purchase_return
	Number              string        `json:"number"`
	InvoiceDate         string        `json:"invoice_date"` // YYYY-MM-DD
	DueDate             string        `json:"due_date,omitempty"`
	RefInvoiceID        string        `json:"ref_invoice_id,omitempty"`
	SupplierInvoiceNo   string        `json:"supplier_invoice_no,omitempty"`
	SupplierInvoiceDate string        `json:"supplier_invoice_date,omitempty"`
	PartyID             string        `json:"party_id,omitempty"`
	PartyName           string        `json:"party_name"`
	PartyPhone          string        `json:"party_phone,omitempty"`
	PartyGSTIN          string        `json:"party_gstin,omitempty"`
	PlaceOfSupply       string        `json:"place_of_supply"`
	InterState          bool          `json:"inter_state"`
	SupplyCategory      string        `json:"supply_category,omitempty"` // "", sez_*, export_*, deemed_export
	ReverseCharge       bool          `json:"reverse_charge,omitempty"`
	BillOfSupply        bool          `json:"bill_of_supply,omitempty"`
	TaxablePaise        int64         `json:"taxable_paise"`
	DiscountPaise       int64         `json:"discount_paise,omitempty"`
	CGSTPaise           int64         `json:"cgst_paise"`
	SGSTPaise           int64         `json:"sgst_paise"`
	IGSTPaise           int64         `json:"igst_paise"`
	CessPaise           int64         `json:"cess_paise"`
	TCSPaise            int64         `json:"tcs_paise,omitempty"`
	RoundOffPaise       int64         `json:"round_off_paise"`
	GrandTotalPaise     int64         `json:"grand_total_paise"`
	IRN                 string        `json:"irn,omitempty"`
	Lines               []InvoiceLine `json:"lines"`
	Tenders             []Tender      `json:"tenders,omitempty"`
	CreatedBy           string        `json:"created_by"`
}

// PaymentPayload is the data of lenden.payment.captured / .refunded.
type PaymentPayload struct {
	PaymentID        string `json:"payment_id"`                   // lenden's id (stable, used for idempotency)
	RefundID         string `json:"refund_id,omitempty"`          // set on .refunded; each partial refund has its own
	PaymentRequestID string `json:"payment_request_id,omitempty"` // counter QR/link request this capture answers
	InvoiceID        string `json:"invoice_id,omitempty"`
	PartyID          string `json:"party_id,omitempty"`
	PartyPhone       string `json:"party_phone,omitempty"`
	AmountPaise      int64  `json:"amount_paise"`
	Method           string `json:"method"`  // upi | card | netbanking | wallet | cash | cheque | bank
	Gateway          string `json:"gateway"` // razorpay | cashfree | manual
	GatewayRef       string `json:"gateway_ref"`
	CapturedAt       string `json:"captured_at"` // RFC3339
}

// SettlementPayload is the data of lenden.settlement.processed: the gateway
// paid the store's bank account for a batch of captures, net of its fee.
type SettlementPayload struct {
	SettlementID    string   `json:"settlement_id"` // lenden's id (idempotency)
	Gateway         string   `json:"gateway"`       // razorpay | cashfree
	GatewayRef      string   `json:"gateway_ref"`   // settlement id at the gateway
	UTR             string   `json:"utr,omitempty"` // bank reference of the payout
	GrossPaise      int64    `json:"gross_paise"`   // captures settled
	FeePaise        int64    `json:"fee_paise"`     // MDR / platform fee (ex tax)
	FeeTaxPaise     int64    `json:"fee_tax_paise"` // GST on the fee (input credit)
	RefundsPaise    int64    `json:"refunds_paise"` // refunds netted in this settlement
	NetPaise        int64    `json:"net_paise"`     // credited to the bank account
	PaymentIDs      []string `json:"payment_ids,omitempty"`
	SettledAt       string   `json:"settled_at"` // RFC3339
	BankAccountLast string   `json:"bank_account_last,omitempty"`
}

// StockLowPayload is the data of bhandar.stock.low.
type StockLowPayload struct {
	ItemID       string  `json:"item_id"`
	ItemName     string  `json:"item_name"`
	StoreID      string  `json:"store_id"`
	OnHand       float64 `json:"on_hand"`
	ReorderLevel float64 `json:"reorder_level"`
	ReorderQty   float64 `json:"reorder_qty"`
	SupplierID   string  `json:"preferred_supplier_id,omitempty"`
}

// BatchExpiringPayload is the data of bhandar.batch.expiring.
type BatchExpiringPayload struct {
	ItemID     string  `json:"item_id"`
	ItemName   string  `json:"item_name"`
	StoreID    string  `json:"store_id"`
	BatchNo    string  `json:"batch_no"`
	ExpiryDate string  `json:"expiry_date"`
	Qty        float64 `json:"qty"`
	DaysLeft   int     `json:"days_left"`
}

// StockMovement is one line of bhandar.stock.moved.
type StockMovement struct {
	MovementID    string  `json:"movement_id"`
	LineNo        int     `json:"line_no"`
	ItemID        string  `json:"item_id"`
	ItemName      string  `json:"item_name"`
	Unit          string  `json:"unit"`
	BatchNo       string  `json:"batch_no,omitempty"`
	Qty           float64 `json:"qty"`             // signed: positive into the store, negative out
	UnitCostPaise int64   `json:"unit_cost_paise"` // weighted-average cost applied to this movement
	ValuePaise    int64   `json:"value_paise"`     // round(qty × unit cost), signed
	Reason        string  `json:"reason"`          // sale | purchase | sale_return | purchase_return | transfer_in | transfer_out | adjustment | opening | production_consume | production_output | expiry_writeoff
}

// StockMovedPayload is the data of bhandar.stock.moved: every stock movement
// posted for one source document, with its cost, so bahi can book COGS and
// inventory (perpetual costing) and buddhi can track margins.
type StockMovedPayload struct {
	StoreID    string          `json:"store_id"`
	Source     string          `json:"source"`    // aadesh.invoice | aadesh.note | nirmaan.production | adjustment | transfer | opening
	SourceID   string          `json:"source_id"` // invoice id, run id, transfer id …
	Reason     string          `json:"reason"`
	Movements  []StockMovement `json:"movements"`
	ValuePaise int64           `json:"value_paise"` // Σ movement value, signed
	OccurredAt string          `json:"occurred_at"` // RFC3339
	Reversal   bool            `json:"reversal,omitempty"`
}

// ProductionLine is a material consumed or produced.
type ProductionLine struct {
	ItemID        string  `json:"item_id"`
	Qty           float64 `json:"qty"`
	Unit          string  `json:"unit,omitempty"` // unit the qty is expressed in; empty = the item's base unit
	UnitCostPaise int64   `json:"unit_cost_paise"`
	BatchNo       string  `json:"batch_no,omitempty"`
}

// ProductionCompletedPayload is the data of nirmaan.production.completed.
type ProductionCompletedPayload struct {
	ProductionRunID string           `json:"production_run_id"`
	StoreID         string           `json:"store_id"`
	BOMID           string           `json:"bom_id"`
	Consumed        []ProductionLine `json:"consumed"`
	Produced        []ProductionLine `json:"produced"`
	TotalCostPaise  int64            `json:"total_cost_paise"`
	CompletedAt     string           `json:"completed_at"`
}

// ImportCompletedPayload is the data of aayaat.import.completed.
type ImportCompletedPayload struct {
	JobID       string `json:"job_id"`
	Kind        string `json:"kind"` // parties | items | opening_balances | opening_stock | invoices
	Total       int    `json:"total"`
	Imported    int    `json:"imported"`
	Failed      int    `json:"failed"`
	RequestedBy string `json:"requested_by"`
	NotifyPhone string `json:"notify_phone,omitempty"`
}

// EInvoicePayload is the data of kar.einvoice.generated: the IRP registered (or
// cancelled) an invoice. aadesh stores the IRN, acknowledgement and signed QR on
// the invoice so it prints them; Status tells which.
type EInvoicePayload struct {
	InvoiceID    string `json:"invoice_id"`
	Number       string `json:"number"`
	GSTIN        string `json:"gstin"`         // supplier registration that filed it
	Status       string `json:"status"`        // generated | cancelled | failed
	IRN          string `json:"irn,omitempty"` // 64-char hash
	AckNo        string `json:"ack_no,omitempty"`
	AckDate      string `json:"ack_date,omitempty"`  // RFC3339
	SignedQR     string `json:"signed_qr,omitempty"` // JWS the IRP signed; printed as a QR code
	CancelledAt  string `json:"cancelled_at,omitempty"`
	Error        string `json:"error,omitempty"` // when Status=failed: the IRP's message
	ErrorCode    string `json:"error_code,omitempty"`
	GeneratedVia string `json:"generated_via,omitempty"` // gsp provider name (simulator in dev)
}

// EWayPayload is the data of kar.eway.generated: an e-way bill was generated,
// updated (Part-B / extension) or cancelled for an invoice.
type EWayPayload struct {
	InvoiceID  string `json:"invoice_id"`
	Number     string `json:"number"`
	Status     string `json:"status"` // generated | updated | extended | cancelled | failed
	EWBNo      string `json:"ewb_no,omitempty"`
	EWBDate    string `json:"ewb_date,omitempty"`   // RFC3339
	ValidUpto  string `json:"valid_upto,omitempty"` // RFC3339
	VehicleNo  string `json:"vehicle_no,omitempty"`
	DistanceKM int    `json:"distance_km,omitempty"`
	Error      string `json:"error,omitempty"`
}

// TenantCreatedPayload is the data of pehchaan.tenant.created.
type TenantCreatedPayload struct {
	TenantID     string `json:"tenant_id"`
	BusinessName string `json:"business_name"`
	GSTIN        string `json:"gstin,omitempty"`
	StateCode    string `json:"state_code"`
	OwnerUserID  string `json:"owner_user_id"`
	OwnerPhone   string `json:"owner_phone"`
	StoreID      string `json:"store_id"` // the default store created with the tenant
}
