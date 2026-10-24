'use client'

import { Suspense, useState } from 'react'
import Link from 'next/link'
import { BookOpen } from 'lucide-react'
import { Badge, Card, Empty, Money, QueryState, Skeleton, statusTone } from '@/components/ui'
import { downloadFile, fmtDate, todayIST, useGet, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import type { DayBook } from '@/lib/types.ledger'
import { csvRupees, toCSV } from '@/lib/ledger/csv'
import { presetRange, type DateRange } from '@/lib/ledger/util'
import { ReportShell } from '@/components/ledger/ReportShell'
import { VOUCHER_TYPE_LABEL, VoucherModal } from '@/components/ledger/VoucherModal'
import { NoPermission, useQueryParam } from '@/components/ledger/bits'

export default function DayBookPage() {
  return <Suspense fallback={<Skeleton />}><DayBookInner /></Suspense>
}

function DayBookInner() {
  const canDo = usePerm()
  const [range, setRange] = useState<DateRange>(() => presetRange('today', todayIST()))
  const [voucher, setVoucher] = useQueryParam('voucher')
  const q = useGet<DayBook>(canDo(PERMS.reportRead) ? '/bahi/v1/daybook' : null, { from: range.from, to: range.to })
  const onCSV = () => {
    const rows: (string | number)[][] = []
    for (const v of q.data?.vouchers ?? []) for (const e of v.entries ?? []) rows.push([v.date, v.number, v.type, v.narration, e.account_code, e.account_name, csvRupees(e.debit_paise, true), csvRupees(e.credit_paise, true)])
    downloadFile(`day-book-${range.from}-${range.to}.csv`, toCSV(['Date', 'Voucher', 'Type', 'Narration', 'Code', 'Account', 'Debit', 'Credit'], rows), 'text/csv')
  }
  if (!canDo(PERMS.reportRead)) return <Card><NoPermission what="reports" /></Card>
  return (
    <ReportShell title="Day book" range={{ value: range, onChange: setRange, presets: ['today', 'yesterday', 'this_week', 'this_month', 'custom'] }} onCSV={q.data ? onCSV : undefined}>
      <QueryState isLoading={q.isLoading} error={q.error} data={q.data} retry={() => q.refetch()}>
        {(d) => d.vouchers.length === 0 ? <Card><Empty icon={BookOpen} title="Nothing posted in this period" text="Vouchers appear here the moment a bill, payment or stock movement is booked." /></Card> : (
          <div className="space-y-3">
            <p className="text-xs text-slate-500">{d.count} voucher{d.count === '1' ? '' : 's'} · total <Money paise={d.vouchers.reduce((a, v) => a + v.total_paise, 0)} className="font-semibold" /></p>
            {d.vouchers.map((v) => (
              <Card key={v.id} padded={false} className="break-inside-avoid">
                <div className="flex flex-wrap items-center justify-between gap-2 border-b border-slate-100 px-4 py-2.5">
                  <div className="flex flex-wrap items-center gap-2 text-sm">
                    <button type="button" onClick={() => setVoucher(v.id)} className="font-mono font-semibold text-brand-700 hover:underline cursor-pointer">{v.number}</button>
                    <Badge tone="gray">{VOUCHER_TYPE_LABEL[v.type] ?? v.type}</Badge>
                    {v.status !== 'posted' && <Badge tone={statusTone(v.status)}>{v.status}</Badge>}
                    <span className="text-slate-500">{fmtDate(v.date)}</span>
                    {v.narration && <span className="text-slate-600">· {v.narration}</span>}
                  </div>
                  <div className="text-xs text-slate-500">{v.source}{v.source_ref ? ` · ${v.source_ref}` : ''}</div>
                </div>
                <table className="w-full">
                  <tbody className="divide-y divide-slate-50">
                    {(v.entries ?? []).map((e) => (
                      <tr key={e.line_no} className="text-sm">
                        <td className="w-14 px-4 py-1.5 font-mono text-[11px] text-slate-400">{e.account_code}</td>
                        <td className="px-2 py-1.5">{e.party_id ? <Link href={`/app/parties/${e.party_id}`} className="hover:underline">{e.account_name}</Link> : e.account_name}</td>
                        <td className="num w-32 px-4 py-1.5 text-right">{e.debit_paise ? <Money paise={e.debit_paise} /> : ''}</td>
                        <td className="num w-32 px-4 py-1.5 text-right text-emerald-700">{e.credit_paise ? <Money paise={e.credit_paise} /> : ''}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </Card>
            ))}
          </div>
        )}
      </QueryState>
      <VoucherModal voucherId={voucher} onClose={() => setVoucher(null)} onOpen={setVoucher} />
    </ReportShell>
  )
}
