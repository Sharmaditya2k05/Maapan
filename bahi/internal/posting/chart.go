// Package posting holds bahi's pure accounting rules: the default chart of
// accounts and how each business event becomes a balanced voucher. No I/O, so
// every rule is unit-tested.
package posting

// System account codes. Rules refer to accounts by these codes; the ledger
// resolves them to per-tenant account IDs.
const (
	GroupAssets           = "1000"
	Cash                  = "1100"
	Bank                  = "1110"
	ChequesInHand         = "1115" // cheques received, not yet deposited
	GatewayClearing       = "1130" // Razorpay/Cashfree captures until the payout lands in Bank
	SundryDebtors         = "1200" // group: one child account per customer
	WalkInCustomers       = "1210"
	StockInHand           = "1300" // group
	StockFinished         = "1310"
	StockRaw              = "1320"
	WorkInProgress        = "1330"
	InputTaxes            = "1400" // group
	InputCGST             = "1410"
	InputSGST             = "1411"
	InputIGST             = "1412"
	InputCess             = "1413"
	GroupLiabilities      = "2000"
	SundryCreditors       = "2100" // group: one child account per supplier
	CashSuppliers         = "2190"
	OutputTaxes           = "2200" // group
	OutputCGST            = "2210"
	OutputSGST            = "2211"
	OutputIGST            = "2212"
	OutputCess            = "2213"
	GroupEquity           = "3000"
	Capital               = "3100"
	OpeningDiff           = "3900"
	GroupIncome           = "4000"
	Sales                 = "4100"
	SalesReturns          = "4110"
	StockGain             = "4150" // positive stock adjustments (perpetual inventory)
	OtherIncome           = "4900"
	GroupExpenses         = "5000"
	Purchases             = "5100"
	PurchaseReturns       = "5110"
	COGS                  = "5150" // cost of goods sold (perpetual inventory)
	StockLoss             = "5160" // shrinkage, expiry write-offs
	DirectExpenses        = "5200" // group
	OtherDirectExpenses   = "5290"
	IndirectExpenses      = "5500" // group
	BankCharges           = "5510"
	OtherIndirectExpenses = "5590"
	DiscountAllowed       = "5800"
	RoundOff              = "5900"
)

// Inventory is the stock account bhandar.stock.moved reclassifies against.
// bahi cannot tell raw material from finished goods on a movement, so every
// costed movement uses Stock - Finished Goods; the Stock-in-hand group total is
// what the balance sheet shows.
const Inventory = StockFinished

// SeedAccount is one row of the default chart.
type SeedAccount struct {
	Code    string
	Name    string
	Group   string // asset | liability | income | expense | equity
	Parent  string // parent code, "" for roots
	IsGroup bool
	IsCash  bool
	IsBank  bool
}

// DefaultChart is the Indian retail chart of accounts seeded for each new tenant.
// Parents always appear before children.
var DefaultChart = []SeedAccount{
	{Code: GroupAssets, Name: "Assets", Group: "asset", IsGroup: true},
	{Code: Cash, Name: "Cash-in-hand", Group: "asset", Parent: GroupAssets, IsCash: true},
	{Code: Bank, Name: "Bank Account", Group: "asset", Parent: GroupAssets, IsBank: true},
	{Code: ChequesInHand, Name: "Cheques in Hand", Group: "asset", Parent: GroupAssets},
	{Code: GatewayClearing, Name: "Payment Gateway Clearing", Group: "asset", Parent: GroupAssets},
	{Code: SundryDebtors, Name: "Sundry Debtors", Group: "asset", Parent: GroupAssets, IsGroup: true},
	{Code: WalkInCustomers, Name: "Walk-in Customers", Group: "asset", Parent: SundryDebtors},
	{Code: StockInHand, Name: "Stock-in-hand", Group: "asset", Parent: GroupAssets, IsGroup: true},
	{Code: StockFinished, Name: "Stock - Finished Goods", Group: "asset", Parent: StockInHand},
	{Code: StockRaw, Name: "Stock - Raw Materials", Group: "asset", Parent: StockInHand},
	{Code: WorkInProgress, Name: "Work-in-Progress", Group: "asset", Parent: StockInHand},
	{Code: InputTaxes, Name: "Duties & Taxes (Input)", Group: "asset", Parent: GroupAssets, IsGroup: true},
	{Code: InputCGST, Name: "Input CGST", Group: "asset", Parent: InputTaxes},
	{Code: InputSGST, Name: "Input SGST", Group: "asset", Parent: InputTaxes},
	{Code: InputIGST, Name: "Input IGST", Group: "asset", Parent: InputTaxes},
	{Code: InputCess, Name: "Input Cess", Group: "asset", Parent: InputTaxes},

	{Code: GroupLiabilities, Name: "Liabilities", Group: "liability", IsGroup: true},
	{Code: SundryCreditors, Name: "Sundry Creditors", Group: "liability", Parent: GroupLiabilities, IsGroup: true},
	{Code: CashSuppliers, Name: "Cash / Unregistered Suppliers", Group: "liability", Parent: SundryCreditors},
	{Code: OutputTaxes, Name: "Duties & Taxes (Output)", Group: "liability", Parent: GroupLiabilities, IsGroup: true},
	{Code: OutputCGST, Name: "Output CGST", Group: "liability", Parent: OutputTaxes},
	{Code: OutputSGST, Name: "Output SGST", Group: "liability", Parent: OutputTaxes},
	{Code: OutputIGST, Name: "Output IGST", Group: "liability", Parent: OutputTaxes},
	{Code: OutputCess, Name: "Output Cess", Group: "liability", Parent: OutputTaxes},

	{Code: GroupEquity, Name: "Capital & Equity", Group: "equity", IsGroup: true},
	{Code: Capital, Name: "Capital Account", Group: "equity", Parent: GroupEquity},
	{Code: OpeningDiff, Name: "Opening Balance Difference", Group: "equity", Parent: GroupEquity},

	{Code: GroupIncome, Name: "Income", Group: "income", IsGroup: true},
	{Code: Sales, Name: "Sales", Group: "income", Parent: GroupIncome},
	{Code: SalesReturns, Name: "Sales Returns", Group: "income", Parent: GroupIncome},
	{Code: StockGain, Name: "Stock Gain", Group: "income", Parent: GroupIncome},
	{Code: OtherIncome, Name: "Other Income", Group: "income", Parent: GroupIncome},

	{Code: GroupExpenses, Name: "Expenses", Group: "expense", IsGroup: true},
	{Code: Purchases, Name: "Purchases", Group: "expense", Parent: GroupExpenses},
	{Code: PurchaseReturns, Name: "Purchase Returns", Group: "expense", Parent: GroupExpenses},
	{Code: DirectExpenses, Name: "Direct Expenses", Group: "expense", Parent: GroupExpenses, IsGroup: true},
	{Code: COGS, Name: "Cost of Goods Sold", Group: "expense", Parent: DirectExpenses},
	{Code: StockLoss, Name: "Stock Loss / Write-off", Group: "expense", Parent: DirectExpenses},
	{Code: OtherDirectExpenses, Name: "Other Direct Expenses", Group: "expense", Parent: DirectExpenses},
	{Code: IndirectExpenses, Name: "Indirect Expenses", Group: "expense", Parent: GroupExpenses, IsGroup: true},
	{Code: BankCharges, Name: "Bank Charges", Group: "expense", Parent: IndirectExpenses},
	{Code: OtherIndirectExpenses, Name: "Other Indirect Expenses", Group: "expense", Parent: IndirectExpenses},
	{Code: DiscountAllowed, Name: "Discount Allowed", Group: "expense", Parent: IndirectExpenses},
	{Code: RoundOff, Name: "Round Off", Group: "expense", Parent: IndirectExpenses},
}

// GrossProfitCodes are the leaf accounts whose movement is part of gross profit
// in addition to every leaf under the Direct Expenses group.
var GrossProfitCodes = map[string]bool{Sales: true, SalesReturns: true, Purchases: true, PurchaseReturns: true}
