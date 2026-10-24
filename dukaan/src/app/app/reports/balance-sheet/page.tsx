'use client'

import { useState } from 'react'
import { Landmark } from 'lucide-react'
import { Badge, Card, Empty, Money, QueryState } from '@/components/ui'
import { downloadFile, todayIST, useGet, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import type { BSLine, BalanceSheet } from '@/lib/types.ledger'
import { csvRupees, toCSV } from '@/lib/ledger/csv'
import { ReportShell } from '@/components/ledger/ReportShell'
import { NoPermission } from '@/components/ledger/bits'

export default function BalanceSheetPage() {
  const canDo = usePerm()
  const [asOf, setAsOf] = useState(todayIST())
  const q = useGet<BalanceSheet>(canDo(PERMS.reportRead) ? '/bahi/v1/reports/balance-sheet' : null, { as_of: asOf })
  const onCSV = () => q.data && downloadFile(`balance-sheet-${asOf}.csv`, toCSV(['Section', 'Code', 'Account', 'Amount'], [
    ...q.data.assets.map((l) => ['Assets', l.code, l.name, csvRupees(l.amount_paise)]),
    ...q.data.liabilities.map((l) => ['Liabilities', l.code, l.name, csvRupees(l.amount_paise)]),
    ...q.data.equity.map((l) => ['Equity', l.code, l.name, csvRupees(l.amount_paise)]),
  ]), 'text/csv')
  if (!canDo(PERMS.reportRead)) return <Card><NoPermission what="reports" /></Card>
  return (
    <ReportShell title="Balance sheet" asOf={{ value: asOf, onChange: setAsOf }} onCSV={q.data ? onCSV : undefined}
      actions={q.data && <Badge tone={q.data.balanced ? 'green' : 'red'}>{q.data.balanced ? 'Balanced' : 'Not balanced'}</Badge>}>
      <QueryState isLoading={q.isLoading} error={q.error} data={q.data} retry={() => q.refetch()}>
        {(d) => d.assets.length + d.liabilities.length === 0 && d.equity.every((l) => l.amount_paise === 0) ? <Card><Empty icon={Landmark} title="Nothing on the books yet" /></Card> : (
          <div className="grid gap-4 md:grid-cols-2 print:grid-cols-2">
            <Side title="Assets" sections={[{ title: 'Assets', lines: d.assets }]} total={d.total_assets_paise} />
            <Side title="Liabilities & equity" sections={[{ title: 'Liabilities', lines: d.liabilities }, { title: 'Equity', lines: d.equity }]} total={d.total_liabilities_paise + d.total_equity_paise} />
          </div>
        )}
      </QueryState>
    </ReportShell>
  )
}

function Side({ title, sections, total }: { title: string; sections: { title: string; lines: BSLine[] }[]; total: number }) {
  return (
    <Card padded={false} className="break-inside-avoid">
      <div className="border-b border-slate-100 px-4 py-3 text-sm font-bold">{title}</div>
      {sections.map((s) => (
        <div key={s.title}>
          {sections.length > 1 && <div className="bg-slate-50/70 px-4 py-1.5 text-[11px] font-semibold uppercase tracking-wider text-slate-500">{s.title}</div>}
          {s.lines.length === 0 ? <p className="px-4 py-2 text-xs text-slate-400">None</p> : (
            <table className="w-full"><tbody className="divide-y divide-slate-50">
              {s.lines.map((l, i) => (
                <tr key={`${l.code}-${i}`}><td className="w-14 px-4 py-1.5 font-mono text-[11px] text-slate-400">{l.code}</td><td className="px-2 py-1.5 text-sm">{l.name}</td><td className="num px-4 py-1.5 text-right text-sm"><Money paise={l.amount_paise} /></td></tr>
              ))}
            </tbody></table>
          )}
        </div>
      ))}
      <div className="flex items-center justify-between border-t-2 border-slate-200 bg-slate-50 px-4 py-3 font-bold print:bg-transparent"><span>Total {title.toLowerCase()}</span><Money paise={total} /></div>
    </Card>
  )
}
