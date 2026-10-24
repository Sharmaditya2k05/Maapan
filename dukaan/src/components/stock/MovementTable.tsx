'use client'

import Link from 'next/link'
import { Badge, Money, Table } from '@/components/ui'
import { fmtDate, fmtQty, useStore } from '@/lib/hooks'
import { reasonLabel, reasonTone, sourceHref, sourceLabel } from '@/lib/stock/reasons'
import { storeName } from '@/lib/stock/scope'
import type { Movement } from '@/lib/types.stock'

/** Stock ledger rows: signed qty, cost, value, running balance, reason badge and a link to the source document. */
export function MovementTable({ rows, showItem, showStore, showRunning = true }: { rows: Movement[]; showItem?: boolean; showStore?: boolean; showRunning?: boolean }) {
  const { stores } = useStore()
  return (
    <Table>
      <thead className="bg-slate-50/60">
        <tr>
          <th className="th">Date</th>
          {showItem && <th className="th">Item</th>}
          {showStore && <th className="th">Store</th>}
          <th className="th">Reason</th>
          <th className="th">Batch</th>
          <th className="th text-right">Qty</th>
          <th className="th text-right">Unit cost</th>
          <th className="th text-right">Value</th>
          {showRunning && <th className="th text-right">Balance</th>}
          <th className="th">Source</th>
          <th className="th">Note</th>
        </tr>
      </thead>
      <tbody className="divide-y divide-slate-100">
        {rows.map((m) => {
          const href = sourceHref(m)
          const src = <>{sourceLabel(m.source)} <span className="font-mono text-[11px] text-slate-400">{m.source_id.slice(0, 8)}</span></>
          return (
            <tr key={m.id} className="hover:bg-slate-50/60">
              <td className="td whitespace-nowrap text-slate-600">{fmtDate(m.created_at, true)}</td>
              {showItem && <td className="td"><Link href={`/app/items/${m.item_id}`} className="font-semibold hover:underline">{m.item_name || m.item_id.slice(0, 8)}</Link></td>}
              {showStore && <td className="td text-slate-600">{storeName(stores, m.store_id)}</td>}
              <td className="td"><Badge tone={reasonTone(m.reason)}>{reasonLabel(m.reason)}</Badge></td>
              <td className="td font-mono text-xs text-slate-600">{m.batch_no || '—'}</td>
              <td className={`td num text-right font-semibold ${m.qty < 0 ? 'text-rose-600' : 'text-emerald-700'}`}>{m.qty > 0 ? '+' : ''}{fmtQty(m.qty)}{m.unit ? <span className="ml-1 text-xs font-normal text-slate-400">{m.unit}</span> : null}</td>
              <td className="td text-right"><Money paise={m.unit_cost_paise} className="text-slate-600" /></td>
              <td className="td text-right"><Money paise={m.value_paise} signed /></td>
              {showRunning && <td className="td num text-right font-semibold">{m.running_qty === undefined ? '—' : fmtQty(m.running_qty)}</td>}
              <td className="td text-xs">{href ? <Link href={href} className="link">{src}</Link> : <span className="text-slate-600">{src}</span>}</td>
              <td className="td max-w-[220px] truncate text-xs text-slate-500" title={m.note}>{m.note || ''}</td>
            </tr>
          )
        })}
      </tbody>
    </Table>
  )
}
