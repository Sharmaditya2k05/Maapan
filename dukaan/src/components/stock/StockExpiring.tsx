'use client'

import { useState } from 'react'
import Link from 'next/link'
import { useRouter } from 'next/navigation'
import { CalendarClock, Undo2 } from 'lucide-react'
import { Badge, Button, Empty, ErrorBox, Field, Input, Modal, Money, QueryState, Select, Table } from '@/components/ui'
import { fmtDate, fmtQty, usePerm, useGet, useStore } from '@/lib/hooks'
import { errorMessage, post } from '@/lib/api'
import { PERMS } from '@/lib/rbac'
import { openPurchaseDraft } from '@/lib/stock/handoff'
import { expiryTone } from '@/lib/stock/reasons'
import { storeName } from '@/lib/stock/scope'
import type { ExpiringBatch, PurchaseDraftBody, ReturnToVendorInput } from '@/lib/types.stock'
import { StoreSelect } from './StoreSelect'
import { SupplierPicker } from './SupplierPicker'
import { useAction } from './hooks'

/** Expired and expiring batch stock with days-left colouring; return-to-vendor drafts a debit note. */
export function StockExpiring() {
  const router = useRouter()
  const { storeId, stores } = useStore()
  const canDo = usePerm()
  const [store, setStore] = useState(storeId ?? '')
  const [days, setDays] = useState(30)
  const [rtv, setRtv] = useState<ExpiringBatch | null>(null)
  const [qty, setQty] = useState('')
  const [supplier, setSupplier] = useState('')
  const [err, setErr] = useState('')
  const list = useGet<{ days: number; items: ExpiringBatch[] }>('/bhandar/v1/batches/expiring', { days, store_id: store })
  const ret = useAction<{ draft: PurchaseDraftBody }, { batchId: string; body: ReturnToVendorInput }>(
    ({ batchId, body }) => post(`/bhandar/v1/batches/${batchId}/return-to-vendor`, body), { silent: true })

  const startRtv = (b: ExpiringBatch) => { setRtv(b); setQty(String(b.qty)); setSupplier(''); setErr('') }
  const submitRtv = async () => {
    if (!rtv) return
    const q = Number(qty)
    if (!Number.isFinite(q) || q <= 0 || q > rtv.qty) return setErr(`Quantity must be between 0 and ${fmtQty(rtv.qty)} ${rtv.unit}.`)
    try {
      const r = await ret.mutateAsync({ batchId: rtv.batch_id, body: { store_id: rtv.store_id, qty: q, supplier_id: supplier || undefined } })
      if (!r.draft.party_id && !supplier) return setErr('This item has no preferred supplier: pick the supplier to return it to.')
      openPurchaseDraft({ ...r.draft, party_id: r.draft.party_id || supplier }, router)
    } catch (e) { setErr(errorMessage(e)) }
  }

  const items = list.data?.items ?? []
  const expired = items.filter((i) => i.days_left < 0)
  const value = items.reduce((a, b) => a + b.value_paise, 0)

  return (
    <>
      <div className="flex flex-col gap-3 border-b border-slate-100 p-3 sm:p-4 md:flex-row md:items-center">
        <StoreSelect value={store} onChange={setStore} allowAll />
        <Select aria-label="Horizon" value={days} onChange={(e) => setDays(Number(e.target.value))} className="w-auto">
          {[7, 15, 30, 60, 90, 180].map((d) => <option key={d} value={d}>Within {d} days</option>)}
        </Select>
        {list.data && <div className="text-sm text-slate-600 md:ml-auto">{items.length} batch{items.length === 1 ? '' : 'es'} · {expired.length} expired · <Money paise={value} className="font-semibold" /> at cost</div>}
      </div>
      <QueryState isLoading={list.isLoading} error={list.error} data={list.data?.items} retry={() => list.refetch()}
        empty={<Empty icon={CalendarClock} title="Nothing expiring" text={`No batch stock expires within ${days} days. Expiry dates come from purchase lines of batch-tracked items.`} />}>
        {(rows) => (
          <Table>
            <thead className="bg-slate-50/60"><tr><th className="th">Item</th>{!store && <th className="th">Store</th>}<th className="th">Batch</th><th className="th">Expiry</th><th className="th text-right">Qty</th><th className="th text-right">Avg cost</th><th className="th text-right">Value</th><th className="th" /></tr></thead>
            <tbody className="divide-y divide-slate-100">
              {rows.map((b) => (
                <tr key={`${b.batch_id}-${b.store_id}`} className={`hover:bg-slate-50/60 ${b.days_left < 0 ? 'bg-rose-50/40' : ''}`}>
                  <td className="td"><Link href={`/app/items/${b.item_id}`} className="font-semibold hover:underline">{b.item_name}</Link></td>
                  {!store && <td className="td text-slate-600">{storeName(stores, b.store_id)}</td>}
                  <td className="td font-mono text-xs">{b.batch_no}</td>
                  <td className="td"><span className="inline-flex items-center gap-2 text-sm">{fmtDate(b.expiry_date)}<Badge tone={expiryTone(b.days_left)}>{b.days_left < 0 ? `expired ${-b.days_left}d ago` : b.days_left === 0 ? 'expires today' : `${b.days_left}d left`}</Badge></span></td>
                  <td className="td num text-right font-semibold">{fmtQty(b.qty)} <span className="text-xs font-normal text-slate-400">{b.unit}</span></td>
                  <td className="td text-right"><Money paise={b.avg_cost_paise} className="text-slate-600" /></td>
                  <td className="td text-right"><Money paise={b.value_paise} /></td>
                  <td className="td text-right">{canDo(PERMS.stockManage) && <Button size="sm" variant="secondary" icon={Undo2} onClick={() => startRtv(b)}>Return to vendor</Button>}</td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
      </QueryState>

      <Modal open={!!rtv} onClose={() => setRtv(null)} title={`Return to vendor · ${rtv?.item_name ?? ''}`}
        footer={<><Button variant="secondary" onClick={() => setRtv(null)}>Cancel</Button><Button onClick={submitRtv} loading={ret.isPending}>Draft debit note</Button></>}>
        {rtv && (
          <div className="space-y-4">
            <p className="text-sm text-slate-600">Batch <span className="font-mono">{rtv.batch_no}</span> expiring {fmtDate(rtv.expiry_date)} at {storeName(stores, rtv.store_id)}. A purchase return (debit note) is drafted against the original purchase bill when known; stock moves out when you post it.</p>
            <Field label={`Quantity to return (${rtv.unit})`} hint={`${fmtQty(rtv.qty)} held`}><Input value={qty} onChange={(e) => setQty(e.target.value.replace(/[^0-9.]/g, ''))} inputMode="decimal" className="num" /></Field>
            <Field label="Supplier" hint="Leave empty to use the item's preferred supplier"><SupplierPicker value={supplier} onChange={setSupplier} /></Field>
            {err && <ErrorBox message={err} />}
          </div>
        )}
      </Modal>
    </>
  )
}
