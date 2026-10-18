package ledger

import (
	"net/http"

	"github.com/taraju/mool/auth"
	"github.com/taraju/mool/events"
	"github.com/taraju/mool/httpx"
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
	mux.Handle("GET /v1/parties/{id}/statement", user(s.handlePartyStatement, auth.PermLedgerRead))

	// Vouchers
	mux.Handle("GET /v1/vouchers", user(s.handleListVouchers, auth.PermLedgerRead))
	mux.Handle("POST /v1/vouchers", user(s.handleCreateVoucher, auth.PermLedgerWrite))
	mux.Handle("GET /v1/vouchers/{id}", user(s.handleGetVoucher, auth.PermLedgerRead))
	mux.Handle("POST /v1/vouchers/{id}/reverse", user(s.handleReverseVoucher, auth.PermLedgerWrite))

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

	// Reports (all accept ?format=csv)
	mux.Handle("GET /v1/reports/trial-balance", user(s.handleTrialBalance, auth.PermReportRead))
	mux.Handle("GET /v1/reports/profit-loss", user(s.handleProfitLoss, auth.PermReportRead))
	mux.Handle("GET /v1/reports/balance-sheet", user(s.handleBalanceSheet, auth.PermReportRead))
	mux.Handle("GET /v1/reports/aging", user(s.handleAging, auth.PermReportRead))
	mux.Handle("GET /v1/reports/cash-flow", user(s.handleCashFlow, auth.PermReportRead))
	mux.Handle("GET /v1/reports/daybook", user(s.handleDayBook, auth.PermReportRead))
	mux.Handle("GET /v1/reports/summary", user(s.handleSummary, auth.PermReportRead))

	// Internal (service secret)
	mux.Handle("GET /internal/v1/parties/{id}", internal(s.handleInternalParty))
	mux.Handle("POST /internal/v1/bulk/parties", internal(s.handleBulkParties))
	mux.Handle("POST /internal/v1/bulk/opening-balances", internal(s.handleBulkOpening))
	mux.Handle("GET /internal/v1/reports/summary", internal(s.handleSummary))

	mux.Handle("POST /events", events.Handler(v, trustIAM, s.EventRoutes()))
}
