'use client'

/** Running-balance ledger (party / account / cash book / bank book). Table on ≥ md, stacked rows below. */
import Link from 'next/link'
import { fmtDate } from '@/lib/hooks'
import { fmtDrCr } from '@/lib/ledger/util'
import { formatINR } from '@/lib/money'
import type { Statement } from '@/lib/types'
import { Badge, Money } from '@/components/ui'

export type StatementLine = Statement['lines'][number]

export function voucherHref(id: string) { return `/app/bank?tab=vouchers&voucher=${id}` }

export function StatementTable({ statement: st, emptyText = 'No entries in this period.' }: { statement: Statement; emptyText?: string }) {
  const rows = st.lines ?? []
  return (
    <>
      <div className="divide-y divide-slate-100 md:hidden print:hidden">
        <div className="flex items-center justify-between bg-slate-50/70 px-4 py-2.5 text-xs text-slate-500"><span>Opening balance · {fmtDate(st.from)}</span><span className="num font-semibold text-ink">{fmtDrCr(st.opening_balance_paise)}</span></div>
        {rows.length === 0 && <p className="px-4 py-8 text-center text-sm text-slate-500">{emptyText}</p>}
        {rows.map((l, i) => (
          <Link key={`${l.voucher_id}-${i}`} href={voucherHref(l.voucher_id)} className="flex items-center gap-3 px-4 py-3 active:bg-slate-50">
            <div className={`grid h-9 w-9 shrink-0 place-items-center rounded-xl text-sm font-bold ${l.credit_paise ? 'bg-emerald-50 text-emerald-600' : 'bg-slate-100 text-slate-600'}`}>{l.credit_paise ? 'Cr' : 'Dr'}</div>
            <div className="min-w-0 flex-1">
              <div className="truncate text-sm font-semibold">{l.narration || l.voucher_type}</div>
              <div className="truncate text-xs text-slate-500">{fmtDate(l.date)} · <span className="font-mono">{l.voucher_number}</span>{l.reference ? ` · ${l.reference}` : ''}</div>
            </div>
            <div className="shrink-0 text-right">
              <div className={`num text-sm font-bold ${l.credit_paise ? 'text-emerald-700' : ''}`}>{l.credit_paise ? formatINR(l.credit_paise) : formatINR(l.debit_paise)}</div>
              <div className="num text-[11px] text-slate-400">{fmtDrCr(l.balance_paise)}</div>
            </div>
          </Link>
        ))}
        <div className="flex items-center justify-between bg-slate-50/70 px-4 py-2.5 text-xs text-slate-500"><span>Closing balance · {fmtDate(st.to)}</span><span className="num font-bold text-ink">{fmtDrCr(st.closing_balance_paise)}</span></div>
      </div>

      <div className="hidden overflow-x-auto md:block print:block">
        <table className="w-full min-w-[640px] border-collapse">
          <thead className="bg-slate-50/60 print:bg-transparent">
            <tr><th className="th">Date</th><th className="th">Voucher</th><th className="th">Particulars</th><th className="th text-right">Debit</th><th className="th text-right">Credit</th><th className="th text-right">Balance</th></tr>
          </thead>
          <tbody className="divide-y divide-slate-100">
            <tr className="bg-slate-50/40">
              <td className="td text-slate-500">{fmtDate(st.from)}</td><td className="td" />
              <td className="td font-medium text-slate-500">Opening balance</td><td className="td" /><td className="td" />
              <td className="td num text-right font-semibold">{fmtDrCr(st.opening_balance_paise)}</td>
            </tr>
            {rows.length === 0 && <tr><td colSpan={6} className="px-4 py-8 text-center text-sm text-slate-500">{emptyText}</td></tr>}
            {rows.map((l, i) => (
              <tr key={`${l.voucher_id}-${i}`} className="hover:bg-slate-50/60">
                <td className="td whitespace-nowrap text-slate-600">{fmtDate(l.date)}</td>
                <td className="td"><Link href={voucherHref(l.voucher_id)} className="font-mono text-xs font-semibold text-brand-700 hover:underline">{l.voucher_number}</Link><div className="print:hidden"><Badge tone="gray" className="mt-0.5">{l.voucher_type.replace('_', ' ')}</Badge></div></td>
                <td className="td"><div className="font-medium">{l.narration || '—'}</div>{l.reference && <div className="text-xs text-slate-500">{l.reference}</div>}</td>
                <td className="td num text-right">{l.debit_paise ? <Money paise={l.debit_paise} /> : ''}</td>
                <td className="td num text-right text-emerald-700">{l.credit_paise ? <Money paise={l.credit_paise} /> : ''}</td>
                <td className="td num text-right font-semibold">{fmtDrCr(l.balance_paise)}</td>
              </tr>
            ))}
          </tbody>
          <tfoot className="border-t-2 border-slate-200 bg-slate-50 font-bold print:bg-transparent">
            <tr>
              <td className="td text-slate-500">{fmtDate(st.to)}</td><td className="td" /><td className="td">Closing balance</td>
              <td className="td num text-right"><Money paise={st.total_debit_paise} /></td>
              <td className="td num text-right"><Money paise={st.total_credit_paise} /></td>
              <td className="td num text-right">{fmtDrCr(st.closing_balance_paise)}</td>
            </tr>
          </tfoot>
        </table>
      </div>
    </>
  )
}

/** CSV rows for a statement (same columns as bahi's own CSV). */
export function statementCSV(st: Statement): { header: string[]; rows: (string | number)[][] } {
  const r = (p: number) => (p / 100).toFixed(2)
  const rows: (string | number)[][] = [[st.from, '', '', 'Opening balance', '', '', r(st.opening_balance_paise)]]
  for (const l of st.lines ?? []) rows.push([l.date, l.voucher_number, l.voucher_type, l.narration, r(l.debit_paise), r(l.credit_paise), r(l.balance_paise)])
  rows.push([st.to, '', '', 'Closing balance', r(st.total_debit_paise), r(st.total_credit_paise), r(st.closing_balance_paise)])
  return { header: ['Date', 'Voucher', 'Type', 'Narration', 'Debit', 'Credit', 'Balance (Dr+)'], rows }
}
