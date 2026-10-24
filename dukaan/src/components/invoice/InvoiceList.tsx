'use client'

/**
 * Paged document list with date presets, status / payment filters and search.
 * Table from md up, stacked rows below, totals footer for the page shown.
 */
import { useState, type ReactNode } from 'react'
import { useRouter } from 'next/navigation'
import { FileText, type LucideIcon } from 'lucide-react'
import { Avatar, Badge, Card, Empty, Field, Input, Money, Pagination, QueryState, SearchInput, Select, statusTone } from '@/components/ui'
import { fmtDate, fyStartIST, todayIST, useDebounced, useList, useStore } from '@/lib/hooks'
import type { DocType } from '@/lib/types'
import type { InvoiceFull } from '@/lib/types.pos'
import { isNote } from '@/lib/pos/invoice'

type Preset = 'today' | 'yesterday' | 'week' | 'month' | 'fy' | 'all' | 'custom'

function shift(date: string, days: number) {
  const d = new Date(`${date}T00:00:00+05:30`)
  d.setDate(d.getDate() + days)
  return new Date(d.getTime() + 5.5 * 3600_000).toISOString().slice(0, 10)
}

export function presetRange(p: Preset, custom: { from: string; to: string }): { from?: string; to?: string } {
  const today = todayIST()
  switch (p) {
    case 'today': return { from: today, to: today }
    case 'yesterday': { const y = shift(today, -1); return { from: y, to: y } }
    case 'week': { const dow = (new Date(`${today}T00:00:00+05:30`).getDay() + 6) % 7; return { from: shift(today, -dow), to: today } }
    case 'month': return { from: `${today.slice(0, 7)}-01`, to: today }
    case 'fy': return { from: fyStartIST(), to: today }
    case 'custom': return { from: custom.from || undefined, to: custom.to || undefined }
    default: return {}
  }
}

export function InvoiceList({ docType, rowHref, emptyTitle, emptyText, emptyAction, partyLabel = 'Party', icon = FileText, allStores }: {
  docType: DocType
  rowHref: (inv: InvoiceFull) => string
  emptyTitle: string
  emptyText?: string
  emptyAction?: ReactNode
  partyLabel?: string
  icon?: LucideIcon
  /** Owners may list every store; default is the active store. */
  allStores?: boolean
}) {
  const router = useRouter()
  const { storeId } = useStore()
  const [q, setQ] = useState('')
  const dq = useDebounced(q)
  const [preset, setPreset] = useState<Preset>('month')
  const [custom, setCustom] = useState({ from: '', to: '' })
  const [status, setStatus] = useState('')
  const [pay, setPay] = useState('')
  const [offset, setOffset] = useState(0)
  const range = presetRange(preset, custom)
  const note = isNote(docType)
  const limit = 50

  const list = useList<InvoiceFull>('/aadesh/v1/invoices', {
    doc_type: docType, store_id: allStores ? undefined : storeId, status, payment_status: note ? undefined : pay, from: range.from, to: range.to, q: dq.trim(), limit, offset,
  })
  const reset = <T,>(set: (v: T) => void) => (v: T) => { set(v); setOffset(0) }

  return (
    <Card padded={false}>
      <div className="flex flex-col gap-3 border-b border-slate-100 p-3 sm:p-4">
        <div className="flex flex-wrap items-end gap-2">
          <Field label="Period" className="w-40">
            <Select value={preset} onChange={(e) => reset(setPreset)(e.target.value as Preset)}>
              <option value="today">Today</option><option value="yesterday">Yesterday</option><option value="week">This week</option>
              <option value="month">This month</option><option value="fy">This financial year</option><option value="all">All time</option><option value="custom">Custom…</option>
            </Select>
          </Field>
          {preset === 'custom' && (
            <>
              <Field label="From" className="w-40"><Input type="date" value={custom.from} max={custom.to || undefined} onChange={(e) => reset(setCustom)({ ...custom, from: e.target.value })} /></Field>
              <Field label="To" className="w-40"><Input type="date" value={custom.to} min={custom.from || undefined} onChange={(e) => reset(setCustom)({ ...custom, to: e.target.value })} /></Field>
            </>
          )}
          <Field label="Status" className="w-36">
            <Select value={status} onChange={(e) => reset(setStatus)(e.target.value)}><option value="">All</option><option value="posted">Posted</option><option value="cancelled">Cancelled</option></Select>
          </Field>
          {!note && (
            <Field label="Payment" className="w-36">
              <Select value={pay} onChange={(e) => reset(setPay)(e.target.value)}><option value="">All</option><option value="unpaid">Unpaid</option><option value="partial">Partial</option><option value="paid">Paid</option></Select>
            </Field>
          )}
          <div className="ml-auto w-full md:w-auto"><SearchInput value={q} onChange={reset(setQ)} placeholder={`Number or ${partyLabel.toLowerCase()}`} /></div>
        </div>
      </div>
      <QueryState isLoading={list.isLoading} error={list.error} data={list.data} retry={() => list.refetch()}>
        {(d) => d.items.length === 0 ? (
          <Empty icon={icon} title={dq || status || pay || preset !== 'all' ? 'No documents match' : emptyTitle} text={dq || status || pay || preset !== 'all' ? 'Try a wider period or clear the filters.' : emptyText} action={emptyAction} />
        ) : (
          <>
            <ul className="divide-y divide-slate-100 md:hidden">
              {d.items.map((inv) => (
                <li key={inv.id}>
                  <button type="button" onClick={() => router.push(rowHref(inv))} className="flex w-full items-center gap-3 px-4 py-3 text-left hover:bg-slate-50 cursor-pointer">
                    <Avatar name={inv.party_name} className="h-9 w-9 text-xs" />
                    <div className="min-w-0 flex-1">
                      <div className="truncate text-sm font-semibold">{inv.party_name}</div>
                      <div className="text-xs text-slate-500"><span className="font-mono">{inv.number}</span> · {fmtDate(inv.invoice_date)}</div>
                    </div>
                    <div className="text-right">
                      <Money paise={inv.grand_total_paise} className="block text-sm font-bold" />
                      <Badge tone={inv.status === 'cancelled' ? 'red' : note ? 'green' : statusTone(inv.payment_status)}>{inv.status === 'cancelled' ? 'cancelled' : note ? 'posted' : inv.payment_status}</Badge>
                    </div>
                  </button>
                </li>
              ))}
            </ul>
            <div className="hidden overflow-x-auto md:block">
              <table className="w-full min-w-[760px]">
                <thead className="bg-slate-50/60">
                  <tr><th className="th">Number</th><th className="th">{partyLabel}</th><th className="th">Date</th>{!note && <th className="th">Due</th>}<th className="th text-right">Amount</th>{!note && <th className="th text-right">Balance</th>}<th className="th">Status</th></tr>
                </thead>
                <tbody className="divide-y divide-slate-100">
                  {d.items.map((inv) => (
                    <tr key={inv.id} onClick={() => router.push(rowHref(inv))} className="cursor-pointer transition hover:bg-brand-50/40">
                      <td className="td font-mono text-xs font-semibold text-brand-700">{inv.number}</td>
                      <td className="td">
                        <div className="flex items-center gap-3"><Avatar name={inv.party_name} className="h-9 w-9 text-xs" /><div><div className="font-semibold">{inv.party_name}</div><div className="text-xs text-slate-500">{inv.party_gstin || inv.party_phone || '—'}</div></div></div>
                      </td>
                      <td className="td text-slate-600">{fmtDate(inv.invoice_date)}</td>
                      {!note && <td className="td text-slate-600">{inv.due_date ? fmtDate(inv.due_date) : '—'}</td>}
                      <td className="td num text-right font-semibold"><Money paise={inv.grand_total_paise} /></td>
                      {!note && <td className={`td num text-right ${inv.balance_paise > 0 && inv.status === 'posted' ? 'font-semibold text-amber-600' : 'text-slate-400'}`}><Money paise={inv.status === 'posted' ? inv.balance_paise : 0} /></td>}
                      <td className="td"><div className="flex flex-wrap gap-1"><Badge tone={statusTone(inv.status)}>{inv.status}</Badge>{!note && inv.status === 'posted' && <Badge tone={statusTone(inv.payment_status)}>{inv.payment_status}</Badge>}</div></td>
                    </tr>
                  ))}
                </tbody>
                <tfoot>
                  <tr className="border-t border-slate-200 bg-slate-50/60 text-sm font-semibold">
                    <td className="td" colSpan={note ? 3 : 4}>{d.items.length} of {d.total} on this page</td>
                    <td className="td num text-right"><Money paise={d.items.filter((i) => i.status === 'posted').reduce((a, i) => a + i.grand_total_paise, 0)} /></td>
                    {!note && <td className="td num text-right text-amber-600"><Money paise={d.items.filter((i) => i.status === 'posted').reduce((a, i) => a + i.balance_paise, 0)} /></td>}
                    <td className="td" />
                  </tr>
                </tfoot>
              </table>
            </div>
            <Pagination total={d.total} limit={d.limit || limit} offset={d.offset} onChange={setOffset} />
          </>
        )}
      </QueryState>
    </Card>
  )
}
