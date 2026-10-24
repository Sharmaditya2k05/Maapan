'use client'

/** GET /buddhi/v1/dashboard: today vs yesterday, last 7 days (one series per chart), top items, payment mix. */
import { useState } from 'react'
import { Bar, BarChart, CartesianGrid, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import { IndianRupee, Percent, Receipt, ReceiptIndianRupee, XCircle } from 'lucide-react'
import { Badge, Card, Empty, Input, Money, QueryState, StatCard, Table } from '@/components/ui'
import { fmtDate, fmtQty, todayIST, useGet, useStore } from '@/lib/hooks'
import { formatINR } from '@/lib/money'
import type { AiDashboard } from '@/lib/types.more'
import { StoreScope } from './common'

const NAVY = '#0b3a78'
const GREEN = '#0ea673'
const GRID = '#e2e8f0'
const INK_MUTED = '#64748b'

const TENDER_LABEL: Record<string, string> = { cash: 'Cash', upi: 'UPI', card: 'Card', bank: 'Bank', credit: 'Credit (udhaar)', other: 'Other' }
const shortDay = (d: string) => new Date(`${d}T00:00:00+05:30`).toLocaleDateString('en-IN', { weekday: 'short', timeZone: 'Asia/Kolkata' })
const rupees = (p: number) => p / 100
const compactINR = (r: number) => (Math.abs(r) >= 1e5 ? `₹${(r / 1e5).toFixed(1)}L` : Math.abs(r) >= 1e3 ? `₹${(r / 1e3).toFixed(0)}k` : `₹${r.toFixed(0)}`)

export function DashboardTab() {
  const { storeId } = useStore()
  const [store, setStore] = useState(storeId ?? '')
  const [date, setDate] = useState(todayIST())
  const q = useGet<AiDashboard>('/buddhi/v1/dashboard', { store_id: store || undefined, date })

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <StoreScope value={store || storeId || ''} onChange={setStore} allowAll />
        <Input type="date" className="w-auto" value={date} max={todayIST()} onChange={(e) => setDate(e.target.value || todayIST())} aria-label="Dashboard date" />
        {q.data && <span className="text-xs text-slate-500">from {q.data.source} · {fmtDate(q.data.generated_at, true)}</span>}
      </div>
      <QueryState isLoading={q.isLoading} error={q.error} data={q.data} retry={() => q.refetch()}>
        {(d) => {
          const t = d.today
          const series = d.last_7_days.map((k) => ({ day: shortDay(k.date), date: k.date, sales: rupees(k.sales_paise), bills: k.bills, cancelled: k.cancelled }))
          const mix = Object.entries(d.payment_mix_paise).sort((a, b) => b[1] - a[1])
          const mixTotal = mix.reduce((a, [, v]) => a + v, 0)
          const noData = t.bills === 0 && d.last_7_days.every((k) => k.bills === 0)
          return (
            <>
              <div className="stagger grid grid-cols-2 gap-3 sm:gap-4 xl:grid-cols-5">
                <StatCard label="Sales" value={<Money paise={t.sales_paise} />} icon={IndianRupee} trend={d.sales_change_pct ?? null} hint={d.sales_change_pct !== undefined ? 'vs yesterday' : <>yesterday <Money paise={d.yesterday.sales_paise} /></>} />
                <StatCard label="Bills" value={t.bills.toLocaleString('en-IN')} icon={Receipt} tone="sky" hint={t.cancelled ? `${t.cancelled} cancelled` : 'none cancelled'} />
                <StatCard label="Average bill" value={<Money paise={t.avg_bill_paise} />} icon={ReceiptIndianRupee} tone="gold" />
                <StatCard label="GST collected" value={<Money paise={t.tax_paise} />} icon={Percent} tone="brand" hint={<>on taxable <Money paise={t.taxable_paise} /></>} />
                <StatCard label="Gross margin" value={t.margin_base_paise > 0 ? `${t.gross_margin_pct.toFixed(1)}%` : '—'} icon={XCircle} tone="rose" hint={t.margin_base_paise > 0 ? <Money paise={t.gross_margin_paise} /> : 'needs item costs'} />
              </div>

              {noData ? (
                <Card><Empty icon={Receipt} title="No sales recorded for these days" text="Bills made at the counter feed this dashboard within seconds." /></Card>
              ) : (
                <div className="grid gap-4 lg:grid-cols-2">
                  <Card title="Sales · last 7 days" padded={false}>
                    <div className="h-56 px-2 pt-4 sm:h-64" role="img" aria-label="Daily sales for the last 7 days">
                      <ResponsiveContainer width="100%" height="100%">
                        <LineChart data={series} margin={{ top: 8, right: 16, bottom: 0, left: 0 }}>
                          <CartesianGrid vertical={false} stroke={GRID} strokeDasharray="2 4" />
                          <XAxis dataKey="day" tickLine={false} axisLine={false} tick={{ fontSize: 12, fill: INK_MUTED }} />
                          <YAxis width={56} tickLine={false} axisLine={false} tick={{ fontSize: 11, fill: INK_MUTED }} tickFormatter={compactINR} />
                          <Tooltip cursor={{ stroke: GRID }} contentStyle={{ borderRadius: 12, border: '1px solid #e2e8f0', fontSize: 12 }}
                            formatter={(v) => [formatINR(Math.round(Number(v) * 100)), 'Sales']} labelFormatter={(_, p) => { const row = p?.[0]?.payload as { date?: string } | undefined; return row?.date ? fmtDate(row.date) : '' }} />
                          <Line type="monotone" dataKey="sales" stroke={NAVY} strokeWidth={2} dot={{ r: 3, strokeWidth: 2, fill: '#fff' }} activeDot={{ r: 5 }} isAnimationActive={false} />
                        </LineChart>
                      </ResponsiveContainer>
                    </div>
                  </Card>
                  <Card title="Bills · last 7 days" padded={false}>
                    <div className="h-56 px-2 pt-4 sm:h-64" role="img" aria-label="Bills per day for the last 7 days">
                      <ResponsiveContainer width="100%" height="100%">
                        <BarChart data={series} margin={{ top: 8, right: 16, bottom: 0, left: 0 }} barCategoryGap="35%">
                          <CartesianGrid vertical={false} stroke={GRID} strokeDasharray="2 4" />
                          <XAxis dataKey="day" tickLine={false} axisLine={false} tick={{ fontSize: 12, fill: INK_MUTED }} />
                          <YAxis width={36} allowDecimals={false} tickLine={false} axisLine={false} tick={{ fontSize: 11, fill: INK_MUTED }} />
                          <Tooltip cursor={{ fill: '#f1f5f9' }} contentStyle={{ borderRadius: 12, border: '1px solid #e2e8f0', fontSize: 12 }}
                            formatter={(v) => [String(v), 'Bills']} labelFormatter={(_, p) => { const row = p?.[0]?.payload as { date?: string; cancelled?: number } | undefined; return row?.date ? `${fmtDate(row.date)}${row.cancelled ? ` · ${row.cancelled} cancelled` : ''}` : '' }} />
                          <Bar dataKey="bills" fill={GREEN} radius={[4, 4, 0, 0]} isAnimationActive={false} />
                        </BarChart>
                      </ResponsiveContainer>
                    </div>
                  </Card>
                </div>
              )}

              <details className="card">
                <summary className="cursor-pointer px-4 py-3 text-sm font-bold">Last 7 days as a table</summary>
                <Table>
                  <thead><tr><th className="th">Day</th><th className="th text-right">Sales</th><th className="th text-right">Bills</th><th className="th text-right">Cancelled</th><th className="th text-right">Avg bill</th><th className="th text-right">GST</th><th className="th text-right">Margin</th></tr></thead>
                  <tbody>
                    {d.last_7_days.map((k) => (
                      <tr key={k.date} className="border-t border-slate-100">
                        <td className="td">{fmtDate(k.date)}</td>
                        <td className="td text-right"><Money paise={k.sales_paise} /></td>
                        <td className="td num text-right">{k.bills}</td>
                        <td className="td num text-right">{k.cancelled || '—'}</td>
                        <td className="td text-right"><Money paise={k.avg_bill_paise} /></td>
                        <td className="td text-right"><Money paise={k.tax_paise} /></td>
                        <td className="td num text-right">{k.margin_base_paise > 0 ? `${k.gross_margin_pct.toFixed(1)}%` : '—'}</td>
                      </tr>
                    ))}
                  </tbody>
                </Table>
              </details>

              <div className="grid gap-4 lg:grid-cols-2">
                <Card title={`Top items · ${fmtDate(d.date)}`} padded={false}>
                  {d.top_items.length === 0 ? <Empty icon={Receipt} title="No item sales" text="Top sellers of the day appear here." /> : (
                    <Table>
                      <thead><tr><th className="th">#</th><th className="th">Item</th><th className="th text-right">Qty</th><th className="th text-right">Sales</th></tr></thead>
                      <tbody>
                        {d.top_items.map((it, i) => (
                          <tr key={it.item_key} className="border-t border-slate-100">
                            <td className="td text-slate-400">{i + 1}</td>
                            <td className="td font-medium">{it.name || it.item_key}</td>
                            <td className="td num text-right">{it.qty ? fmtQty(it.qty) : '—'}</td>
                            <td className="td text-right font-semibold"><Money paise={it.sales_paise} /></td>
                          </tr>
                        ))}
                      </tbody>
                    </Table>
                  )}
                </Card>
                <Card title={`Payment mix · ${fmtDate(d.date)}`}>
                  {mix.length === 0 ? <Empty icon={IndianRupee} title="No payments" text="Cash, UPI and card collections of the day." /> : (
                    <ul className="space-y-3">
                      {mix.map(([mode, paise]) => {
                        const pct = mixTotal > 0 ? Math.round((100 * paise) / mixTotal) : 0
                        return (
                          <li key={mode}>
                            <div className="mb-1 flex items-center justify-between text-sm"><span className="font-medium">{TENDER_LABEL[mode] ?? mode}</span><span className="flex items-center gap-2"><Badge tone="gray">{pct}%</Badge><Money paise={paise} className="font-semibold" /></span></div>
                            <div className="h-2 w-full overflow-hidden rounded-full bg-slate-100"><div className="h-full rounded-full bg-brand-500" style={{ width: `${pct}%` }} /></div>
                          </li>
                        )
                      })}
                    </ul>
                  )}
                </Card>
              </div>
            </>
          )
        }}
      </QueryState>
    </div>
  )
}
