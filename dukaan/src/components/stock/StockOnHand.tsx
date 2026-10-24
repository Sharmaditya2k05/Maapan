'use client'

import { useState } from 'react'
import Link from 'next/link'
import { Boxes, Download, X } from 'lucide-react'
import { Badge, Button, Empty, Money, Pagination, QueryState, StatCard, Table, Toggle, useToast } from '@/components/ui'
import { downloadFile, fmtDate, fmtQty, useGet, useList, usePerm, useStore } from '@/lib/hooks'
import { errorMessage, get } from '@/lib/api'
import { PERMS } from '@/lib/rbac'
import { csvMoney, toCSV } from '@/lib/stock/csv'
import { fetchAllPages, storeName } from '@/lib/stock/scope'
import type { Balance, ItemFull, Valuation } from '@/lib/types.stock'
import type { ListResponse } from '@/lib/types'
import { ItemPicker } from './ItemPicker'
import { StoreSelect } from './StoreSelect'

const LIMIT = 50

/** On-hand positions per store (optionally per batch) with value, totals and CSV export. */
export function StockOnHand() {
  const { storeId, stores } = useStore()
  const canDo = usePerm()
  const toast = useToast()
  const [store, setStore] = useState(storeId ?? '')
  const [byBatch, setByBatch] = useState(false)
  const [item, setItem] = useState<Pick<ItemFull, 'id' | 'name'> | null>(null)
  const [offset, setOffset] = useState(0)
  const [exporting, setExporting] = useState(false)
  const filters = { store_id: store, item_id: item?.id ?? '', by_batch: byBatch }
  const stock = useList<Balance>('/bhandar/v1/stock', { ...filters, limit: LIMIT, offset })
  const valuation = useGet<Valuation>(canDo(PERMS.reportRead) || canDo(PERMS.stockManage) ? '/bhandar/v1/analytics/valuation' : null, { store_id: store })

  const exportCSV = async () => {
    setExporting(true)
    try {
      const rows = await fetchAllPages<Balance>((limit, off) => get<ListResponse<Balance>>('/bhandar/v1/stock', { ...filters, by_batch: String(byBatch), limit, offset: off }))
      downloadFile(`stock-on-hand${store ? `-${storeName(stores, store)}` : ''}.csv`, toCSV(
        ['Store', 'Item', 'Batch', 'Expiry', 'Qty', 'Unit', 'Avg cost', 'Value', 'Re-order level', 'Last in', 'Last out'],
        rows.map((b) => [storeName(stores, b.store_id), b.item_name, b.batch_no, b.expiry_date, b.qty, b.unit, csvMoney(b.avg_cost_paise), csvMoney(b.value_paise), b.reorder_level, b.last_in_at, b.last_out_at]),
      ), 'text/csv')
    } catch (e) { toast(errorMessage(e), 'error') } finally { setExporting(false) }
  }

  return (
    <>
      {valuation.data && (
        <div className="grid grid-cols-2 gap-3 border-b border-slate-100 p-3 sm:p-4 lg:grid-cols-4">
          <StatCard label="Stock value (at cost)" value={<Money paise={valuation.data.total_value_paise} />} icon={Boxes} hint={store ? storeName(stores, store) : 'all stores'} />
          <StatCard label="Items in stock" value={valuation.data.items.length.toLocaleString('en-IN')} icon={Boxes} tone="sky" />
          <StatCard label="Negative positions" value={valuation.data.items.filter((i) => i.qty < 0).length} icon={Boxes} tone="rose" hint="books behind the counter" />
          <StatCard label="As of" value={<span className="text-base">{fmtDate(valuation.data.as_of, true)}</span>} icon={Boxes} tone="gold" />
        </div>
      )}
      <div className="flex flex-col gap-3 border-b border-slate-100 p-3 sm:p-4 md:flex-row md:items-center">
        <StoreSelect value={store} onChange={(v) => { setStore(v); setOffset(0) }} allowAll />
        <div className="flex-1 md:max-w-sm">
          {item ? (
            <div className="flex h-10 items-center gap-2 rounded-xl border border-slate-200 bg-slate-50 px-3 text-sm"><span className="min-w-0 flex-1 truncate font-semibold">{item.name}</span><button type="button" onClick={() => { setItem(null); setOffset(0) }} className="rounded p-1 text-slate-400 hover:text-ink cursor-pointer" aria-label="Clear item filter"><X size={14} /></button></div>
          ) : <ItemPicker onPick={(it) => { setItem(it); setOffset(0) }} placeholder="Filter by item" />}
        </div>
        <Toggle checked={byBatch} onChange={(v) => { setByBatch(v); setOffset(0) }} label="By batch" />
        <div className="md:ml-auto"><Button variant="secondary" size="sm" icon={Download} onClick={exportCSV} loading={exporting}>CSV</Button></div>
      </div>
      <QueryState isLoading={stock.isLoading} error={stock.error} data={stock.data} retry={() => stock.refetch()}>
        {(d) => d.items.length === 0 ? <Empty icon={Boxes} title="Nothing in stock" text="Purchases, opening stock and transfers in create positions here." /> : (
          <>
            <Table>
              <thead className="bg-slate-50/60">
                <tr>
                  <th className="th">Item</th>{!store && <th className="th">Store</th>}
                  {byBatch && <><th className="th">Batch</th><th className="th">Expiry</th></>}
                  <th className="th text-right">On hand</th><th className="th text-right">Avg cost</th><th className="th text-right">Value</th><th className="th">Last in / out</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100">
                {d.items.map((b) => (
                  <tr key={`${b.store_id}-${b.item_id}-${b.batch_id ?? ''}`} className="hover:bg-slate-50/60">
                    <td className="td"><Link href={`/app/items/${b.item_id}`} className="font-semibold hover:underline">{b.item_name}</Link>{!byBatch && b.reorder_level && b.qty <= b.reorder_level ? <Badge tone="amber" className="ml-2">re-order</Badge> : null}</td>
                    {!store && <td className="td text-slate-600">{storeName(stores, b.store_id)}</td>}
                    {byBatch && <><td className="td font-mono text-xs">{b.batch_no || '—'}</td><td className="td text-xs text-slate-600">{b.expiry_date ? fmtDate(b.expiry_date) : '—'}</td></>}
                    <td className={`td num text-right font-semibold ${b.qty < 0 ? 'text-rose-600' : ''}`}>{fmtQty(b.qty)} <span className="text-xs font-normal text-slate-400">{b.unit}</span></td>
                    <td className="td text-right"><Money paise={b.avg_cost_paise} className="text-slate-600" /></td>
                    <td className="td text-right"><Money paise={b.value_paise} /></td>
                    <td className="td text-xs text-slate-500">{fmtDate(b.last_in_at) || '—'} / {fmtDate(b.last_out_at) || '—'}</td>
                  </tr>
                ))}
              </tbody>
              <tfoot>
                <tr className="border-t border-slate-200 bg-slate-50/60 text-sm font-semibold">
                  <td className="td" colSpan={1 + (store ? 0 : 1) + (byBatch ? 2 : 0)}>This page ({d.items.length} of {d.total})</td>
                  <td className="td num text-right">{fmtQty(d.items.reduce((a, b) => a + b.qty, 0))}</td>
                  <td className="td" />
                  <td className="td text-right"><Money paise={d.items.reduce((a, b) => a + b.value_paise, 0)} /></td>
                  <td className="td" />
                </tr>
              </tfoot>
            </Table>
            <Pagination total={d.total} limit={d.limit || LIMIT} offset={d.offset} onChange={setOffset} />
          </>
        )}
      </QueryState>
    </>
  )
}
