'use client'

/** GET /buddhi/v1/forecast/reorder?store_id&lead_days → items to re-order with the forecast behind each suggestion. */
import { useState } from 'react'
import Link from 'next/link'
import { AlertTriangle, PackageSearch, TrendingUp } from 'lucide-react'
import { Badge, Card, Empty, Field, Input, QueryState, Table } from '@/components/ui'
import { fmtQty, useGet, useStore } from '@/lib/hooks'
import type { ReorderReport } from '@/lib/types.more'
import { StoreScope } from './common'

export function ForecastTab() {
  const { storeId } = useStore()
  const [store, setStore] = useState(storeId ?? '')
  const [lead, setLead] = useState('')
  const leadDays = Number(lead)
  const q = useGet<ReorderReport>('/buddhi/v1/forecast/reorder', { store_id: store || storeId || undefined, lead_days: lead !== '' && leadDays >= 0 && leadDays <= 90 ? leadDays : undefined })

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-end gap-3">
        <StoreScope value={store || storeId || ''} onChange={setStore} />
        <Field label="Supplier lead time (days)" hint="Blank = business default"><Input className="num w-32" inputMode="numeric" placeholder={q.data ? String(q.data.lead_days) : '7'} value={lead} onChange={(e) => setLead(e.target.value.replace(/\D/g, '').slice(0, 2))} /></Field>
        {q.data && <p className="pb-6 text-xs text-slate-500">Forecast covers {q.data.lead_days} lead day{q.data.lead_days === 1 ? '' : 's'} + {q.data.cover_days} days of cover. Demand is smoothed daily sales with day-of-week seasonality (no AI tokens used).</p>}
      </div>
      <QueryState isLoading={q.isLoading} error={q.error} data={q.data} retry={() => q.refetch()}>
        {(d) => (
          <>
            {d.warnings.map((w, i) => (
              <div key={i} className="flex items-start gap-2 rounded-xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-900" role="alert"><AlertTriangle size={16} className="mt-0.5 shrink-0" /> {w}</div>
            ))}
            <Card padded={false}>
              {d.items.length === 0 ? <Empty icon={PackageSearch} title="Nothing to re-order" text="Stock covers the forecast demand over the lead time. The nightly forecast refreshes at 02:00." /> : (
                <>
                  <ul className="divide-y divide-slate-100 md:hidden">
                    {d.items.map((it) => (
                      <li key={it.item_id} className="px-4 py-3">
                        <div className="flex items-start justify-between gap-3">
                          <div className="min-w-0"><div className="truncate text-sm font-semibold">{it.name}</div><div className="text-xs text-slate-500">{fmtQty(it.on_hand)} on hand · {fmtQty(it.forecast_daily_demand)}/day</div></div>
                          <div className="text-right"><div className="num text-base font-bold text-brand-700">+{fmtQty(it.suggested_qty)}</div><ReasonBadge reason={it.reason} /></div>
                        </div>
                      </li>
                    ))}
                  </ul>
                  <div className="hidden md:block">
                    <Table>
                      <thead><tr><th className="th">Item</th><th className="th text-right">On hand</th><th className="th text-right">Demand / day</th><th className="th text-right">Demand over lead time</th><th className="th text-right">Suggested order</th><th className="th">Why</th></tr></thead>
                      <tbody>
                        {d.items.map((it) => {
                          const shortfall = it.forecast_demand_over_lead_time > it.on_hand
                          return (
                            <tr key={it.item_id} className="border-t border-slate-100">
                              <td className="td font-medium"><Link href={`/app/items?q=${encodeURIComponent(it.name)}`} className="text-ink hover:underline">{it.name}</Link></td>
                              <td className={`td num text-right ${shortfall ? 'text-rose-700' : ''}`}>{fmtQty(it.on_hand)}</td>
                              <td className="td num text-right">{it.forecast_daily_demand ? fmtQty(it.forecast_daily_demand) : '—'}</td>
                              <td className="td num text-right">{it.forecast_demand_over_lead_time ? fmtQty(it.forecast_demand_over_lead_time) : '—'}</td>
                              <td className="td num text-right text-base font-bold text-brand-700">+{fmtQty(it.suggested_qty)}</td>
                              <td className="td"><ReasonBadge reason={it.reason} /></td>
                            </tr>
                          )
                        })}
                      </tbody>
                    </Table>
                  </div>
                  <p className="px-4 py-3 text-xs text-slate-500">Raise purchase bills from <Link href="/app/purchases/new" className="link">Purchases</Link>; suggested quantities round up to cover the lead time plus the cover window.</p>
                </>
              )}
            </Card>
          </>
        )}
      </QueryState>
    </div>
  )
}

function ReasonBadge({ reason }: { reason: string }) {
  return reason === 'forecast'
    ? <Badge tone="brand"><TrendingUp size={12} /> forecast</Badge>
    : <Badge tone="amber"><AlertTriangle size={12} /> below re-order level</Badge>
}
