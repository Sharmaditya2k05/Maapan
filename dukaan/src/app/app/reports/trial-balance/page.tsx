'use client'

import { useState } from 'react'
import Link from 'next/link'
import { Scale } from 'lucide-react'
import { Badge, Card, Empty, Money, QueryState } from '@/components/ui'
import { downloadFile, todayIST, useGet, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import type { TrialBalance } from '@/lib/types.ledger'
import { csvRupees, toCSV } from '@/lib/ledger/csv'
import { GROUP_LABEL } from '@/components/ledger/AccountPicker'
import { ReportShell } from '@/components/ledger/ReportShell'
import { NoPermission } from '@/components/ledger/bits'

export default function TrialBalancePage() {
  const canDo = usePerm()
  const [asOf, setAsOf] = useState(todayIST())
  const q = useGet<TrialBalance>(canDo(PERMS.reportRead) ? '/bahi/v1/reports/trial-balance' : null, { as_of: asOf })
  const onCSV = () => q.data && downloadFile(`trial-balance-${asOf}.csv`, toCSV(['Code', 'Account', 'Group', 'Debit', 'Credit'],
    [...q.data.rows.map((r) => [r.code, r.name, r.group, csvRupees(r.debit_paise, true), csvRupees(r.credit_paise, true)]), ['', 'Total', '', csvRupees(q.data.total_debit_paise), csvRupees(q.data.total_credit_paise)]]), 'text/csv')
  if (!canDo(PERMS.reportRead)) return <Card><NoPermission what="reports" /></Card>
  return (
    <ReportShell title="Trial balance" asOf={{ value: asOf, onChange: setAsOf }} onCSV={q.data ? onCSV : undefined}
      actions={q.data && <Badge tone={q.data.balanced ? 'green' : 'red'}>{q.data.balanced ? 'Balanced' : 'Not balanced'}</Badge>}>
      <Card padded={false}>
        <QueryState isLoading={q.isLoading} error={q.error} data={q.data} retry={() => q.refetch()}>
          {(d) => d.rows.length === 0 ? <Empty icon={Scale} title="No balances yet" text="Post the first bill or opening balance and the trial balance fills in." /> : (
            <div className="overflow-x-auto">
              <table className="w-full min-w-[560px]">
                <thead className="bg-slate-50/60 print:bg-transparent"><tr><th className="th">Code</th><th className="th">Account</th><th className="th">Group</th><th className="th text-right">Debit</th><th className="th text-right">Credit</th></tr></thead>
                <tbody className="divide-y divide-slate-100">
                  {d.rows.map((r, i) => (
                    <tr key={r.account_id} className={`hover:bg-slate-50/60 ${i > 0 && d.rows[i - 1].group !== r.group ? 'border-t-2 border-slate-200' : ''}`}>
                      <td className="td font-mono text-xs text-slate-500">{r.code}</td>
                      <td className="td"><Link href={`/app/reports/accounts?account=${r.account_id}`} className="font-medium hover:underline">{r.name}</Link></td>
                      <td className="td text-xs text-slate-500">{GROUP_LABEL[r.group] ?? r.group}</td>
                      <td className="td num text-right">{r.debit_paise ? <Money paise={r.debit_paise} /> : ''}</td>
                      <td className="td num text-right">{r.credit_paise ? <Money paise={r.credit_paise} /> : ''}</td>
                    </tr>
                  ))}
                </tbody>
                <tfoot className="border-t-2 border-slate-200 bg-slate-50 font-bold print:bg-transparent">
                  <tr><td className="td" colSpan={3}>Total{!d.balanced && <span className="ml-2 text-xs font-semibold text-rose-600">difference <Money paise={d.total_debit_paise - d.total_credit_paise} /></span>}</td><td className="td num text-right"><Money paise={d.total_debit_paise} /></td><td className="td num text-right"><Money paise={d.total_credit_paise} /></td></tr>
                </tfoot>
              </table>
            </div>
          )}
        </QueryState>
      </Card>
    </ReportShell>
  )
}
