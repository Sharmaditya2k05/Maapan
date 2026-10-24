'use client'

import { useState } from 'react'
import { TrendingUp } from 'lucide-react'
import { Card, Empty, Money, QueryState } from '@/components/ui'
import { downloadFile, todayIST, useGet, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import { formatINR } from '@/lib/money'
import type { PLLine, ProfitLoss } from '@/lib/types.ledger'
import { csvRupees, toCSV } from '@/lib/ledger/csv'
import { presetRange, type DateRange } from '@/lib/ledger/util'
import { ReportShell } from '@/components/ledger/ReportShell'
import { NoPermission, SectionTitle } from '@/components/ledger/bits'

/** Lines bahi counts towards gross profit (sales, returns, purchases and the Direct Expenses group: COGS 5150, stock loss 5160, 52xx). */
const isTrading = (l: PLLine) => ['4100', '4110', '5100', '5110', '5150', '5160'].includes(l.code) || l.code.startsWith('52')
const LABEL: Record<string, string> = { '5150': 'Cost of goods sold', '5160': 'Stock loss / write-off' }

export default function ProfitLossPage() {
  const canDo = usePerm()
  const [range, setRange] = useState<DateRange>(() => presetRange('this_fy', todayIST()))
  const q = useGet<ProfitLoss>(canDo(PERMS.reportRead) ? '/bahi/v1/reports/profit-loss' : null, { from: range.from, to: range.to })
  const onCSV = () => q.data && downloadFile(`profit-loss-${range.from}-${range.to}.csv`, toCSV(['Section', 'Code', 'Account', 'Amount'], [
    ...q.data.income.map((l) => ['Income', l.code, l.name, csvRupees(l.amount_paise)]),
    ...q.data.expenses.map((l) => ['Expense', l.code, l.name, csvRupees(l.amount_paise)]),
    ['', '', 'Gross profit', csvRupees(q.data.gross_profit_paise)], ['', '', 'Net profit', csvRupees(q.data.net_profit_paise)],
  ]), 'text/csv')
  if (!canDo(PERMS.reportRead)) return <Card><NoPermission what="reports" /></Card>

  return (
    <ReportShell title="Profit & loss" range={{ value: range, onChange: setRange, presets: ['this_month', 'last_month', 'this_quarter', 'this_fy', 'last_fy', 'custom'] }} onCSV={q.data ? onCSV : undefined}>
      <QueryState isLoading={q.isLoading} error={q.error} data={q.data} retry={() => q.refetch()}>
        {(d) => {
          const tradingIncome = d.income.filter(isTrading)
          const tradingExp = d.expenses.filter(isTrading)
          const otherIncome = d.income.filter((l) => !isTrading(l))
          const otherExp = d.expenses.filter((l) => !isTrading(l))
          const sales = tradingIncome.reduce((a, l) => a + l.amount_paise, 0)
          const cogs = tradingExp.find((l) => l.code === '5150')
          const loss = tradingExp.find((l) => l.code === '5160')
          const margin = sales ? (d.gross_profit_paise / sales) * 100 : null
          if (d.income.length === 0 && d.expenses.length === 0) return <Card><Empty icon={TrendingUp} title="Nothing in this period" text="Sales, purchases and expenses posted in the period show up here." /></Card>
          return (
            <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(0,1fr)] print:block">
              <Card padded={false} className="break-inside-avoid">
                <SectionTitle><span className="px-4 pt-3">Trading account</span></SectionTitle>
                <Lines lines={tradingIncome} sign={1} empty="No sales in the period" />
                <Lines lines={tradingExp} sign={-1} empty="No direct costs in the period" highlight={['5150', '5160']} />
                <div className={`mx-4 mb-4 mt-2 flex items-center justify-between rounded-xl px-4 py-3 font-bold ${d.gross_profit_paise >= 0 ? 'bg-emerald-50 text-emerald-800' : 'bg-rose-50 text-rose-800'}`}>
                  <span>Gross profit{margin !== null && <span className="ml-2 text-xs font-medium opacity-80">{margin.toFixed(1)}% of sales</span>}</span><Money paise={d.gross_profit_paise} />
                </div>
                {(cogs || loss) && (
                  <p className="px-4 pb-4 text-xs text-slate-500">
                    Perpetual inventory: {cogs && <>cost of goods sold {formatINR(cogs.amount_paise)}</>}{cogs && loss && ' · '}{loss && <>stock loss {formatINR(loss.amount_paise)}</>}. Purchases of stock-tracked items reach the P&amp;L only when sold.
                  </p>
                )}
              </Card>
              <Card padded={false} className="break-inside-avoid">
                <SectionTitle><span className="px-4 pt-3">Other income &amp; expenses</span></SectionTitle>
                <Lines lines={otherIncome} sign={1} empty="No other income" />
                <Lines lines={otherExp} sign={-1} empty="No indirect expenses" />
                <div className="mx-4 mb-2 mt-2 space-y-1 text-sm">
                  <div className="flex justify-between text-slate-600"><span>Total income</span><Money paise={d.total_income_paise} /></div>
                  <div className="flex justify-between text-slate-600"><span>Total expenses</span><Money paise={d.total_expenses_paise} /></div>
                </div>
                <div className={`mx-4 mb-4 flex items-center justify-between rounded-xl px-4 py-3 text-lg font-extrabold ${d.net_profit_paise >= 0 ? 'bg-brand-950 text-white' : 'bg-rose-600 text-white'}`}>
                  <span>Net {d.net_profit_paise >= 0 ? 'profit' : 'loss'}</span><Money paise={Math.abs(d.net_profit_paise)} />
                </div>
              </Card>
            </div>
          )
        }}
      </QueryState>
    </ReportShell>
  )
}

function Lines({ lines, sign, empty, highlight = [] }: { lines: PLLine[]; sign: 1 | -1; empty: string; highlight?: string[] }) {
  if (lines.length === 0) return <p className="px-4 py-2 text-xs text-slate-400">{empty}</p>
  return (
    <table className="w-full">
      <tbody className="divide-y divide-slate-50">
        {lines.map((l) => (
          <tr key={l.code} className={highlight.includes(l.code) ? 'bg-amber-50/60' : ''}>
            <td className="w-14 px-4 py-1.5 font-mono text-[11px] text-slate-400">{l.code}</td>
            <td className="px-2 py-1.5 text-sm">{LABEL[l.code] ?? l.name}{sign < 0 && <span className="ml-1 text-xs text-slate-400">(less)</span>}</td>
            <td className={`num px-4 py-1.5 text-right text-sm ${sign < 0 ? 'text-rose-700' : ''}`}>{sign < 0 ? '−' : ''}<Money paise={l.amount_paise} /></td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}
