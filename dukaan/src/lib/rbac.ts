/** Copy of mool/auth/rbac.go — used only to hide UI. The server enforces the real rules. */
export type Role = 'super_admin' | 'store_manager' | 'cashier' | 'accountant'

export const PERMS = {
  invoiceCreate: 'invoice:create', invoiceCancel: 'invoice:cancel', invoiceRead: 'invoice:read',
  purchaseManage: 'purchase:manage', paymentCollect: 'payment:collect', ledgerRead: 'ledger:read',
  ledgerWrite: 'ledger:write', partyManage: 'party:manage', itemManage: 'item:manage', stockManage: 'stock:manage',
  bomManage: 'bom:manage', reportRead: 'report:read', gstFile: 'gst:file', importRun: 'import:run',
  userManage: 'user:manage', settingsManage: 'settings:manage', aiUse: 'ai:use',
} as const
export type Perm = (typeof PERMS)[keyof typeof PERMS]

const P = PERMS
const MATRIX: Record<Role, Perm[]> = {
  super_admin: Object.values(P),
  store_manager: [P.invoiceCreate, P.invoiceCancel, P.invoiceRead, P.purchaseManage, P.paymentCollect, P.ledgerRead,
    P.partyManage, P.itemManage, P.stockManage, P.bomManage, P.reportRead, P.aiUse],
  cashier: [P.invoiceCreate, P.invoiceRead, P.paymentCollect, P.partyManage],
  accountant: [P.invoiceRead, P.purchaseManage, P.paymentCollect, P.ledgerRead, P.ledgerWrite, P.partyManage,
    P.reportRead, P.gstFile, P.importRun, P.aiUse],
}

export const can = (role: Role | undefined, perm: Perm) => !!role && MATRIX[role]?.includes(perm)

export const ROLE_LABEL: Record<Role, string> = {
  super_admin: 'Owner / Admin', store_manager: 'Store Manager', cashier: 'Cashier', accountant: 'Accountant',
}
