package events

// Payload structs for every event type. Publishers fill them, subscribers
// decode them with Envelope.Decode. Add fields freely; never rename or remove
// one without updating every subscriber in ARCHITECTURE.md §5.

// InvoiceLine is one line of an invoice/note event.
type InvoiceLine struct {
	LineNo         int     `json:"line_no"`
	ItemID         string  `json:"item_id,omitempty"`
	Name           string  `json:"name"`
	HSN            string  `json:"hsn,omitempty"`
	Unit           string  `json:"unit"`
	BatchNo        string  `json:"batch_no,omitempty"`
	Qty            float64 `json:"qty"`
	UnitPricePaise int64   `json:"unit_price_paise"`
	DiscountPaise  int64   `json:"discount_paise"`
	PriceInclTax   bool    `json:"price_includes_tax"`
	TaxRateBP      int     `json:"tax_rate_bp"`
	TaxablePaise   int64   `json:"taxable_paise"`
	CGSTPaise      int64   `json:"cgst_paise"`
	SGSTPaise      int64   `json:"sgst_paise"`
	IGSTPaise      int64   `json:"igst_paise"`
	CessPaise      int64   `json:"cess_paise"`
	TotalPaise     int64   `json:"total_paise"`
}

// Tender is a counter payment recorded on the invoice.
type Tender struct {
	Mode        string `json:"mode"` // cash | card | upi | bank | credit | other
	AmountPaise int64  `json:"amount_paise"`
	Reference   string `json:"reference,omitempty"`
}

// InvoicePayload is the data of aadesh.invoice.created / .cancelled and aadesh.note.created.
type InvoicePayload struct {
	InvoiceID       string        `json:"invoice_id"`
	StoreID         string        `json:"store_id"`
	DocType         string        `json:"doc_type"` // sale | purchase | sale_return | purchase_return
	Number          string        `json:"number"`
	InvoiceDate     string        `json:"invoice_date"` // YYYY-MM-DD
	RefInvoiceID    string        `json:"ref_invoice_id,omitempty"`
	PartyID         string        `json:"party_id,omitempty"`
	PartyName       string        `json:"party_name"`
	PartyPhone      string        `json:"party_phone,omitempty"`
	PartyGSTIN      string        `json:"party_gstin,omitempty"`
	PlaceOfSupply   string        `json:"place_of_supply"`
	InterState      bool          `json:"inter_state"`
	TaxablePaise    int64         `json:"taxable_paise"`
	CGSTPaise       int64         `json:"cgst_paise"`
	SGSTPaise       int64         `json:"sgst_paise"`
	IGSTPaise       int64         `json:"igst_paise"`
	CessPaise       int64         `json:"cess_paise"`
	RoundOffPaise   int64         `json:"round_off_paise"`
	GrandTotalPaise int64         `json:"grand_total_paise"`
	Lines           []InvoiceLine `json:"lines"`
	Tenders         []Tender      `json:"tenders,omitempty"`
	CreatedBy       string        `json:"created_by"`
}

// PaymentPayload is the data of lenden.payment.captured / .refunded.
type PaymentPayload struct {
	PaymentID   string `json:"payment_id"` // lenden's id (stable, used for idempotency)
	InvoiceID   string `json:"invoice_id,omitempty"`
	PartyID     string `json:"party_id,omitempty"`
	PartyPhone  string `json:"party_phone,omitempty"`
	AmountPaise int64  `json:"amount_paise"`
	Method      string `json:"method"`  // upi | card | netbanking | wallet
	Gateway     string `json:"gateway"` // razorpay
	GatewayRef  string `json:"gateway_ref"`
	CapturedAt  string `json:"captured_at"` // RFC3339
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

// ProductionLine is a material consumed or produced.
type ProductionLine struct {
	ItemID        string  `json:"item_id"`
	Qty           float64 `json:"qty"`
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
