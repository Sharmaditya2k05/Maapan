'use client'

/**
 * Record money in or out against a party.
 *  - receipt  → lenden POST /v1/payments/manual (cash / cheque / bank / upi / card; idempotent on a key we keep
 *               for retries of the same submission and renew as soon as the draft changes or the receipt is saved)
 *  - payment  → bahi POST /v1/vouchers type "payment" (Dr party, Cr cash/bank account) — lenden only models receipts.
 */
import { useEffect, useMemo, useState } from 'react'
import { ArrowDownLeft, ArrowUpRight } from 'lucide-react'
import { Button, Field, Input, Modal, MoneyInput, Select, Textarea } from '@/components/ui'
import { post } from '@/lib/api'
import { fmtDate, todayIST, useList, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import type { Invoice, Party } from '@/lib/types'
import type { ManualReceiptInput, ManualVoucherInput, Payment, VoucherView } from '@/lib/types.ledger'
import { formatINR } from '@/lib/money'
import { AccountPicker, type LedgerTarget } from './AccountPicker'
import { PartyPicker } from './PartyPicker'
import { NoPermission } from './bits'
import { useAction } from './useAction'

export type ReceiptDirection = 'receipt' | 'payment'
type Method = ManualReceiptInput['method']

export interface ReceiptModalProps {
  open: boolean
  onClose: () => void
  /** Pre-selected party (the picker is hidden when given). */
  party?: Party | null
  /** Pre-selected invoice to settle. */
  invoice?: Invoice | null
  direction?: ReceiptDirection
  /** Hide the receipt/payment switch. */
  lockDirection?: boolean
  onSaved?: (result: Payment | VoucherView, direction: ReceiptDirection) => void
}

const METHODS: { value: Method; label: string }[] = [
  { value: 'cash', label: 'Cash' }, { value: 'upi', label: 'UPI' }, { value: 'bank', label: 'Bank transfer (NEFT/IMPS/RTGS)' },
  { value: 'cheque', label: 'Cheque' }, { value: 'card', label: 'Card (own terminal)' },
]

export function ReceiptModal({ open, onClose, party: fixedParty, invoice: fixedInvoice, direction: initialDirection, lockDirection, onSaved }: ReceiptModalProps) {
  const canDo = usePerm()
  const [direction, setDirection] = useState<ReceiptDirection>(initialDirection ?? (fixedParty?.type === 'supplier' ? 'payment' : 'receipt'))
  const [party, setParty] = useState<Party | null>(fixedParty ?? null)
  const [amount, setAmount] = useState(fixedInvoice?.balance_paise ?? 0)
  const [method, setMethod] = useState<Method>('cash')
  const [reference, setReference] = useState('')
  const [date, setDate] = useState(todayIST())
  const [invoiceId, setInvoiceId] = useState(fixedInvoice?.id ?? '')
  const [notes, setNotes] = useState('')
  const [account, setAccount] = useState<LedgerTarget | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [idemKey, setIdemKey] = useState(() => crypto.randomUUID())

  // Reset the draft each time the modal opens.
  useEffect(() => {
    if (!open) return
    setDirection(initialDirection ?? (fixedParty?.type === 'supplier' ? 'payment' : 'receipt'))
    setParty(fixedParty ?? null)
    setAmount(fixedInvoice?.balance_paise ?? 0)
    setMethod('cash'); setReference(''); setDate(todayIST()); setInvoiceId(fixedInvoice?.id ?? ''); setNotes(''); setAccount(null); setError(null)
    setIdemKey(crypto.randomUUID())
  }, [open, fixedParty, fixedInvoice, initialDirection])

  // A changed draft is a different submission: new key. An unchanged retry reuses the old one.
  const draftSig = `${party?.id}|${amount}|${method}|${reference}|${date}|${invoiceId}|${notes}`
  useEffect(() => { setIdemKey(crypto.randomUUID()) }, [draftSig])

  const isReceipt = direction === 'receipt'
  const docType = isReceipt ? 'sale' : 'purchase'
  const openInvoices = useList<Invoice>(open && party && canDo(PERMS.invoiceRead) && !fixedInvoice ? '/aadesh/v1/invoices' : null,
    { party_id: party?.id, doc_type: docType, status: 'posted', limit: 100 })
  const unpaid = useMemo(() => (openInvoices.data?.items ?? []).filter((i) => i.balance_paise > 0), [openInvoices.data])
  const chosenInvoice = fixedInvoice ?? unpaid.find((i) => i.id === invoiceId)

  const receipt = useAction<Payment, ManualReceiptInput>((body) => post<Payment>('/lenden/v1/payments/manual', body), {
    invalidate: ['/lenden/v1/payments', '/bahi/v1/parties', '/aadesh/v1/invoices', '/bahi/v1/reports', '/bahi/v1/cashbook', '/bahi/v1/bankbook', '/bahi/v1/vouchers'],
    successMessage: 'Receipt recorded',
    onSuccess: (p) => { setIdemKey(crypto.randomUUID()); onSaved?.(p, 'receipt'); onClose() },
  })
  const payment = useAction<VoucherView, ManualVoucherInput>((body) => post<VoucherView>('/bahi/v1/vouchers', body), {
    invalidate: ['/bahi/v1/parties', '/bahi/v1/vouchers', '/bahi/v1/reports', '/bahi/v1/cashbook', '/bahi/v1/bankbook', '/bahi/v1/daybook'],
    successMessage: 'Payment voucher posted',
    onSuccess: (v) => { onSaved?.(v, 'payment'); onClose() },
  })
  const busy = receipt.isPending || payment.isPending

  const submit = async () => {
    setError(null)
    if (!party) return setError('Pick a party first.')
    if (amount <= 0) return setError('Enter an amount.')
    if (isReceipt) {
      if (method === 'cheque' && !reference.trim()) return setError('Cheque receipts need the cheque number.')
      const body: ManualReceiptInput = {
        party_id: party.id, amount_paise: amount, method, idempotency_key: idemKey, received_at: date,
        ...(chosenInvoice ? { invoice_id: chosenInvoice.id } : {}),
        ...(reference.trim() ? { reference: reference.trim() } : {}),
        ...(notes.trim() ? { notes: notes.trim() } : {}),
      }
      await receipt.mutateAsync(body).catch(() => undefined)
    } else {
      if (!account?.account_id) return setError('Pick the cash or bank account the money went out of.')
      const body: ManualVoucherInput = {
        type: 'payment', date, narration: notes.trim() || `Payment to ${party.name}${chosenInvoice ? ` against ${chosenInvoice.number}` : ''}`,
        reference: reference.trim() || undefined,
        entries: [{ party_id: party.id, debit_paise: amount, credit_paise: 0 }, { account_id: account.account_id, debit_paise: 0, credit_paise: amount }],
      }
      await payment.mutateAsync(body).catch(() => undefined)
    }
  }

  const allowed = isReceipt ? canDo(PERMS.paymentCollect) : canDo(PERMS.ledgerWrite)
  const title = isReceipt ? 'Record receipt' : 'Record payment'

  return (
    <Modal open={open} onClose={onClose} title={title}
      footer={allowed ? <><Button variant="secondary" onClick={onClose}>Cancel</Button><Button onClick={submit} loading={busy} icon={isReceipt ? ArrowDownLeft : ArrowUpRight}>{isReceipt ? `Receive ${amount ? formatINR(amount) : ''}` : `Pay ${amount ? formatINR(amount) : ''}`}</Button></> : undefined}>
      {!allowed ? <NoPermission what={isReceipt ? 'collecting payments' : 'posting vouchers'} /> : (
        <div className="space-y-4">
          {!lockDirection && (
            <div className="inline-flex rounded-xl bg-slate-100 p-1 text-sm font-semibold">
              {(['receipt', 'payment'] as const).map((d) => (
                <button key={d} type="button" onClick={() => { setDirection(d); setInvoiceId(''); setAccount(null) }}
                  className={`flex items-center gap-1.5 rounded-lg px-3 py-1.5 cursor-pointer ${direction === d ? 'bg-white text-ink shadow-sm' : 'text-slate-500'}`}>
                  {d === 'receipt' ? <ArrowDownLeft size={14} /> : <ArrowUpRight size={14} />}{d === 'receipt' ? 'Money in' : 'Money out'}
                </button>
              ))}
            </div>
          )}
          {!fixedParty && (
            <Field label="Party"><PartyPicker value={party} onChange={(p) => { setParty(p); setInvoiceId('') }} autoFocus type={isReceipt ? undefined : 'supplier'} /></Field>
          )}
          {fixedParty && <div className="rounded-xl bg-slate-50 px-4 py-3 text-sm"><span className="text-slate-500">{isReceipt ? 'From' : 'To'}</span> <span className="font-semibold">{fixedParty.name}</span></div>}
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Amount"><MoneyInput value={amount} onChange={setAmount} autoFocus={!!fixedParty} /></Field>
            <Field label="Date"><Input type="date" value={date} max={todayIST()} onChange={(e) => setDate(e.target.value)} /></Field>
          </div>
          {isReceipt ? (
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Method"><Select value={method} onChange={(e) => setMethod(e.target.value as Method)}>{METHODS.map((m) => <option key={m.value} value={m.value}>{m.label}</option>)}</Select></Field>
              <Field label={method === 'cheque' ? 'Cheque number' : 'Reference'} hint={method === 'cheque' ? 'Required for cheques' : 'UTR, UPI ref, card slip…'}>
                <Input value={reference} onChange={(e) => setReference(e.target.value)} placeholder={method === 'cheque' ? '000123' : 'Optional'} />
              </Field>
            </div>
          ) : (
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Paid from"><AccountPicker value={account} onChange={setAccount} filter={(a) => a.is_cash || a.is_bank} placeholder="Cash or bank account" /></Field>
              <Field label="Reference" hint="Cheque no, UTR…"><Input value={reference} onChange={(e) => setReference(e.target.value)} placeholder="Optional" /></Field>
            </div>
          )}
          {party && !fixedInvoice && isReceipt && canDo(PERMS.invoiceRead) && (
            <Field label="Against invoice" hint={unpaid.length === 0 ? 'No unpaid invoices — this is an on-account receipt.' : 'Optional. Leave blank for an on-account receipt.'}>
              <Select value={invoiceId} onChange={(e) => { setInvoiceId(e.target.value); const inv = unpaid.find((i) => i.id === e.target.value); if (inv && !amount) setAmount(inv.balance_paise) }} disabled={unpaid.length === 0}>
                <option value="">On account</option>
                {unpaid.map((i) => <option key={i.id} value={i.id}>{i.number} · {fmtDate(i.invoice_date)} · due {formatINR(i.balance_paise)}</option>)}
              </Select>
            </Field>
          )}
          {fixedInvoice && <div className="rounded-xl bg-slate-50 px-4 py-3 text-sm">Against <span className="font-mono font-semibold">{fixedInvoice.number}</span> · due <span className="num font-semibold">{formatINR(fixedInvoice.balance_paise)}</span></div>}
          {chosenInvoice && amount > chosenInvoice.balance_paise && <p className="text-xs text-amber-700">More than the invoice balance — the excess will sit on the party’s account.</p>}
          <Field label="Notes"><Textarea rows={2} value={notes} onChange={(e) => setNotes(e.target.value)} placeholder={isReceipt ? 'Optional' : 'Narration (optional)'} /></Field>
          {error && <p className="text-sm text-rose-600">{error}</p>}
        </div>
      )}
    </Modal>
  )
}
