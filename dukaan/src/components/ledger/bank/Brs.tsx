'use client'

/** Bank reconciliation statement (bahi GET /v1/reports/brs?bank_account_id=&as_of=[&from=]). */
import { useState } from 'react'
import Link from 'next/link'
import { CheckCircle2, Download, Scale } from 'lucide-react'
import { Badge, Button, Empty, Field, Input, Money, QueryState, Select } from '@/components/ui'
import { api } from '@/lib/api'
import { downloadFile, fmtDate, todayIST, useGet, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import type { BRS, BRSItem } from '@/lib/types.phase4'
import { NoPermission } from '../bits'
import { useAccounts } from '../AccountPicker'
import { voucherHref } from '../StatementTable'
import { useAction } from '../useAction'

export function Brs() {
  const canDo = usePerm()
  const { accounts } = useAccounts()
  const banks = accounts.filter((a) => a.is_bank)
  const [bankId, setBankId] = useState('')
  const bank = bankId || banks[0]?.id || ''
  const [asOf, setAsOf] = useState(todayIST())
  const [from, setFrom] = useState('')
  const query = { bank_account_id: bank, as_of: asOf, from: from || undefined }
  const q = useGet<BRS>(bank && canDo(PERMS.reportRead) ? '/bahi/v1/reports/brs' : null, query)
  const csv = useAction<Blob>(() => api<Blob>('/bahi/v1/reports/brs', { method: 'GET', raw: true, query: { ...query, format: 'csv' } }), {
    onSuccess: (b) => downloadFile(`brs-${asOf}.csv`, b),
  })
  if (!canDo(PERMS.reportRead)) return <NoPermission what="reports" />

  return (
    <>
      <div className="mb-4 flex flex-wrap items-end gap-3">
        <Field label="Bank account"><Select value={bank} onChange={(e) => setBankId(e.target.value)} className="input-sm w-auto">{banks.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}</Select></Field>
        <Field label="As of"><Input type="date" className="input-sm" value={asOf} max={todayIST()} onChange={(e) => setAsOf(e.target.value || todayIST())} /></Field>
        <Field label="Statements start" hint="Postings before this count as cleared"><Input type="date" className="input-sm" value={from} onChange={(e) => setFrom(e.target.value)} /></Field>
        <Button size="sm" variant="secondary" icon={Download} loading={csv.isPending} disabled={!bank} onClick={() => csv.mutate()} className="ml-auto">CSV</Button>
      </div>
      {!bank ? <Empty icon={Scale} title="No bank account" text="Create a bank account first." /> : (
        <QueryState isLoading={q.isLoading} error={q.error} data={q.data} retry={() => q.refetch()}>
          {(b) => {
            const reconciled = b.difference_paise === 0
            return (
              <div className="space-y-4">
                <div className="overflow-hidden rounded-2xl border border-slate-200">
                  <Row label="Balance as per books" paise={b.balance_per_books_paise} strong />
                  <Row label="Add: cheques issued but not presented" paise={b.unpresented_paise} />
                  <Row label="Less: deposits not yet credited by the bank" paise={-b.uncredited_paise} />
                  <Row label="Add: bank credits not in the books" paise={b.bank_credits_not_in_books_paise} />
                  <Row label="Less: bank debits not in the books" paise={-b.bank_debits_not_in_books_paise} />
                  <Row label="Balance as per bank (computed)" paise={b.balance_per_bank_computed_paise} strong />
                  {b.balance_per_statement_paise !== undefined && b.balance_per_statement_paise !== null && <Row label="Balance as per last imported statement" paise={b.balance_per_statement_paise} />}
                </div>
                {b.difference_paise === undefined || b.difference_paise === null
                  ? <p className="text-sm text-slate-500">No imported statement balance on or before {fmtDate(b.as_of)} to compare with.</p>
                  : <div className={`flex items-center gap-2 rounded-xl px-4 py-3 text-sm font-semibold ${reconciled ? 'bg-emerald-50 text-emerald-800' : 'bg-rose-50 text-rose-700'}`}>
                      {reconciled ? <><CheckCircle2 size={16} /> Reconciled — books and statement agree.</> : <>Difference <Money paise={b.difference_paise} signed /> (statement − computed). Import missing statements or match the remaining lines.</>}
                    </div>}
                <Items title="Cheques issued, not presented" items={b.unpresented_cheques} />
                <Items title="Deposits not credited" items={b.uncredited_deposits} />
                <Items title="Bank credits not in the books" items={b.bank_credits_not_in_books} />
                <Items title="Bank debits not in the books" items={b.bank_debits_not_in_books} />
              </div>
            )
          }}
        </QueryState>
      )}
    </>
  )
}

function Row({ label, paise, strong }: { label: string; paise: number; strong?: boolean }) {
  return <div className={`flex items-center justify-between gap-3 border-b border-slate-100 px-4 py-2.5 text-sm last:border-0 ${strong ? 'bg-slate-50 font-bold' : ''}`}><span>{label}</span><Money paise={paise} signed={!strong} /></div>
}

function Items({ title, items }: { title: string; items: BRSItem[] | null }) {
  const list = items ?? []
  if (list.length === 0) return null
  return (
    <div>
      <h3 className="mb-1 flex items-center gap-2 text-sm font-bold">{title} <Badge>{list.length}</Badge></h3>
      <div className="divide-y divide-slate-100 rounded-xl border border-slate-200">
        {list.map((i, n) => (
          <div key={`${i.voucher_id ?? i.line_id}-${n}`} className="flex items-center gap-3 px-4 py-2 text-sm">
            <span className="w-24 shrink-0 text-slate-500">{fmtDate(i.date)}</span>
            <span className="min-w-0 flex-1 truncate">{i.voucher_id ? <Link href={voucherHref(i.voucher_id)} className="font-mono text-xs text-brand-700 hover:underline">{i.voucher_number}</Link> : null} {i.description}{i.reference ? <span className="text-xs text-slate-400"> · {i.reference}</span> : null}</span>
            <Money paise={i.amount_paise} className="font-semibold" />
          </div>
        ))}
      </div>
    </div>
  )
}
