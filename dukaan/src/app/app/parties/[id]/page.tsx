'use client'

import { Suspense, useMemo, useState } from 'react'
import Link from 'next/link'
import { useParams, useRouter } from 'next/navigation'
import { Archive, ArchiveRestore, ArrowDownLeft, ArrowUpRight, ChevronLeft, FileText, Mail, MapPin, MessageCircle, Pencil, Phone, Printer, Download, Wallet } from 'lucide-react'
import { Avatar, Badge, Button, Card, Confirm, Empty, Money, Pagination, QueryState, Skeleton, statusTone, Table, Tabs } from '@/components/ui'
import { api } from '@/lib/api'
import { downloadFile, fmtDate, todayIST, useGet, useList, usePerm, useStore } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import { STATES } from '@/lib/gst'
import { formatINR } from '@/lib/money'
import type { Invoice, Party, Statement } from '@/lib/types'
import type { Payment, PartyPatch } from '@/lib/types.ledger'
import { csvFileName, toCSV } from '@/lib/ledger/csv'
import { balanceLabel, creditUsagePct, fmtDrCr, invoiceHref, presetRange, waLink, type DateRange } from '@/lib/ledger/util'
import { DateRangePicker } from '@/components/ledger/DateRangePicker'
import { StatementTable, statementCSV } from '@/components/ledger/StatementTable'
import { PartyModal } from '@/components/ledger/PartyModal'
import { ReceiptModal } from '@/components/ledger/ReceiptModal'
import { KV, MobileRow, NoPermission, useTabParam } from '@/components/ledger/bits'
import { useAction } from '@/components/ledger/useAction'

const TABS = ['statement', 'invoices', 'payments'] as const
type Tab = (typeof TABS)[number]

export default function PartyDetailPage() {
  return <Suspense fallback={<Skeleton />}><PartyDetailInner /></Suspense>
}

function PartyDetailInner() {
  const { id } = useParams<{ id: string }>()
  const router = useRouter()
  const canDo = usePerm()
  const { store } = useStore()
  const [tab, setTab] = useTabParam<Tab>(TABS, 'statement')
  const [edit, setEdit] = useState(false)
  const [receipt, setReceipt] = useState<'receipt' | 'payment' | null>(null)
  const [confirmArchive, setConfirmArchive] = useState(false)
  const [range, setRange] = useState<DateRange>(() => presetRange('this_fy', todayIST()))

  const party = useGet<Party>(canDo(PERMS.partyManage) ? `/bahi/v1/parties/${id}` : null)
  const p = party.data
  const statement = useGet<Statement>(p && canDo(PERMS.ledgerRead) && tab === 'statement' ? `/bahi/v1/parties/${id}/statement` : null, { from: range.from, to: range.to })

  const archive = useAction<Party, boolean>((archived) => api<Party>(`/bahi/v1/parties/${id}`, { method: 'PATCH', body: { archived } satisfies PartyPatch }), {
    invalidate: ['/bahi/v1/parties'], onSuccess: () => setConfirmArchive(false),
  })

  const wa = useMemo(() => {
    if (!p || !statement.data) return null
    const st = statement.data
    const closing = balanceLabel(st.closing_balance_paise)
    const text = `Namaste ${p.name},\n\nYour statement from ${store?.name ?? 'us'} for ${fmtDate(st.from)} to ${fmtDate(st.to)}:\nOpening: ${fmtDrCr(st.opening_balance_paise)}\nBilled (Dr): ${formatINR(st.total_debit_paise)}\nReceived (Cr): ${formatINR(st.total_credit_paise)}\nClosing: ${closing.direction === 'settled' ? 'Settled' : closing.text}\n\nThank you.`
    return waLink(p.phone, text)
  }, [p, statement.data, store?.name])

  if (!canDo(PERMS.partyManage)) return <Card><NoPermission what="parties" /></Card>

  return (
    <>
      <QueryState isLoading={party.isLoading} error={party.error} data={p} retry={() => party.refetch()}>
        {(pt) => {
          const bal = balanceLabel(pt.balance_paise)
          const usage = creditUsagePct(pt.balance_paise, pt.credit_limit_paise)
          const isCustomer = pt.type !== 'supplier'
          return (
            <>
              <div className="no-print mb-5 flex flex-wrap items-start justify-between gap-3">
                <div className="flex min-w-0 items-center gap-3">
                  <Link href="/app/parties" aria-label="Back to parties" className="grid h-10 w-10 shrink-0 place-items-center rounded-xl border border-slate-200 bg-white text-slate-600 hover:bg-slate-50"><ChevronLeft size={18} /></Link>
                  <Avatar name={pt.name} className="h-12 w-12 rounded-2xl text-base" />
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <h1 className="truncate text-lg font-extrabold sm:text-xl">{pt.name}</h1>
                      <Badge tone={pt.type === 'supplier' ? 'blue' : pt.type === 'both' ? 'amber' : 'brand'}>{pt.type}</Badge>
                      {pt.archived && <Badge tone="gray">archived</Badge>}
                    </div>
                    <div className="mt-0.5 flex flex-wrap items-center gap-2 text-xs text-slate-500">
                      {pt.gstin ? <span className="flex items-center gap-1.5 font-mono">{pt.gstin}<Badge tone={pt.gst_verified ? 'green' : 'gray'}>{pt.gst_verified ? 'verified' : 'unverified'}</Badge></span> : <span>Unregistered</span>}
                      {pt.phone && <span className="flex items-center gap-1"><Phone size={12} />{pt.phone}</span>}
                    </div>
                  </div>
                </div>
                <div className="flex w-full flex-wrap gap-2 sm:w-auto">
                  {canDo(PERMS.paymentCollect) && <Button variant="gold" icon={ArrowDownLeft} className="flex-1 sm:flex-none" onClick={() => setReceipt('receipt')}>Record receipt</Button>}
                  {canDo(PERMS.ledgerWrite) && <Button variant="secondary" icon={ArrowUpRight} onClick={() => setReceipt('payment')}>Record payment</Button>}
                  {wa && <a href={wa} target="_blank" rel="noopener noreferrer"><Button variant="secondary" icon={MessageCircle}><span className="hidden sm:inline">Statement on</span> WhatsApp</Button></a>}
                  <Button variant="secondary" icon={Pencil} onClick={() => setEdit(true)} aria-label="Edit party"><span className="hidden sm:inline">Edit</span></Button>
                  <Button variant="ghost" icon={pt.archived ? ArchiveRestore : Archive} aria-label={pt.archived ? 'Unarchive party' : 'Archive party'} onClick={() => (pt.archived ? archive.mutate(false) : setConfirmArchive(true))} loading={archive.isPending}>
                    <span className="hidden sm:inline">{pt.archived ? 'Unarchive' : 'Archive'}</span>
                  </Button>
                </div>
              </div>

              <div className="grid gap-4 lg:grid-cols-[300px_minmax(0,1fr)]">
                <div className="no-print space-y-4">
                  <Card padded={false}>
                    <div className={`p-5 ${bal.tone === 'green' ? 'bg-emerald-50' : bal.tone === 'red' ? 'bg-rose-50' : 'bg-slate-50'}`}>
                      <div className="text-xs font-medium text-slate-600">{bal.direction === 'collect' ? 'To collect' : bal.direction === 'pay' ? 'To pay' : 'Balance'}</div>
                      <div className={`num text-3xl font-extrabold ${bal.tone === 'green' ? 'text-emerald-700' : bal.tone === 'red' ? 'text-rose-700' : 'text-slate-500'}`}>{bal.direction === 'settled' ? 'Settled' : formatINR(Math.abs(pt.balance_paise))}</div>
                      {pt.credit_limit_paise > 0 && (
                        <div className="mt-3">
                          <div className="flex justify-between text-[11px] text-slate-600"><span>Credit used</span><span className="num">{usage}% of {formatINR(pt.credit_limit_paise)}</span></div>
                          <div className="mt-1 h-2 overflow-hidden rounded-full bg-white/70" role="progressbar" aria-valuenow={usage} aria-valuemin={0} aria-valuemax={100} aria-label="Credit used">
                            <div className={`h-full rounded-full ${usage >= 100 ? 'bg-rose-500' : usage >= 80 ? 'bg-amber-500' : 'bg-brand-500'}`} style={{ width: `${usage}%` }} />
                          </div>
                          {pt.credit_days > 0 && <div className="mt-1 text-[11px] text-slate-500">{pt.credit_days} credit days</div>}
                        </div>
                      )}
                    </div>
                    <dl className="space-y-1 p-5 text-sm">
                      {pt.phone && <div className="flex items-center gap-2.5 text-slate-600"><Phone size={15} /><a href={`tel:${pt.phone}`} className="hover:underline">{pt.phone}</a></div>}
                      {pt.email && <div className="flex items-center gap-2.5 text-slate-600"><Mail size={15} /><a href={`mailto:${pt.email}`} className="truncate hover:underline">{pt.email}</a></div>}
                      {(pt.address || pt.state_code) && <div className="flex items-start gap-2.5 text-slate-600"><MapPin size={15} className="mt-0.5 shrink-0" /><span>{pt.address}{pt.address && pt.state_code ? ', ' : ''}{STATES[pt.state_code] ?? ''}</span></div>}
                      {pt.legal_name && <KV k="Legal name" v={pt.legal_name} />}
                    </dl>
                  </Card>
                </div>

                <Card padded={false}>
                  <div className="no-print flex flex-col gap-3 border-b border-slate-100 p-3 sm:p-4 md:flex-row md:items-center md:justify-between">
                    <Tabs value={tab} onChange={setTab} options={[{ value: 'statement', label: 'Statement' }, { value: 'invoices', label: 'Invoices' }, { value: 'payments', label: 'Payments' }]} />
                    {tab === 'statement' && canDo(PERMS.ledgerRead) && (
                      <div className="flex flex-wrap items-center gap-2">
                        <DateRangePicker value={range} onChange={setRange} presets={['today', 'this_month', 'last_month', 'this_fy', 'last_fy', 'custom']} />
                        <Button size="sm" variant="secondary" icon={Download} disabled={!statement.data} onClick={() => { if (!statement.data) return; const c = statementCSV(statement.data); downloadFile(csvFileName(`statement ${pt.name}`, `${range.from}-${range.to}`), toCSV(c.header, c.rows), 'text/csv') }}>CSV</Button>
                        <Button size="sm" variant="secondary" icon={Printer} disabled={!statement.data} onClick={() => window.print()}>Print</Button>
                      </div>
                    )}
                  </div>

                  {tab === 'statement' && (
                    !canDo(PERMS.ledgerRead) ? <NoPermission what="ledgers" /> : (
                      <QueryState isLoading={statement.isLoading} error={statement.error} data={statement.data} retry={() => statement.refetch()}>
                        {(st) => (
                          <div className="print-root print-a4">
                            <div className="mb-4 hidden print:block">
                              <div className="text-lg font-extrabold">{store?.name ?? 'Taraju'}</div>
                              {store?.gstin && <div className="text-xs">GSTIN {store.gstin}</div>}
                              <div className="mt-2 text-base font-bold">Statement of account — {pt.name}</div>
                              <div className="text-xs">{[pt.gstin && `GSTIN ${pt.gstin}`, pt.phone, pt.address].filter(Boolean).join(' · ')}</div>
                              <div className="text-xs">{fmtDate(st.from)} – {fmtDate(st.to)}</div>
                            </div>
                            <StatementTable statement={st} emptyText="No transactions with this party in the period." />
                            <div className="hidden print:block mt-6 text-xs">Closing balance: {balanceLabel(st.closing_balance_paise).text}. Balances marked Dr are receivable by us; Cr are payable by us.</div>
                          </div>
                        )}
                      </QueryState>
                    )
                  )}
                  {tab === 'invoices' && <InvoicesTab partyId={pt.id} isCustomer={isCustomer} onReceipt={() => setReceipt('receipt')} />}
                  {tab === 'payments' && <PaymentsTab partyId={pt.id} />}
                </Card>
              </div>

              <PartyModal open={edit} onClose={() => setEdit(false)} party={pt} />
              <ReceiptModal open={receipt !== null} onClose={() => setReceipt(null)} party={pt} direction={receipt ?? 'receipt'} lockDirection
                onSaved={() => { void party.refetch(); void statement.refetch() }} />
              <Confirm open={confirmArchive} onClose={() => setConfirmArchive(false)} onConfirm={() => archive.mutate(true)} loading={archive.isPending} danger title={`Archive ${pt.name}?`} confirmLabel="Archive"
                text="The party is hidden from pickers and lists. Its ledger history stays, and you can unarchive it any time." />
            </>
          )
        }}
      </QueryState>
      {party.error && <div className="mt-4"><Button variant="secondary" onClick={() => router.push('/app/parties')}>Back to parties</Button></div>}
    </>
  )
}

function InvoicesTab({ partyId, isCustomer, onReceipt }: { partyId: string; isCustomer: boolean; onReceipt: () => void }) {
  const canDo = usePerm()
  const [offset, setOffset] = useState(0)
  const list = useList<Invoice>(canDo(PERMS.invoiceRead) ? '/aadesh/v1/invoices' : null, { party_id: partyId, limit: 25, offset })
  if (!canDo(PERMS.invoiceRead)) return <NoPermission what="invoices" />
  return (
    <QueryState isLoading={list.isLoading} error={list.error} data={list.data} retry={() => list.refetch()}>
      {(d) => d.items.length === 0 ? <Empty icon={FileText} title="No invoices yet" text={isCustomer ? 'Bills made for this customer show up here.' : 'Purchase bills from this supplier show up here.'} /> : (
        <>
          <div className="divide-y divide-slate-100 md:hidden">
            {d.items.map((inv) => (
              <Link key={inv.id} href={invoiceHref(inv)} className="block">
                <MobileRow title={<span className="font-mono">{inv.number}</span>} subtitle={`${fmtDate(inv.invoice_date)} · ${inv.doc_type.replace('_', ' ')}`}
                  right={<Money paise={inv.grand_total_paise} />} rightSub={inv.status === 'cancelled' ? 'cancelled' : inv.balance_paise > 0 ? `${formatINR(inv.balance_paise)} due` : 'paid'} />
              </Link>
            ))}
          </div>
          <div className="hidden md:block">
            <Table>
              <thead className="bg-slate-50/60"><tr><th className="th">Invoice</th><th className="th">Date</th><th className="th">Type</th><th className="th">Status</th><th className="th text-right">Total</th><th className="th text-right">Balance due</th><th className="th" /></tr></thead>
              <tbody className="divide-y divide-slate-100">
                {d.items.map((inv) => (
                  <tr key={inv.id} className="hover:bg-slate-50/60">
                    <td className="td"><Link href={invoiceHref(inv)} className="font-mono text-xs font-semibold text-brand-700 hover:underline">{inv.number}</Link></td>
                    <td className="td whitespace-nowrap text-slate-600">{fmtDate(inv.invoice_date)}{inv.due_date && inv.balance_paise > 0 && <div className="text-[11px] text-slate-400">due {fmtDate(inv.due_date)}</div>}</td>
                    <td className="td capitalize text-slate-600">{inv.doc_type.replace('_', ' ')}</td>
                    <td className="td"><Badge tone={statusTone(inv.status === 'cancelled' ? 'cancelled' : inv.payment_status)}>{inv.status === 'cancelled' ? 'cancelled' : inv.payment_status}</Badge></td>
                    <td className="td num text-right"><Money paise={inv.grand_total_paise} /></td>
                    <td className="td num text-right font-semibold">{inv.balance_paise > 0 ? <Money paise={inv.balance_paise} /> : <span className="text-slate-400">—</span>}</td>
                    <td className="td text-right">{inv.doc_type === 'sale' && inv.balance_paise > 0 && inv.status === 'posted' && canDo(PERMS.paymentCollect) && <Button size="sm" variant="secondary" icon={Wallet} onClick={onReceipt}>Receive</Button>}</td>
                  </tr>
                ))}
              </tbody>
            </Table>
          </div>
          <Pagination total={d.total} limit={d.limit} offset={d.offset} onChange={setOffset} />
        </>
      )}
    </QueryState>
  )
}

function PaymentsTab({ partyId }: { partyId: string }) {
  const canDo = usePerm()
  const [offset, setOffset] = useState(0)
  const list = useList<Payment>(canDo(PERMS.paymentCollect) ? '/lenden/v1/payments' : null, { party_id: partyId, limit: 25, offset })
  if (!canDo(PERMS.paymentCollect)) return <NoPermission what="payments" />
  if (list.isLoading) return <Skeleton />
  return (
    <QueryState isLoading={list.isLoading} error={list.error} data={list.data} retry={() => list.refetch()}>
      {(d) => d.items.length === 0 ? <Empty icon={Wallet} title="No payments received" text="Receipts recorded here or collected through UPI / payment links show up here." /> : (
        <>
          <Table>
            <thead className="bg-slate-50/60"><tr><th className="th">Date</th><th className="th">Method</th><th className="th">Reference</th><th className="th">Status</th><th className="th text-right">Amount</th><th className="th text-right">Refunded</th></tr></thead>
            <tbody className="divide-y divide-slate-100">
              {d.items.map((pm) => (
                <tr key={pm.id} className="hover:bg-slate-50/60">
                  <td className="td whitespace-nowrap text-slate-600"><Link href={`/app/payments?payment=${pm.id}`} className="hover:underline">{fmtDate(pm.captured_at, true)}</Link></td>
                  <td className="td"><Badge>{pm.method}</Badge> <span className="text-xs text-slate-500">{pm.gateway}</span></td>
                  <td className="td font-mono text-xs text-slate-600">{pm.gateway_ref || pm.notes || '—'}</td>
                  <td className="td"><Badge tone={statusTone(pm.status === 'captured' ? 'paid' : pm.status === 'refunded' ? 'cancelled' : 'partial')}>{pm.status.replace('_', ' ')}</Badge></td>
                  <td className="td num text-right font-semibold"><Money paise={pm.amount_paise} /></td>
                  <td className="td num text-right text-slate-600">{pm.refunded_paise ? <Money paise={pm.refunded_paise} /> : '—'}</td>
                </tr>
              ))}
            </tbody>
          </Table>
          <Pagination total={d.total} limit={d.limit} offset={d.offset} onChange={setOffset} />
        </>
      )}
    </QueryState>
  )
}
