'use client'

/** Batch-tracked items ask for a batch: pick one in stock (GET /bhandar/v1/items/{id}/batches) or type it. */
import { useEffect, useState } from 'react'
import { Button, Field, Input, Modal, Money, Spinner } from '@/components/ui'
import { fmtDate, fmtQty, useGet, useStore } from '@/lib/hooks'
import type { Batch } from '@/lib/types.pos'
import type { ItemLike } from '@/lib/pos/cart'

export function BatchPicker({ item, open, onClose, onPick }: { item: ItemLike | null; open: boolean; onClose: () => void; onPick: (batchNo: string) => void }) {
  const { storeId } = useStore()
  const [typed, setTyped] = useState('')
  useEffect(() => { if (open) setTyped('') }, [open])
  const q = useGet<{ batches: Batch[] }>(open && item ? `/bhandar/v1/items/${item.id}/batches` : null, { store_id: storeId })
  const batches = (q.data?.batches ?? []).filter((b) => b.qty > 0)
  const today = new Date().toISOString().slice(0, 10)

  return (
    <Modal open={open} onClose={onClose} title={`Batch for ${item?.name ?? ''}`}
      footer={<><Button variant="secondary" onClick={onClose}>Cancel</Button><Button onClick={() => typed.trim() && onPick(typed.trim())} disabled={!typed.trim()}>Use typed batch</Button></>}>
      {q.isLoading && <div className="flex items-center gap-2 text-sm text-slate-500"><Spinner className="h-4 w-4" /> Loading batches…</div>}
      {q.error && <p className="text-sm text-slate-500">Batches could not be loaded (offline?). Type the batch number below.</p>}
      {batches.length > 0 && (
        <ul className="max-h-64 divide-y divide-slate-100 overflow-y-auto rounded-xl border border-slate-200">
          {batches.map((b) => {
            const expired = !!b.expiry_date && b.expiry_date < today
            return (
              <li key={b.id}>
                <button type="button" onClick={() => onPick(b.batch_no)} className="flex w-full items-center justify-between gap-3 px-3 py-2.5 text-left hover:bg-brand-50 cursor-pointer" disabled={expired}>
                  <div>
                    <div className="font-mono text-sm font-semibold">{b.batch_no}</div>
                    <div className={`text-xs ${expired ? 'text-rose-600' : 'text-slate-500'}`}>{b.expiry_date ? `${expired ? 'Expired' : 'Expires'} ${fmtDate(b.expiry_date)}` : 'No expiry'}{b.mrp_paise ? <> · MRP <Money paise={b.mrp_paise} /></> : null}</div>
                  </div>
                  <span className="num text-sm">{fmtQty(b.qty)} {item?.unit}</span>
                </button>
              </li>
            )
          })}
        </ul>
      )}
      {!q.isLoading && !q.error && batches.length === 0 && <p className="text-sm text-slate-500">No stock in any batch at this store. Type the batch number to bill anyway.</p>}
      <Field label="Or type a batch number" className="mt-4">
        <Input autoFocus className="font-mono" value={typed} onChange={(e) => setTyped(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter' && typed.trim()) onPick(typed.trim()) }} />
      </Field>
    </Modal>
  )
}
