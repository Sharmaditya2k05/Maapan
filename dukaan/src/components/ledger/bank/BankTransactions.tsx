'use client'

/** Bank transactions tab: list / add deposit·withdrawal·charges·interest / reconcile (bahi /v1/bank/transactions). */
import { useState } from 'react'
import Link from 'next/link'
import { CheckCircle2, Landmark, Plus } from 'lucide-react'
import { Badge, Button, Empty, Field, Input, Modal, Money, MoneyInput, QueryState, Select, Table } from '@/components/ui'
import { post } from '@/lib/api'
import { downloadFile, fmtDate, todayIST, useGet, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import type { BankTxn, BankTxnInput, BankTxnKind } from '@/lib/types.ledger'
import { csvRupees, toCSV } from '@/lib/ledger/csv'
import { presetRange, type DateRange } from '@/lib/ledger/util'
import { AccountPicker, useAccounts, type LedgerTarget } from '../AccountPicker'
import { DateRangePicker } from '../DateRangePicker'
import { MobileRow } from '../bits'
import { voucherHref } from '../StatementTable'
import { useAction } from '../useAction'

const KINDS: { value: BankTxnKind; label: string; hint: string }[] = [
  { value: 'deposit', label: 'Deposit', hint: 'Cash taken to the bank (Dr Bank · Cr Cash)' },
  { value: 'withdrawal', label: 'Withdrawal', hint: 'Cash drawn from the bank (Dr Cash · Cr Bank)' },
  { value: 'charges', label: 'Bank charges', hint: 'Dr Bank Charges · Cr Bank' },
  { value: 'interest', label: 'Interest credited', hint: 'Dr Bank · Cr Other Income' },
]
const kindTone = (k: BankTxnKind) => (k === 'deposit' || k === 'interest' ? 'green' : k === 'charges' ? 'red' : 'amber')

export function BankTransactions() {
  const canDo = usePerm()
  const [range, setRange] = useState<DateRange>(() => presetRange('this_month', todayIST()))
  const [bankId, setBankId] = useState('')
  const [reconciled, setReconciled] = useState('')
  const [add, setAdd] = useState(false)
  const [reconcile, setReconcile] = useState<BankTxn | null>(null)
  const { accounts } = useAccounts()
  const banks = accounts.filter((a) => a.is_bank)
  const bankName = (id: string) => banks.find((b) => b.id === id)?.name ?? id.slice(0, 8)
  const q = useGet<{ items: BankTxn[] }>('/bahi/v1/bank/transactions', { from: range.from, to: range.to, bank_account_id: bankId || undefined, reconciled: reconciled || undefined })
  const items = q.data?.items ?? []
  const exportCSV = () => downloadFile(`bank-transactions-${range.from}-${range.to}.csv`, toCSV(['Date', 'Bank account', 'Kind', 'Reference', 'Amount', 'Reconciled on'],
    items.map((t) => [t.date, bankName(t.bank_account_id), t.kind, t.reference ?? '', csvRupees(t.amount_paise), t.reconciled_at ?? ''])), 'text/csv')

  return (
    <>
      <div className="mb-4 flex flex-wrap items-center gap-2">
        <DateRangePicker value={range} onChange={setRange} />
        {banks.length > 1 && <Select value={bankId} onChange={(e) => setBankId(e.target.value)} className="w-auto input-sm" aria-label="Bank account"><option value="">All bank accounts</option>{banks.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}</Select>}
        <Select value={reconciled} onChange={(e) => setReconciled(e.target.value)} className="w-auto input-sm" aria-label="Reconciled"><option value="">All</option><option value="false">Not reconciled</option><option value="true">Reconciled</option></Select>
        <div className="ml-auto flex gap-2">
          <Button size="sm" variant="secondary" onClick={exportCSV} disabled={items.length === 0}>CSV</Button>
          {canDo(PERMS.ledgerWrite) && <Button size="sm" icon={Plus} onClick={() => setAdd(true)}>Add transaction</Button>}
        </div>
      </div>
      <QueryState isLoading={q.isLoading} error={q.error} data={q.data} retry={() => q.refetch()}>
        {() => items.length === 0 ? <Empty icon={Landmark} title="No bank transactions" text="Deposits, withdrawals, charges and interest recorded here post straight to the ledger." action={canDo(PERMS.ledgerWrite) ? <Button icon={Plus} onClick={() => setAdd(true)}>Add transaction</Button> : undefined} /> : (
          <>
            <div className="divide-y divide-slate-100 md:hidden">
              {items.map((t) => (
                <MobileRow key={t.id} title={<span className="flex items-center gap-1.5"><Badge tone={kindTone(t.kind)}>{t.kind}</Badge>{t.reconciled_at && <CheckCircle2 size={14} className="text-emerald-600" aria-label="Reconciled" />}</span>}
                  subtitle={`${fmtDate(t.date)} · ${bankName(t.bank_account_id)}${t.reference ? ` · ${t.reference}` : ''}`} right={<Money paise={t.amount_paise} />}
                  onClick={canDo(PERMS.ledgerWrite) && !t.reconciled_at ? () => setReconcile(t) : undefined} rightSub={t.reconciled_at ? `cleared ${fmtDate(t.reconciled_at)}` : canDo(PERMS.ledgerWrite) ? 'tap to reconcile' : 'not reconciled'} />
              ))}
            </div>
            <div className="hidden md:block">
              <Table>
                <thead className="bg-slate-50/60"><tr><th className="th">Date</th><th className="th">Bank account</th><th className="th">Kind</th><th className="th">Reference</th><th className="th text-right">Amount</th><th className="th">Reconciled</th><th className="th" /></tr></thead>
                <tbody className="divide-y divide-slate-100">
                  {items.map((t) => (
                    <tr key={t.id} className="hover:bg-slate-50/60">
                      <td className="td whitespace-nowrap text-slate-600">{fmtDate(t.date)}</td>
                      <td className="td">{bankName(t.bank_account_id)}</td>
                      <td className="td"><Badge tone={kindTone(t.kind)}>{t.kind}</Badge></td>
                      <td className="td text-slate-600">{t.reference || '—'} · <Link href={voucherHref(t.voucher_id)} className="link text-xs">voucher</Link></td>
                      <td className="td num text-right font-semibold"><Money paise={t.amount_paise} /></td>
                      <td className="td">{t.reconciled_at ? <span className="flex items-center gap-1 text-xs text-emerald-700"><CheckCircle2 size={14} />{fmtDate(t.reconciled_at)}</span> : <span className="text-xs text-slate-400">Pending</span>}</td>
                      <td className="td text-right">{!t.reconciled_at && canDo(PERMS.ledgerWrite) && <Button size="sm" variant="secondary" onClick={() => setReconcile(t)}>Reconcile</Button>}</td>
                    </tr>
                  ))}
                </tbody>
              </Table>
            </div>
          </>
        )}
      </QueryState>
      <AddBankTxnModal open={add} onClose={() => setAdd(false)} banks={banks.map((b) => ({ id: b.id, name: b.name }))} />
      <ReconcileModal txn={reconcile} onClose={() => setReconcile(null)} />
    </>
  )
}

function AddBankTxnModal({ open, onClose, banks }: { open: boolean; onClose: () => void; banks: { id: string; name: string }[] }) {
  const [kind, setKind] = useState<BankTxnKind>('deposit')
  const [bankId, setBankId] = useState('')
  const [amount, setAmount] = useState(0)
  const [date, setDate] = useState(todayIST())
  const [reference, setReference] = useState('')
  const [counter, setCounter] = useState<LedgerTarget | null>(null)
  const [error, setError] = useState<string | null>(null)
  const save = useAction<BankTxn, BankTxnInput>((body) => post<BankTxn>('/bahi/v1/bank/transactions', body), {
    invalidate: ['/bahi/v1/bank/transactions', '/bahi/v1/bankbook', '/bahi/v1/cashbook', '/bahi/v1/vouchers', '/bahi/v1/accounts', '/bahi/v1/reports', '/bahi/v1/daybook'],
    successMessage: 'Transaction posted',
    onSuccess: () => { setAmount(0); setReference(''); setCounter(null); onClose() },
  })
  const bank = bankId || banks[0]?.id || ''
  const hint = KINDS.find((k) => k.value === kind)?.hint
  const submit = () => {
    setError(null)
    if (!bank) return setError('Pick a bank account.')
    if (amount <= 0) return setError('Enter an amount.')
    save.mutate({ bank_account_id: bank, kind, amount_paise: amount, date, reference: reference.trim() || undefined, counter_account_id: counter?.account_id || undefined })
  }
  return (
    <Modal open={open} onClose={onClose} title="Add bank transaction" footer={<><Button variant="secondary" onClick={onClose}>Cancel</Button><Button onClick={submit} loading={save.isPending}>Post</Button></>}>
      <div className="space-y-4">
        <Field label="Kind" hint={hint}><Select value={kind} onChange={(e) => setKind(e.target.value as BankTxnKind)}>{KINDS.map((k) => <option key={k.value} value={k.value}>{k.label}</option>)}</Select></Field>
        <Field label="Bank account"><Select value={bank} onChange={(e) => setBankId(e.target.value)}>{banks.length === 0 && <option value="">No bank accounts</option>}{banks.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}</Select></Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Amount"><MoneyInput value={amount} onChange={setAmount} autoFocus /></Field>
          <Field label="Date"><Input type="date" value={date} max={todayIST()} onChange={(e) => setDate(e.target.value)} /></Field>
        </div>
        <Field label="Reference" hint="Slip no, UTR, narration on the statement"><Input value={reference} onChange={(e) => setReference(e.target.value)} placeholder="Optional" /></Field>
        <Field label="Other side (optional)" hint="Defaults to Cash for deposits/withdrawals, Bank Charges for charges, Other Income for interest.">
          <AccountPicker value={counter} onChange={setCounter} filter={(a) => !a.is_bank} placeholder="Default account" />
        </Field>
        {error && <p className="text-sm text-rose-600">{error}</p>}
      </div>
    </Modal>
  )
}

function ReconcileModal({ txn, onClose }: { txn: BankTxn | null; onClose: () => void }) {
  const [date, setDate] = useState(todayIST())
  const save = useAction<void, { id: string; cleared_on: string }>((i) => post<void>(`/bahi/v1/bank/transactions/${i.id}/reconcile`, { cleared_on: i.cleared_on }), {
    invalidate: ['/bahi/v1/bank/transactions'], successMessage: 'Marked reconciled', onSuccess: onClose,
  })
  return (
    <Modal open={!!txn} onClose={onClose} title="Reconcile with bank statement"
      footer={<><Button variant="secondary" onClick={onClose}>Cancel</Button><Button loading={save.isPending} onClick={() => txn && save.mutate({ id: txn.id, cleared_on: date })}>Mark reconciled</Button></>}>
      {txn && (
        <div className="space-y-4">
          <p className="text-sm text-slate-600">{txn.kind} of <Money paise={txn.amount_paise} className="font-semibold" /> dated {fmtDate(txn.date)}{txn.reference ? ` (${txn.reference})` : ''}.</p>
          <Field label="Cleared on (per bank statement)"><Input type="date" value={date} max={todayIST()} onChange={(e) => setDate(e.target.value)} /></Field>
        </div>
      )}
    </Modal>
  )
}
