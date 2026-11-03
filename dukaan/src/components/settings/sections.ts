import { BellOff, Bot, Building2, CalendarCheck, CreditCard, FileBadge, FileDown, FileText, Hash, History, Info, Lock, MessageSquare, MonitorSmartphone, Printer, Store, Users, type LucideIcon } from 'lucide-react'
import { PERMS, type Perm } from '@/lib/rbac'

export type SectionId = 'business' | 'registrations' | 'stores' | 'users' | 'pos-devices' | 'audit' | 'numbering' | 'period-lock' | 'year-end' | 'tally' | 'gateways' | 'gst' | 'messaging' | 'consents' | 'ai' | 'device' | 'about'

export interface Section { id: SectionId; label: string; model: string; hint: string; icon: LucideIcon; perms: Perm[] }

/** Settings sections (/app/settings?tab=<id>). Shown when the role holds ANY of perms; empty = everyone. */
export const SECTIONS: Section[] = [
  { id: 'business', label: 'Business', model: 'pehchaan', hint: 'Name, GSTIN, address', icon: Building2, perms: [PERMS.settingsManage] },
  { id: 'registrations', label: 'GST registrations', model: 'pehchaan', hint: 'GSTINs, PIN, e-invoice', icon: FileBadge, perms: [PERMS.settingsManage] },
  { id: 'stores', label: 'Stores', model: 'pehchaan', hint: 'Locations and their registration', icon: Store, perms: [PERMS.settingsManage] },
  { id: 'users', label: 'Users', model: 'pehchaan', hint: 'Who can do what', icon: Users, perms: [PERMS.userManage] },
  { id: 'pos-devices', label: 'POS devices', model: 'pehchaan', hint: 'Counters registered to stores', icon: MonitorSmartphone, perms: [PERMS.settingsManage] },
  { id: 'audit', label: 'Audit log', model: 'pehchaan', hint: 'Users, devices and edits to the books', icon: History, perms: [PERMS.userManage, PERMS.ledgerRead] },
  { id: 'numbering', label: 'Numbering', model: 'aadesh', hint: 'Invoice prefixes per store', icon: Hash, perms: [PERMS.settingsManage] },
  { id: 'period-lock', label: 'Period lock', model: 'bahi', hint: 'Close the books up to a date', icon: Lock, perms: [PERMS.settingsManage] },
  { id: 'year-end', label: 'Year-end close', model: 'bahi', hint: 'Carry the year’s profit to capital', icon: CalendarCheck, perms: [PERMS.settingsManage] },
  { id: 'tally', label: 'Tally export', model: 'bahi', hint: 'Tally Prime XML of the books', icon: FileDown, perms: [PERMS.reportRead] },
  { id: 'gateways', label: 'Payment gateways', model: 'lenden', hint: 'Razorpay / Cashfree keys', icon: CreditCard, perms: [PERMS.settingsManage] },
  { id: 'gst', label: 'GST', model: 'kar', hint: 'Thresholds, portal users, HSN', icon: FileText, perms: [PERMS.gstFile, PERMS.settingsManage] },
  { id: 'messaging', label: 'Messaging', model: 'soochna', hint: 'Senders, templates, reminders, failures', icon: MessageSquare, perms: [PERMS.settingsManage] },
  { id: 'consents', label: 'Message consent', model: 'soochna', hint: 'Opt-outs and do-not-message', icon: BellOff, perms: [PERMS.partyManage] },
  { id: 'ai', label: 'AI', model: 'buddhi', hint: 'Ask Maapan, budget, usage', icon: Bot, perms: [PERMS.settingsManage, PERMS.aiUse] },
  { id: 'device', label: 'Device & printer', model: 'dukaan', hint: 'This counter only', icon: Printer, perms: [] },
  { id: 'about', label: 'About', model: 'sanchalan', hint: 'Version and connection', icon: Info, perms: [] },
]
