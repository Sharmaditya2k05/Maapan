/** Import kinds as aayaat defines them (internal/mapping) with UI copy. */
import { Boxes, FileClock, Package, ReceiptText, Scale, Users, type LucideIcon } from 'lucide-react'
import type { Tone } from '@/components/ui'
import type { ImportKind, ImportStatus } from '@/lib/types.more'

export interface KindMeta {
  kind: ImportKind
  label: string
  icon: LucideIcon
  description: string
  goesTo: string
  /** Opening date applies (as_of). */
  asOf: boolean
  /** A default store applies (and the Store / Godown column can pick one per row via store_map). */
  store: boolean
  /** The store is required when no Store column is mapped (opening_stock). */
  storeRequired?: boolean
  /** Dr/Cr reconciliation is reported after validation. */
  recon: boolean
  /** Ledger / group name → account code map (opening_balances). */
  accountMap?: boolean
  /** Document type when the file has no type column (invoices). */
  docType?: boolean
  /** Side + bill-wise / party-totals mode (open_bills). */
  bills?: boolean
}

export const IMPORT_KINDS: KindMeta[] = [
  { kind: 'parties', label: 'Parties', icon: Users, asOf: true, store: false, recon: true, goesTo: 'Parties & Ledgers',
    description: 'Customers and suppliers with phone, GSTIN, state, address and opening balance (Dr/Cr). MargBooks: Party / Ledger master.' },
  { kind: 'items', label: 'Items', icon: Package, asOf: true, store: true, recon: false, goesTo: 'Items (+ opening stock)',
    description: 'Item master with code, barcode, HSN, unit / alt unit, MRP, rates and GST %. An opening qty column also brings the opening stock in (needs a store). Tally: Stock Items.' },
  { kind: 'opening_balances', label: 'Opening balances', icon: Scale, asOf: true, store: false, recon: true, accountMap: true, goesTo: 'Cash & Bank, ledgers',
    description: 'Trial balance / ledger balances by account code, ledger name or party, as at the opening date. Dr and Cr must tally. Tally: Other Ledgers.' },
  { kind: 'opening_stock', label: 'Opening stock', icon: Boxes, asOf: true, store: true, storeRequired: true, recon: false, goesTo: 'Stock',
    description: 'Quantity and cost per item, optionally batch-wise with expiry, per store / godown on the opening date. Marg batch stock; Tally: Stock Batches.' },
  { kind: 'open_bills', label: 'Open bills', icon: ReceiptText, asOf: true, store: false, recon: true, bills: true, goesTo: 'Parties (bill-wise outstanding)',
    description: 'Bill-by-bill outstanding per party with bill date and due date, so aging by due date works from day one. Marg Bill-wise Outstanding; Tally: Bills.' },
  { kind: 'invoices', label: 'Invoice history', icon: FileClock, asOf: true, store: true, recon: false, docType: true, goesTo: 'Ledger (history / vouchers)',
    description: 'Past sales, purchases and notes at invoice level. Before the opening date kept as history; on or after it posted as vouchers. Marg Sale / Purchase Register; Tally: Vouchers.' },
]

export const kindMeta = (k: string) => IMPORT_KINDS.find((m) => m.kind === k)
export const kindLabel = (k: string) => kindMeta(k)?.label ?? k

export const JOB_LABEL: Record<ImportStatus, string> = {
  uploaded: 'Uploaded', mapped: 'Mapped', validated: 'Validated', queued: 'Queued', running: 'Importing', completed: 'Completed', failed: 'Failed', rolled_back: 'Rolled back',
}
export const jobTone = (s: ImportStatus): Tone =>
  s === 'completed' ? 'green' : s === 'failed' ? 'red' : s === 'rolled_back' ? 'gray' : s === 'running' || s === 'queued' ? 'amber' : s === 'validated' ? 'brand' : 'blue'

export const isJobBusy = (s: ImportStatus | undefined) => s === 'queued' || s === 'running'

/** Wizard step a job resumes at. */
export function stepForStatus(s: ImportStatus): WizardStep {
  switch (s) {
    case 'uploaded': case 'mapped': return 'mapping'
    case 'validated': return 'validate'
    default: return 'commit'
  }
}

export type WizardStep = 'kind' | 'upload' | 'mapping' | 'validate' | 'commit'
export const STEPS: { id: WizardStep; label: string }[] = [
  { id: 'kind', label: 'What to import' }, { id: 'upload', label: 'Upload file' }, { id: 'mapping', label: 'Map columns' }, { id: 'validate', label: 'Check rows' }, { id: 'commit', label: 'Import' },
]

export function fmtBytes(n: number) {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}
