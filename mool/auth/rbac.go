package auth

// Role names. Stored on the user in pehchaan and carried in the JWT.
const (
	RoleSuperAdmin   = "super_admin"
	RoleStoreManager = "store_manager"
	RoleCashier      = "cashier"
	RoleAccountant   = "accountant"
)

// Permissions. See sanchalan/docs/ARCHITECTURE.md §3.
const (
	PermInvoiceCreate  = "invoice:create"
	PermInvoiceCancel  = "invoice:cancel"
	PermInvoiceRead    = "invoice:read"
	PermPurchaseManage = "purchase:manage"
	PermPaymentCollect = "payment:collect"
	PermLedgerRead     = "ledger:read"
	PermLedgerWrite    = "ledger:write"
	PermPartyManage    = "party:manage"
	PermItemManage     = "item:manage"
	PermStockManage    = "stock:manage"
	PermBOMManage      = "bom:manage"
	PermReportRead     = "report:read"
	PermGSTFile        = "gst:file"
	PermImportRun      = "import:run"
	PermUserManage     = "user:manage"
	PermSettingsManage = "settings:manage"
	PermAIUse          = "ai:use"
)

var rolePerms = map[string]map[string]bool{
	RoleSuperAdmin: set(
		PermInvoiceCreate, PermInvoiceCancel, PermInvoiceRead, PermPurchaseManage, PermPaymentCollect,
		PermLedgerRead, PermLedgerWrite, PermPartyManage, PermItemManage, PermStockManage, PermBOMManage,
		PermReportRead, PermGSTFile, PermImportRun, PermUserManage, PermSettingsManage, PermAIUse,
	),
	RoleStoreManager: set(
		PermInvoiceCreate, PermInvoiceCancel, PermInvoiceRead, PermPurchaseManage, PermPaymentCollect,
		PermLedgerRead, PermPartyManage, PermItemManage, PermStockManage, PermBOMManage, PermReportRead, PermAIUse,
	),
	RoleCashier: set(
		PermInvoiceCreate, PermInvoiceRead, PermPaymentCollect, PermPartyManage,
	),
	RoleAccountant: set(
		PermInvoiceRead, PermPurchaseManage, PermPaymentCollect, PermLedgerRead, PermLedgerWrite,
		PermPartyManage, PermReportRead, PermGSTFile, PermImportRun, PermAIUse,
	),
}

func set(ps ...string) map[string]bool {
	m := make(map[string]bool, len(ps))
	for _, p := range ps {
		m[p] = true
	}
	return m
}

// ValidRole reports whether r is a known role.
func ValidRole(r string) bool { _, ok := rolePerms[r]; return ok }

// Can reports whether role has perm.
func Can(role, perm string) bool { return rolePerms[role][perm] }

// Permissions lists the permissions of role (for the frontend to hide UI).
func Permissions(role string) []string {
	out := make([]string, 0, len(rolePerms[role]))
	for p := range rolePerms[role] {
		out = append(out, p)
	}
	return out
}

// StoreScoped reports whether the role is limited to the stores in its claim.
func StoreScoped(role string) bool { return role == RoleCashier || role == RoleStoreManager }
