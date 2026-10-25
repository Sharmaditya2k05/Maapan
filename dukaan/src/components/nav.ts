import {
  BarChart3, Boxes, FileText, Landmark, LayoutDashboard, Package, Receipt, ScanLine, Settings, ShoppingCart, Sparkles, Upload, Users, Wallet, Factory,
  type LucideIcon,
} from 'lucide-react'
import { PERMS, type Perm } from '@/lib/rbac'

export interface NavItem {
  href: string
  label: string
  /** The Maapan model behind this screen, shown in brackets. */
  model: string
  icon: LucideIcon
  group: 'Counter' | 'Billing' | 'Money' | 'Stock' | 'More'
  /** Shown when the role holds ANY of these; empty = everyone. */
  perms: Perm[]
  exact?: boolean
}

/** Every screen of the app. Pages live under src/app/app/<path>. */
export const NAV: NavItem[] = [
  { href: '/app', label: 'Dashboard', model: 'dukaan', icon: LayoutDashboard, group: 'Counter', perms: [], exact: true },
  { href: '/app/pos', label: 'POS Counter', model: 'aadesh', icon: ScanLine, group: 'Counter', perms: [PERMS.invoiceCreate] },

  { href: '/app/sales', label: 'Sales Invoices', model: 'aadesh', icon: FileText, group: 'Billing', perms: [PERMS.invoiceRead] },
  { href: '/app/purchases', label: 'Purchases', model: 'aadesh', icon: ShoppingCart, group: 'Billing', perms: [PERMS.purchaseManage] },
  { href: '/app/returns', label: 'Returns & Notes', model: 'aadesh', icon: Receipt, group: 'Billing', perms: [PERMS.invoiceRead] },

  { href: '/app/parties', label: 'Parties & Ledgers', model: 'bahi', icon: Users, group: 'Money', perms: [PERMS.partyManage, PERMS.ledgerRead] },
  { href: '/app/payments', label: 'Payments', model: 'lenden', icon: Wallet, group: 'Money', perms: [PERMS.paymentCollect] },
  { href: '/app/bank', label: 'Cash & Bank', model: 'bahi', icon: Landmark, group: 'Money', perms: [PERMS.ledgerRead] },
  { href: '/app/reports', label: 'Reports', model: 'bahi', icon: BarChart3, group: 'Money', perms: [PERMS.reportRead] },
  { href: '/app/gst', label: 'GST', model: 'kar', icon: FileText, group: 'Money', perms: [PERMS.gstFile, PERMS.reportRead] },

  { href: '/app/items', label: 'Items', model: 'bhandar', icon: Package, group: 'Stock', perms: [PERMS.itemManage, PERMS.invoiceCreate] },
  { href: '/app/stock', label: 'Stock & Transfers', model: 'bhandar', icon: Boxes, group: 'Stock', perms: [PERMS.stockManage, PERMS.reportRead] },
  { href: '/app/production', label: 'Production', model: 'nirmaan', icon: Factory, group: 'Stock', perms: [PERMS.bomManage] },

  { href: '/app/import', label: 'Import Data', model: 'aayaat', icon: Upload, group: 'More', perms: [PERMS.importRun] },
  { href: '/app/ai', label: 'Ask Maapan', model: 'buddhi', icon: Sparkles, group: 'More', perms: [PERMS.aiUse] },
  { href: '/app/settings', label: 'Settings', model: 'pehchaan', icon: Settings, group: 'More', perms: [PERMS.settingsManage, PERMS.userManage] },
]

export const NAV_GROUPS: NavItem['group'][] = ['Counter', 'Billing', 'Money', 'Stock', 'More']
