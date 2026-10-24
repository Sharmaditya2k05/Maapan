/** Import kinds as aayaat defines them (internal/mapping) with UI copy. */
import { Boxes, Package, Scale, Users, type LucideIcon } from 'lucide-react'
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
  /** A target store is required (opening_stock). */
  store: boolean
  /** Dr/Cr reconciliation is reported after validation. */
  recon: boolean
}

export const IMPORT_KINDS: KindMeta[] = [
  { kind: 'parties', label: 'Parties', icon: Users, asOf: true, store: false, recon: true, goesTo: 'Parties & Ledgers',
    description: 'Customers and suppliers with phone, GSTIN, state, address and opening balance (Dr/Cr). MargBooks: Party / Ledger master.' },
  { kind: 'items', label: 'Items', icon: Package, asOf: false, store: false, recon: false, goesTo: 'Items',
    description: 'Item master with code, barcode, HSN, unit, MRP, sale and purchase rate, GST % and re-order level.' },
  { kind: 'opening_balances', label: 'Opening balances', icon: Scale, asOf: true, store: false, recon: true, goesTo: 'Cash & Bank, ledgers',
    description: 'Trial balance / ledger balances by account code or party code, as at the opening date. Dr and Cr must tally.' },
  { kind: 'opening_stock', label: 'Opening stock', icon: Boxes, asOf: true, store: true, recon: false, goesTo: 'Stock (one store)',
    description: 'Quantity and cost per item (optionally batch-wise with expiry) received into one store on the opening date.' },
]

export const kindMeta = (k: string) => IMPORT_KINDS.find((m) => m.kind === k)
export const kindLabel = (k: string) => kindMeta(k)?.label ?? k

export const JOB_LABEL: Record<ImportStatus, string> = {
  uploaded: 'Uploaded', mapped: 'Mapped', validated: 'Validated', queued: 'Queued', running: 'Importing', completed: 'Completed', failed: 'Failed',
}
export const jobTone = (s: ImportStatus): Tone =>
  s === 'completed' ? 'green' : s === 'failed' ? 'red' : s === 'running' || s === 'queued' ? 'amber' : s === 'validated' ? 'brand' : 'blue'

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
