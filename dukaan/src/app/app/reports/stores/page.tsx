'use client'

import { useState } from 'react'
import Link from 'next/link'
import { Store as StoreIcon } from 'lucide-react'
import { Card, Empty, Money, QueryState } from '@/components/ui'
import { api } from '@/lib/api'
import { downloadFile, todayIST, useGet, usePerm, useStore } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import type { ListResponse, Store } from '@/lib/types'
import type { StorePLReport } from '@/lib/types.phase4'
import { presetRange, type DateRange } from '@/lib/ledger/util'
import { ReportShell } from '@/components/ledger/ReportShell'
import { NoPermission } from '@/components/ledger/bits'
import { useAction } from '@/components/ledger/useAction'

/** bahi GET /v1/reports/profit-loss/stores — P&L per store (entries carry the store they were posted for). */
export default function StorePLPage() {
  const canDo = usePerm()
  const { stores: mine } = useStore()
  const all = useGet<ListResponse<Store>>('/pehchaan/v1/stores')
  const [range, setRange] = useState<DateRange>(() => presetRange('this_fy', todayIST()))
  const q = useGet<StorePLReport>(canDo(PERMS.reportRead) ? '/bahi/v1/reports/profit-loss/stores' : null, { from: range.from, to: range.to })
  const csv = useAction<Blob>(() => api<Blob>('/bahi/v1/reports/profit-loss/stores', { method: 'GET', raw: true, query: { from: range.from, to: range.to, format: 'csv' } }), {
    onSuccess: (b) => downloadFile(`store-pl-${range.from}-${range.to}.csv`, b),
  })
  if (!canDo(PERMS.reportRead)) return <Card><NoPermission what="reports" /></Card>
  const name = (id: string) => (id ? (all.data?.items.find((s) => s.id === id)?.name ?? mine.find((s) => s.id === id)?.name ?? `Store ${id.slice(0, 8)}`) : 'Not tagged to a store')

  return (
    <ReportShell title="Store-wise profit & loss" range={{ value: range, onChange: setRange, presets: ['this_month', 'last_month', 'this_quarter', 'this_fy', 'last_fy', 'custom'] }} onCSV={() => csv.mutate()}>
      <Card padded={false}>
        <QueryState isLoading={q.isLoading} error={q.error} data={q.data} retry={() => q.refetch()}>
          {(d) => d.stores.length === 0 ? <Empty icon={StoreIcon} title="Nothing in this period" /> : (
            <div className="overflow-x-auto">
              <table className="w-full min-w-[640px]">
                <thead className="bg-slate-50/60"><tr><th className="th">Store</th><th className="th text-right">Income</th><th className="th text-right">Expenses</th><th className="th text-right">Gross profit</th><th className="th text-right">Net profit</th><th className="th text-right">Net margin</th></tr></thead>
                <tbody className="divide-y divide-slate-100">
                  {d.stores.map((s) => (
                    <tr key={s.store_id || 'none'} className="hover:bg-slate-50/60">
                      <td className="td font-semibold">{name(s.store_id)}{!s.store_id && <div className="text-[11px] font-normal text-slate-400">manual journals and other untagged entries</div>}</td>
                      <td className="td num text-right"><Money paise={s.income_paise} /></td>
                      <td className="td num text-right text-slate-600"><Money paise={s.expenses_paise} /></td>
                      <td className="td num text-right"><Money paise={s.gross_profit_paise} signed /></td>
                      <td className={`td num text-right font-bold ${s.net_profit_paise < 0 ? 'text-rose-700' : 'text-emerald-700'}`}><Money paise={s.net_profit_paise} /></td>
                      <td className="td num text-right text-slate-500">{s.income_paise ? `${((s.net_profit_paise / s.income_paise) * 100).toFixed(1)}%` : '—'}</td>
                    </tr>
                  ))}
                </tbody>
                <tfoot className="border-t-2 border-slate-200 bg-slate-50 font-bold">
                  <tr><td className="td">Total</td>
                    {(['income_paise', 'expenses_paise', 'gross_profit_paise', 'net_profit_paise'] as const).map((k) => <td key={k} className="td num text-right"><Money paise={d.stores.reduce((a, s) => a + s[k], 0)} /></td>)}<td />
                  </tr>
                </tfoot>
              </table>
              <p className="px-4 py-3 text-xs text-slate-500">Detail for one store: <Link href="/app/reports/profit-loss" className="link">Profit &amp; loss</Link> with the store filter.</p>
            </div>
          )}
        </QueryState>
      </Card>
    </ReportShell>
  )
}
