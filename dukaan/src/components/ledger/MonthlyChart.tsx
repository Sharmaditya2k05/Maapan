'use client'

/**
 * Sales vs purchases per month of the FY. Palette validated for CVD (navy #1e4fa0 / green #0ea673);
 * thin bars with rounded tops, hairline grid, tooltip on every bar, legend, and a table view for screen readers / print.
 */
import { Bar, BarChart, CartesianGrid, Legend, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import { formatINR } from '@/lib/money'
import { Money } from '@/components/ui'

export interface MonthPoint { label: string; sales_paise: number; purchases_paise: number }

const SALES = '#1e4fa0'
const PURCHASES = '#0ea673'

/** ₹ in compact Indian units for axis ticks. */
export function compactINR(paise: number): string {
  const r = paise / 100
  const a = Math.abs(r)
  const s = a >= 1e7 ? `${(r / 1e7).toFixed(a >= 1e8 ? 0 : 1)}Cr` : a >= 1e5 ? `${(r / 1e5).toFixed(a >= 1e6 ? 0 : 1)}L` : a >= 1e3 ? `${(r / 1e3).toFixed(0)}K` : r.toFixed(0)
  return `₹${s}`
}

export function MonthlyChart({ data, loading }: { data: MonthPoint[]; loading?: boolean }) {
  const rows = data.map((d) => ({ label: d.label, Sales: d.sales_paise, Purchases: d.purchases_paise }))
  return (
    <div>
      <div className="h-64 sm:h-72" aria-busy={loading}>
        <ResponsiveContainer>
          <BarChart data={rows} margin={{ top: 8, right: 8, left: 0, bottom: 0 }} barCategoryGap="30%" barGap={2}>
            <CartesianGrid stroke="#eef2f6" vertical={false} />
            <XAxis dataKey="label" tickLine={false} axisLine={false} tick={{ fontSize: 11, fill: '#64748b' }} interval={0} />
            <YAxis tickLine={false} axisLine={false} width={56} tick={{ fontSize: 11, fill: '#94a3b8' }} tickFormatter={(v: number) => compactINR(v)} />
            <Tooltip cursor={{ fill: '#f1f5f9' }} contentStyle={{ borderRadius: 12, fontSize: 12, border: '1px solid #e2e8f0' }}
              formatter={(v) => formatINR(Number(v))} />
            <Legend iconType="square" iconSize={10} wrapperStyle={{ fontSize: 12 }} />
            <Bar dataKey="Sales" fill={SALES} radius={[4, 4, 0, 0]} maxBarSize={24} isAnimationActive={false} />
            <Bar dataKey="Purchases" fill={PURCHASES} radius={[4, 4, 0, 0]} maxBarSize={24} isAnimationActive={false} />
          </BarChart>
        </ResponsiveContainer>
      </div>
      <details className="mt-2 text-xs">
        <summary className="cursor-pointer text-slate-500 hover:text-ink">Show as table</summary>
        <table className="mt-2 w-full">
          <thead><tr><th className="th">Month</th><th className="th text-right">Sales</th><th className="th text-right">Purchases</th></tr></thead>
          <tbody className="divide-y divide-slate-100">
            {data.map((d) => <tr key={d.label}><td className="td">{d.label}</td><td className="td num text-right"><Money paise={d.sales_paise} /></td><td className="td num text-right"><Money paise={d.purchases_paise} /></td></tr>)}
          </tbody>
        </table>
      </details>
    </div>
  )
}
