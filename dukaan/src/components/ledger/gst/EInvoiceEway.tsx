'use client'

/**
 * GST centre → e-Invoice & e-Way, for the selected registration:
 *  - Sales: posted sales of this GSTIN (aadesh, store_gstin=) with their IRN / EWB status and actions
 *  - e-Invoices: kar GET /v1/einvoices?status=&from=&to=&gstin= — retry failed (IRP code + message), cancel
 *  - e-Way bills: kar GET /v1/eway-bills?status=&from=&to=&gstin= — Part-B, extend, cancel
 * The IRP / NIC JSON downloads for offline upload stay available.
 */
import { Fragment, useEffect, useMemo, useState } from 'react'
import Link from 'next/link'
import { Ban, ChevronDown, ChevronRight, Clock, FileCheck2, FileJson, FileText, RefreshCw, Truck } from 'lucide-react'
import { Badge, Button, Card, Empty, Money, Pagination, QueryState, SearchInput, Table, Tabs, Toggle, statusTone } from '@/components/ui'
import { fmtDate, todayIST, useDebounced, useList, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import { ewayCancelCheck, ewayExtendCheck, ewayModeLabel, irnCancelCheck } from '@/lib/compliance'
import type { Invoice } from '@/lib/types'
import type { EInvoiceRecord, EWayRecord } from '@/lib/types.kar'
import { presetRange, type DateRange } from '@/lib/ledger/util'
import { DateRangePicker } from '../DateRangePicker'
import { WarningList } from '../bits'
import { CompliancePanel } from '@/components/compliance/CompliancePanel'
import { ComplianceDialogs, ServerError, useEinvoiceJson, useGenerateIrn, type ComplianceDialog, type DocRef } from '@/components/compliance/actions'

type View = 'sales' | 'einvoices' | 'eway'
const EINV_STATUSES = ['', 'generated', 'failed', 'pending', 'cancelled'] as const
const EWB_STATUSES = ['', 'generated', 'failed', 'cancelled'] as const

const ewayHref = (r: Pick<EWayRecord, 'invoice_id' | 'doc_type'>) =>
  r.doc_type === 'purchase' || r.doc_type === 'purchase_return' ? `/app/purchases/${r.invoice_id}`
    : r.doc_type === 'sale_return' ? `/app/returns/${r.invoice_id}` : `/app/sales/${r.invoice_id}`
const einvHref = (r: Pick<EInvoiceRecord, 'invoice_id' | 'doc_type'>) => (r.doc_type === 'INV' ? `/app/sales/${r.invoice_id}` : `/app/returns/${r.invoice_id}`)

function StatusChips<T extends string>({ value, onChange, options }: { value: T; onChange: (v: T) => void; options: readonly T[] }) {
  return (
    <div className="flex flex-wrap gap-1" role="group" aria-label="Status filter">
      {options.map((s) => (
        <button key={s || 'all'} type="button" onClick={() => onChange(s)} aria-pressed={value === s}
          className={`rounded-full px-3 py-1 text-xs font-semibold ring-1 ring-inset transition cursor-pointer ${value === s ? 'bg-brand-700 text-white ring-brand-700' : 'bg-white text-slate-600 ring-slate-200 hover:text-ink'}`}>
          {s || 'All'}
        </button>
      ))}
    </div>
  )
}

export function EInvoiceEway({ gstin }: { gstin?: string }) {
  const canDo = usePerm()
  const canLists = canDo(PERMS.reportRead)
  const [view, setView] = useState<View>('sales')
  const [range, setRange] = useState<DateRange>(() => presetRange('this_month', todayIST()))
  const views: { value: View; label: string }[] = [{ value: 'sales', label: 'Sales' }, ...(canLists ? [{ value: 'einvoices' as View, label: 'e-Invoices' }, { value: 'eway' as View, label: 'e-Way bills' }] : [])]
  return (
    <>
      <div className="mb-4 flex flex-wrap items-center gap-2">
        <Tabs value={view} onChange={setView} options={views} />
        <DateRangePicker value={range} onChange={setRange} />
      </div>
      {view === 'sales' && <SalesView gstin={gstin} range={range} />}
      {view === 'einvoices' && canLists && <EInvoiceList gstin={gstin} range={range} />}
      {view === 'eway' && canLists && <EWayList gstin={gstin} range={range} />}
    </>
  )
}

/* ---------------- sales of this GSTIN ---------------- */

function SalesView({ gstin, range }: { gstin?: string; range: DateRange }) {
  const canDo = usePerm()
  const [q, setQ] = useState('')
  const [b2bOnly, setB2bOnly] = useState(true)
  const dq = useDebounced(q)
  const [offset, setOffset] = useState(0)
  const [expanded, setExpanded] = useState<string | null>(null)
  const [dialog, setDialog] = useState<ComplianceDialog | null>(null)
  const [warn, setWarn] = useState<{ doc: string; items: string[] } | null>(null)
  useEffect(() => setOffset(0), [range.from, range.to, dq, gstin])
  const list = useList<Invoice>('/aadesh/v1/invoices', { doc_type: 'sale', status: 'posted', store_gstin: gstin, from: range.from, to: range.to, q: dq, limit: 50, offset })
  // aadesh has no "registered buyer" filter; B2B = party_gstin present (exports / SEZ carry a supply category).
  const rows = useMemo(() => (list.data?.items ?? []).filter((i) => !b2bOnly || !!i.party_gstin || !!i.supply_category), [list.data, b2bOnly])
  const gen = useGenerateIrn()
  const json = useEinvoiceJson((items, doc) => setWarn({ doc: doc.number, items }))
  const ref = (inv: Invoice): DocRef => ({ id: inv.id, number: inv.number, total_paise: inv.grand_total_paise, party_name: inv.party_name, store_state: inv.store_state })

  return (
    <>
      <div className="mb-3 flex flex-wrap items-center gap-3">
        <SearchInput value={q} onChange={setQ} placeholder="Invoice no, party" className="md:w-56" />
        <Toggle checked={b2bOnly} onChange={setB2bOnly} label={<span className="text-xs text-slate-600">B2B / export only</span>} />
      </div>
      {warn && <div className="mb-3"><WarningList items={warn.items.map((w) => `${warn.doc}: ${w}`)} /></div>}
      {gen.error && <div className="mb-3"><ServerError error={gen.error} /></div>}
      <Card padded={false}>
        <QueryState isLoading={list.isLoading} error={list.error} data={list.data} retry={() => list.refetch()}>
          {(d) => rows.length === 0 ? <Empty icon={FileText} title={b2bOnly ? 'No B2B sales in this period' : 'No sales in this period'} text={d.items.length ? `${d.items.length} sales found, none to a registered buyer.` : 'Posted sales of this GSTIN appear here for e-invoicing and e-way bills.'} /> : (
            <>
              <Table>
                <thead className="bg-slate-50/60"><tr><th className="th w-8" /><th className="th">Invoice</th><th className="th">Buyer</th><th className="th text-right">Total</th><th className="th">e-Invoice</th><th className="th">e-Way bill</th><th className="th text-right">Actions</th></tr></thead>
                <tbody className="divide-y divide-slate-100">
                  {rows.map((inv) => {
                    const open = expanded === inv.id
                    const es = inv.einvoice_status
                    const busy = gen.isPending && gen.variables?.id === inv.id
                    return (
                      <Fragment key={inv.id}>
                        <tr className="hover:bg-slate-50/60">
                          <td className="td"><button type="button" onClick={() => setExpanded(open ? null : inv.id)} aria-label={open ? 'Hide e-invoice details' : 'Show e-invoice details'} aria-expanded={open} className="grid h-7 w-7 place-items-center rounded text-slate-400 hover:text-ink cursor-pointer">{open ? <ChevronDown size={14} /> : <ChevronRight size={14} />}</button></td>
                          <td className="td"><Link href={`/app/sales/${inv.id}`} className="font-mono text-xs font-semibold text-brand-700 hover:underline">{inv.number}</Link><div className="text-xs text-slate-500">{fmtDate(inv.invoice_date)}{inv.inter_state && ' · inter-state'}</div></td>
                          <td className="td"><div className="font-medium">{inv.party_name}</div>{inv.party_gstin && <div className="font-mono text-[11px] text-slate-500">{inv.party_gstin}</div>}</td>
                          <td className="td num text-right font-semibold"><Money paise={inv.grand_total_paise} /></td>
                          <td className="td">
                            {es ? <Badge tone={es === 'generated' ? 'green' : statusTone(es)}>{es === 'generated' ? 'IRN' : es}</Badge> : <span className="text-xs text-slate-400">—</span>}
                            {es === 'failed' && inv.einvoice_error && <div className="mt-0.5 max-w-[16rem] truncate text-[11px] text-rose-600" title={inv.einvoice_error}>{inv.einvoice_error_code ? `${inv.einvoice_error_code}: ` : ''}{inv.einvoice_error}</div>}
                          </td>
                          <td className="td">{inv.ewb_no ? <><span className="font-mono text-xs">{inv.ewb_no}</span>{inv.ewb_status && inv.ewb_status !== 'generated' && <Badge tone={statusTone(inv.ewb_status)} className="ml-1">{inv.ewb_status}</Badge>}{inv.ewb_valid_upto && inv.ewb_status === 'generated' && <div className="text-[11px] text-slate-500">till {fmtDate(inv.ewb_valid_upto, true)}</div>}</> : <span className="text-xs text-slate-400">—</span>}</td>
                          <td className="td text-right">
                            <div className="flex justify-end gap-1.5">
                              {canDo(PERMS.gstFile) && (!es || es === 'failed') && <Button size="sm" variant="secondary" icon={es === 'failed' ? RefreshCw : FileCheck2} loading={busy} onClick={() => gen.mutate(ref(inv))}>{es === 'failed' ? 'Retry' : 'IRN'}</Button>}
                              {canDo(PERMS.gstFile) && <Button size="sm" variant="ghost" icon={FileJson} loading={json.isPending && json.variables?.id === inv.id} onClick={() => json.mutate(ref(inv))} title="IRP JSON for offline upload">JSON</Button>}
                              {inv.ewb_status !== 'generated' && <Button size="sm" variant="secondary" icon={Truck} onClick={() => setDialog({ kind: 'eway', doc: ref(inv) })}>e-Way</Button>}
                            </div>
                          </td>
                        </tr>
                        {open && <tr className="bg-slate-50/50"><td colSpan={7} className="px-4 py-3"><CompliancePanel invoice={inv} /></td></tr>}
                      </Fragment>
                    )
                  })}
                </tbody>
              </Table>
              <Pagination total={d.total} limit={d.limit} offset={d.offset} onChange={setOffset} />
              {b2bOnly && d.total > d.items.length && <p className="px-4 pb-3 text-[11px] text-slate-400">B2B filtering happens on this page of results; page through for more.</p>}
            </>
          )}
        </QueryState>
      </Card>
      <ComplianceDialogs dialog={dialog} onClose={() => setDialog(null)} />
    </>
  )
}

/* ---------------- kar e-invoice list ---------------- */

function EInvoiceList({ gstin, range }: { gstin?: string; range: DateRange }) {
  const canDo = usePerm()
  const [status, setStatus] = useState<(typeof EINV_STATUSES)[number]>('')
  const [offset, setOffset] = useState(0)
  const [dialog, setDialog] = useState<ComplianceDialog | null>(null)
  useEffect(() => setOffset(0), [range.from, range.to, status, gstin])
  const list = useList<EInvoiceRecord>('/kar/v1/einvoices', { status, gstin, from: range.from, to: range.to, limit: 50, offset })
  const gen = useGenerateIrn()
  const canFile = canDo(PERMS.gstFile)
  const ref = (r: EInvoiceRecord): DocRef => ({ id: r.invoice_id, number: r.invoice_number, total_paise: r.total_paise, party_name: r.party_name })

  return (
    <>
      <div className="mb-3"><StatusChips value={status} onChange={setStatus} options={EINV_STATUSES} /></div>
      {gen.error && <div className="mb-3"><ServerError error={gen.error} /></div>}
      <Card padded={false}>
        <QueryState isLoading={list.isLoading} error={list.error} data={list.data} retry={() => list.refetch()}>
          {(d) => d.items.length === 0 ? <Empty icon={FileCheck2} title="No e-invoices" text="IRNs generated (automatically or on request) for this GSTIN appear here." /> : (
            <>
              <Table>
                <thead className="bg-slate-50/60"><tr><th className="th">Document</th><th className="th">Buyer</th><th className="th text-right">Value</th><th className="th">Status</th><th className="th">IRN / Ack</th><th className="th text-right">Actions</th></tr></thead>
                <tbody className="divide-y divide-slate-100">
                  {d.items.map((r) => {
                    const cancel = irnCancelCheck(r, null)
                    return (
                      <tr key={r.invoice_id} className="align-top hover:bg-slate-50/60">
                        <td className="td"><Link href={einvHref(r)} className="font-mono text-xs font-semibold text-brand-700 hover:underline">{r.invoice_number}</Link><div className="text-xs text-slate-500">{r.doc_type} · {fmtDate(r.doc_date)}{r.supply_type && r.supply_type !== 'B2B' ? ` · ${r.supply_type}` : ''}</div></td>
                        <td className="td"><div className="font-medium">{r.party_name || '—'}</div>{r.party_gstin && <div className="font-mono text-[11px] text-slate-500">{r.party_gstin}</div>}</td>
                        <td className="td num text-right"><Money paise={r.total_paise} /></td>
                        <td className="td">
                          <Badge tone={r.status === 'generated' ? 'green' : statusTone(r.status)}>{r.status}</Badge>
                          <div className="mt-0.5 text-[11px] text-slate-400">{r.triggered_by}{r.attempts > 1 ? ` · ${r.attempts} attempts` : ''}</div>
                        </td>
                        <td className="td max-w-[22rem]">
                          {r.irn && <div className="truncate font-mono text-[11px]" title={r.irn}>{r.irn}</div>}
                          {r.ack_no && <div className="text-[11px] text-slate-500">Ack {r.ack_no} · {fmtDate(r.ack_date, true)}</div>}
                          {r.status === 'generated' && r.cancellable_until && <div className="text-[11px] text-slate-400">cancellable until {fmtDate(r.cancellable_until, true)}</div>}
                          {r.cancelled_at && <div className="text-[11px] text-slate-500">cancelled {fmtDate(r.cancelled_at, true)}{r.cancel_remark ? ` · ${r.cancel_remark}` : ''}</div>}
                          {(r.status === 'failed' || r.status === 'pending') && (r.error_message || r.error_code) && <div className="mt-0.5 whitespace-normal break-words text-[11px] text-rose-600">{r.error_code && <span className="font-mono font-semibold">{r.error_code} </span>}{r.error_message}</div>}
                        </td>
                        <td className="td text-right">
                          {canFile && (
                            <div className="flex justify-end gap-1.5">
                              {(r.status === 'failed' || r.status === 'pending') && <Button size="sm" variant="secondary" icon={RefreshCw} loading={gen.isPending && gen.variables?.id === r.invoice_id} onClick={() => gen.mutate(ref(r))}>Retry</Button>}
                              {r.status === 'generated' && <Button size="sm" variant="ghost" icon={Ban} className="text-rose-700" disabled={!cancel.ok} title={cancel.ok ? 'Cancel the IRN (fails while an e-way bill is active)' : cancel.reason} onClick={() => setDialog({ kind: 'cancel-irn', doc: ref(r), einvoice: r })}>Cancel IRN</Button>}
                            </div>
                          )}
                        </td>
                      </tr>
                    )
                  })}
                </tbody>
              </Table>
              <Pagination total={d.total} limit={d.limit} offset={d.offset} onChange={setOffset} />
            </>
          )}
        </QueryState>
      </Card>
      <ComplianceDialogs dialog={dialog} onClose={() => setDialog(null)} />
    </>
  )
}

/* ---------------- kar e-way bill list ---------------- */

function EWayList({ gstin, range }: { gstin?: string; range: DateRange }) {
  const canDo = usePerm()
  const [status, setStatus] = useState<(typeof EWB_STATUSES)[number]>('')
  const [offset, setOffset] = useState(0)
  const [dialog, setDialog] = useState<ComplianceDialog | null>(null)
  useEffect(() => setOffset(0), [range.from, range.to, status, gstin])
  const list = useList<EWayRecord>('/kar/v1/eway-bills', { status, gstin, from: range.from, to: range.to, limit: 50, offset })
  const ref = (r: EWayRecord): DocRef => ({ id: r.invoice_id, number: r.invoice_number, store_state: r.gstin.slice(0, 2) })

  return (
    <>
      <div className="mb-3"><StatusChips value={status} onChange={setStatus} options={EWB_STATUSES} /></div>
      <Card padded={false}>
        <QueryState isLoading={list.isLoading} error={list.error} data={list.data} retry={() => list.refetch()}>
          {(d) => d.items.length === 0 ? <Empty icon={Truck} title="No e-way bills" text="E-way bills generated for this GSTIN's documents appear here." /> : (
            <>
              <Table>
                <thead className="bg-slate-50/60"><tr><th className="th">Document</th><th className="th">EWB</th><th className="th">Valid until</th><th className="th">Vehicle</th><th className="th">Status</th><th className="th text-right">Actions</th></tr></thead>
                <tbody className="divide-y divide-slate-100">
                  {d.items.map((r) => {
                    const active = r.status === 'generated'
                    const ext = ewayExtendCheck(r)
                    const can = ewayCancelCheck(r)
                    return (
                      <tr key={r.invoice_id} className="align-top hover:bg-slate-50/60">
                        <td className="td"><Link href={ewayHref(r)} className="font-mono text-xs font-semibold text-brand-700 hover:underline">{r.invoice_number}</Link><div className="text-xs text-slate-500">{fmtDate(r.doc_date)}{r.irn ? ' · from IRN' : ''}</div></td>
                        <td className="td"><div className="font-mono text-xs font-semibold">{r.ewb_no || '—'}</div>{r.ewb_date && <div className="text-[11px] text-slate-500">{fmtDate(r.ewb_date, true)}</div>}</td>
                        <td className="td text-xs">{r.valid_upto ? fmtDate(r.valid_upto, true) : <span className="text-slate-400">Part-A only</span>}{r.extensions > 0 && <div className="text-[11px] text-slate-400">{r.extensions} extension{r.extensions === 1 ? '' : 's'}</div>}</td>
                        <td className="td text-xs"><span className="font-mono">{r.vehicle_no || '—'}</span><div className="text-[11px] text-slate-500">{ewayModeLabel(r.mode)} · {r.distance_km} km{r.vehicle_type === 'O' ? ' · ODC' : ''}</div></td>
                        <td className="td max-w-[18rem]">
                          <Badge tone={active ? 'green' : statusTone(r.status)}>{r.status}</Badge>
                          {r.below_threshold && <Badge tone="gray" className="ml-1">below threshold</Badge>}
                          {r.status === 'failed' && r.error_message && <div className="mt-0.5 whitespace-normal break-words text-[11px] text-rose-600">{r.error_code && <span className="font-mono font-semibold">{r.error_code} </span>}{r.error_message}</div>}
                          {r.cancelled_at && <div className="text-[11px] text-slate-500">cancelled {fmtDate(r.cancelled_at, true)}</div>}
                        </td>
                        <td className="td text-right">
                          <div className="flex flex-wrap justify-end gap-1.5">
                            {active && <Button size="sm" variant="secondary" icon={Truck} onClick={() => setDialog({ kind: 'part-b', doc: ref(r), eway: r })}>Part-B</Button>}
                            {active && <Button size="sm" variant="ghost" icon={Clock} disabled={!ext.ok} title={ext.ok ? undefined : ext.reason} onClick={() => setDialog({ kind: 'extend', doc: ref(r), eway: r })}>Extend</Button>}
                            {active && canDo(PERMS.invoiceCancel) && <Button size="sm" variant="ghost" icon={Ban} className="text-rose-700" disabled={!can.ok} title={can.ok ? undefined : can.reason} onClick={() => setDialog({ kind: 'cancel-ewb', doc: ref(r), eway: r })}>Cancel</Button>}
                            {(r.status === 'failed' || r.status === 'cancelled') && <Button size="sm" variant="secondary" icon={Truck} onClick={() => setDialog({ kind: 'eway', doc: ref(r) })}>New bill</Button>}
                          </div>
                        </td>
                      </tr>
                    )
                  })}
                </tbody>
              </Table>
              <Pagination total={d.total} limit={d.limit} offset={d.offset} onChange={setOffset} />
            </>
          )}
        </QueryState>
      </Card>
      <ComplianceDialogs dialog={dialog} onClose={() => setDialog(null)} />
    </>
  )
}
