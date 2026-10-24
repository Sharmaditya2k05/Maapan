'use client'

import { Suspense, useMemo, useState } from 'react'
import { useRouter, useSearchParams } from 'next/navigation'
import { Plus, Scale, Trash2 } from 'lucide-react'
import { Button, Card, Field, IconButton, Input, Money, MoneyInput, PageHeader, Select, Skeleton, Textarea } from '@/components/ui'
import { post } from '@/lib/api'
import { todayIST, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import { formatINR } from '@/lib/money'
import type { ManualVoucherInput, ManualVoucherType, VoucherView } from '@/lib/types.ledger'
import { voucherBalance } from '@/lib/ledger/util'
import { AccountPicker, type LedgerTarget } from '@/components/ledger/AccountPicker'
import { NoPermission } from '@/components/ledger/bits'
import { useAction } from '@/components/ledger/useAction'

interface Row { key: number; target: LedgerTarget | null; debit: number; credit: number }

const TYPES: { value: ManualVoucherType; label: string; hint: string }[] = [
  { value: 'journal', label: 'Journal', hint: 'Any adjustment between accounts or parties' },
  { value: 'receipt', label: 'Receipt', hint: 'Money in: Dr Cash/Bank · Cr Party or income' },
  { value: 'payment', label: 'Payment', hint: 'Money out: Dr Party or expense · Cr Cash/Bank' },
  { value: 'contra', label: 'Contra', hint: 'Between cash and bank accounts only' },
]

let seq = 1
const blank = (): Row => ({ key: seq++, target: null, debit: 0, credit: 0 })

export default function NewVoucherPage() {
  return <Suspense fallback={<Skeleton />}><NewVoucherInner /></Suspense>
}

function NewVoucherInner() {
  const canDo = usePerm()
  const router = useRouter()
  const params = useSearchParams()
  const initialType = (params.get('type') as ManualVoucherType | null) ?? 'journal'
  const [type, setType] = useState<ManualVoucherType>(TYPES.some((t) => t.value === initialType) ? initialType : 'journal')
  const [date, setDate] = useState(todayIST())
  const [narration, setNarration] = useState('')
  const [reference, setReference] = useState('')
  const [rows, setRows] = useState<Row[]>(() => [blank(), blank()])
  const [error, setError] = useState<string | null>(null)

  const entries = useMemo(() => rows.map((r) => ({ account_id: r.target?.account_id, party_id: r.target?.party_id, debit_paise: r.debit, credit_paise: r.credit })), [rows])
  const bal = voucherBalance(entries)

  const save = useAction<VoucherView, ManualVoucherInput>((body) => post<VoucherView>('/bahi/v1/vouchers', body), {
    invalidate: ['/bahi/v1/vouchers', '/bahi/v1/parties', '/bahi/v1/accounts', '/bahi/v1/reports', '/bahi/v1/daybook', '/bahi/v1/cashbook', '/bahi/v1/bankbook'],
    successMessage: 'Voucher posted',
    onSuccess: (v) => router.push(`/app/bank?tab=vouchers&voucher=${v.id}`),
  })

  const update = (key: number, patch: Partial<Row>) => setRows((rs) => rs.map((r) => (r.key === key ? { ...r, ...patch } : r)))
  const remove = (key: number) => setRows((rs) => (rs.length > 2 ? rs.filter((r) => r.key !== key) : rs))
  const addRow = () => setRows((rs) => {
    // Pre-fill the balancing amount on the new line.
    const next = blank()
    if (bal.diff > 0) next.credit = bal.diff; else if (bal.diff < 0) next.debit = -bal.diff
    return [...rs, next]
  })

  const submit = () => {
    setError(null)
    if (!narration.trim()) return setError('Write a narration — it is what the accountant reads later.')
    if (!bal.ok) return setError('Every line needs an account and exactly one of debit or credit, and debits must equal credits.')
    const body: ManualVoucherInput = {
      type, date, narration: narration.trim(), reference: reference.trim() || undefined,
      entries: rows.map((r) => ({ ...(r.target?.party_id ? { party_id: r.target.party_id } : { account_id: r.target?.account_id }), debit_paise: r.debit, credit_paise: r.credit })),
    }
    save.mutate(body)
  }

  if (!canDo(PERMS.ledgerWrite)) return <><PageHeader title="New voucher" back={() => router.push('/app/bank')} /><Card><NoPermission what="posting vouchers" /></Card></>
  const contra = type === 'contra'
  const hint = TYPES.find((t) => t.value === type)?.hint

  return (
    <>
      <PageHeader title="New voucher" subtitle={hint} back={() => router.push('/app/bank?tab=vouchers')} />
      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_300px]">
        <Card>
          <div className="grid gap-4 sm:grid-cols-[10rem_10rem_minmax(0,1fr)]">
            <Field label="Type"><Select value={type} onChange={(e) => { setType(e.target.value as ManualVoucherType); setRows([blank(), blank()]) }}>{TYPES.map((t) => <option key={t.value} value={t.value}>{t.label}</option>)}</Select></Field>
            <Field label="Date"><Input type="date" value={date} max={todayIST()} onChange={(e) => setDate(e.target.value)} /></Field>
            <Field label="Reference" hint="Bill no, UTR… (optional)"><Input value={reference} onChange={(e) => setReference(e.target.value)} /></Field>
          </div>
          <Field label="Narration" className="mt-4"><Textarea rows={2} value={narration} onChange={(e) => setNarration(e.target.value)} placeholder="Being rent for October paid by cheque 000123" /></Field>

          <div className="mt-6">
            <div className="mb-2 hidden grid-cols-[minmax(0,1fr)_9rem_9rem_2.5rem] gap-2 px-1 text-[11px] font-semibold uppercase tracking-wider text-slate-500 sm:grid"><span>Account</span><span className="text-right">Debit</span><span className="text-right">Credit</span><span /></div>
            <div className="space-y-3">
              {rows.map((r, i) => (
                <div key={r.key} className="grid gap-2 rounded-xl border border-slate-100 p-2 sm:grid-cols-[minmax(0,1fr)_9rem_9rem_2.5rem] sm:items-start sm:border-0 sm:p-0">
                  <AccountPicker value={r.target} onChange={(t) => update(r.key, { target: t })} allowParties={!contra} filter={contra ? (a) => a.is_cash || a.is_bank : undefined}
                    placeholder={contra ? 'Cash or bank account' : `Account or party for line ${i + 1}`} />
                  <div className="grid grid-cols-2 gap-2 sm:contents">
                    <MoneyInput aria-label={`Line ${i + 1} debit`} value={r.debit} onChange={(v) => update(r.key, { debit: v, credit: v ? 0 : r.credit })} placeholder="Debit" />
                    <MoneyInput aria-label={`Line ${i + 1} credit`} value={r.credit} onChange={(v) => update(r.key, { credit: v, debit: v ? 0 : r.debit })} placeholder="Credit" />
                  </div>
                  <IconButton icon={Trash2} label={`Remove line ${i + 1}`} onClick={() => remove(r.key)} disabled={rows.length <= 2} className="justify-self-end sm:justify-self-auto" />
                </div>
              ))}
            </div>
            <div className="mt-3 flex flex-wrap items-center justify-between gap-3">
              <Button variant="secondary" size="sm" icon={Plus} onClick={addRow} disabled={rows.length >= 200}>Add line{bal.diff !== 0 ? ` (${formatINR(Math.abs(bal.diff))} to balance)` : ''}</Button>
              <div className="flex items-center gap-4 text-sm">
                <span>Dr <Money paise={bal.debit} className="font-bold" /></span>
                <span>Cr <Money paise={bal.credit} className="font-bold" /></span>
                <span className={`inline-flex items-center gap-1 rounded-lg px-2 py-1 text-xs font-bold ${bal.balanced ? 'bg-emerald-50 text-emerald-700' : 'bg-amber-50 text-amber-700'}`} role="status">
                  <Scale size={12} />{bal.balanced ? 'Balanced' : bal.debit === 0 && bal.credit === 0 ? 'Enter amounts' : `Off by ${formatINR(Math.abs(bal.diff))}`}
                </span>
              </div>
            </div>
          </div>
          {error && <p className="mt-4 text-sm text-rose-600">{error}</p>}
          <div className="mt-6 flex justify-end gap-2">
            <Button variant="secondary" onClick={() => router.push('/app/bank?tab=vouchers')}>Cancel</Button>
            <Button onClick={submit} loading={save.isPending} disabled={!bal.ok || !narration.trim()}>Post voucher</Button>
          </div>
        </Card>
        <Card title="How bahi reads this">
          <ul className="space-y-2 text-sm text-slate-600">
            <li>Debits must equal credits; the server enforces it too.</li>
            <li>Pick a <strong>party</strong> to post to its own ledger (Sundry Debtors / Creditors).</li>
            <li><strong>Contra</strong> vouchers may only touch cash and bank accounts.</li>
            <li>Dates on or before the period lock are refused with <span className="font-mono text-xs">409 period_locked</span>.</li>
            <li>Manual vouchers can be reversed later from the voucher view; event-driven ones are reversed by cancelling their document.</li>
          </ul>
        </Card>
      </div>
    </>
  )
}
