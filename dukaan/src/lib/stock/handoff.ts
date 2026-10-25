/**
 * Hand-off to the purchases screen: a pre-filled purchase bill or purchase return
 * (aadesh CreateInput shape) travels through sessionStorage and /app/purchases/new
 * picks it up. bhandar's purchase-draft and return-to-vendor endpoints already
 * return bodies in this shape.
 */
import type { PurchaseDraftBody } from '../types.stock'
import { PURCHASE_DRAFT_KEY } from '../types.more'

export { PURCHASE_DRAFT_KEY }

/** Label-sheet job for /app/items/labels (items + copies, layout). */
export const LABEL_JOB_KEY = 'maapan.labelJob'
export interface LabelJob { items: { item_id: string; copies: number }[]; show_price: boolean; columns: number }

export function storePurchaseDraft(body: PurchaseDraftBody) {
  sessionStorage.setItem(PURCHASE_DRAFT_KEY, JSON.stringify(body))
}

/** Store the draft and open the purchases screen. */
export function openPurchaseDraft(body: PurchaseDraftBody, router: { push: (href: string) => void }) {
  storePurchaseDraft(body)
  router.push('/app/purchases/new')
}

export function storeLabelJob(job: LabelJob) {
  sessionStorage.setItem(LABEL_JOB_KEY, JSON.stringify(job))
}

export function readLabelJob(): LabelJob | null {
  try {
    const raw = sessionStorage.getItem(LABEL_JOB_KEY)
    if (!raw) return null
    const j = JSON.parse(raw) as Partial<LabelJob>
    if (!Array.isArray(j.items) || j.items.length === 0) return null
    return { items: j.items, show_price: j.show_price ?? true, columns: j.columns ?? 4 }
  } catch {
    return null
  }
}
