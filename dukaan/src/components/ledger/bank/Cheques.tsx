'use client'

/** Cheques tab: received / issued cheques and their lifecycle (bahi /v1/cheques). Money posts only on "cleared". */
import { useState } from 'react'
import Link from 'next/link'
import { FileCheck2, Plus } from 'lucide-react'
import { Badge, Button, Empty, Field, Input, Modal, Money, MoneyInput, QueryState, Select, Table, Textarea } from '@/components/ui'
import { post } from '@/lib/api'
import { downloadFile, fmtDate, todayIST, useGet, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import type { Party } from '@/lib/types'
import type { Cheque, ChequeInput, ChequeStatus, ChequeStatusInput } from '@/lib/types.ledger'
import { csvRupees, toCSV } from '@/lib/ledger/csv'
import { chequeNextStatuses } from '@/lib/ledger/util'
import { useAccounts } from '../AccountPicker'
import { PartyPicker } from '../PartyPicker'
import { MobileRow } from '../bits'
import { voucherHref } from '../StatementTable'
import { useAction } from '../useAction'

const chequeTone = (s: ChequeStatus) => (s === 'cleared' ? 'green' : s === 'bounced' ? 'red' : s === 'cancelled' ? 'gray' : s === 'deposited' ? 'blue' : 'amber')

export function Cheques() {
  const canDo = usePerm()
  const [status, setStatus] = useState('')
  const [direction, setDirection] = useState('')
  const [add, setAdd] = useState(false)
  const [move, setMove] = useState<Cheque | null>(null)
  const { accounts } = useAccounts()
  const banks = accounts.filter((a) => a.is_bank)
  const bankName = (id: string) => banks.find((b) => b.id === id)?.name ?? '—'
  const q = useGet<{ items: Cheque[] }>('/bahi/v1/cheques', { status: status || undefined, direction: direction || undefined })
  const items = q.data?.items ?? []
  const exportCSV = () => downloadFile('cheques.csv', toCSV(['Cheque no', 'Date', 'Direction', 'Party', 'Drawee bank', 'Our bank account', 'Amount', 'Status', 'Status date'],
    items.map((c) => [c.cheque_no, c.cheque_date, c.direction, c.party_name, c.drawee_bank ?? '', bankName(c.bank_account_id), csvRupees(c.amount_paise), c.status, c.status_date ?? ''])), 'text/csv')

  return (
    <>
      <div className="mb-4 flex flex-wrap items-center gap-2">
        <Select value={direction} onChange={(e) => setDirection(e.target.value)} className="w-auto input-sm" aria-label="Direction"><option value="">Received & issued</option><option value="received">Received</option><option value="issued">Issued</option></Select>
        <Select value={status} onChange={(e) => setStatus(e.target.value)} className="w-auto input-sm" aria-label="Status"><option value="">All statuses</option>{(['received', 'deposited', 'issued', 'cleared', 'bounced', 'cancelled'] as const).map((s) => <option key={s} value={s}>{s}</option>)}</Select>
        <div className="ml-auto flex gap-2">
          <Button size="sm" variant="secondary" onClick={exportCSV} disabled={items.length === 0}>CSV</Button>
          {canDo(PERMS.paymentCollect) && <Button size="sm" icon={Plus} onClick={() => setAdd(true)}>Record cheque</Button>}
        </div>
      </div>
      <QueryState isLoading={q.isLoading} error={q.error} data={q.data} retry={() => q.refetch()}>
        {() => items.length === 0 ? <Empty icon={FileCheck2} title="No cheques" text="Record cheques you receive or issue; the ledger is posted when they clear." action={canDo(PERMS.paymentCollect) ? <Button icon={Plus} onClick={() => setAdd(true)}>Record cheque</Button> : undefined} /> : (
          <>
            <div className="divide-y divide-slate-100 md:hidden">
              {items.map((c) => (
                <MobileRow key={c.id} onClick={canDo(PERMS.ledgerWrite) && chequeNextStatuses(c.direction, c.status).length ? () => setMove(c) : undefined}
                  title={<span className="flex items-center gap-1.5"><span className="font-mono">{c.cheque_no}</span><Badge tone={chequeTone(c.status)}>{c.status}</Badge></span>}
                  subtitle={`${c.direction} · ${c.party_name} · ${fmtDate(c.cheque_date)}`} right={<Money paise={c.amount_paise} />} />
              ))}
            </div>
            <div className="hidden md:block">
              <Table>
                <thead className="bg-slate-50/60"><tr><th className="th">Cheque</th><th className="th">Party</th><th className="th">Direction</th><th className="th">Bank</th><th className="th text-right">Amount</th><th className="th">Status</th><th className="th" /></tr></thead>
                <tbody className="divide-y divide-slate-100">
                  {items.map((c) => {
                    const next = chequeNextStatuses(c.direction, c.status)
                    return (
                      <tr key={c.id} className="hover:bg-slate-50/60">
                        <td className="td"><div className="font-mono text-sm font-semibold">{c.cheque_no}</div><div className="text-xs text-slate-500">{fmtDate(c.cheque_date)}</div></td>
                        <td className="td"><Link href={`/app/parties/${c.party_id}`} className="font-medium hover:underline">{c.party_name}</Link></td>
                        <td className="td capitalize text-slate-600">{c.direction}</td>
                        <td className="td text-slate-600"><div>{bankName(c.bank_account_id)}</div>{c.drawee_bank && <div className="text-xs text-slate-400">drawn on {c.drawee_bank}</div>}</td>
                        <td className="td num text-right font-semibold"><Money paise={c.amount_paise} /></td>
                        <td className="td"><Badge tone={chequeTone(c.status)}>{c.status}</Badge>{c.status_date && <div className="text-[11px] text-slate-400">{fmtDate(c.status_date)}</div>}{c.voucher_id && <Link href={voucherHref(c.voucher_id)} className="link block text-[11px]">voucher</Link>}</td>
                        <td className="td text-right">{next.length > 0 && canDo(PERMS.ledgerWrite) && <Button size="sm" variant="secondary" onClick={() => setMove(c)}>Update</Button>}</td>
                      </tr>
                    )
                  })}
                </tbody>
              </Table>
            </div>
          </>
        )}
      </QueryState>
      <AddChequeModal open={add} onClose={() => setAdd(false)} banks={banks.map((b) => ({ id: b.id, name: b.name }))} />
      <ChequeStatusModal cheque={move} onClose={() => setMove(null)} />
    </>
  )
}

function AddChequeModal({ open, onClose, banks }: { open: boolean; onClose: () => void; banks: { id: string; name: string }[] }) {
  const [direction, setDirection] = useState<'received' | 'issued'>('received')
  const [party, setParty] = useState<Party | null>(null)
  const [bankId, setBankId] = useState('')
  const [chequeNo, setChequeNo] = useState('')
  const [chequeDate, setChequeDate] = useState(todayIST())
  const [drawee, setDrawee] = useState('')
  const [amount, setAmount] = useState(0)
  const [notes, setNotes] = useState('')
  const [error, setError] = useState<string | null>(null)
  const save = useAction<Cheque, ChequeInput>((body) => post<Cheque>('/bahi/v1/cheques', body), {
    invalidate: ['/bahi/v1/cheques'], successMessage: 'Cheque recorded',
    onSuccess: () => { setParty(null); setChequeNo(''); setAmount(0); setDrawee(''); setNotes(''); onClose() },
  })
  const bank = bankId || banks[0]?.id || ''
  const submit = () => {
    setError(null)
    if (!party) return setError('Pick the party.')
    if (!bank) return setError('Pick the bank account.')
    if (!chequeNo.trim()) return setError('Cheque number is required.')
    if (amount <= 0) return setError('Enter the amount.')
    save.mutate({ direction, party_id: party.id, bank_account_id: bank, cheque_no: chequeNo.trim(), cheque_date: chequeDate, drawee_bank: drawee.trim() || undefined, amount_paise: amount, notes: notes.trim() || undefined })
  }
  return (
    <Modal open={open} onClose={onClose} title="Record cheque" footer={<><Button variant="secondary" onClick={onClose}>Cancel</Button><Button onClick={submit} loading={save.isPending}>Save</Button></>}>
      <div className="space-y-4">
        <div className="inline-flex rounded-xl bg-slate-100 p-1 text-sm font-semibold">
          {(['received', 'issued'] as const).map((d) => <button key={d} type="button" onClick={() => setDirection(d)} className={`rounded-lg px-3 py-1.5 capitalize cursor-pointer ${direction === d ? 'bg-white text-ink shadow-sm' : 'text-slate-500'}`}>{d}</button>)}
        </div>
        <Field label={direction === 'received' ? 'Received from' : 'Issued to'}><PartyPicker value={party} onChange={setParty} autoFocus /></Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Cheque number"><Input value={chequeNo} onChange={(e) => setChequeNo(e.target.value)} className="font-mono" placeholder="000123" /></Field>
          <Field label="Cheque date"><Input type="date" value={chequeDate} onChange={(e) => setChequeDate(e.target.value)} /></Field>
          <Field label="Amount"><MoneyInput value={amount} onChange={setAmount} /></Field>
          <Field label={direction === 'received' ? 'Deposit into' : 'Drawn on our account'}><Select value={bank} onChange={(e) => setBankId(e.target.value)}>{banks.length === 0 && <option value="">No bank accounts</option>}{banks.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}</Select></Field>
        </div>
        <Field label="Drawee bank" hint="The bank printed on the cheque (optional)"><Input value={drawee} onChange={(e) => setDrawee(e.target.value)} placeholder="HDFC Bank, Pune" /></Field>
        <Field label="Notes"><Textarea rows={2} value={notes} onChange={(e) => setNotes(e.target.value)} /></Field>
        {error && <p className="text-sm text-rose-600">{error}</p>}
      </div>
    </Modal>
  )
}

function ChequeStatusModal({ cheque, onClose }: { cheque: Cheque | null; onClose: () => void }) {
  const next = cheque ? chequeNextStatuses(cheque.direction, cheque.status) : []
  const [status, setStatus] = useState<string>('')
  const [date, setDate] = useState(todayIST())
  const [notes, setNotes] = useState('')
  const chosen = (status && next.includes(status) ? status : next[0]) as ChequeStatus | undefined
  const save = useAction<Cheque, { id: string; body: ChequeStatusInput }>((i) => post<Cheque>(`/bahi/v1/cheques/${i.id}/status`, i.body), {
    invalidate: ['/bahi/v1/cheques', '/bahi/v1/vouchers', '/bahi/v1/bankbook', '/bahi/v1/parties', '/bahi/v1/reports', '/bahi/v1/accounts'],
    successMessage: 'Cheque updated', onSuccess: () => { setStatus(''); setNotes(''); onClose() },
  })
  return (
    <Modal open={!!cheque} onClose={onClose} title={cheque ? `Cheque ${cheque.cheque_no}` : 'Cheque'}
      footer={<><Button variant="secondary" onClick={onClose}>Cancel</Button><Button loading={save.isPending} disabled={!chosen} onClick={() => cheque && chosen && save.mutate({ id: cheque.id, body: { status: chosen, date, notes: notes.trim() || undefined } })}>Mark {chosen}</Button></>}>
      {cheque && (
        <div className="space-y-4">
          <p className="text-sm text-slate-600"><Money paise={cheque.amount_paise} className="font-semibold" /> {cheque.direction} · {cheque.party_name} · currently <Badge tone={chequeTone(cheque.status)}>{cheque.status}</Badge></p>
          <Field label="New status" hint={chosen === 'cleared' ? (cheque.direction === 'received' ? 'Posts a receipt: Dr Bank · Cr Party' : 'Posts a payment: Dr Party · Cr Bank') : chosen === 'bounced' ? 'No ledger entry; follow up with the party.' : undefined}>
            <Select value={chosen ?? ''} onChange={(e) => setStatus(e.target.value)}>{next.map((s) => <option key={s} value={s}>{s}</option>)}</Select>
          </Field>
          <Field label="Date"><Input type="date" value={date} max={todayIST()} onChange={(e) => setDate(e.target.value)} /></Field>
          <Field label="Notes"><Textarea rows={2} value={notes} onChange={(e) => setNotes(e.target.value)} placeholder="Optional" /></Field>
        </div>
      )}
    </Modal>
  )
}
