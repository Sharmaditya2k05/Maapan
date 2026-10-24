'use client'

/**
 * F2 — collect payment and save the bill. Cash (quick notes, change due), UPI
 * (static QR already paid, or a lenden QR polled until paid), card, credit for
 * saved customers. Mixed tenders allowed; non-cash can never exceed the total
 * (invoice.Settle), the remainder goes on account.
 */
import { useCallback, useEffect, useMemo, useState } from 'react'
import { Banknote, CreditCard, HandCoins, QrCode, Trash2, WalletCards } from 'lucide-react'
import { Button, ErrorBox, Field, IconButton, Input, Kbd, Modal, Money, MoneyInput } from '@/components/ui'
import { fmtDate, todayIST } from '@/lib/hooks'
import { post } from '@/lib/api'
import type { Party, Tender, TenderMode } from '@/lib/types'
import type { CreditLimitDetails, PaymentRequestFull, PaymentRequestInput } from '@/lib/types.pos'
import { balanceDue, settle } from '@/lib/pos/cart'
import { lendenPhone, TENDER_LABEL } from '@/lib/pos/invoice'
import { UpiQrDialog } from '@/components/invoice/UpiQrDialog'

type Tab = 'cash' | 'upi' | 'card' | 'credit'
const QUICK = [10000, 20000, 50000, 200000]

export function TenderDialog({ open, onClose, total, party, storeId, defaultMode, askPhone, saving, error, creditDetails, canOverride, onComplete }: {
  open: boolean
  onClose: () => void
  total: number
  party: Party | null
  storeId: string
  defaultMode: 'cash' | 'upi' | 'card'
  askPhone: boolean
  saving: boolean
  error: string | null
  creditDetails: CreditLimitDetails | null
  canOverride: boolean
  onComplete: (tenders: Tender[], opts: { phone?: string; override?: boolean }) => void
}) {
  const [tenders, setTenders] = useState<Tender[]>([])
  const [tab, setTab] = useState<Tab>(defaultMode)
  const [amount, setAmount] = useState(0)
  const [ref, setRef] = useState('')
  const [upiMode, setUpiMode] = useState<'static' | 'qr'>('qr')
  const [phone, setPhone] = useState('')
  const [qrOpen, setQrOpen] = useState(false)
  const [override, setOverride] = useState(false)
  const [localErr, setLocalErr] = useState<string | null>(null)

  const s = useMemo(() => settle(total, tenders), [total, tenders])
  const due = balanceDue(total, tenders)

  useEffect(() => {
    if (!open) return
    setTenders([]); setTab(defaultMode); setAmount(total); setRef(''); setUpiMode('qr'); setPhone(''); setOverride(false); setLocalErr(null)
  }, [open, defaultMode, total])
  useEffect(() => { setAmount(due); setRef('') }, [tab, due])

  const add = (mode: TenderMode, amt: number, extra: Partial<Tender> = {}) => {
    setLocalErr(null)
    if (!(amt > 0)) return setLocalErr('Enter an amount')
    if (mode === 'credit' && tenders.some((t) => t.mode === 'credit')) return setLocalErr('Only one credit tender per bill')
    const next = [...tenders, { mode, amount_paise: Math.round(amt), ...extra }]
    const chk = settle(total, next)
    if (chk.error) return setLocalErr('Card, UPI and bank tenders together cannot exceed the bill total')
    setTenders(next)
  }
  const remove = (i: number) => setTenders(tenders.filter((_, j) => j !== i))

  const phoneOk = lendenPhone(phone || party?.phone)
  const createRequest = useCallback(async (): Promise<PaymentRequestFull> => {
    const body: PaymentRequestInput = { amount_paise: amount, purpose: 'on_account', kind: 'upi_qr', store_id: storeId }
    if (party?.id) body.party_id = party.id
    const ph = lendenPhone(phone || party?.phone)
    if (ph) body.party_phone = ph
    return post<PaymentRequestFull>('/lenden/v1/payment-requests', body)
  }, [amount, storeId, party, phone])

  const complete = () => {
    setLocalErr(null)
    if (s.error) return setLocalErr(s.error)
    if (!party && s.credit > 0 && tenders.length > 0) return setLocalErr('A walk-in customer cannot be billed on credit: collect the balance or add a saved customer.')
    onComplete(tenders, { phone: phone.trim() || undefined, override })
  }

  const tabs: { id: Tab; label: string; icon: typeof Banknote; disabled?: boolean }[] = [
    { id: 'cash', label: 'Cash', icon: Banknote }, { id: 'upi', label: 'UPI', icon: QrCode }, { id: 'card', label: 'Card', icon: CreditCard },
    { id: 'credit', label: 'Credit', icon: HandCoins, disabled: !party },
  ]
  const dueDate = party?.credit_days ? fmtDate(new Date(Date.now() + party.credit_days * 864e5 + 5.5 * 3600_000).toISOString().slice(0, 10)) : fmtDate(todayIST())

  return (
    <Modal open={open} onClose={onClose} title="Collect payment" size="lg"
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={saving}>Back <Kbd>Esc</Kbd></Button>
          <Button variant="gold" size="lg" onClick={complete} loading={saving} disabled={!!s.error} className="min-w-48">
            {s.credit > 0 && tenders.length > 0 ? <>Save with <Money paise={s.credit} /> on credit</> : tenders.length === 0 ? 'Save unpaid' : 'Complete sale'}
          </Button>
        </>
      }>
      <div className="grid gap-4 md:grid-cols-[1fr_260px]">
        <div>
          <div className="flex items-end justify-between rounded-2xl bg-gradient-to-br from-brand-700 to-brand-900 p-4 text-white">
            <div><div className="text-xs text-brand-200">Grand total</div><Money paise={total} className="text-4xl font-extrabold tracking-tight" /></div>
            <div className="text-right">
              {s.change > 0 ? <><div className="text-xs text-brand-200">Change due</div><Money paise={s.change} className="text-2xl font-bold text-amber-300" /></>
                : <><div className="text-xs text-brand-200">{due > 0 ? 'Balance' : 'Collected'}</div><Money paise={due > 0 ? due : s.paid} className="text-2xl font-bold" /></>}
            </div>
          </div>

          <div className="mt-3 flex gap-1 overflow-x-auto rounded-xl bg-slate-100 p-1">
            {tabs.map((t) => (
              <button key={t.id} type="button" disabled={t.disabled} onClick={() => setTab(t.id)} title={t.disabled ? 'Add a saved customer to sell on credit' : undefined}
                className={`inline-flex flex-1 items-center justify-center gap-1.5 whitespace-nowrap rounded-lg px-3 py-2 text-sm font-semibold transition cursor-pointer disabled:cursor-not-allowed disabled:opacity-40 ${tab === t.id ? 'bg-white text-ink shadow-sm' : 'text-slate-500 hover:text-ink'}`}>
                <t.icon size={15} /> {t.label}
              </button>
            ))}
          </div>

          <div className="mt-3 space-y-3">
            {tab === 'cash' && (
              <>
                <div className="flex flex-wrap gap-2">
                  <Button variant="secondary" size="sm" onClick={() => add('cash', due)} disabled={due <= 0}>Exact <Money paise={due} /></Button>
                  {QUICK.map((p) => <Button key={p} variant="secondary" size="sm" onClick={() => add('cash', p)}>₹{p / 100}</Button>)}
                </div>
                <Field label="Cash received">
                  <div className="flex gap-2">
                    <div className="flex-1"><MoneyInput autoFocus value={amount} onChange={setAmount} onKeyDown={(e) => { if (e.key === 'Enter') { e.preventDefault(); if (amount > 0 && (tenders.length === 0 || due > 0)) add('cash', amount); else complete() } }} /></div>
                    <Button onClick={() => add('cash', amount)}>Add</Button>
                  </div>
                </Field>
                <p className="text-xs text-slate-500">Tip: type the note handed over and press <Kbd>Enter</Kbd> — change is worked out for you. <Kbd>Enter</Kbd> again saves the bill.</p>
              </>
            )}
            {tab === 'upi' && (
              <>
                <div className="inline-flex rounded-xl bg-slate-100 p-1 text-xs font-semibold">
                  <button type="button" onClick={() => setUpiMode('qr')} className={`rounded-lg px-3 py-1.5 cursor-pointer ${upiMode === 'qr' ? 'bg-white shadow-sm' : 'text-slate-500'}`}>Show QR</button>
                  <button type="button" onClick={() => setUpiMode('static')} className={`rounded-lg px-3 py-1.5 cursor-pointer ${upiMode === 'static' ? 'bg-white shadow-sm' : 'text-slate-500'}`}>Static QR / already paid</button>
                </div>
                <Field label="Amount"><MoneyInput value={amount} onChange={setAmount} /></Field>
                {upiMode === 'static' ? (
                  <>
                    <Field label="UPI reference (UTR)" hint="From the customer's payment confirmation"><Input value={ref} onChange={(e) => setRef(e.target.value)} className="font-mono" onKeyDown={(e) => e.key === 'Enter' && add('upi', amount, { reference: ref.trim() || undefined })} /></Field>
                    <Button onClick={() => add('upi', amount, { reference: ref.trim() || undefined })} icon={QrCode}>Mark paid by UPI</Button>
                  </>
                ) : (
                  <>
                    {!party && (
                      <Field label="Customer mobile" hint="lenden needs a party or a mobile number for the request" error={phone && !phoneOk ? 'Enter a 10-digit Indian mobile' : undefined}>
                        <Input inputMode="tel" value={phone} onChange={(e) => setPhone(e.target.value)} placeholder="98765 43210" />
                      </Field>
                    )}
                    <Button icon={QrCode} onClick={() => setQrOpen(true)} disabled={!(amount > 0) || (!party?.id && !phoneOk)}>Show QR for <Money paise={amount} /></Button>
                  </>
                )}
              </>
            )}
            {tab === 'card' && (
              <>
                <Field label="Amount"><MoneyInput autoFocus value={amount} onChange={setAmount} /></Field>
                <Field label="Approval / reference (optional)"><Input value={ref} onChange={(e) => setRef(e.target.value)} className="font-mono" onKeyDown={(e) => e.key === 'Enter' && add('card', amount, { reference: ref.trim() || undefined })} /></Field>
                <Button onClick={() => add('card', amount, { reference: ref.trim() || undefined })} icon={CreditCard}>Add card payment</Button>
              </>
            )}
            {tab === 'credit' && party && (
              <>
                <Field label="On account" hint={<>Due {dueDate}{party.credit_days ? ` (${party.credit_days} credit days)` : ''}{party.credit_limit_paise > 0 ? <> · limit <Money paise={party.credit_limit_paise} />, balance <Money paise={party.balance_paise} signed /></> : ''}</>}>
                  <MoneyInput autoFocus value={amount} onChange={setAmount} />
                </Field>
                <Button onClick={() => add('credit', amount)} icon={HandCoins}>Put on {party.name}&apos;s account</Button>
              </>
            )}
          </div>
          {askPhone && !party && tab !== 'upi' && (
            <Field label="Customer mobile (optional)" className="mt-3" hint="For a WhatsApp copy of the bill"><Input inputMode="tel" value={phone} onChange={(e) => setPhone(e.target.value)} placeholder="98765 43210" /></Field>
          )}
        </div>

        <div className="flex flex-col rounded-2xl border border-slate-200 bg-slate-50 p-3">
          <div className="mb-2 flex items-center gap-2 text-xs font-bold uppercase tracking-wider text-slate-500"><WalletCards size={14} /> Tenders</div>
          {tenders.length === 0 ? <p className="text-sm text-slate-500">Nothing collected yet{party ? ' — the full amount goes on account when you save.' : '.'}</p> : (
            <ul className="space-y-1.5">
              {tenders.map((t, i) => (
                <li key={i} className="flex items-center justify-between gap-2 rounded-xl bg-white px-3 py-2 text-sm shadow-sm">
                  <div className="min-w-0"><div className="font-semibold">{TENDER_LABEL[t.mode]}</div>{t.reference && <div className="truncate font-mono text-xs text-slate-500">{t.reference}</div>}</div>
                  <Money paise={t.amount_paise} className="font-semibold" />
                  <IconButton icon={Trash2} label="Remove tender" onClick={() => remove(i)} className="h-8 w-8" />
                </li>
              ))}
            </ul>
          )}
          <dl className="mt-auto space-y-1 border-t border-slate-200 pt-3 text-sm">
            <div className="flex justify-between text-slate-600"><dt>Paid</dt><dd><Money paise={s.paid} /></dd></div>
            {s.change > 0 && <div className="flex justify-between font-semibold text-amber-700"><dt>Change</dt><dd><Money paise={s.change} /></dd></div>}
            {s.credit > 0 && <div className="flex justify-between font-semibold text-rose-700"><dt>On credit</dt><dd><Money paise={s.credit} /></dd></div>}
          </dl>
        </div>
      </div>

      {(localErr || error) && <div className="mt-4"><ErrorBox message={localErr ?? error ?? ''} /></div>}
      {creditDetails && (
        <div className="mt-3 rounded-xl border border-amber-200 bg-amber-50 p-3 text-sm text-amber-900">
          <div className="font-semibold">Credit limit reached</div>
          <div className="mt-1 grid grid-cols-3 gap-2 text-xs">
            <div>Limit<br /><Money paise={creditDetails.credit_limit_paise} className="font-semibold" /></div>
            <div>Balance<br /><Money paise={creditDetails.balance_paise} signed className="font-semibold" /></div>
            <div>This bill<br /><Money paise={creditDetails.bill_credit_paise} className="font-semibold" /></div>
          </div>
          {canOverride ? (
            <label className="mt-2 flex items-center gap-2 text-sm"><input type="checkbox" checked={override} onChange={(e) => setOverride(e.target.checked)} /> Override the credit limit and save anyway</label>
          ) : <p className="mt-2 text-xs">Collect the balance now, or ask a manager to bill past the limit.</p>}
        </div>
      )}

      <UpiQrDialog open={qrOpen} onClose={() => setQrOpen(false)} amountPaise={amount} create={createRequest}
        onPaid={(r) => { add('upi', r.amount_paise, { payment_request_id: r.id, reference: r.gateway_ref }); setQrOpen(false) }} />
    </Modal>
  )
}
