'use client'

import { useState } from 'react'
import { useRouter } from 'next/navigation'
import { ArrowRight, Truck } from 'lucide-react'
import { Badge, Empty, Pagination, QueryState, Table, Tabs, statusTone } from '@/components/ui'
import { fmtDate, useList, useStore } from '@/lib/hooks'
import { TRANSFER_STATUS_LABEL } from '@/lib/stock/reasons'
import { storeName } from '@/lib/stock/scope'
import type { Transfer, TransferStatus } from '@/lib/types.stock'
import { StoreSelect } from './StoreSelect'

type Filter = 'all' | TransferStatus
const LIMIT = 50

/** Inter-store transfers list; click a row for the detail with dispatch / receive / cancel. */
export function StockTransfers() {
  const router = useRouter()
  const { storeId, stores } = useStore()
  const [store, setStore] = useState(storeId ?? '')
  const [status, setStatus] = useState<Filter>('all')
  const [offset, setOffset] = useState(0)
  const list = useList<Transfer>('/bhandar/v1/transfers', { store_id: store, status: status === 'all' ? '' : status, limit: LIMIT, offset })

  return (
    <>
      <div className="flex flex-col gap-3 border-b border-slate-100 p-3 sm:p-4 md:flex-row md:items-center md:justify-between">
        <Tabs value={status} onChange={(v) => { setStatus(v); setOffset(0) }} options={[{ value: 'all', label: 'All' }, { value: 'draft', label: 'Draft' }, { value: 'dispatched', label: 'In transit' }, { value: 'received', label: 'Received' }, { value: 'cancelled', label: 'Cancelled' }]} />
        <StoreSelect value={store} onChange={(v) => { setStore(v); setOffset(0) }} allowAll label="Store (either side)" />
      </div>
      <QueryState isLoading={list.isLoading} error={list.error} data={list.data} retry={() => list.refetch()}>
        {(d) => d.items.length === 0 ? (
          <Empty icon={Truck} title="No transfers" text={stores.length > 1 ? 'Move stock between your stores: draft → dispatched → received.' : 'Transfers need at least two stores. Add one under Settings → Stores.'} />
        ) : (
          <>
            <Table>
              <thead className="bg-slate-50/60"><tr><th className="th">Created</th><th className="th">Route</th><th className="th">Status</th><th className="th">Dispatched</th><th className="th">Received</th><th className="th">Notes</th></tr></thead>
              <tbody className="divide-y divide-slate-100">
                {d.items.map((t) => (
                  <tr key={t.id} onClick={() => router.push(`/app/stock/transfers/${t.id}`)} className="cursor-pointer hover:bg-slate-50/60">
                    <td className="td whitespace-nowrap text-slate-600">{fmtDate(t.created_at, true)}</td>
                    <td className="td"><span className="inline-flex items-center gap-1.5 font-semibold">{storeName(stores, t.from_store_id)} <ArrowRight size={14} className="text-slate-400" /> {storeName(stores, t.to_store_id)}</span></td>
                    <td className="td"><Badge tone={statusTone(t.status)}>{TRANSFER_STATUS_LABEL[t.status] ?? t.status}</Badge></td>
                    <td className="td text-xs text-slate-600">{fmtDate(t.dispatched_at, true) || '—'}</td>
                    <td className="td text-xs text-slate-600">{fmtDate(t.received_at, true) || (t.cancelled_at ? <span className="text-rose-600">cancelled {fmtDate(t.cancelled_at)}</span> : '—')}</td>
                    <td className="td max-w-[260px] truncate text-xs text-slate-500" title={t.notes}>{t.notes || ''}</td>
                  </tr>
                ))}
              </tbody>
            </Table>
            <Pagination total={d.total} limit={d.limit || LIMIT} offset={d.offset} onChange={setOffset} />
          </>
        )}
      </QueryState>
    </>
  )
}
