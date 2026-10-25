import { Bot, Building2, CreditCard, FileText, Hash, Info, Lock, Printer, Store, Users, type LucideIcon } from 'lucide-react'
import { PERMS, type Perm } from '@/lib/rbac'

export type SectionId = 'business' | 'stores' | 'users' | 'numbering' | 'period-lock' | 'gateways' | 'gst' | 'ai' | 'device' | 'about'

export interface Section { id: SectionId; label: string; hint: string; icon: LucideIcon; perms: Perm[] }

/** Settings sections (/app/settings?tab=<id>). Shown when the role holds ANY of perms; empty = everyone. */
export const SECTIONS: Section[] = [
  { id: 'business', label: 'Business', hint: 'Name, GSTIN, address', icon: Building2, perms: [PERMS.settingsManage] },
  { id: 'stores', label: 'Stores', hint: 'Locations and their GSTINs', icon: Store, perms: [PERMS.settingsManage] },
  { id: 'users', label: 'Users', hint: 'Who can do what', icon: Users, perms: [PERMS.userManage] },
  { id: 'numbering', label: 'Numbering', hint: 'Invoice prefixes per store', icon: Hash, perms: [PERMS.settingsManage] },
  { id: 'period-lock', label: 'Period lock', hint: 'Close the books up to a date', icon: Lock, perms: [PERMS.settingsManage] },
  { id: 'gateways', label: 'Payment gateways', hint: 'Razorpay / Cashfree keys', icon: CreditCard, perms: [PERMS.settingsManage] },
  { id: 'gst', label: 'GST', hint: 'Scheme, e-invoice, thresholds', icon: FileText, perms: [PERMS.gstFile] },
  { id: 'ai', label: 'AI', hint: 'Ask Maapan, budget, usage', icon: Bot, perms: [PERMS.settingsManage, PERMS.aiUse] },
  { id: 'device', label: 'Device & printer', hint: 'This counter only', icon: Printer, perms: [] },
  { id: 'about', label: 'About', hint: 'Version and connection', icon: Info, perms: [] },
]
