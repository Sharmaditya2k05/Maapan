'use client'

/**
 * e-Invoice (IRP INV-01) and e-Way bill (NIC) JSON for recent B2B sales.
 * POST /kar/v1/einvoices/{id}/json (gst:file) · POST /kar/v1/eway-bills/{id}/json (invoice:read, transport details)
 * GET  /kar/v1/einvoices/{id} · /kar/v1/eway-bills/{id} show what was generated before.
 */
import { Fragment, useEffect, useMemo, useState } from 'react'
import Link from 'next/link'
import { ChevronDown, ChevronRight, Download, FileJson, FileText, Truck } from 'lucide-react'
import { Badge, Button, Card, Empty, Field, Input, Modal, Money, Pagination, QueryState, SearchInput, Select, Table } from '@/components/ui'
import { ApiError, post } from '@/lib/api'
import { fmtDate, todayIST, useDebounced, useGet, useList, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import type { Invoice } from '@/lib/types'
import type { EinvoiceResponse, EwayResponse, EwayTransportInput, StoredCompliance } from '@/lib/types.ledger'
import { presetRange, type DateRange } from '@/lib/ledger/util'
import { DateRangePicker } from '../DateRangePicker'
import { KV, WarningList } from '../bits'
import { useAction } from '../useAction'
import { downloadJSON } from './shared'

export function EInvoiceEway() {
  const canDo = usePerm()
  const [range, setRange] = useState<DateRange>(() => presetRange('this_month', todayIST()))
  const [q, setQ] = useState('')
  const dq = useDebounced(q)
  const [offset, setOffset] = useState(0)
  const [eway, setEway] = useState<Invoice | null>(null)
  const [result, setResult] = useState<{ title: string; warnings?: string[]; note?: string } | null>(null)
  const [expanded, setExpanded] = useState<string | null>(null)
  useEffect(() => setOffset(0), [range.from, range.to, dq])
  const list = useList<Invoice>(canDo(PERMS.invoiceRead) ? '/aadesh/v1/invoices' : null, { doc_type: 'sale', status: 'posted', from: range.from, to: range.to, q: dq, limit: 50, offset })
  // aadesh has no "registered buyer" filter; B2B = party_gstin present.
  const b2b = useMemo(() => (list.data?.items ?? []).filter((i) => !!i.party_gstin), [list.data])

  const einv = useAction<EinvoiceResponse, Invoice>((inv) => post<EinvoiceResponse>(`/kar/v1/einvoices/${inv.id}/json`, {}), {
    onSuccess: (r, inv) => { downloadJSON(`EINV_${inv.number.replace(/\//g, '-')}.json`, r.payload); setResult({ title: `e-Invoice JSON for ${inv.number} downloaded`, warnings: r.warnings }) },
  })

  return (
    <>
      <div className="mb-4 flex flex-wrap items-center gap-2">
        <DateRangePicker value={range} onChange={setRange} />
        <SearchInput value={q} onChange={setQ} placeholder="Invoice no, party" className="md:w-56" />
        <span className="ml-auto text-xs text-slate-500">Only B2B sales (buyer with a GSTIN) are shown.</span>
      </div>
      <Card padded={false}>
        <QueryState isLoading={list.isLoading} error={list.error} data={list.data} retry={() => list.refetch()}>
          {(d) => b2b.length === 0 ? <Empty icon={FileText} title="No B2B sales in this period" text={d.items.length ? `${d.items.length} sales found, none to a registered buyer.` : 'Sales to GST-registered buyers appear here for e-invoicing and e-way bills.'} /> : (
            <>
              <Table>
                <thead className="bg-slate-50/60"><tr><th className="th w-8" /><th className="th">Invoice</th><th className="th">Buyer</th><th className="th">GSTIN</th><th className="th text-right">Total</th><th className="th">IRN</th><th className="th text-right">Actions</th></tr></thead>
                <tbody className="divide-y divide-slate-100">
                  {b2b.map((inv) => {
                    const open = expanded === inv.id
                    return (
                      <Fragment key={inv.id}>
                        <tr className="hover:bg-slate-50/60">
                          <td className="td"><button type="button" onClick={() => setExpanded(open ? null : inv.id)} aria-label={open ? 'Hide generated documents' : 'Show generated documents'} className="grid h-7 w-7 place-items-center rounded text-slate-400 hover:text-ink cursor-pointer">{open ? <ChevronDown size={14} /> : <ChevronRight size={14} />}</button></td>
                          <td className="td"><Link href={`/app/sales/${inv.id}`} className="font-mono text-xs font-semibold text-brand-700 hover:underline">{inv.number}</Link><div className="text-xs text-slate-500">{fmtDate(inv.invoice_date)}{inv.inter_state && ' · inter-state'}</div></td>
                          <td className="td">{inv.party_id ? <Link href={`/app/parties/${inv.party_id}`} className="font-medium hover:underline">{inv.party_name}</Link> : inv.party_name}</td>
                          <td className="td font-mono text-xs">{inv.party_gstin}</td>
                          <td className="td num text-right font-semibold"><Money paise={inv.grand_total_paise} /></td>
                          <td className="td">{inv.irn ? <Badge tone="green">IRN</Badge> : <span className="text-xs text-slate-400">—</span>}</td>
                          <td className="td text-right">
                            <div className="flex justify-end gap-1.5">
                              {canDo(PERMS.gstFile) && <Button size="sm" variant="secondary" icon={FileJson} loading={einv.isPending && einv.variables?.id === inv.id} onClick={() => einv.mutate(inv)}>e-Invoice</Button>}
                              <Button size="sm" variant="secondary" icon={Truck} onClick={() => setEway(inv)}>e-Way</Button>
                            </div>
                          </td>
                        </tr>
                        {open && <tr className="bg-slate-50/50"><td colSpan={7} className="px-4 py-3"><Stored invoice={inv} /></td></tr>}
                      </Fragment>
                    )
                  })}
                </tbody>
              </Table>
              <Pagination total={d.total} limit={d.limit} offset={d.offset} onChange={setOffset} />
              {d.total > d.items.length && <p className="px-4 pb-3 text-[11px] text-slate-400">B2B filtering happens on this page of results; page through for more.</p>}
            </>
          )}
        </QueryState>
      </Card>
      <EwayModal invoice={eway} onClose={() => setEway(null)} onDone={(r, inv) => { setEway(null); setResult({ title: `e-Way bill JSON for ${inv.number} downloaded`, warnings: r.warnings, note: r.below_threshold ? 'The consignment is below your e-way bill threshold — the JSON is generated anyway, but a bill is not mandatory.' : undefined }) }} />
      <Modal open={!!result} onClose={() => setResult(null)} title={result?.title ?? ''} footer={<Button onClick={() => setResult(null)}>Done</Button>}>
        {result?.note && <p className="mb-3 rounded-xl bg-sky-50 px-4 py-3 text-sm text-sky-800">{result.note}</p>}
        {result?.warnings?.length ? <WarningList items={result.warnings} /> : <p className="text-sm text-slate-600">No warnings. Upload the file on the portal / through your GSP.</p>}
      </Modal>
    </>
  )
}

function Stored({ invoice }: { invoice: Invoice }) {
  const canDo = usePerm()
  const file = invoice.number.replace(/\//g, '-')
  return (
    <div className="grid gap-2 sm:grid-cols-2">
      {canDo(PERMS.gstFile) ? <StoredItem label="e-Invoice" path={`/kar/v1/einvoices/${invoice.id}`} file={`EINV_${file}.json`} /> : <div className="text-xs text-slate-400">e-Invoice records need GST filing access.</div>}
      <StoredItem label="e-Way bill" path={`/kar/v1/eway-bills/${invoice.id}`} file={`EWB_${file}.json`} />
    </div>
  )
}

/** One previously generated document (404 = not generated yet). */
function StoredItem({ label, path, file }: { label: string; path: string; file: string }) {
  const q = useGet<StoredCompliance>(path, undefined, { retry: false })
  const missing = q.error instanceof ApiError && q.error.status === 404
  const stored = q.data
  return (
    <div className="flex flex-wrap items-center justify-between gap-2 rounded-xl bg-white px-3 py-2 ring-1 ring-slate-200">
      <div className="text-sm">
        <span className="font-semibold">{label}</span>{' '}
        {q.isLoading ? <span className="text-xs text-slate-400">checking…</span>
          : stored ? <span className="text-xs text-slate-500">generated {fmtDate(stored.created_at, true)}</span>
          : missing ? <span className="text-xs text-slate-400">not generated yet</span>
          : q.error ? <span className="text-xs text-rose-600">{(q.error as Error).message}</span> : null}
      </div>
      {stored && <Button size="sm" variant="ghost" icon={Download} onClick={() => downloadJSON(file, stored.payload)}>Download again</Button>}
    </div>
  )
}

function EwayModal({ invoice, onClose, onDone }: { invoice: Invoice | null; onClose: () => void; onDone: (r: EwayResponse, inv: Invoice) => void }) {
  const [t, setT] = useState<EwayTransportInput>({ trans_mode: 'road', trans_distance: 0 })
  const [error, setError] = useState<string | null>(null)
  useEffect(() => { if (invoice) { setT({ trans_mode: 'road', trans_distance: 0 }); setError(null) } }, [invoice])
  const gen = useAction<EwayResponse, { inv: Invoice; body: EwayTransportInput }>((i) => post<EwayResponse>(`/kar/v1/eway-bills/${i.inv.id}/json`, i.body), {
    onSuccess: (r, i) => { downloadJSON(`EWB_${i.inv.number.replace(/\//g, '-')}.json`, r.payload); onDone(r, i.inv) },
  })
  const set = <K extends keyof EwayTransportInput>(k: K, v: EwayTransportInput[K]) => setT((x) => ({ ...x, [k]: v }))
  const submit = () => {
    setError(null)
    if (!invoice) return
    if (!t.trans_distance || t.trans_distance <= 0) return setError('Approximate distance in km is required.')
    if (t.trans_mode === 'road' && !t.vehicle_no?.trim() && !t.transporter_id?.trim()) return setError('For road, give the vehicle number or the transporter ID.')
    const body: EwayTransportInput = { trans_mode: t.trans_mode, trans_distance: Math.round(t.trans_distance) }
    if (t.vehicle_no?.trim()) body.vehicle_no = t.vehicle_no.trim().toUpperCase().replace(/\s+/g, '')
    if (t.transporter_id?.trim()) body.transporter_id = t.transporter_id.trim().toUpperCase()
    if (t.transporter_name?.trim()) body.transporter_name = t.transporter_name.trim()
    if (t.trans_doc_no?.trim()) body.trans_doc_no = t.trans_doc_no.trim()
    if (t.trans_doc_date) body.trans_doc_date = t.trans_doc_date
    if (t.vehicle_type) body.vehicle_type = t.vehicle_type
    if (t.from_pincode) body.from_pincode = t.from_pincode
    if (t.to_pincode) body.to_pincode = t.to_pincode
    gen.mutate({ inv: invoice, body })
  }
  const pin = (v: string) => { const n = Number(v.replace(/\D/g, '').slice(0, 6)); return n || undefined }
  return (
    <Modal open={!!invoice} onClose={onClose} title={invoice ? `e-Way bill · ${invoice.number}` : 'e-Way bill'} size="lg"
      footer={<><Button variant="secondary" onClick={onClose}>Cancel</Button><Button onClick={submit} loading={gen.isPending} icon={Truck}>Generate JSON</Button></>}>
      {invoice && (
        <div className="space-y-4">
          <dl className="grid gap-x-6 rounded-xl bg-slate-50 px-4 py-2 sm:grid-cols-2"><KV k="Consignee" v={invoice.party_name} /><KV k="Value" v={<Money paise={invoice.grand_total_paise} />} /></dl>
          <div className="grid gap-4 sm:grid-cols-3">
            <Field label="Mode"><Select value={t.trans_mode} onChange={(e) => set('trans_mode', e.target.value as EwayTransportInput['trans_mode'])}><option value="road">Road</option><option value="rail">Rail</option><option value="air">Air</option><option value="ship">Ship</option></Select></Field>
            <Field label="Distance (km)"><Input type="number" min={1} inputMode="numeric" value={t.trans_distance || ''} onChange={(e) => set('trans_distance', Number(e.target.value))} /></Field>
            <Field label="Vehicle type"><Select value={t.vehicle_type ?? ''} onChange={(e) => set('vehicle_type', (e.target.value || undefined) as EwayTransportInput['vehicle_type'])}><option value="">Regular</option><option value="R">Regular (R)</option><option value="O">Over-dimensional (O)</option></Select></Field>
            <Field label="Vehicle no"><Input value={t.vehicle_no ?? ''} onChange={(e) => set('vehicle_no', e.target.value)} placeholder="MH12AB1234" className="font-mono uppercase" /></Field>
            <Field label="Transporter ID (GSTIN / TRANSIN)"><Input value={t.transporter_id ?? ''} onChange={(e) => set('transporter_id', e.target.value)} className="font-mono uppercase" placeholder="Optional" /></Field>
            <Field label="Transporter name"><Input value={t.transporter_name ?? ''} onChange={(e) => set('transporter_name', e.target.value)} placeholder="Optional" /></Field>
            <Field label="Transport doc no" hint="LR / RR / AWB"><Input value={t.trans_doc_no ?? ''} onChange={(e) => set('trans_doc_no', e.target.value)} placeholder="Optional" /></Field>
            <Field label="Transport doc date"><Input type="date" value={t.trans_doc_date ?? ''} onChange={(e) => set('trans_doc_date', e.target.value || undefined)} /></Field>
            <div className="grid grid-cols-2 gap-2">
              <Field label="From PIN" hint="If not in the store address"><Input inputMode="numeric" value={t.from_pincode ?? ''} onChange={(e) => set('from_pincode', pin(e.target.value))} placeholder="411001" /></Field>
              <Field label="To PIN"><Input inputMode="numeric" value={t.to_pincode ?? ''} onChange={(e) => set('to_pincode', pin(e.target.value))} placeholder="411002" /></Field>
            </div>
          </div>
          {error && <p className="text-sm text-rose-600">{error}</p>}
          <p className="text-xs text-slate-500">kar builds the NIC bulk-upload JSON (v1.0.0621). Upload it on ewaybillgst.gov.in; the EWB number is not fetched automatically yet.</p>
        </div>
      )}
    </Modal>
  )
}
