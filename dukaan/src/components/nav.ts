import {
  BarChart3, Boxes, FileText, Landmark, LayoutDashboard, Package, Receipt, ScanLine, Settings, ShoppingCart, Sparkles, Upload, Users, Wallet, Factory,
  type LucideIcon,
} from 'lucide-react'
import { PERMS, type Perm } from '@/lib/rbac'

export interface NavItem {
  href: string
  label: string
  icon: LucideIcon
  group: 'Counter' | 'Billing' | 'Money' | 'Stock' | 'More'
  /** Shown when the role holds ANY of these; empty = everyone. */
  perms: Perm[]
  exact?: boolean
}

/** Every screen of the app. Pages live under src/app/app/<path>. */
export const NAV: NavItem[] = [
  { href: '/app', label: 'Dashboard', icon: LayoutDashboard, group: 'Counter', perms: [], exact: true },
  { href: '/app/pos', label: 'POS Counter', icon: ScanLine, group: 'Counter', perms: [PERMS.invoiceCreate] },

  { href: '/app/sales', label: 'Sales Invoices', icon: FileText, group: 'Billing', perms: [PERMS.invoiceRead] },
  { href: '/app/purchases', label: 'Purchases', icon: ShoppingCart, group: 'Billing', perms: [PERMS.purchaseManage] },
  { href: '/app/returns', label: 'Returns & Notes', icon: Receipt, group: 'Billing', perms: [PERMS.invoiceRead] },

  { href: '/app/parties', label: 'Parties & Ledgers', icon: Users, group: 'Money', perms: [PERMS.partyManage, PERMS.ledgerRead] },
  { href: '/app/payments', label: 'Payments', icon: Wallet, group: 'Money', perms: [PERMS.paymentCollect] },
  { href: '/app/bank', label: 'Cash & Bank', icon: Landmark, group: 'Money', perms: [PERMS.ledgerRead] },
  { href: '/app/reports', label: 'Reports', icon: BarChart3, group: 'Money', perms: [PERMS.reportRead] },
  { href: '/app/gst', label: 'GST', icon: FileText, group: 'Money', perms: [PERMS.gstFile, PERMS.reportRead] },

  { href: '/app/items', label: 'Items', icon: Package, group: 'Stock', perms: [PERMS.itemManage, PERMS.invoiceCreate] },
  { href: '/app/stock', label: 'Stock & Transfers', icon: Boxes, group: 'Stock', perms: [PERMS.stockManage, PERMS.reportRead] },
  { href: '/app/production', label: 'Production', icon: Factory, group: 'Stock', perms: [PERMS.bomManage] },

  { href: '/app/import', label: 'Import Data', icon: Upload, group: 'More', perms: [PERMS.importRun] },
  { href: '/app/ai', label: 'Ask Maapan', icon: Sparkles, group: 'More', perms: [PERMS.aiUse] },
  { href: '/app/settings', label: 'Settings', icon: Settings, group: 'More', perms: [PERMS.settingsManage, PERMS.userManage] },
]

export const NAV_GROUPS: NavItem['group'][] = ['Counter', 'Billing', 'Money', 'Stock', 'More']
