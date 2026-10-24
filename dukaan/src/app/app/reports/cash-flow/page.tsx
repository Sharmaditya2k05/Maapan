'use client'

import { useState } from 'react'
import { Wallet } from 'lucide-react'
import { Card, Empty, Money, QueryState } from '@/components/ui'
import { downloadFile, todayIST, useGet, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import type { CashFlow } from '@/lib/types.ledger'
import { csvRupees, toCSV } from '@/lib/ledger/csv'
import { presetRange, type DateRange } from '@/lib/ledger/util'
import { ReportShell } from '@/components/ledger/ReportShell'
import { VOUCHER_TYPE_LABEL } from '@/components/ledger/VoucherModal'
import { NoPermission } from '@/components/ledger/bits'

export default function CashFlowPage() {
  const canDo = usePerm()
  const [range, setRange] = useState<DateRange>(() => presetRange('this_month', todayIST()))
  const q = useGet<CashFlow>(canDo(PERMS.reportRead) ? '/bahi/v1/reports/cash-flow' : null, { from: range.from, to: range.to })
  const onCSV = () => q.data && downloadFile(`cash-flow-${range.from}-${range.to}.csv`, toCSV(['Voucher type', 'In', 'Out'],
    [...q.data.lines.map((l) => [l.voucher_type, csvRupees(l.in_paise), csvRupees(l.out_paise)]), ['Opening', csvRupees(q.data.opening_paise), ''], ['Closing', csvRupees(q.data.closing_paise), '']]), 'text/csv')
  if (!canDo(PERMS.reportRead)) return <Card><NoPermission what="reports" /></Card>
  return (
    <ReportShell title="Cash flow" subtitle="Cash + bank together; contra transfers between them are excluded" range={{ value: range, onChange: setRange }} onCSV={q.data ? onCSV : undefined}>
      <Card padded={false}>
        <QueryState isLoading={q.isLoading} error={q.error} data={q.data} retry={() => q.refetch()}>
          {(d) => (
            <div className="overflow-x-auto">
              <table className="w-full min-w-[480px]">
                <thead className="bg-slate-50/60 print:bg-transparent"><tr><th className="th">Voucher type</th><th className="th text-right">Money in</th><th className="th text-right">Money out</th><th className="th text-right">Net</th></tr></thead>
                <tbody className="divide-y divide-slate-100">
                  <tr className="bg-slate-50/40"><td className="td font-medium text-slate-500">Opening cash + bank</td><td className="td" /><td className="td" /><td className="td num text-right font-semibold"><Money paise={d.opening_paise} /></td></tr>
                  {d.lines.length === 0 && <tr><td colSpan={4}><Empty icon={Wallet} title="No cash or bank movement in this period" /></td></tr>}
                  {d.lines.map((l) => (
                    <tr key={l.voucher_type} className="hover:bg-slate-50/60">
                      <td className="td font-medium">{VOUCHER_TYPE_LABEL[l.voucher_type] ?? l.voucher_type}</td>
                      <td className="td num text-right text-emerald-700">{l.in_paise ? <Money paise={l.in_paise} /> : ''}</td>
                      <td className="td num text-right text-rose-700">{l.out_paise ? <Money paise={l.out_paise} /> : ''}</td>
                      <td className="td num text-right"><Money paise={l.in_paise - l.out_paise} signed /></td>
                    </tr>
                  ))}
                </tbody>
                <tfoot className="border-t-2 border-slate-200 bg-slate-50 font-bold print:bg-transparent">
                  <tr><td className="td">Period total</td><td className="td num text-right text-emerald-700"><Money paise={d.total_in_paise} /></td><td className="td num text-right text-rose-700"><Money paise={d.total_out_paise} /></td><td className="td num text-right"><Money paise={d.net_paise} signed /></td></tr>
                  <tr><td className="td" colSpan={3}>Closing cash + bank</td><td className="td num text-right text-lg"><Money paise={d.closing_paise} /></td></tr>
                </tfoot>
              </table>
            </div>
          )}
        </QueryState>
      </Card>
    </ReportShell>
  )
}
