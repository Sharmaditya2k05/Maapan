'use client'

/**
 * e-Invoice / e-Way bill panel for an invoice page: IRN, ack, signed QR, status and IRP error,
 * e-way bill number / validity / vehicle and history, with every action the role may take.
 * kar is the source of truth (GET /kar/v1/einvoices/{id}, /kar/v1/eway-bills/{id}); the invoice's
 * own copy (filled by kar's events) is the fallback while kar is unreachable.
 */
import { useState } from 'react'
import { Ban, Clock, Download, FileCheck2, FileJson, RefreshCw, Truck } from 'lucide-react'
import { Badge, Button, Card, statusTone } from '@/components/ui'
import { ApiError } from '@/lib/api'
import { fmtDate, useGet, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import { ewayCancelCheck, ewayExtendCheck, ewayModeLabel, irnCancelCheck } from '@/lib/compliance'
import type { InvoiceFull } from '@/lib/types.pos'
import type { EInvoiceRecord, EWayRecord } from '@/lib/types.kar'
import { KV, WarningList } from '@/components/ledger/bits'
import { downloadJSON } from '@/components/ledger/gst/shared'
import { ComplianceDialogs, ServerError, fileName, useEinvoiceJson, useGenerateIrn, type ComplianceDialog, type DocRef } from './actions'
import { QrSvg } from './QrSvg'

const missing = (e: unknown) => e instanceof ApiError && e.status === 404

/** kar's record when it exists, else what aadesh stored from kar's events. */
function useCompliance(inv: InvoiceFull) {
  const e = useGet<EInvoiceRecord>(`/kar/v1/einvoices/${inv.id}`, undefined, { retry: false })
  const w = useGet<EWayRecord>(`/kar/v1/eway-bills/${inv.id}`, undefined, { retry: false })
  const einvoice: EInvoiceRecord | null = e.data ?? (inv.irn || inv.einvoice_status ? {
    invoice_id: inv.id, invoice_number: inv.number, gstin: inv.store_gstin ?? '', doc_type: '', total_paise: inv.grand_total_paise,
    status: inv.einvoice_status ?? 'generated', irn: inv.irn, ack_no: inv.ack_no, ack_date: inv.ack_date, signed_qr: inv.signed_qr,
    error_code: inv.einvoice_error_code, error_message: inv.einvoice_error, cancelled_at: inv.irn_cancelled_at,
    attempts: 0, triggered_by: '', created_by: '', created_at: '', updated_at: '',
  } : null)
  const eway: EWayRecord | null = w.data ?? (inv.ewb_no || inv.ewb_status ? {
    invoice_id: inv.id, invoice_number: inv.number, gstin: inv.store_gstin ?? '', status: inv.ewb_status ?? 'generated', ewb_no: inv.ewb_no,
    ewb_date: inv.ewb_date, valid_upto: inv.ewb_valid_upto, vehicle_no: inv.ewb_vehicle_no, distance_km: inv.ewb_distance_km ?? 0,
    error_message: inv.ewb_error, extensions: 0, below_threshold: false, created_by: '', created_at: '', updated_at: '',
  } : null)
  return {
    einvoice, eway,
    loading: e.isLoading || w.isLoading,
    eError: e.error && !missing(e.error) ? e.error : null,
    wError: w.error && !missing(w.error) ? w.error : null,
    fromKar: { e: !!e.data, w: !!w.data },
  }
}

const docRef = (inv: InvoiceFull): DocRef => ({ id: inv.id, number: inv.number, total_paise: inv.grand_total_paise, party_name: inv.party_name, store_state: inv.store_state })

export function CompliancePanel({ invoice: inv }: { invoice: InvoiceFull }) {
  const canDo = usePerm()
  const { einvoice, eway, loading, eError, wError, fromKar } = useCompliance(inv)
  const [dialog, setDialog] = useState<ComplianceDialog | null>(null)
  const [jsonWarn, setJsonWarn] = useState<string[] | null>(null)
  const gen = useGenerateIrn()
  const json = useEinvoiceJson((w) => setJsonWarn(w))
  const posted = inv.status === 'posted'
  const doc = docRef(inv)
  const einvoiceable = inv.doc_type !== 'purchase'
  const canFile = canDo(PERMS.gstFile)
  const qr = einvoice?.signed_qr
  const irnOk = irnCancelCheck(einvoice, eway)
  const ewbCancel = ewayCancelCheck(eway)
  const ewbExtend = ewayExtendCheck(eway)
  const ewayActive = eway?.status === 'generated'
  const eStatus = einvoice?.status

  if (!einvoiceable && !eway && !posted) return null

  return (
    <Card title={einvoiceable ? 'e-Invoice & e-Way bill' : 'e-Way bill'} actions={loading ? <span className="text-xs text-slate-400">checking kar…</span> : null}>
      <div className="@container">
        <div className="grid gap-5 @2xl:grid-cols-2">
          {einvoiceable && (
            <section className="min-w-0">
              <div className="mb-2 flex flex-wrap items-center gap-2">
                <FileCheck2 size={16} className="text-brand-700" /><h3 className="text-sm font-bold">e-Invoice</h3>
                {eStatus ? <Badge tone={eStatus === 'generated' ? 'green' : statusTone(eStatus)}>{eStatus === 'generated' ? 'IRN generated' : eStatus}</Badge> : <Badge>not generated</Badge>}
                {einvoice && !fromKar.e && <span className="text-[11px] text-slate-400">(from the invoice)</span>}
              </div>
              {eError ? <ServerError error={eError} /> : null}
              {einvoice ? (
                <div className="flex flex-col gap-3 @md:flex-row">
                  {qr && eStatus !== 'failed' && <div className="shrink-0 self-start rounded-xl bg-white p-1 ring-1 ring-slate-200"><QrSvg text={qr} size={148} title={`Signed QR of ${inv.number}`} /></div>}
                  <dl className="min-w-0 flex-1 divide-y divide-slate-100">
                    {einvoice.irn && <KV k="IRN" v={<span className="break-all font-mono text-[11px]">{einvoice.irn}</span>} />}
                    {einvoice.ack_no && <KV k="Ack no" v={einvoice.ack_no} mono />}
                    {einvoice.ack_date && <KV k="Ack date" v={fmtDate(einvoice.ack_date, true)} />}
                    {eStatus === 'generated' && einvoice.cancellable_until && <KV k="Cancellable until" v={fmtDate(einvoice.cancellable_until, true)} />}
                    {einvoice.cancelled_at && <KV k="IRN cancelled" v={`${fmtDate(einvoice.cancelled_at, true)}${einvoice.cancel_remark ? ` · ${einvoice.cancel_remark}` : ''}`} />}
                    {einvoice.provider === 'simulator' && <KV k="Registered on" v={<Badge tone="amber">IRP simulator — not a real IRN</Badge>} />}
                  </dl>
                </div>
              ) : !eError && <p className="text-sm text-slate-500">{inv.party_gstin || inv.supply_category ? 'No IRN yet. Registrations with e-invoicing get one automatically for B2B and export documents.' : 'B2C documents do not need an IRN.'}</p>}
              {(eStatus === 'failed' || eStatus === 'pending') && (einvoice?.error_message || einvoice?.error_code) && (
                <div className="mt-2 rounded-xl bg-rose-50 px-3 py-2 text-sm text-rose-700">
                  <div className="font-semibold">{eStatus === 'pending' ? 'Will retry' : 'Rejected by the IRP'}{einvoice.error_code ? <> · code <span className="font-mono">{einvoice.error_code}</span></> : null}</div>
                  <div className="break-words">{einvoice.error_message}</div>
                  {eStatus === 'failed' && <div className="mt-1 text-xs text-rose-600">Fix the cause (item HSN, buyer address / PIN, registration city / PIN…) and retry.</div>}
                </div>
              )}
              <ServerError error={gen.error} />
              {jsonWarn && jsonWarn.length > 0 && <div className="mt-2"><WarningList items={jsonWarn} /></div>}
              {canFile && (
                <div className="mt-3 flex flex-wrap gap-2">
                  {posted && (!einvoice || eStatus === 'failed') && <Button size="sm" icon={eStatus === 'failed' ? RefreshCw : FileCheck2} loading={gen.isPending} onClick={() => gen.mutate(doc)}>{eStatus === 'failed' ? 'Retry IRN' : 'Generate IRN'}</Button>}
                  {eStatus === 'generated' && einvoice?.irn && <Button size="sm" variant="secondary" icon={Ban} className="text-rose-700" disabled={!irnOk.ok} title={irnOk.ok ? undefined : irnOk.reason} onClick={() => setDialog({ kind: 'cancel-irn', doc, einvoice, eway })}>Cancel IRN</Button>}
                  <Button size="sm" variant="ghost" icon={FileJson} loading={json.isPending} onClick={() => { setJsonWarn(null); json.mutate(doc) }}>IRP JSON</Button>
                  {einvoice?.signed_invoice && <Button size="sm" variant="ghost" icon={Download} onClick={() => downloadJSON(`SIGNED_${fileName(inv.number)}.json`, { Irn: einvoice.irn, AckNo: einvoice.ack_no, AckDt: einvoice.ack_date, SignedInvoice: einvoice.signed_invoice, SignedQRCode: einvoice.signed_qr })}>Signed invoice</Button>}
                </div>
              )}
              {eStatus === 'generated' && !irnOk.ok && canFile && <p className="mt-1 text-xs text-slate-500">{irnOk.reason}</p>}
            </section>
          )}

          <section className="min-w-0">
            <div className="mb-2 flex flex-wrap items-center gap-2">
              <Truck size={16} className="text-brand-700" /><h3 className="text-sm font-bold">e-Way bill</h3>
              {eway ? <Badge tone={eway.status === 'generated' ? 'green' : statusTone(eway.status)}>{eway.status === 'generated' ? (eway.valid_upto ? 'active' : 'Part-A only') : eway.status}</Badge> : <Badge>none</Badge>}
            </div>
            {wError ? <ServerError error={wError} /> : null}
            {eway ? (
              <dl className="divide-y divide-slate-100">
                {eway.ewb_no && <KV k="EWB no" v={eway.ewb_no} mono />}
                {eway.ewb_date && <KV k="Generated" v={fmtDate(eway.ewb_date, true)} />}
                <KV k="Valid until" v={eway.valid_upto ? fmtDate(eway.valid_upto, true) : '—'} />
                {(eway.vehicle_no || eway.mode) && <KV k="Vehicle" v={`${eway.vehicle_no || '—'} · ${ewayModeLabel(eway.mode)}${eway.vehicle_type === 'O' ? ' · ODC' : ''}`} />}
                <KV k="Distance" v={`${eway.distance_km} km`} />
                {eway.extensions > 0 && <KV k="Extensions" v={eway.extensions} />}
                {eway.cancelled_at && <KV k="Cancelled" v={`${fmtDate(eway.cancelled_at, true)}${eway.cancel_remark ? ` · ${eway.cancel_remark}` : ''}`} />}
              </dl>
            ) : !wError && <p className="text-sm text-slate-500">Needed when goods worth more than the threshold move (₹50,000; ₹1,00,000 intra-state in some states).</p>}
            {eway?.status === 'failed' && (eway.error_message || eway.error_code) && <div className="mt-2 rounded-xl bg-rose-50 px-3 py-2 text-sm text-rose-700"><span className="font-semibold">Rejected{eway.error_code ? ` · ${eway.error_code}` : ''}:</span> {eway.error_message}</div>}
            {eway?.warnings?.length ? <div className="mt-2"><WarningList items={eway.warnings} /></div> : null}
            {(eway?.history?.length ?? 0) > 1 && (
              <details className="mt-2 text-xs text-slate-600"><summary className="cursor-pointer font-semibold">History ({eway!.history!.length})</summary>
                <ul className="mt-1 space-y-0.5">{eway!.history!.map((h, i) => <li key={i}>{fmtDate(h.at, true)} · {h.action}{h.vehicle_no ? ` · ${h.vehicle_no}` : ''}{h.valid_upto ? ` · until ${fmtDate(h.valid_upto, true)}` : ''}</li>)}</ul>
              </details>
            )}
            {canDo(PERMS.invoiceRead) && (
              <div className="mt-3 flex flex-wrap gap-2">
                {posted && !ewayActive && <Button size="sm" icon={Truck} onClick={() => setDialog({ kind: 'eway', doc })}>{eway?.status === 'failed' ? 'Retry e-way bill' : 'Generate e-way bill'}</Button>}
                {ewayActive && <Button size="sm" variant="secondary" icon={Truck} onClick={() => setDialog({ kind: 'part-b', doc, eway })}>Update Part-B</Button>}
                {ewayActive && <Button size="sm" variant="secondary" icon={Clock} disabled={!ewbExtend.ok} title={ewbExtend.ok ? undefined : ewbExtend.reason} onClick={() => setDialog({ kind: 'extend', doc, eway })}>Extend</Button>}
                {ewayActive && canDo(PERMS.invoiceCancel) && <Button size="sm" variant="secondary" icon={Ban} className="text-rose-700" disabled={!ewbCancel.ok} title={ewbCancel.ok ? undefined : ewbCancel.reason} onClick={() => setDialog({ kind: 'cancel-ewb', doc, eway })}>Cancel</Button>}
              </div>
            )}
          </section>
        </div>
      </div>
      <ComplianceDialogs dialog={dialog} onClose={() => setDialog(null)} />
    </Card>
  )
}
