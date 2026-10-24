'use client'

import { Suspense, useState } from 'react'
import Link from 'next/link'
import { Hourglass, MessageCircle } from 'lucide-react'
import { Card, Empty, Money, QueryState, Skeleton, Tabs } from '@/components/ui'
import { downloadFile, fmtDate, todayIST, useGet, usePerm, useStore } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import { formatINR } from '@/lib/money'
import type { AgingReport } from '@/lib/types.ledger'
import { csvRupees, toCSV } from '@/lib/ledger/csv'
import { waLink } from '@/lib/ledger/util'
import { ReportShell } from '@/components/ledger/ReportShell'
import { NoPermission, useTabParam } from '@/components/ledger/bits'

const KINDS = ['receivables', 'payables'] as const
type Kind = (typeof KINDS)[number]

export default function AgingPage() {
  return <Suspense fallback={<Skeleton />}><AgingInner /></Suspense>
}

function AgingInner() {
  const canDo = usePerm()
  const { store } = useStore()
  const [kind, setKind] = useTabParam<Kind>(KINDS, 'receivables', 'kind')
  const [asOf, setAsOf] = useState(todayIST())
  const q = useGet<AgingReport>(canDo(PERMS.reportRead) ? '/bahi/v1/reports/aging' : null, { kind, as_of: asOf })
  const onCSV = () => q.data && downloadFile(`${kind}-aging-${asOf}.csv`, toCSV(['Party', 'Phone', '0-30', '31-60', '61-90', '90+', 'Total', 'Advance', 'Oldest open'],
    [...q.data.rows.map((r) => [r.party_name, r.phone ?? '', csvRupees(r.d0_30_paise), csvRupees(r.d31_60_paise), csvRupees(r.d61_90_paise), csvRupees(r.d90_plus_paise), csvRupees(r.total_paise), csvRupees(r.advance_paise), r.oldest_open_date ?? '']),
      ['Total', '', csvRupees(q.data.totals.d0_30_paise), csvRupees(q.data.totals.d31_60_paise), csvRupees(q.data.totals.d61_90_paise), csvRupees(q.data.totals.d90_plus_paise), csvRupees(q.data.totals.total_paise), csvRupees(q.data.totals.advance_paise), '']]), 'text/csv')
  if (!canDo(PERMS.reportRead)) return <Card><NoPermission what="reports" /></Card>
  const receivable = kind === 'receivables'

  return (
    <ReportShell title={receivable ? 'Receivables aging' : 'Payables aging'} asOf={{ value: asOf, onChange: setAsOf }} onCSV={q.data ? onCSV : undefined}
      filters={<Tabs value={kind} onChange={setKind} options={[{ value: 'receivables', label: 'To collect' }, { value: 'payables', label: 'To pay' }]} />}>
      <Card padded={false}>
        <QueryState isLoading={q.isLoading} error={q.error} data={q.data} retry={() => q.refetch()}>
          {(d) => d.rows.length === 0 ? <Empty icon={Hourglass} title={receivable ? 'Nothing outstanding from customers' : 'Nothing outstanding to suppliers'} text="Open balances are settled FIFO against the oldest bills." /> : (
            <>
              <div className="grid grid-cols-2 gap-px bg-slate-100 sm:grid-cols-4 print:hidden">
                {([['0–30 days', d.totals.d0_30_paise, 'text-emerald-700'], ['31–60 days', d.totals.d31_60_paise, 'text-amber-700'], ['61–90 days', d.totals.d61_90_paise, 'text-orange-700'], ['Over 90 days', d.totals.d90_plus_paise, 'text-rose-700']] as const).map(([l, v, c]) => (
                  <div key={l} className="bg-white px-4 py-3"><div className="text-xs text-slate-500">{l}</div><div className={`num text-lg font-bold ${c}`}>{formatINR(v)}</div></div>
                ))}
              </div>
              <div className="overflow-x-auto">
                <table className="w-full min-w-[720px]">
                  <thead className="bg-slate-50/60 print:bg-transparent"><tr><th className="th">Party</th><th className="th text-right">0–30</th><th className="th text-right">31–60</th><th className="th text-right">61–90</th><th className="th text-right">90+</th><th className="th text-right">Total</th><th className="th text-right">Advance</th><th className="th print:hidden" /></tr></thead>
                  <tbody className="divide-y divide-slate-100">
                    {d.rows.map((r) => {
                      const wa = receivable && r.phone ? waLink(r.phone, `Namaste ${r.party_name}, a gentle reminder from ${store?.name ?? 'us'}: ${formatINR(r.total_paise)} is outstanding as of ${fmtDate(asOf)}${r.oldest_open_date ? ` (oldest bill ${fmtDate(r.oldest_open_date)})` : ''}. Thank you.`) : null
                      return (
                        <tr key={r.party_id} className="hover:bg-slate-50/60">
                          <td className="td"><Link href={`/app/parties/${r.party_id}`} className="font-semibold hover:underline">{r.party_name}</Link>{r.phone && <div className="text-xs text-slate-500">{r.phone}</div>}{r.oldest_open_date && <div className="text-[11px] text-slate-400">oldest {fmtDate(r.oldest_open_date)}</div>}</td>
                          <Bucket v={r.d0_30_paise} /><Bucket v={r.d31_60_paise} tone="text-amber-700" /><Bucket v={r.d61_90_paise} tone="text-orange-700" /><Bucket v={r.d90_plus_paise} tone="text-rose-700 font-semibold" />
                          <td className="td num text-right font-bold"><Money paise={r.total_paise} /></td>
                          <td className="td num text-right text-slate-500">{r.advance_paise ? <Money paise={r.advance_paise} /> : ''}</td>
                          <td className="td text-right print:hidden">{wa && <a href={wa} target="_blank" rel="noopener noreferrer" aria-label={`Remind ${r.party_name} on WhatsApp`} title="Remind on WhatsApp" className="inline-grid h-9 w-9 place-items-center rounded-xl text-slate-500 transition hover:bg-slate-100 hover:text-ink"><MessageCircle size={18} /></a>}</td>
                        </tr>
                      )
                    })}
                  </tbody>
                  <tfoot className="border-t-2 border-slate-200 bg-slate-50 font-bold print:bg-transparent">
                    <tr><td className="td">Total · {d.rows.length} parties</td><Bucket v={d.totals.d0_30_paise} /><Bucket v={d.totals.d31_60_paise} /><Bucket v={d.totals.d61_90_paise} /><Bucket v={d.totals.d90_plus_paise} /><td className="td num text-right"><Money paise={d.totals.total_paise} /></td><td className="td num text-right">{d.totals.advance_paise ? <Money paise={d.totals.advance_paise} /> : ''}</td><td className="print:hidden" /></tr>
                  </tfoot>
                </table>
              </div>
            </>
          )}
        </QueryState>
      </Card>
    </ReportShell>
  )
}

function Bucket({ v, tone = '' }: { v: number; tone?: string }) {
  return <td className={`td num text-right ${v ? tone : 'text-slate-300'}`}>{v ? <Money paise={v} /> : '—'}</td>
}
