'use client'

import { Suspense, useState } from 'react'
import Link from 'next/link'
import { Hourglass, MessageCircle } from 'lucide-react'
import { Card, Empty, Money, QueryState, Skeleton, Tabs } from '@/components/ui'
import { api } from '@/lib/api'
import { downloadFile, fmtDate, todayIST, useGet, usePerm, useStore } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import { formatINR } from '@/lib/money'
import type { AgingReport } from '@/lib/types.ledger'
import type { DueAgingReport } from '@/lib/types.phase4'
import { csvRupees, toCSV } from '@/lib/ledger/csv'
import { waLink } from '@/lib/ledger/util'
import { ReportShell } from '@/components/ledger/ReportShell'
import { NoPermission, useTabParam } from '@/components/ledger/bits'
import { useAction } from '@/components/ledger/useAction'

const KINDS = ['receivables', 'payables'] as const
type Kind = (typeof KINDS)[number]
const BASES = ['due', 'date'] as const
type Basis = (typeof BASES)[number]

export default function AgingPage() {
  return <Suspense fallback={<Skeleton />}><AgingInner /></Suspense>
}

function AgingInner() {
  const canDo = usePerm()
  const [kind, setKind] = useTabParam<Kind>(KINDS, 'receivables', 'kind')
  const [basis, setBasis] = useTabParam<Basis>(BASES, 'due', 'basis')
  const [asOf, setAsOf] = useState(todayIST())
  const allowed = canDo(PERMS.reportRead)
  const due = useGet<DueAgingReport>(allowed && basis === 'due' ? '/bahi/v1/reports/aging' : null, { kind, as_of: asOf, basis: 'due' })
  const byDate = useGet<AgingReport>(allowed && basis === 'date' ? '/bahi/v1/reports/aging' : null, { kind, as_of: asOf, basis: 'date' })
  const dueCSV = useAction<Blob>(() => api<Blob>('/bahi/v1/reports/aging', { method: 'GET', raw: true, query: { kind, as_of: asOf, basis: 'due', format: 'csv' } }), {
    onSuccess: (b) => downloadFile(`${kind}-aging-due-${asOf}.csv`, b),
  })
  const dateCSV = () => byDate.data && downloadFile(`${kind}-aging-${asOf}.csv`, toCSV(['Party', 'Phone', '0-30', '31-60', '61-90', '90+', 'Total', 'Advance', 'Oldest open'],
    [...byDate.data.rows.map((r) => [r.party_name, r.phone ?? '', csvRupees(r.d0_30_paise), csvRupees(r.d31_60_paise), csvRupees(r.d61_90_paise), csvRupees(r.d90_plus_paise), csvRupees(r.total_paise), csvRupees(r.advance_paise), r.oldest_open_date ?? ''])]), 'text/csv')
  if (!allowed) return <Card><NoPermission what="reports" /></Card>
  const receivable = kind === 'receivables'

  return (
    <ReportShell title={receivable ? 'Receivables aging' : 'Payables aging'} asOf={{ value: asOf, onChange: setAsOf }}
      subtitle={basis === 'due' ? `Bill-wise, by days past due date · as of ${fmtDate(asOf)}` : `FIFO by voucher date · as of ${fmtDate(asOf)}`}
      onCSV={basis === 'due' ? () => dueCSV.mutate() : byDate.data ? dateCSV : undefined}
      filters={<>
        <Tabs value={kind} onChange={setKind} options={[{ value: 'receivables', label: 'To collect' }, { value: 'payables', label: 'To pay' }]} />
        <Tabs value={basis} onChange={setBasis} options={[{ value: 'due', label: 'By due date' }, { value: 'date', label: 'By bill date' }]} />
      </>}>
      <Card padded={false}>
        {basis === 'due'
          ? <QueryState isLoading={due.isLoading} error={due.error} data={due.data} retry={() => due.refetch()}>{(d) => <DueTable d={d} receivable={receivable} asOf={asOf} />}</QueryState>
          : <QueryState isLoading={byDate.isLoading} error={byDate.error} data={byDate.data} retry={() => byDate.refetch()}>{(d) => <DateTable d={d} receivable={receivable} />}</QueryState>}
      </Card>
    </ReportShell>
  )
}

function DueTable({ d, receivable, asOf }: { d: DueAgingReport; receivable: boolean; asOf: string }) {
  const { store } = useStore()
  if (d.rows.length === 0) return <Empty icon={Hourglass} title={receivable ? 'Nothing outstanding from customers' : 'Nothing outstanding to suppliers'} text="Open bills are aged by how many days they are past their due date (bill date + credit days)." />
  const t = d.totals
  return (
    <>
      <div className="grid grid-cols-2 gap-px bg-slate-100 sm:grid-cols-5 print:hidden">
        {([['Not yet due', t.not_due_paise, 'text-sky-700'], ['1–30 days overdue', t.d0_30_paise, 'text-amber-700'], ['31–60', t.d31_60_paise, 'text-orange-700'], ['61–90', t.d61_90_paise, 'text-rose-600'], ['Over 90', t.d90_plus_paise, 'text-rose-800']] as const).map(([l, v, c]) => (
          <div key={l} className="bg-white px-4 py-3"><div className="text-xs text-slate-500">{l}</div><div className={`num text-lg font-bold ${c}`}>{formatINR(v)}</div></div>
        ))}
      </div>
      <div className="overflow-x-auto">
        <table className="w-full min-w-[860px]">
          <thead className="bg-slate-50/60 print:bg-transparent"><tr><th className="th">Party</th><th className="th text-right">Not due</th><th className="th text-right">1–30</th><th className="th text-right">31–60</th><th className="th text-right">61–90</th><th className="th text-right">90+</th><th className="th text-right">Bills</th><th className="th text-right">On account</th><th className="th text-right">Net</th><th className="th print:hidden" /></tr></thead>
          <tbody className="divide-y divide-slate-100">
            {d.rows.map((r) => {
              const overdue = r.d0_30_paise + r.d31_60_paise + r.d61_90_paise + r.d90_plus_paise
              const wa = receivable && r.phone && overdue > 0 ? waLink(r.phone, `Namaste ${r.party_name}, a gentle reminder from ${store?.name ?? 'us'}: ${formatINR(overdue)} is past due as of ${fmtDate(asOf)}${r.oldest_due_date ? ` (oldest due ${fmtDate(r.oldest_due_date)})` : ''}. Thank you.`) : null
              return (
                <tr key={r.party_id} className="hover:bg-slate-50/60">
                  <td className="td"><Link href={`/app/parties/${r.party_id}?tab=bills`} className="font-semibold hover:underline">{r.party_name}</Link><div className="text-[11px] text-slate-400">{r.open_bills} open bill{r.open_bills === 1 ? '' : 's'}{r.credit_days ? ` · ${r.credit_days} credit days` : ''}{r.oldest_due_date ? ` · oldest due ${fmtDate(r.oldest_due_date)}` : ''}</div></td>
                  <B v={r.not_due_paise} tone="text-sky-700" /><B v={r.d0_30_paise} tone="text-amber-700" /><B v={r.d31_60_paise} tone="text-orange-700" /><B v={r.d61_90_paise} tone="text-rose-600" /><B v={r.d90_plus_paise} tone="text-rose-800 font-semibold" />
                  <td className="td num text-right font-semibold"><Money paise={r.total_paise} /></td>
                  <td className="td num text-right text-slate-500">{r.on_account_paise ? <>−<Money paise={r.on_account_paise} /></> : ''}</td>
                  <td className="td num text-right font-bold"><Money paise={r.net_paise} /></td>
                  <td className="td text-right print:hidden">{wa && <a href={wa} target="_blank" rel="noopener noreferrer" aria-label={`Remind ${r.party_name} on WhatsApp`} title="Remind on WhatsApp" className="inline-grid h-9 w-9 place-items-center rounded-xl text-slate-500 transition hover:bg-slate-100 hover:text-ink"><MessageCircle size={18} /></a>}</td>
                </tr>
              )
            })}
          </tbody>
          <tfoot className="border-t-2 border-slate-200 bg-slate-50 font-bold print:bg-transparent">
            <tr><td className="td">Total · {d.rows.length} parties</td><B v={t.not_due_paise} /><B v={t.d0_30_paise} /><B v={t.d31_60_paise} /><B v={t.d61_90_paise} /><B v={t.d90_plus_paise} /><td className="td num text-right"><Money paise={t.total_paise} /></td><td className="td num text-right">{t.on_account_paise ? <>−<Money paise={t.on_account_paise} /></> : ''}</td><td className="td num text-right"><Money paise={t.net_paise} /></td><td className="print:hidden" /></tr>
          </tfoot>
        </table>
      </div>
      <p className="px-4 py-3 text-xs text-slate-500">Net = bills outstanding − money on account (unallocated receipts / payments) = ledger balance. Allocate on-account money from the party’s Bill-wise tab.</p>
    </>
  )
}

function DateTable({ d, receivable }: { d: AgingReport; receivable: boolean }) {
  if (d.rows.length === 0) return <Empty icon={Hourglass} title={receivable ? 'Nothing outstanding from customers' : 'Nothing outstanding to suppliers'} text="Open balances are settled FIFO against the oldest bills." />
  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[720px]">
        <thead className="bg-slate-50/60 print:bg-transparent"><tr><th className="th">Party</th><th className="th text-right">0–30</th><th className="th text-right">31–60</th><th className="th text-right">61–90</th><th className="th text-right">90+</th><th className="th text-right">Total</th><th className="th text-right">Advance</th></tr></thead>
        <tbody className="divide-y divide-slate-100">
          {d.rows.map((r) => (
            <tr key={r.party_id} className="hover:bg-slate-50/60">
              <td className="td"><Link href={`/app/parties/${r.party_id}`} className="font-semibold hover:underline">{r.party_name}</Link>{r.oldest_open_date && <div className="text-[11px] text-slate-400">oldest {fmtDate(r.oldest_open_date)}</div>}</td>
              <B v={r.d0_30_paise} /><B v={r.d31_60_paise} tone="text-amber-700" /><B v={r.d61_90_paise} tone="text-orange-700" /><B v={r.d90_plus_paise} tone="text-rose-700 font-semibold" />
              <td className="td num text-right font-bold"><Money paise={r.total_paise} /></td>
              <td className="td num text-right text-slate-500">{r.advance_paise ? <Money paise={r.advance_paise} /> : ''}</td>
            </tr>
          ))}
        </tbody>
        <tfoot className="border-t-2 border-slate-200 bg-slate-50 font-bold print:bg-transparent">
          <tr><td className="td">Total</td><B v={d.totals.d0_30_paise} /><B v={d.totals.d31_60_paise} /><B v={d.totals.d61_90_paise} /><B v={d.totals.d90_plus_paise} /><td className="td num text-right"><Money paise={d.totals.total_paise} /></td><td className="td num text-right">{d.totals.advance_paise ? <Money paise={d.totals.advance_paise} /> : ''}</td></tr>
        </tfoot>
      </table>
    </div>
  )
}

function B({ v, tone = '' }: { v: number; tone?: string }) {
  return <td className={`td num text-right ${v ? tone : 'text-slate-300'}`}>{v ? <Money paise={v} /> : '—'}</td>
}
