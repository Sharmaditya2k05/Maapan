'use client'

import { useMemo, useState } from 'react'
import Link from 'next/link'
import { useRouter } from 'next/navigation'
import { ArrowRight, Lock, Package, ShoppingCart } from 'lucide-react'
import { Badge, Button, Card, Empty, ErrorBox, Money, QueryState, Table, useToast } from '@/components/ui'
import { fmtQty, useGet, useList, usePerm, useSave, useStore } from '@/lib/hooks'
import { errorMessage } from '@/lib/api'
import { PERMS } from '@/lib/rbac'
import { openPurchaseDraft } from '@/lib/stock/handoff'
import { storeName } from '@/lib/stock/scope'
import type { PurchaseDraft, PurchaseDraftInput, Suggestion } from '@/lib/types.stock'
import type { Party } from '@/lib/types'
import { StoreSelect } from './StoreSelect'
import { SupplierPicker } from './SupplierPicker'

/** Items at/below their re-order level → purchase drafts per supplier → hand-off to the purchase bill screen. */
export function StockReorder() {
  const router = useRouter()
  const { storeId, stores } = useStore()
  const canDo = usePerm()
  const toast = useToast()
  const [store, setStore] = useState(storeId ?? '')
  const [selected, setSelected] = useState<Record<string, true>>({})
  const [drafts, setDrafts] = useState<PurchaseDraft[] | null>(null)
  const [pickedSupplier, setPickedSupplier] = useState<Record<number, string>>({})
  const [err, setErr] = useState('')
  const allowed = canDo(PERMS.stockManage) || canDo(PERMS.purchaseManage)
  const sugg = useGet<{ store_id: string; items: Suggestion[] }>(allowed ? '/bhandar/v1/reorder/suggestions' : null, { store_id: store })
  const suppliers = useList<Party>(allowed ? '/bahi/v1/parties' : null, { type: 'supplier', limit: 500 }, { staleTime: 5 * 60_000 })
  const supplierName = useMemo(() => new Map((suppliers.data?.items ?? []).map((p) => [p.id, p.name])), [suppliers.data])
  const draft = useSave<{ drafts: PurchaseDraft[] }, PurchaseDraftInput>('/bhandar/v1/reorder/purchase-draft', { silent: true })

  if (!allowed) return <Empty icon={Lock} title="Buyers only" text="Re-order suggestions need stock:manage or purchase:manage." />

  const rows = sugg.data?.items ?? []
  const ids = Object.keys(selected)
  const allSelected = rows.length > 0 && rows.every((r) => selected[r.item_id])

  const makeDrafts = async () => {
    setErr('')
    try {
      const r = await draft.mutateAsync({ store_id: store || undefined, item_ids: ids.length ? ids : undefined })
      setDrafts(r.drafts)
      setPickedSupplier({})
      if (r.drafts.length === 0) toast('Nothing to draft: the selected items are no longer below their level', 'info')
    } catch (e) { setErr(errorMessage(e)) }
  }

  const open = (d: PurchaseDraft, i: number) => {
    const party = d.supplier_id || pickedSupplier[i]
    if (!party) return setErr('Pick a supplier for this draft first.')
    openPurchaseDraft({ ...d.body, party_id: party, store_id: d.body.store_id || store || storeId }, router)
  }

  return (
    <>
      <div className="flex flex-col gap-3 border-b border-slate-100 p-3 sm:p-4 md:flex-row md:items-center md:justify-between">
        <StoreSelect value={store} onChange={(v) => { setStore(v); setSelected({}); setDrafts(null) }} />
        <div className="flex items-center gap-2">
          <span className="text-xs text-slate-500">{ids.length ? `${ids.length} selected` : rows.length ? 'Nothing selected = all items' : ''}</span>
          <Button icon={ShoppingCart} onClick={makeDrafts} loading={draft.isPending} disabled={rows.length === 0}>Create purchase drafts</Button>
        </div>
      </div>
      {err && <div className="p-3 sm:p-4"><ErrorBox message={err} /></div>}

      {drafts && (
        <div className="space-y-3 border-b border-slate-100 bg-brand-50/40 p-3 sm:p-4">
          <div className="text-sm font-semibold">Purchase drafts for {storeName(stores, store || storeId)}</div>
          {drafts.length === 0 && <p className="text-sm text-slate-500">No drafts.</p>}
          {drafts.map((d, i) => (
            <Card key={`${d.supplier_id}-${i}`}>
              <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
                <div className="min-w-0 flex-1">
                  <div className="font-semibold">{d.supplier_id ? supplierName.get(d.supplier_id) ?? `Supplier ${d.supplier_id.slice(0, 8)}` : <Badge tone="amber">No preferred supplier</Badge>}</div>
                  <div className="text-xs text-slate-500">{d.items} item{d.items === 1 ? '' : 's'} · est. <Money paise={d.value_paise} /> · {d.body.lines.map((l) => `${fmtQty(l.qty)} ${l.unit ?? ''} ${l.name}`).join(', ')}</div>
                  {!d.supplier_id && <div className="mt-2 max-w-sm"><SupplierPicker value={pickedSupplier[i] ?? ''} onChange={(id) => setPickedSupplier((p) => ({ ...p, [i]: id }))} /></div>}
                </div>
                {canDo(PERMS.purchaseManage) ? (
                  <Button icon={ArrowRight} onClick={() => open(d, i)} disabled={!d.supplier_id && !pickedSupplier[i]}>Open purchase bill</Button>
                ) : <span className="text-xs text-slate-500">purchase:manage needed to bill</span>}
              </div>
            </Card>
          ))}
        </div>
      )}

      <QueryState isLoading={sugg.isLoading} error={sugg.error} data={sugg.data?.items} retry={() => sugg.refetch()}
        empty={<Empty icon={Package} title="Stock is healthy" text="No active item is at or below its re-order level at this store. Set levels on items to get suggestions." />}>
        {(items) => (
          <Table>
            <thead className="bg-slate-50/60">
              <tr>
                <th className="th w-10"><input type="checkbox" className="h-4 w-4 accent-brand-600" checked={allSelected} onChange={() => setSelected(allSelected ? {} : Object.fromEntries(items.map((r) => [r.item_id, true])))} aria-label="Select all" /></th>
                <th className="th">Item</th><th className="th text-right">On hand</th><th className="th text-right">Level</th><th className="th text-right">Suggested</th><th className="th text-right">Purchase price</th><th className="th text-right">Est. value</th><th className="th">Supplier</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100">
              {items.map((r) => (
                <tr key={r.item_id} className={`hover:bg-slate-50/60 ${selected[r.item_id] ? 'bg-brand-50/40' : ''}`}>
                  <td className="td"><input type="checkbox" className="h-4 w-4 accent-brand-600" checked={!!selected[r.item_id]} onChange={() => setSelected((s) => { const n = { ...s }; if (n[r.item_id]) delete n[r.item_id]; else n[r.item_id] = true; return n })} aria-label={`Select ${r.name}`} /></td>
                  <td className="td"><Link href={`/app/items/${r.item_id}`} className="font-semibold hover:underline">{r.name}</Link><div className="text-xs text-slate-500">{[r.sku, r.hsn && `HSN ${r.hsn}`].filter(Boolean).join(' · ')}</div></td>
                  <td className={`td num text-right font-semibold ${r.on_hand <= 0 ? 'text-rose-600' : 'text-amber-600'}`}>{fmtQty(r.on_hand)} <span className="text-xs font-normal text-slate-400">{r.unit}</span></td>
                  <td className="td num text-right text-slate-600">{fmtQty(r.reorder_level)}</td>
                  <td className="td num text-right font-semibold text-brand-700">+{fmtQty(r.suggested_qty)}</td>
                  <td className="td text-right"><Money paise={r.purchase_price_paise} className="text-slate-600" /></td>
                  <td className="td text-right"><Money paise={Math.round(r.suggested_qty * r.purchase_price_paise)} /></td>
                  <td className="td text-xs">{r.preferred_supplier_id ? supplierName.get(r.preferred_supplier_id) ?? r.preferred_supplier_id.slice(0, 8) : <span className="text-slate-400">—</span>}</td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
      </QueryState>
    </>
  )
}
