'use client'

import { useState } from 'react'
import Link from 'next/link'
import { Bar, BarChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import { BarChart3, Download, Lock } from 'lucide-react'
import { Button, Card, Empty, Money, QueryState, Select, StatCard, Table } from '@/components/ui'
import { downloadFile, fmtDate, fmtQty, useGet, usePerm, useStore } from '@/lib/hooks'
import { formatINR } from '@/lib/money'
import { PERMS } from '@/lib/rbac'
import { csvMoney, toCSV } from '@/lib/stock/csv'
import { storeName } from '@/lib/stock/scope'
import type { AgingRow, DeadStockRow, Valuation } from '@/lib/types.stock'
import { StoreSelect } from './StoreSelect'

/** ₹ in lakh / crore for axis ticks. */
function compactRupees(paise: number) {
  const r = paise / 100
  if (Math.abs(r) >= 1e7) return `₹${(r / 1e7).toFixed(1)} Cr`
  if (Math.abs(r) >= 1e5) return `₹${(r / 1e5).toFixed(1)} L`
  if (Math.abs(r) >= 1e3) return `₹${(r / 1e3).toFixed(0)}k`
  return `₹${r.toFixed(0)}`
}

/** Dead stock, aging buckets and valuation, each with CSV export. */
export function StockAnalytics() {
  const { storeId, stores } = useStore()
  const canDo = usePerm()
  const [store, setStore] = useState(storeId ?? '')
  const [deadDays, setDeadDays] = useState(90)
  const [showAll, setShowAll] = useState(false)
  const allowed = canDo(PERMS.reportRead) || canDo(PERMS.stockManage)
  const valuation = useGet<Valuation>(allowed ? '/bhandar/v1/analytics/valuation' : null, { store_id: store })
  const dead = useGet<{ days: number; items: DeadStockRow[]; total_value_paise: number }>(allowed ? '/bhandar/v1/analytics/dead-stock' : null, { days: deadDays, store_id: store })
  const aging = useGet<{ buckets: AgingRow[] }>(allowed ? '/bhandar/v1/analytics/stock-aging' : null, { store_id: store })
  if (!allowed) return <Empty icon={Lock} title="Reports only" text="Stock analytics need report:read or stock:manage." />
  const scope = store ? storeName(stores, store) : 'all stores'

  return (
    <div className="space-y-4 p-3 sm:p-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <StoreSelect value={store} onChange={setStore} allowAll />
        <span className="text-xs text-slate-500">Weighted-average cost · {scope}</span>
      </div>

      {valuation.data && (
        <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
          <StatCard label="Stock value" value={<Money paise={valuation.data.total_value_paise} />} icon={BarChart3} hint={`as of ${fmtDate(valuation.data.as_of, true)}`} />
          <StatCard label="Items with stock" value={valuation.data.items.length.toLocaleString('en-IN')} icon={BarChart3} tone="sky" />
          <StatCard label="Dead stock value" value={dead.data ? <Money paise={dead.data.total_value_paise} /> : '—'} icon={BarChart3} tone="rose" hint={`no sale in ${deadDays} days`} />
          <StatCard label="Dead stock share" value={dead.data && valuation.data.total_value_paise > 0 ? `${((dead.data.total_value_paise / valuation.data.total_value_paise) * 100).toFixed(0)}%` : '—'} icon={BarChart3} tone="gold" />
        </div>
      )}

      <div className="grid gap-4 lg:grid-cols-2">
        <Card title="Stock aging" actions={aging.data && <Button size="sm" variant="ghost" icon={Download} onClick={() => downloadFile('stock-aging.csv', toCSV(['Bucket (days since receipt)', 'Items', 'Qty', 'Value'], aging.data!.buckets.map((b) => [b.bucket, b.items, b.qty, csvMoney(b.value_paise)])), 'text/csv')}>CSV</Button>}>
          <QueryState isLoading={aging.isLoading} error={aging.error} data={aging.data?.buckets} retry={() => aging.refetch()}>
            {(buckets) => buckets.every((b) => b.items === 0) ? <Empty icon={BarChart3} title="No stock to age" /> : (
              <>
                <div className="h-56 w-full" role="img" aria-label="Stock value by days since last receipt">
                  <ResponsiveContainer width="100%" height="100%">
                    <BarChart data={buckets} margin={{ top: 8, right: 8, left: 0, bottom: 0 }}>
                      <CartesianGrid vertical={false} stroke="#e2e8f0" />
                      <XAxis dataKey="bucket" tickLine={false} axisLine={false} tick={{ fontSize: 12, fill: '#64748b' }} />
                      <YAxis tickLine={false} axisLine={false} width={64} tick={{ fontSize: 11, fill: '#64748b' }} tickFormatter={(v: number) => compactRupees(v)} />
                      <Tooltip cursor={{ fill: 'rgba(148,163,184,.12)' }} formatter={(v) => formatINR(Number(v))} labelFormatter={(l) => `${l} days since receipt`} contentStyle={{ borderRadius: 12, fontSize: 12 }} />
                      <Bar dataKey="value_paise" name="Stock value" fill="#0a8a60" radius={[4, 4, 0, 0]} maxBarSize={56} />
                    </BarChart>
                  </ResponsiveContainer>
                </div>
                <table className="mt-3 w-full text-sm">
                  <thead><tr><th className="th">Days since receipt</th><th className="th text-right">Items</th><th className="th text-right">Qty</th><th className="th text-right">Value</th></tr></thead>
                  <tbody className="divide-y divide-slate-100">{buckets.map((b) => <tr key={b.bucket}><td className="td font-semibold">{b.bucket}</td><td className="td num text-right">{b.items}</td><td className="td num text-right">{fmtQty(b.qty)}</td><td className="td text-right"><Money paise={b.value_paise} /></td></tr>)}</tbody>
                </table>
              </>
            )}
          </QueryState>
        </Card>

        <Card title="Dead stock" actions={
          <>
            <Select aria-label="Idle days" value={deadDays} onChange={(e) => setDeadDays(Number(e.target.value))} className="input-sm w-auto">{[30, 60, 90, 180, 365].map((d) => <option key={d} value={d}>{d} days</option>)}</Select>
            {dead.data && <Button size="sm" variant="ghost" icon={Download} onClick={() => downloadFile(`dead-stock-${deadDays}d.csv`, toCSV(['Store', 'Item', 'Qty', 'Unit', 'Avg cost', 'Value', 'Idle days', 'Last in', 'Last out'], dead.data!.items.map((r) => [storeName(stores, r.store_id), r.name, r.qty, r.unit, csvMoney(r.avg_cost_paise), csvMoney(r.value_paise), r.idle_days, r.last_in_at, r.last_out_at])), 'text/csv')}>CSV</Button>}
          </>
        } padded={false}>
          <QueryState isLoading={dead.isLoading} error={dead.error} data={dead.data?.items} retry={() => dead.refetch()}
            empty={<Empty icon={BarChart3} title="No dead stock" text={`Everything in stock sold at least once in the last ${deadDays} days.`} />}>
            {(rows) => (
              <Table className="max-h-96 overflow-y-auto">
                <thead className="sticky top-0 bg-slate-50"><tr><th className="th">Item</th>{!store && <th className="th">Store</th>}<th className="th text-right">Qty</th><th className="th text-right">Value</th><th className="th text-right">Idle</th></tr></thead>
                <tbody className="divide-y divide-slate-100">
                  {rows.map((r) => (
                    <tr key={`${r.store_id}-${r.item_id}`}>
                      <td className="td"><Link href={`/app/items/${r.item_id}`} className="font-semibold hover:underline">{r.name}</Link></td>
                      {!store && <td className="td text-xs text-slate-600">{storeName(stores, r.store_id)}</td>}
                      <td className="td num text-right">{fmtQty(r.qty)} <span className="text-xs text-slate-400">{r.unit}</span></td>
                      <td className="td text-right"><Money paise={r.value_paise} /></td>
                      <td className="td num text-right text-slate-600">{r.idle_days}d</td>
                    </tr>
                  ))}
                </tbody>
              </Table>
            )}
          </QueryState>
        </Card>
      </div>

      <Card title="Valuation by item" padded={false} actions={
        <>
          {valuation.data && valuation.data.items.length > 25 && <Button size="sm" variant="ghost" onClick={() => setShowAll((s) => !s)}>{showAll ? 'Top 25' : `All ${valuation.data.items.length}`}</Button>}
          {valuation.data && <Button size="sm" variant="ghost" icon={Download} onClick={() => downloadFile(`valuation-${valuation.data!.as_of.slice(0, 10)}.csv`, toCSV(['Item', 'Qty', 'Unit', 'Avg cost', 'Value'], valuation.data!.items.map((r) => [r.name, r.qty, r.unit, csvMoney(r.avg_cost_paise), csvMoney(r.value_paise)])), 'text/csv')}>CSV</Button>}
        </>
      }>
        <QueryState isLoading={valuation.isLoading} error={valuation.error} data={valuation.data} retry={() => valuation.refetch()}>
          {(v) => v.items.length === 0 ? <Empty icon={BarChart3} title="No stock" /> : (
            <Table>
              <thead className="bg-slate-50/60"><tr><th className="th">Item</th><th className="th text-right">Qty</th><th className="th text-right">Avg cost</th><th className="th text-right">Value</th><th className="th text-right">Share</th></tr></thead>
              <tbody className="divide-y divide-slate-100">
                {[...v.items].sort((a, b) => b.value_paise - a.value_paise).slice(0, showAll ? undefined : 25).map((r) => (
                  <tr key={r.item_id}>
                    <td className="td"><Link href={`/app/items/${r.item_id}`} className="font-semibold hover:underline">{r.name}</Link></td>
                    <td className={`td num text-right ${r.qty < 0 ? 'text-rose-600' : ''}`}>{fmtQty(r.qty)} <span className="text-xs text-slate-400">{r.unit}</span></td>
                    <td className="td text-right"><Money paise={r.avg_cost_paise} className="text-slate-600" /></td>
                    <td className="td text-right"><Money paise={r.value_paise} /></td>
                    <td className="td num text-right text-slate-600">{v.total_value_paise > 0 ? `${((r.value_paise / v.total_value_paise) * 100).toFixed(1)}%` : '—'}</td>
                  </tr>
                ))}
              </tbody>
              <tfoot><tr className="border-t border-slate-200 bg-slate-50/60 font-semibold"><td className="td">Total</td><td className="td num text-right">{fmtQty(v.total_qty)}</td><td className="td" /><td className="td text-right"><Money paise={v.total_value_paise} /></td><td className="td" /></tr></tfoot>
            </Table>
          )}
        </QueryState>
      </Card>
    </div>
  )
}
