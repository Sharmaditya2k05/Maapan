'use client'

import { Suspense, useEffect, useMemo, useState } from 'react'
import Link from 'next/link'
import QRCode from 'qrcode'
import { Copy, Link2, MessageCircle, Plus, QrCode, RotateCcw, Wallet, Landmark, Download } from 'lucide-react'
import { Badge, Button, Card, Empty, Field, Input, Modal, Money, MoneyInput, PageHeader, Pagination, QueryState, Select, Skeleton, statusTone, Table, Tabs, useToast } from '@/components/ui'
import { post } from '@/lib/api'
import { downloadFile, fmtDate, todayIST, useGet, useList, usePerm, useStore } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import { formatINR } from '@/lib/money'
import type { Party } from '@/lib/types'
import type { GatewayList, Payment, PaymentRequestInput, PaymentRequestView, RefundInput, RefundResponse, Settlement } from '@/lib/types.ledger'
import { csvRupees, toCSV } from '@/lib/ledger/csv'
import { presetRange, refundStateText, toE164IN, waLink, type DateRange } from '@/lib/ledger/util'
import { DateRangePicker } from '@/components/ledger/DateRangePicker'
import { PartyPicker } from '@/components/ledger/PartyPicker'
import { ReceiptModal } from '@/components/ledger/ReceiptModal'
import { KV, MobileRow, NoPermission, useQueryParam, useTabParam } from '@/components/ledger/bits'
import { useAction } from '@/components/ledger/useAction'

const TABS = ['received', 'links', 'settlements'] as const
type Tab = (typeof TABS)[number]

const METHODS = ['cash', 'upi', 'card', 'bank', 'cheque']
const paymentTone = (s: Payment['status']) => statusTone(s === 'captured' ? 'paid' : s === 'refunded' ? 'cancelled' : 'partial')

export default function PaymentsPage() {
  return <Suspense fallback={<Skeleton />}><PaymentsInner /></Suspense>
}

function PaymentsInner() {
  const canDo = usePerm()
  const [tab, setTab] = useTabParam<Tab>(TABS, 'received')
  const [paymentId, setPaymentId] = useQueryParam('payment')
  const [receipt, setReceipt] = useState(false)

  if (!canDo(PERMS.paymentCollect)) return <><PageHeader title="Payments" /><Card><NoPermission what="payments" /></Card></>

  return (
    <>
      <PageHeader title="Payments" subtitle="Money received at the counter, online and by hand"
        actions={<Button icon={Plus} onClick={() => setReceipt(true)}>Record manual receipt</Button>} />
      <div className="mb-4"><Tabs value={tab} onChange={setTab} options={[{ value: 'received', label: 'Received' }, { value: 'links', label: 'Links & QR' }, { value: 'settlements', label: 'Settlements' }]} /></div>
      {tab === 'received' && <ReceivedTab onOpen={setPaymentId} />}
      {tab === 'links' && <LinksTab />}
      {tab === 'settlements' && <SettlementsTab />}
      <PaymentDrawer id={paymentId} onClose={() => setPaymentId(null)} />
      <ReceiptModal open={receipt} onClose={() => setReceipt(false)} direction="receipt" lockDirection />
    </>
  )
}

/* ---------------- Received ---------------- */

function useGateways() {
  const q = useGet<GatewayList>('/lenden/v1/gateways')
  return useMemo(() => ['manual', ...(q.data?.available ?? [])].filter((g, i, a) => a.indexOf(g) === i), [q.data])
}

function ReceivedTab({ onOpen }: { onOpen: (id: string) => void }) {
  const [status, setStatus] = useState('')
  const [method, setMethod] = useState('')
  const [gateway, setGateway] = useState('')
  const [range, setRange] = useState<DateRange>(() => presetRange('this_month', todayIST()))
  const [offset, setOffset] = useState(0)
  const gateways = useGateways()
  const list = useList<Payment>('/lenden/v1/payments', { status, method, gateway, from: range.from, to: range.to, limit: 50, offset })
  useEffect(() => setOffset(0), [status, method, gateway, range.from, range.to])

  const exportCSV = () => downloadFile(`payments-${range.from}-${range.to}.csv`, toCSV(['Captured at', 'Party / invoice', 'Method', 'Gateway', 'Reference', 'Amount', 'Refunded', 'Status'],
    (list.data?.items ?? []).map((p) => [p.captured_at, p.invoice_id ?? p.party_id ?? p.party_phone ?? '', p.method, p.gateway, p.gateway_ref ?? '', csvRupees(p.amount_paise), csvRupees(p.refunded_paise), p.status])), 'text/csv')

  return (
    <Card padded={false}>
      <div className="flex flex-wrap items-center gap-2 border-b border-slate-100 p-3 sm:p-4">
        <DateRangePicker value={range} onChange={setRange} />
        <Select value={status} onChange={(e) => setStatus(e.target.value)} className="w-auto input-sm" aria-label="Status"><option value="">All statuses</option><option value="captured">Captured</option><option value="partially_refunded">Partially refunded</option><option value="refunded">Refunded</option></Select>
        <Select value={method} onChange={(e) => setMethod(e.target.value)} className="w-auto input-sm" aria-label="Method"><option value="">All methods</option>{METHODS.map((m) => <option key={m} value={m}>{m}</option>)}</Select>
        <Select value={gateway} onChange={(e) => setGateway(e.target.value)} className="w-auto input-sm" aria-label="Gateway"><option value="">All gateways</option>{gateways.map((g) => <option key={g} value={g}>{g}</option>)}</Select>
        <Button size="sm" variant="secondary" icon={Download} className="ml-auto" onClick={exportCSV} disabled={!list.data?.items.length}>CSV</Button>
      </div>
      <QueryState isLoading={list.isLoading} error={list.error} data={list.data} retry={() => list.refetch()}>
        {(d) => d.items.length === 0 ? <Empty icon={Wallet} title="No payments in this period" text="Receipts recorded by hand and online captures both show up here." /> : (
          <>
            <div className="divide-y divide-slate-100 md:hidden">
              {d.items.map((p) => (
                <MobileRow key={p.id} onClick={() => onOpen(p.id)} title={<span className="flex items-center gap-1.5"><Badge>{p.method}</Badge><span className="text-xs text-slate-500">{p.gateway}</span></span>}
                  subtitle={`${fmtDate(p.captured_at, true)}${p.gateway_ref ? ` · ${p.gateway_ref}` : ''}`}
                  right={<Money paise={p.amount_paise} />} rightSub={p.refunded_paise ? `refunded ${formatINR(p.refunded_paise)}` : p.status} />
              ))}
            </div>
            <div className="hidden md:block">
              <Table>
                <thead className="bg-slate-50/60"><tr><th className="th">Date</th><th className="th">Party / invoice</th><th className="th">Method</th><th className="th">Gateway</th><th className="th text-right">Amount</th><th className="th">Status</th><th className="th text-right">Refunded</th></tr></thead>
                <tbody className="divide-y divide-slate-100">
                  {d.items.map((p) => (
                    <tr key={p.id} onClick={() => onOpen(p.id)} className="cursor-pointer hover:bg-brand-50/40">
                      <td className="td whitespace-nowrap text-slate-600">{fmtDate(p.captured_at, true)}</td>
                      <td className="td">
                        {p.party_id ? <Link href={`/app/parties/${p.party_id}`} className="font-semibold hover:underline" onClick={(e) => e.stopPropagation()}>Party</Link> : <span className="text-slate-500">{p.party_phone || 'Walk-in'}</span>}
                        {p.invoice_id && <> · <Link href={`/app/sales/${p.invoice_id}`} className="font-mono text-xs text-brand-700 hover:underline" onClick={(e) => e.stopPropagation()}>invoice</Link></>}
                        {p.gateway_ref && <div className="font-mono text-[11px] text-slate-400">{p.gateway_ref}</div>}
                      </td>
                      <td className="td"><Badge>{p.method}</Badge></td>
                      <td className="td text-slate-600">{p.gateway}</td>
                      <td className="td num text-right font-semibold"><Money paise={p.amount_paise} />{(p.short_paise > 0 || p.excess_paise > 0) && <div className="text-[11px] font-normal text-amber-700">{p.short_paise > 0 ? `short ${formatINR(p.short_paise)}` : `excess ${formatINR(p.excess_paise)}`}</div>}</td>
                      <td className="td"><Badge tone={paymentTone(p.status)}>{p.status.replace('_', ' ')}</Badge></td>
                      <td className="td num text-right text-slate-600">{p.refunded_paise ? <Money paise={p.refunded_paise} /> : '—'}</td>
                    </tr>
                  ))}
                </tbody>
              </Table>
            </div>
            <Pagination total={d.total} limit={d.limit} offset={d.offset} onChange={setOffset} />
          </>
        )}
      </QueryState>
    </Card>
  )
}

/* ---------------- Drawer with refund ---------------- */

function PaymentDrawer({ id, onClose }: { id: string | null; onClose: () => void }) {
  const canDo = usePerm()
  const q = useGet<Payment>(id ? `/lenden/v1/payments/${id}` : null)
  const p = q.data
  const [refunding, setRefunding] = useState(false)
  const [amount, setAmount] = useState(0)
  const [reason, setReason] = useState('')
  const [result, setResult] = useState<RefundResponse | null>(null)
  useEffect(() => { setRefunding(false); setReason(''); setResult(null); setAmount(0) }, [id])
  useEffect(() => { if (p) setAmount(p.amount_paise - p.refunded_paise) }, [p])

  const refund = useAction<RefundResponse, { id: string; body: RefundInput }>((i) => post<RefundResponse>(`/lenden/v1/payments/${i.id}/refund`, i.body), {
    invalidate: ['/lenden/v1/payments', '/bahi/v1/parties', '/aadesh/v1/invoices'],
    onSuccess: (r) => { setResult(r); setRefunding(false) },
  })
  const refundable = p ? p.amount_paise - p.refunded_paise : 0
  const canRefund = canDo(PERMS.invoiceCancel)

  return (
    <Modal open={!!id} onClose={onClose} title={p ? <span className="flex items-center gap-2">Payment <Badge tone={paymentTone(p.status)}>{p.status.replace('_', ' ')}</Badge></span> : 'Payment'}
      footer={<>
        {p && refundable > 0 && canRefund && !refunding && !result && <Button variant="danger" icon={RotateCcw} onClick={() => setRefunding(true)}>Refund</Button>}
        <Button variant="secondary" onClick={onClose}>Close</Button>
      </>}>
      <QueryState isLoading={q.isLoading} error={q.error} data={p} retry={() => q.refetch()}>
        {(pm) => (
          <div className="space-y-4">
            <div className="rounded-2xl bg-slate-50 p-4 text-center">
              <div className="text-xs text-slate-500">Received</div>
              <div className="num text-3xl font-extrabold"><Money paise={pm.amount_paise} /></div>
              <div className="mt-1 text-xs text-slate-500">{fmtDate(pm.captured_at, true)} · {pm.method} via {pm.gateway}</div>
            </div>
            <dl className="divide-y divide-slate-100">
              {pm.party_id && <KV k="Party" v={<Link href={`/app/parties/${pm.party_id}`} className="link">Open party ledger</Link>} />}
              {pm.party_phone && <KV k="Phone" v={pm.party_phone} />}
              {pm.invoice_id && <KV k="Invoice" v={<Link href={`/app/sales/${pm.invoice_id}`} className="link">Open invoice</Link>} />}
              {pm.request_id && <KV k="Payment request" v={pm.request_id} mono />}
              {pm.gateway_ref && <KV k="Gateway reference" v={pm.gateway_ref} mono />}
              {pm.short_paise > 0 && <KV k="Short of request" v={<Money paise={pm.short_paise} />} />}
              {pm.excess_paise > 0 && <KV k="Over the request" v={<Money paise={pm.excess_paise} />} />}
              <KV k="Refunded" v={<Money paise={pm.refunded_paise} />} />
              {pm.notes && <KV k="Notes" v={pm.notes} />}
              {pm.idempotency_key && <KV k="Idempotency key" v={pm.idempotency_key} mono />}
            </dl>
            {!canRefund && refundable > 0 && <p className="text-xs text-slate-500">Refunds need the invoice-cancel permission (store manager or owner).</p>}
            {refunding && (
              <div className="space-y-3 rounded-xl border border-rose-100 bg-rose-50/50 p-4">
                <div className="text-sm font-bold text-rose-800">Refund {pm.gateway === 'manual' ? '(recorded by hand — return the money yourself)' : `through ${pm.gateway}`}</div>
                <div className="grid gap-3 sm:grid-cols-2">
                  <Field label="Amount" hint={`Up to ${formatINR(refundable)}`}><MoneyInput value={amount} onChange={setAmount} /></Field>
                  <Field label="Reason"><Input value={reason} onChange={(e) => setReason(e.target.value)} placeholder="Goods returned, duplicate payment…" /></Field>
                </div>
                <div className="flex justify-end gap-2">
                  <Button size="sm" variant="secondary" onClick={() => setRefunding(false)}>Cancel</Button>
                  <Button size="sm" variant="danger" loading={refund.isPending} disabled={!reason.trim() || amount <= 0 || amount > refundable}
                    onClick={() => refund.mutate({ id: pm.id, body: { amount_paise: amount, reason: reason.trim() } })}>Refund {formatINR(amount)}</Button>
                </div>
              </div>
            )}
            {result && <RefundOutcome r={result} />}
          </div>
        )}
      </QueryState>
    </Modal>
  )
}

function RefundOutcome({ r }: { r: RefundResponse }) {
  const s = refundStateText(r.refund.status)
  const tone = s.tone === 'green' ? 'bg-emerald-50 text-emerald-800' : s.tone === 'amber' ? 'bg-amber-50 text-amber-800' : s.tone === 'red' ? 'bg-rose-50 text-rose-800' : 'bg-slate-50 text-slate-700'
  return (
    <div className={`rounded-xl px-4 py-3 text-sm ${tone}`}>
      <div className="font-bold">{s.title} · {formatINR(r.refund.amount_paise)}</div>
      <p className="mt-0.5 text-xs">{s.text}</p>
      {r.refund.last_error && <p className="mt-1 font-mono text-[11px]">{r.refund.last_error}</p>}
      {r.refund.gateway_ref && <p className="mt-1 font-mono text-[11px]">Gateway ref {r.refund.gateway_ref}</p>}
    </div>
  )
}

/* ---------------- Links & QR ---------------- */

function LinksTab() {
  const toast = useToast()
  const { storeId } = useStore()
  const [party, setParty] = useState<Party | null>(null)
  const [phone, setPhone] = useState('')
  const [amount, setAmount] = useState(0)
  const [kind, setKind] = useState<'payment_link' | 'upi_qr'>('payment_link')
  const [created, setCreated] = useState<PaymentRequestView[]>([])
  const [error, setError] = useState<string | null>(null)
  useEffect(() => { if (party?.phone) setPhone(party.phone) }, [party])

  const create = useAction<PaymentRequestView, PaymentRequestInput>((body) => post<PaymentRequestView>('/lenden/v1/payment-requests', body), {
    onSuccess: (r) => { setCreated((x) => [r, ...x]); setAmount(0) },
  })

  const submit = () => {
    setError(null)
    const e164 = toE164IN(phone)
    if (amount <= 0) return setError('Enter an amount.')
    if (!party && !e164) return setError('Pick a party or enter a 10-digit mobile number.')
    if (kind === 'payment_link' && !e164) return setError('A payment link needs the customer’s mobile number.')
    const body: PaymentRequestInput = {
      amount_paise: amount, purpose: 'on_account', kind, store_id: storeId,
      ...(party ? { party_id: party.id } : {}), ...(e164 ? { party_phone: e164 } : {}),
    }
    create.mutate(body)
  }

  return (
    <div className="grid gap-4 lg:grid-cols-[380px_minmax(0,1fr)]">
      <Card title="New payment link or QR">
        <div className="space-y-4">
          <div className="inline-flex rounded-xl bg-slate-100 p-1 text-sm font-semibold">
            {([['payment_link', 'Payment link', Link2], ['upi_qr', 'UPI QR', QrCode]] as const).map(([k, label, Icon]) => (
              <button key={k} type="button" onClick={() => setKind(k)} className={`flex items-center gap-1.5 rounded-lg px-3 py-1.5 cursor-pointer ${kind === k ? 'bg-white text-ink shadow-sm' : 'text-slate-500'}`}><Icon size={14} />{label}</button>
            ))}
          </div>
          <Field label="Customer"><PartyPicker value={party} onChange={setParty} type="customer" /></Field>
          <Field label="Mobile" hint={kind === 'payment_link' ? 'The link is sent to this number' : 'Optional for a QR'}><Input value={phone} inputMode="tel" onChange={(e) => setPhone(e.target.value)} placeholder="98765 43210" /></Field>
          <Field label="Amount"><MoneyInput value={amount} onChange={setAmount} /></Field>
          {error && <p className="text-sm text-rose-600">{error}</p>}
          <Button className="w-full" loading={create.isPending} onClick={submit} icon={kind === 'payment_link' ? Link2 : QrCode}>Create {kind === 'payment_link' ? 'link' : 'QR'}{amount ? ` for ${formatINR(amount)}` : ''}</Button>
          <p className="text-xs text-slate-500">On-account collection. Payment requests tied to a specific invoice are raised from the invoice itself.</p>
        </div>
      </Card>
      <div className="space-y-4">
        {created.length === 0 ? (
          <Card><Empty icon={Link2} title="No links created in this session" text="lenden does not list past requests yet; links you create now appear here and update as they are paid." /></Card>
        ) : created.map((r) => <RequestCard key={r.id} initial={r} onCopy={() => toast('Copied')} />)}
      </div>
    </div>
  )
}

function copyText(text: string | undefined, done: () => void) {
  if (!text) return
  void navigator.clipboard?.writeText(text).then(done)
}

function RequestCard({ initial, onCopy }: { initial: PaymentRequestView; onCopy: () => void }) {
  const live = useGet<PaymentRequestView>(`/lenden/v1/payment-requests/${initial.id}`, undefined, {
    initialData: initial,
    refetchInterval: (q) => (q.state.data?.status === 'created' || q.state.data?.status === 'partial' ? 5000 : false),
  })
  const r = live.data ?? initial
  const [qr, setQr] = useState<string | null>(null)
  useEffect(() => {
    if (r.kind !== 'upi_qr') return
    if (r.upi_qr_image_url) { setQr(r.upi_qr_image_url); return }
    if (r.upi_intent) void QRCode.toDataURL(r.upi_intent, { margin: 1, width: 240 }).then(setQr).catch(() => setQr(null))
  }, [r.kind, r.upi_intent, r.upi_qr_image_url])
  const share = r.short_url ? waLink(r.party_phone, `Please pay ${formatINR(r.amount_paise)} using this secure link: ${r.short_url}`) : null
  return (
    <Card padded={false}>
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-slate-100 px-4 py-3">
        <div className="flex items-center gap-2 text-sm font-bold">{r.kind === 'upi_qr' ? <QrCode size={16} /> : <Link2 size={16} />}{r.kind === 'upi_qr' ? 'UPI QR' : 'Payment link'} · <Money paise={r.amount_paise} /></div>
        <Badge tone={statusTone(r.status)}>{r.status}{r.status === 'partial' ? ` · ${formatINR(r.paid_paise)} paid` : ''}</Badge>
      </div>
      <div className="flex flex-col gap-4 p-4 sm:flex-row sm:items-start">
        {/* eslint-disable-next-line @next/next/no-img-element -- data: URL rendered by the qrcode lib */}
        {r.kind === 'upi_qr' && (qr ? <img src={qr} alt={`UPI QR for ${formatINR(r.amount_paise)}`} className="h-44 w-44 shrink-0 rounded-xl ring-1 ring-slate-200" /> : <div className="grid h-44 w-44 shrink-0 place-items-center rounded-xl bg-slate-50 text-xs text-slate-400">QR not available</div>)}
        <div className="min-w-0 flex-1 space-y-2 text-sm">
          <dl className="divide-y divide-slate-100">
            <KV k="Gateway" v={r.gateway} />
            {r.party_phone && <KV k="Mobile" v={r.party_phone} />}
            <KV k="Expires" v={fmtDate(r.expires_at, true)} />
            {r.short_url && <KV k="Link" v={<a href={r.short_url} target="_blank" rel="noopener noreferrer" className="link break-all">{r.short_url}</a>} />}
          </dl>
          <div className="flex flex-wrap gap-2">
            {r.short_url && <Button size="sm" variant="secondary" icon={Copy} onClick={() => copyText(r.short_url, onCopy)}>Copy link</Button>}
            {share && <a href={share} target="_blank" rel="noopener noreferrer"><Button size="sm" variant="secondary" icon={MessageCircle}>Share on WhatsApp</Button></a>}
            {r.upi_intent && <Button size="sm" variant="secondary" icon={Copy} onClick={() => copyText(r.upi_intent, onCopy)}>Copy UPI intent</Button>}
          </div>
          {(r.status === 'created' || r.status === 'partial') && <p className="text-xs text-slate-500">Checking for payment every few seconds…</p>}
        </div>
      </div>
    </Card>
  )
}

/* ---------------- Settlements ---------------- */

function SettlementsTab() {
  const [gateway, setGateway] = useState('')
  const [range, setRange] = useState<DateRange>(() => presetRange('this_month', todayIST()))
  const [offset, setOffset] = useState(0)
  const gateways = useGateways().filter((g) => g !== 'manual')
  const list = useList<Settlement>('/lenden/v1/settlements', { gateway, from: range.from, to: range.to, limit: 50, offset })
  useEffect(() => setOffset(0), [gateway, range.from, range.to])
  const exportCSV = () => downloadFile(`settlements-${range.from}-${range.to}.csv`, toCSV(['Settled at', 'Gateway', 'UTR', 'Gateway ref', 'Gross', 'Fee', 'GST on fee', 'Refunds', 'Net'],
    (list.data?.items ?? []).map((s) => [s.settled_at, s.gateway, s.utr ?? '', s.gateway_ref, csvRupees(s.gross_paise), csvRupees(s.fee_paise), csvRupees(s.fee_tax_paise), csvRupees(s.refunds_paise), csvRupees(s.net_paise)])), 'text/csv')
  return (
    <Card padded={false}>
      <div className="flex flex-wrap items-center gap-2 border-b border-slate-100 p-3 sm:p-4">
        <DateRangePicker value={range} onChange={setRange} />
        <Select value={gateway} onChange={(e) => setGateway(e.target.value)} className="w-auto input-sm" aria-label="Gateway"><option value="">All gateways</option>{gateways.map((g) => <option key={g} value={g}>{g}</option>)}</Select>
        <Button size="sm" variant="secondary" icon={Download} className="ml-auto" onClick={exportCSV} disabled={!list.data?.items.length}>CSV</Button>
      </div>
      <QueryState isLoading={list.isLoading} error={list.error} data={list.data} retry={() => list.refetch()}>
        {(d) => d.items.length === 0 ? <Empty icon={Landmark} title="No settlements in this period" text="Gateway payouts land here from the settlement webhook; bahi moves them from Gateway Clearing to Bank net of MDR." /> : (
          <>
            <Table>
              <thead className="bg-slate-50/60"><tr><th className="th">Date</th><th className="th">Gateway</th><th className="th">UTR</th><th className="th text-right">Gross</th><th className="th text-right">Fee</th><th className="th text-right">GST on fee</th><th className="th text-right">Refunds</th><th className="th text-right">Net to bank</th></tr></thead>
              <tbody className="divide-y divide-slate-100">
                {d.items.map((s) => (
                  <tr key={s.id} className="hover:bg-slate-50/60">
                    <td className="td whitespace-nowrap text-slate-600">{fmtDate(s.settled_at, true)}</td>
                    <td className="td">{s.gateway}</td>
                    <td className="td font-mono text-xs">{s.utr || <span className="text-slate-400">—</span>}<div className="text-[11px] text-slate-400">{s.gateway_ref}</div></td>
                    <td className="td num text-right"><Money paise={s.gross_paise} /></td>
                    <td className="td num text-right text-rose-700"><Money paise={s.fee_paise} /></td>
                    <td className="td num text-right text-rose-700"><Money paise={s.fee_tax_paise} /></td>
                    <td className="td num text-right text-slate-600"><Money paise={s.refunds_paise} /></td>
                    <td className="td num text-right font-bold"><Money paise={s.net_paise} /></td>
                  </tr>
                ))}
              </tbody>
            </Table>
            <Pagination total={d.total} limit={d.limit} offset={d.offset} onChange={setOffset} />
          </>
        )}
      </QueryState>
    </Card>
  )
}
