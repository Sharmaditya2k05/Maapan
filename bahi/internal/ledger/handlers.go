package ledger

import (
	"net/http"

	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/events"
	"github.com/maapan/mool/httpx"
)

// Routes registers bahi's HTTP API on mux.
func (s *Service) Routes(mux *http.ServeMux, v *auth.Verifier, trustIAM bool) {
	user := func(h httpx.HandlerFunc, perms ...string) http.Handler {
		return v.User(auth.Require(httpx.H(h), perms...))
	}
	internal := func(h httpx.HandlerFunc) http.Handler { return v.Internal(httpx.H(h)) }

	// Chart of accounts
	mux.Handle("GET /v1/accounts", user(s.handleListAccounts, auth.PermLedgerRead))
	mux.Handle("POST /v1/accounts", user(s.handleCreateAccount, auth.PermLedgerWrite))
	mux.Handle("PATCH /v1/accounts/{id}", user(s.handleUpdateAccount, auth.PermLedgerWrite))
	mux.Handle("DELETE /v1/accounts/{id}", user(s.handleDeleteAccount, auth.PermLedgerWrite))
	mux.Handle("GET /v1/accounts/{id}/statement", user(s.handleAccountStatement, auth.PermLedgerRead))

	// Parties
	mux.Handle("GET /v1/parties", user(s.handleListParties, auth.PermPartyManage))
	mux.Handle("POST /v1/parties", user(s.handleCreateParty, auth.PermPartyManage))
	mux.Handle("GET /v1/parties/{id}", user(s.handleGetParty, auth.PermPartyManage))
	mux.Handle("PATCH /v1/parties/{id}", user(s.handleUpdateParty, auth.PermPartyManage))
	mux.Handle("DELETE /v1/parties/{id}", user(s.handleDeleteParty, auth.PermPartyManage))
	mux.Handle("GET /v1/parties/{id}/statement", user(s.handlePartyStatement, auth.PermLedgerRead)) // ?view=bills: bill-wise
	mux.Handle("GET /v1/parties/{id}/bills", user(s.handlePartyBills, auth.PermLedgerRead))
	mux.Handle("GET /v1/invoice-history", user(s.handleInvoiceHistory, auth.PermLedgerRead))
	mux.Handle("POST /v1/parties/{id}/allocations/auto", user(s.handleAutoAllocate, auth.PermLedgerWrite))

	// Vouchers
	mux.Handle("GET /v1/vouchers", user(s.handleListVouchers, auth.PermLedgerRead))
	mux.Handle("POST /v1/vouchers", user(s.handleCreateVoucher, auth.PermLedgerWrite))
	mux.Handle("GET /v1/vouchers/{id}", user(s.handleGetVoucher, auth.PermLedgerRead))
	mux.Handle("POST /v1/vouchers/{id}/reverse", user(s.handleReverseVoucher, auth.PermLedgerWrite))
	mux.Handle("GET /v1/vouchers/{id}/allocations", user(s.handleGetAllocations, auth.PermLedgerRead))
	mux.Handle("PUT /v1/vouchers/{id}/allocations", user(s.handlePutAllocations, auth.PermLedgerWrite))

	// Books
	mux.Handle("GET /v1/cashbook", user(s.handleCashbook, auth.PermLedgerRead))
	mux.Handle("GET /v1/bankbook", user(s.handleBankbook, auth.PermLedgerRead))
	mux.Handle("GET /v1/daybook", user(s.handleDayBook, auth.PermLedgerRead))

	// Bank & cheques
	mux.Handle("GET /v1/bank/transactions", user(s.handleListBankTxns, auth.PermLedgerRead))
	mux.Handle("POST /v1/bank/transactions", user(s.handleCreateBankTxn, auth.PermLedgerWrite))
	mux.Handle("POST /v1/bank/transactions/{id}/reconcile", user(s.handleReconcileBankTxn, auth.PermLedgerWrite))
	mux.Handle("GET /v1/cheques", user(s.handleListCheques, auth.PermLedgerRead))
	mux.Handle("POST /v1/cheques", user(s.handleCreateCheque, auth.PermPaymentCollect))
	mux.Handle("POST /v1/cheques/{id}/status", user(s.handleChequeStatus, auth.PermLedgerWrite))
	mux.Handle("GET /v1/cheques/pdc", user(s.handlePDCReport, auth.PermLedgerRead))

	// Bank statements & reconciliation
	mux.Handle("GET /v1/bank/statement-presets", user(s.handleStatementPresets, auth.PermLedgerRead))
	mux.Handle("POST /v1/bank/statements/import", user(s.handleImportStatement, auth.PermLedgerWrite))
	mux.Handle("GET /v1/bank/statements", user(s.handleListStatements, auth.PermLedgerRead))
	mux.Handle("GET /v1/bank/statement-lines", user(s.handleListBankLines, auth.PermLedgerRead))
	mux.Handle("POST /v1/bank/auto-match", user(s.handleAutoMatch, auth.PermLedgerWrite))
	mux.Handle("POST /v1/bank/statement-lines/{id}/match", user(s.handleMatchLine, auth.PermLedgerWrite))
	mux.Handle("POST /v1/bank/statement-lines/{id}/unmatch", user(s.handleUnmatchLine, auth.PermLedgerWrite))
	mux.Handle("POST /v1/bank/statement-lines/{id}/ignore", user(s.handleIgnoreLine, auth.PermLedgerWrite))
	mux.Handle("POST /v1/bank/statement-lines/{id}/voucher", user(s.handleLineVoucher, auth.PermLedgerWrite))

	// TDS / TCS
	mux.Handle("GET /v1/tds/sections", user(s.handleTDSSections, auth.PermLedgerRead))
	mux.Handle("POST /v1/tds/compute", user(s.handleTDSCompute, auth.PermLedgerRead))
	mux.Handle("POST /v1/tds/deductions", user(s.handleTDSDeduct, auth.PermLedgerWrite))
	mux.Handle("GET /v1/tds/entries", user(s.handleListTDS, auth.PermLedgerRead))
	mux.Handle("GET /v1/tds/summary", user(s.handleTDSSummary, auth.PermReportRead))
	mux.Handle("POST /v1/tds/challans", user(s.handleCreateChallan, auth.PermLedgerWrite))
	mux.Handle("GET /v1/tds/challans", user(s.handleListChallans, auth.PermLedgerRead))
	mux.Handle("GET /v1/tds/26q", user(s.handle26Q, auth.PermReportRead))
	mux.Handle("GET /v1/tcs/206c1h", user(s.handleTCS1H, auth.PermReportRead))

	// GST set-off (Rule 88A)
	mux.Handle("POST /v1/gst/setoff", user(s.handleGSTSetOff, auth.PermLedgerWrite, auth.PermGSTFile))
	mux.Handle("GET /v1/gst/setoffs", user(s.handleListSetOffs, auth.PermLedgerRead))

	// Year-end close, Tally export, audit trail
	mux.Handle("POST /v1/year-end/close", user(s.handleYearEndClose, auth.PermSettingsManage))
	mux.Handle("GET /v1/year-end", user(s.handleListYearEnds, auth.PermLedgerRead))
	mux.Handle("POST /v1/year-end/{fy}/reopen", user(s.handleYearEndReopen, auth.PermSettingsManage))
	mux.Handle("GET /v1/export/tally", user(s.handleTallyExport, auth.PermReportRead))
	mux.Handle("GET /v1/audit", user(s.handleAudit, auth.PermLedgerRead))

	// Reports (all accept ?format=csv)
	mux.Handle("GET /v1/reports/trial-balance", user(s.handleTrialBalance, auth.PermReportRead))
	mux.Handle("GET /v1/reports/profit-loss", user(s.handleProfitLoss, auth.PermReportRead)) // ?store_id=
	mux.Handle("GET /v1/reports/profit-loss/stores", user(s.handleStorePL, auth.PermReportRead))
	mux.Handle("GET /v1/reports/brs", user(s.handleBRS, auth.PermReportRead))
	mux.Handle("GET /v1/reports/balance-sheet", user(s.handleBalanceSheet, auth.PermReportRead))
	mux.Handle("GET /v1/reports/aging", user(s.handleAging, auth.PermReportRead))
	mux.Handle("GET /v1/reports/cash-flow", user(s.handleCashFlow, auth.PermReportRead))
	mux.Handle("GET /v1/reports/daybook", user(s.handleDayBook, auth.PermReportRead))
	mux.Handle("GET /v1/reports/summary", user(s.handleSummary, auth.PermReportRead))

	// Operations: events bahi could not post, and the period lock
	mux.Handle("GET /v1/quarantine", user(s.handleListQuarantine, auth.PermLedgerRead))
	mux.Handle("POST /v1/quarantine/{event_id}/retry", user(s.handleRetryQuarantine, auth.PermLedgerWrite))
	mux.Handle("GET /v1/settings/period-lock", user(s.handleGetPeriodLock, auth.PermLedgerRead))
	mux.Handle("PUT /v1/settings/period-lock", user(s.handlePutPeriodLock, auth.PermSettingsManage))
	mux.Handle("GET /v1/settings/tds", user(s.handleGetTDSSettings, auth.PermLedgerRead))
	mux.Handle("PUT /v1/settings/tds", user(s.handlePutTDSSettings, auth.PermSettingsManage))

	// Internal (service secret)
	mux.Handle("GET /internal/v1/parties/{id}", internal(s.handleInternalParty))
	mux.Handle("GET /internal/v1/parties/{id}/credit", internal(s.handleInternalCredit))
	mux.Handle("GET /internal/v1/receivables/overdue", internal(s.handleOverdueReceivables))
	mux.Handle("POST /internal/v1/bulk/parties", internal(s.handleBulkParties))
	mux.Handle("POST /internal/v1/bulk/opening-balances", internal(s.handleBulkOpening))
	mux.Handle("POST /internal/v1/bulk/open-bills", internal(s.handleBulkOpenBills))
	mux.Handle("POST /internal/v1/bulk/invoice-history", internal(s.handleBulkInvoiceHistory))
	mux.Handle("POST /internal/v1/bulk/accounts", internal(s.handleBulkAccounts))
	mux.Handle("POST /internal/v1/bulk/rollback", internal(s.handleBulkRollback))
	mux.Handle("GET /internal/v1/reports/summary", internal(s.handleSummary))

	mux.Handle("POST /events", events.Handler(v, trustIAM, s.EventRoutes()))
}
