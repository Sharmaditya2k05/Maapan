/** Labels, tones and source links for stock movements (bhandar model.go reasons/sources). Pure. */
import type { Movement, MovementReason, TransferStatus } from '../types.stock'

type Tone = 'green' | 'amber' | 'red' | 'blue' | 'gray' | 'brand'

export const REASON_LABEL: Record<MovementReason, string> = {
  sale: 'Sale', purchase: 'Purchase', sale_return: 'Sale return', purchase_return: 'Purchase return',
  transfer_in: 'Transfer in', transfer_out: 'Transfer out', adjustment: 'Adjustment', opening: 'Opening stock',
  production_consume: 'Used in production', production_output: 'Produced', expiry_writeoff: 'Expiry write-off',
}

export const reasonLabel = (r: string) => REASON_LABEL[r as MovementReason] ?? r

export function reasonTone(r: string): Tone {
  switch (r) {
    case 'purchase': case 'sale_return': case 'production_output': case 'opening': return 'green'
    case 'sale': case 'purchase_return': case 'production_consume': return 'blue'
    case 'transfer_in': case 'transfer_out': return 'brand'
    case 'expiry_writeoff': return 'red'
    case 'adjustment': return 'amber'
    default: return 'gray'
  }
}

/** Where the source document lives in the app, or null when it has no screen (adjustments, opening stock). */
export function sourceHref(m: Pick<Movement, 'source' | 'source_id' | 'reason'>): string | null {
  if (!m.source_id) return null
  switch (m.source) {
    case 'transfer': return `/app/stock/transfers/${m.source_id}`
    case 'aadesh.invoice': case 'aadesh.invoice.cancel': case 'aadesh.note':
      switch (m.reason) {
        case 'sale': return `/app/sales/${m.source_id}`
        case 'purchase': return `/app/purchases/${m.source_id}`
        case 'sale_return': case 'purchase_return': return `/app/returns/${m.source_id}`
        default: return null
      }
    default: return null
  }
}

export const SOURCE_LABEL: Record<string, string> = {
  'aadesh.invoice': 'Invoice', 'aadesh.invoice.cancel': 'Cancellation', 'aadesh.note': 'Note',
  'nirmaan.production': 'Production', adjustment: 'Adjustment', transfer: 'Transfer', opening: 'Opening',
}
export const sourceLabel = (s: string) => SOURCE_LABEL[s] ?? s

export const TRANSFER_STATUS_LABEL: Record<TransferStatus, string> = { draft: 'Draft', dispatched: 'Dispatched', received: 'Received', cancelled: 'Cancelled' }

/** Same table as bhandar NextTransferStatus: which actions a status allows. */
export function transferActions(status: TransferStatus): { dispatch: boolean; receive: boolean; cancel: boolean } {
  return { dispatch: status === 'draft', receive: status === 'dispatched', cancel: status === 'draft' || status === 'dispatched' }
}

/** Expiry colouring: expired, ≤ 7 days, ≤ 30 days, else fine. */
export function expiryTone(daysLeft: number): Tone {
  if (daysLeft < 0) return 'red'
  if (daysLeft <= 7) return 'red'
  if (daysLeft <= 30) return 'amber'
  return 'green'
}
