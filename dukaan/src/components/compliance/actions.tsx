'use client'

/**
 * e-Invoice / e-Way bill actions shared by the GST centre and the invoice pages:
 * generate / retry IRN, cancel IRN, generate e-way bill (or download its JSON), Part-B update,
 * extend validity, cancel e-way bill. Every server message is shown verbatim (IRP / NIC codes included).
 */
import { useState, type ReactNode } from 'react'
import { Ban, Clock, Download, RefreshCw, Truck } from 'lucide-react'
import { Button, ErrorBox, Field, Input, Modal, Money, Select, useToast } from '@/components/ui'
import { ApiError, api, errorMessage, post } from '@/lib/api'
import { fmtDate, todayIST, useInvalidate } from '@/lib/hooks'
import { STATE_LIST } from '@/lib/gst'
import {
  EWAY_MODES, EWB_CANCEL_REASONS, EXTEND_REASONS, IRN_CANCEL_REASONS, PARTB_REASONS, ewayModeLabel, irnCancelCheck, isValidPin, normaliseVehicleNo,
} from '@/lib/compliance'
import type { ComplianceCancelInput, ComplianceJSON, EInvoiceRecord, EWayExtendInput, EWayGenerateInput, EWayMode, EWayPartBInput, EWayRecord } from '@/lib/types.kar'
import { useAction } from '@/components/ledger/useAction'
import { KV, WarningList } from '@/components/ledger/bits'
import { downloadJSON } from '@/components/ledger/gst/shared'

/** Prefixes refreshed after any compliance change (aadesh catches up through kar's events a moment later). */
export const COMPLIANCE_PATHS = ['/kar/v1/einvoices', '/kar/v1/eway-bills', '/aadesh/v1/invoices']

/** The minimum an action needs to know about the document. */
export interface DocRef { id: string; number: string; total_paise?: number; party_name?: string; store_state?: string }

export const fileName = (number: string) => number.replace(/[/\\\s]+/g, '-')

/** Server error, verbatim, with any IRP / NIC detail list the API attached. */
export function ServerError({ error }: { error: unknown }) {
  if (!error) return null
  const details = error instanceof ApiError ? (error.details as { code?: string; errors?: unknown } | undefined) : undefined
  const list = Array.isArray(details?.errors) ? details.errors : []
  return (
    <div className="space-y-1">
      <ErrorBox message={errorMessage(error)} />
      {list.length > 0 && (
        <ul className="space-y-0.5 rounded-xl bg-rose-50/60 px-4 py-2 text-xs text-rose-700">
          {list.map((x, i) => <li key={i} className="font-mono">{typeof x === 'string' ? x : JSON.stringify(x)}</li>)}
        </ul>
      )}
    </div>
  )
}

/** Refresh kar and aadesh now, and once more after the event has reached aadesh. */
export function useComplianceRefresh() {
  const invalidate = useInvalidate()
  return () => {
    invalidate(...COMPLIANCE_PATHS)
    setTimeout(() => invalidate('/aadesh/v1/invoices'), 2000)
  }
}

/* ---------------- e-invoice ---------------- */

/** POST /kar/v1/einvoices/{id}: generate, or retry a failed one (a manual retry gets a fresh attempt budget). */
export function useGenerateIrn(onDone?: (r: EInvoiceRecord, doc: DocRef) => void) {
  const toast = useToast()
  const refresh = useComplianceRefresh()
  return useAction<EInvoiceRecord, DocRef>((doc) => post<EInvoiceRecord>(`/kar/v1/einvoices/${doc.id}`), {
    silent: true,
    onSuccess: (r, doc) => { refresh(); toast(r.irn ? `IRN generated for ${doc.number}` : `e-Invoice for ${doc.number}: ${r.status}`); onDone?.(r, doc) },
  })
}

/** POST /kar/v1/einvoices/{id}/json — the IRP JSON for offline upload; nothing is registered. */
export function useEinvoiceJson(onWarnings?: (w: string[], doc: DocRef) => void) {
  return useAction<ComplianceJSON, DocRef>((doc) => post<ComplianceJSON>(`/kar/v1/einvoices/${doc.id}/json`), {
    onSuccess: (r, doc) => { downloadJSON(`EINV_${fileName(doc.number)}.json`, r.payload); if (r.warnings?.length) onWarnings?.(r.warnings, doc) },
  })
}

export function CancelIrnModal({ doc, einvoice, eway, onClose, onDone }: { doc: DocRef; einvoice?: EInvoiceRecord | null; eway?: EWayRecord | null; onClose: () => void; onDone?: (r: EInvoiceRecord) => void }) {
  const toast = useToast()
  const refresh = useComplianceRefresh()
  const [reason, setReason] = useState(2)
  const [remark, setRemark] = useState('')
  const [local, setLocal] = useState('')
  const check = einvoice ? irnCancelCheck(einvoice, eway) : null
  const cancel = useAction<EInvoiceRecord, ComplianceCancelInput>((b) => post<EInvoiceRecord>(`/kar/v1/einvoices/${doc.id}/cancel`, b), {
    silent: true, onSuccess: (r) => { refresh(); toast(`IRN of ${doc.number} cancelled`); onDone?.(r); onClose() },
  })
  const submit = () => {
    setLocal('')
    if (!remark.trim()) return setLocal('A remark is required.')
    cancel.mutate({ reason_code: reason, remark: remark.trim() })
  }
  return (
    <Modal open onClose={onClose} title={`Cancel IRN · ${doc.number}`}
      footer={<><Button variant="secondary" onClick={onClose}>Keep it</Button><Button variant="danger" icon={Ban} onClick={submit} loading={cancel.isPending}>Cancel IRN</Button></>}>
      <div className="space-y-4">
        <p className="text-sm text-slate-600">The IRP allows cancelling an IRN only <span className="font-semibold">within 24 hours of acknowledgement</span>, and only when <span className="font-semibold">no active e-way bill</span> exists for it — cancel the e-way bill first. After 24 hours, issue a credit note instead. Cancelling the IRN does not cancel the invoice in your books; do that next if needed.</p>
        {einvoice?.cancellable_until && <dl className="rounded-xl bg-slate-50 px-4 py-2"><KV k="IRN" v={<span className="break-all font-mono text-[11px]">{einvoice.irn}</span>} /><KV k="Cancellable until" v={fmtDate(einvoice.cancellable_until, true)} /></dl>}
        {check && !check.ok && <p className="rounded-xl bg-amber-50 px-4 py-3 text-sm text-amber-800">{check.reason}</p>}
        <div className="grid gap-4 sm:grid-cols-[200px_1fr]">
          <Field label="Reason"><Select value={reason} onChange={(e) => setReason(Number(e.target.value))}>{IRN_CANCEL_REASONS.map((r) => <option key={r.code} value={r.code}>{r.code} · {r.label}</option>)}</Select></Field>
          <Field label="Remark" hint={`${remark.length}/100`}><Input value={remark} maxLength={100} onChange={(e) => setRemark(e.target.value)} placeholder="e.g. wrong buyer GSTIN" autoFocus /></Field>
        </div>
        {local && <ErrorBox message={local} />}
        <ServerError error={cancel.error} />
      </div>
    </Modal>
  )
}

/* ---------------- e-way bill ---------------- */

type Transport = { mode: EWayMode; distance: string; vehicle_no: string; vehicle_type: '' | 'R' | 'O'; transporter_id: string; transporter_name: string; trans_doc_no: string; trans_doc_date: string; from_pin: string; to_pin: string }
const emptyTransport = (): Transport => ({ mode: '1', distance: '', vehicle_no: '', vehicle_type: 'R', transporter_id: '', transporter_name: '', trans_doc_no: '', trans_doc_date: '', from_pin: '', to_pin: '' })

function transportBody(t: Transport): { body?: EWayGenerateInput; error?: string } {
  const km = Number(t.distance)
  if (!Number.isInteger(km) || km < 0 || km > 4000) return { error: 'Distance is a whole number of km (0 lets NIC compute it from the PIN codes, up to 4000).' }
  const vehicle = normaliseVehicleNo(t.vehicle_no)
  if (t.mode === '1' && !vehicle && !t.transporter_id.trim()) return { error: 'For road, give the vehicle number, or the transporter ID to leave Part-B to the transporter.' }
  if (t.mode !== '1' && (!t.trans_doc_no.trim() || !t.trans_doc_date)) return { error: 'Rail, air and ship need the transport document number (RR / AWB / BL) and its date.' }
  for (const [k, v] of [['From PIN', t.from_pin], ['To PIN', t.to_pin]] as const) if (v && !isValidPin(v)) return { error: `${k} must be 6 digits.` }
  const body: EWayGenerateInput = { mode: t.mode, distance_km: km }
  if (vehicle) body.vehicle_no = vehicle
  if (t.vehicle_type && vehicle) body.vehicle_type = t.vehicle_type
  if (t.transporter_id.trim()) body.transporter_id = t.transporter_id.trim().toUpperCase()
  if (t.transporter_name.trim()) body.transporter_name = t.transporter_name.trim()
  if (t.trans_doc_no.trim()) body.trans_doc_no = t.trans_doc_no.trim()
  if (t.trans_doc_date) body.trans_doc_date = t.trans_doc_date
  if (t.from_pin) body.from_pincode = Number(t.from_pin)
  if (t.to_pin) body.to_pincode = Number(t.to_pin)
  return { body }
}

function TransportFields({ t, set, children }: { t: Transport; set: <K extends keyof Transport>(k: K, v: Transport[K]) => void; children?: ReactNode }) {
  const road = t.mode === '1'
  return (
    <div className="@container">
      <div className="grid gap-4 @md:grid-cols-2 @2xl:grid-cols-3">
        <Field label="Mode"><Select value={t.mode} onChange={(e) => set('mode', e.target.value as EWayMode)}>{EWAY_MODES.map((m) => <option key={m.value} value={m.value}>{m.label}</option>)}</Select></Field>
        <Field label="Distance (km)" hint="Approximate; validity is 1 day per 200 km"><Input type="number" min={0} max={4000} inputMode="numeric" value={t.distance} onChange={(e) => set('distance', e.target.value.replace(/\D/g, ''))} /></Field>
        {road && <Field label="Vehicle type"><Select value={t.vehicle_type} onChange={(e) => set('vehicle_type', e.target.value as Transport['vehicle_type'])}><option value="R">Regular</option><option value="O">Over-dimensional cargo</option></Select></Field>}
        {road && <Field label="Vehicle no" hint="MH12AB1234, 22BH1234AA, TR… temporary"><Input value={t.vehicle_no} onChange={(e) => set('vehicle_no', e.target.value.toUpperCase())} className="font-mono uppercase" placeholder="MH12AB1234" /></Field>}
        <Field label="Transporter ID" hint="GSTIN / TRANSIN, optional"><Input value={t.transporter_id} onChange={(e) => set('transporter_id', e.target.value.toUpperCase())} className="font-mono uppercase" maxLength={15} /></Field>
        <Field label="Transporter name"><Input value={t.transporter_name} onChange={(e) => set('transporter_name', e.target.value)} /></Field>
        <Field label={road ? 'Transport doc no (LR)' : 'Transport doc no (RR / AWB / BL)'}><Input value={t.trans_doc_no} onChange={(e) => set('trans_doc_no', e.target.value)} maxLength={15} /></Field>
        <Field label="Transport doc date"><Input type="date" value={t.trans_doc_date} max={todayIST()} onChange={(e) => set('trans_doc_date', e.target.value)} /></Field>
        {children}
      </div>
    </div>
  )
}

/** Generate the e-way bill on NIC (POST /kar/v1/eway-bills/{id}), or download the bulk JSON with the same details. */
export function EwayGenerateModal({ doc, onClose, onDone }: { doc: DocRef; onClose: () => void; onDone?: (r: EWayRecord) => void }) {
  const toast = useToast()
  const refresh = useComplianceRefresh()
  const [t, setT] = useState<Transport>(emptyTransport)
  const [local, setLocal] = useState('')
  const [jsonWarn, setJsonWarn] = useState<string[] | null>(null)
  const set = <K extends keyof Transport>(k: K, v: Transport[K]) => setT((x) => ({ ...x, [k]: v }))
  const gen = useAction<EWayRecord, EWayGenerateInput>((b) => post<EWayRecord>(`/kar/v1/eway-bills/${doc.id}`, b), {
    silent: true, onSuccess: (r) => { refresh(); toast(r.ewb_no ? `E-way bill ${r.ewb_no} generated` : `E-way bill: ${r.status}`); onDone?.(r); onClose() },
  })
  const json = useAction<ComplianceJSON, EWayGenerateInput>((b) => post<ComplianceJSON>(`/kar/v1/eway-bills/${doc.id}/json`, b), {
    silent: true, onSuccess: (r) => { downloadJSON(`EWB_${fileName(doc.number)}.json`, r.payload); setJsonWarn([...(r.warnings ?? []), ...(r.below_threshold ? ['The consignment is below the e-way bill threshold: the bill is optional.'] : [])]) },
  })
  const run = (which: 'gen' | 'json') => {
    setLocal(''); setJsonWarn(null)
    const { body, error } = transportBody(t)
    if (error || !body) return setLocal(error ?? 'Check the transport details.')
    if (which === 'gen') gen.mutate(body); else json.mutate(body)
  }
  return (
    <Modal open onClose={onClose} title={`e-Way bill · ${doc.number}`} size="lg"
      footer={<><Button variant="secondary" onClick={onClose}>Close</Button><Button variant="secondary" icon={Download} onClick={() => run('json')} loading={json.isPending}>Download JSON</Button><Button icon={Truck} onClick={() => run('gen')} loading={gen.isPending}>Generate e-way bill</Button></>}>
      <div className="space-y-4">
        {(doc.party_name || doc.total_paise !== undefined) && <dl className="grid gap-x-6 rounded-xl bg-slate-50 px-4 py-2 sm:grid-cols-2"><KV k="Consignee" v={doc.party_name} /><KV k="Invoice value" v={<Money paise={doc.total_paise} />} /></dl>}
        <TransportFields t={t} set={set}>
          <Field label="From PIN" hint="Only if dispatch is not from the store address"><Input inputMode="numeric" value={t.from_pin} onChange={(e) => set('from_pin', e.target.value.replace(/\D/g, '').slice(0, 6))} className="num" /></Field>
          <Field label="To PIN" hint="Only if delivery is not at the buyer's address"><Input inputMode="numeric" value={t.to_pin} onChange={(e) => set('to_pin', e.target.value.replace(/\D/g, '').slice(0, 6))} className="num" /></Field>
        </TransportFields>
        <p className="text-xs text-slate-500">When the invoice has an IRN the bill is generated from it. Documents older than 180 days are refused. Below the threshold (₹50,000 inter-state; ₹1,00,000 intra-state in some states) the bill is optional — it is generated anyway with a warning.</p>
        {local && <ErrorBox message={local} />}
        <ServerError error={gen.error ?? json.error} />
        {jsonWarn && (jsonWarn.length ? <WarningList items={jsonWarn} /> : <p className="rounded-xl bg-emerald-50 px-4 py-2 text-sm text-emerald-800">JSON downloaded. Upload it on ewaybillgst.gov.in (bulk generation).</p>)}
      </div>
    </Modal>
  )
}

/** PUT /kar/v1/eway-bills/{id}/part-b — vehicle / transport document change. */
export function PartBModal({ doc, eway, onClose }: { doc: DocRef; eway?: EWayRecord | null; onClose: () => void }) {
  const toast = useToast()
  const refresh = useComplianceRefresh()
  const [t, setT] = useState<Transport>(() => ({ ...emptyTransport(), mode: (eway?.mode as EWayMode) || '1' }))
  const [place, setPlace] = useState('')
  const [state, setState] = useState(doc.store_state ?? '')
  const [reason, setReason] = useState<EWayPartBInput['reason_code']>(eway?.vehicle_no ? '1' : '4')
  const [remark, setRemark] = useState('')
  const [local, setLocal] = useState('')
  const set = <K extends keyof Transport>(k: K, v: Transport[K]) => setT((x) => ({ ...x, [k]: v }))
  const save = useAction<EWayRecord, EWayPartBInput>((b) => api<EWayRecord>(`/kar/v1/eway-bills/${doc.id}/part-b`, { method: 'PUT', body: b }), {
    silent: true, onSuccess: (r) => { refresh(); toast(`Part-B updated${r.valid_upto ? ` · valid until ${fmtDate(r.valid_upto, true)}` : ''}`); onClose() },
  })
  const submit = () => {
    setLocal('')
    const vehicle = normaliseVehicleNo(t.vehicle_no)
    if (t.mode === '1' && !vehicle) return setLocal('Vehicle number is required for road.')
    if (t.mode !== '1' && (!t.trans_doc_no.trim() || !t.trans_doc_date)) return setLocal('Transport document number and date are required for rail, air and ship.')
    if (!place.trim()) return setLocal('Where is the goods now? (place of change)')
    if (!state) return setLocal('Choose the state of the place of change.')
    const b: EWayPartBInput = { mode: t.mode, from_place: place.trim(), from_state: state, reason_code: reason }
    if (vehicle) b.vehicle_no = vehicle
    if (vehicle && t.vehicle_type) b.vehicle_type = t.vehicle_type
    if (t.trans_doc_no.trim()) b.trans_doc_no = t.trans_doc_no.trim()
    if (t.trans_doc_date) b.trans_doc_date = t.trans_doc_date
    if (remark.trim()) b.reason_remark = remark.trim()
    save.mutate(b)
  }
  return (
    <Modal open onClose={onClose} title={`Update Part-B · EWB ${eway?.ewb_no ?? ''}`} size="lg"
      footer={<><Button variant="secondary" onClick={onClose}>Cancel</Button><Button icon={Truck} onClick={submit} loading={save.isPending}>Update vehicle</Button></>}>
      <div className="space-y-4">
        {eway && <dl className="grid gap-x-6 rounded-xl bg-slate-50 px-4 py-2 sm:grid-cols-2"><KV k="Current vehicle" v={eway.vehicle_no || '—'} mono /><KV k="Mode" v={ewayModeLabel(eway.mode)} /><KV k="Valid until" v={eway.valid_upto ? fmtDate(eway.valid_upto, true) : 'Part-A only'} /><KV k="Distance" v={`${eway.distance_km} km`} /></dl>}
        <div className="@container">
          <div className="grid gap-4 @md:grid-cols-2">
            <Field label="Mode"><Select value={t.mode} onChange={(e) => set('mode', e.target.value as EWayMode)}>{EWAY_MODES.map((m) => <option key={m.value} value={m.value}>{m.label}</option>)}</Select></Field>
            {t.mode === '1' && <Field label="New vehicle no"><Input value={t.vehicle_no} onChange={(e) => set('vehicle_no', e.target.value.toUpperCase())} className="font-mono uppercase" autoFocus /></Field>}
            {t.mode === '1' && <Field label="Vehicle type"><Select value={t.vehicle_type} onChange={(e) => set('vehicle_type', e.target.value as Transport['vehicle_type'])}><option value="R">Regular</option><option value="O">Over-dimensional cargo</option></Select></Field>}
            <Field label="Transport doc no"><Input value={t.trans_doc_no} onChange={(e) => set('trans_doc_no', e.target.value)} maxLength={15} /></Field>
            <Field label="Transport doc date"><Input type="date" value={t.trans_doc_date} max={todayIST()} onChange={(e) => set('trans_doc_date', e.target.value)} /></Field>
            <Field label="Place of change"><Input value={place} onChange={(e) => setPlace(e.target.value)} placeholder="Nashik" maxLength={50} /></Field>
            <Field label="State"><Select value={state} onChange={(e) => setState(e.target.value)}><option value="">Choose…</option>{STATE_LIST.map(([c, n]) => <option key={c} value={c}>{c} · {n}</option>)}</Select></Field>
            <Field label="Reason"><Select value={reason} onChange={(e) => setReason(e.target.value as EWayPartBInput['reason_code'])}>{PARTB_REASONS.map((r) => <option key={r.code} value={r.code}>{r.label}</option>)}</Select></Field>
            <Field label="Remark"><Input value={remark} onChange={(e) => setRemark(e.target.value)} maxLength={50} placeholder="Optional" /></Field>
          </div>
        </div>
        {local && <ErrorBox message={local} />}
        <ServerError error={save.error} />
      </div>
    </Modal>
  )
}

/** POST /kar/v1/eway-bills/{id}/extend — 8 h either side of expiry. */
export function ExtendModal({ doc, eway, onClose }: { doc: DocRef; eway?: EWayRecord | null; onClose: () => void }) {
  const toast = useToast()
  const refresh = useComplianceRefresh()
  const [d, setD] = useState({ place: '', state: doc.store_state ?? '', pin: '', km: '', status: 'M' as 'M' | 'T', transit: 'R' as 'R' | 'W' | 'O', reason: 99, remark: '', vehicle: '', doc_no: '', doc_date: '' })
  const [local, setLocal] = useState('')
  const set = <K extends keyof typeof d>(k: K, v: (typeof d)[K]) => setD((x) => ({ ...x, [k]: v }))
  const save = useAction<EWayRecord, EWayExtendInput>((b) => post<EWayRecord>(`/kar/v1/eway-bills/${doc.id}/extend`, b), {
    silent: true, onSuccess: (r) => { refresh(); toast(`Validity extended${r.valid_upto ? ` to ${fmtDate(r.valid_upto, true)}` : ''}`); onClose() },
  })
  const submit = () => {
    setLocal('')
    const km = Number(d.km)
    if (!d.place.trim() || !d.state) return setLocal('Current place and state are required.')
    if (!isValidPin(d.pin)) return setLocal('Current PIN code (6 digits) is required.')
    if (!Number.isInteger(km) || km < 1 || km > 4000) return setLocal('Remaining distance is 1–4000 km.')
    if (!d.remark.trim()) return setLocal('A remark is required.')
    const b: EWayExtendInput = { from_place: d.place.trim(), from_state: d.state, from_pincode: Number(d.pin), remaining_distance_km: km, consignment_status: d.status, reason_code: d.reason, remark: d.remark.trim() }
    if (d.status === 'T') b.transit_type = d.transit
    if (d.status === 'M' && d.vehicle.trim()) { b.mode = (eway?.mode as EWayMode) || '1'; b.vehicle_no = normaliseVehicleNo(d.vehicle) }
    if (d.doc_no.trim()) b.trans_doc_no = d.doc_no.trim()
    if (d.doc_date) b.trans_doc_date = d.doc_date
    save.mutate(b)
  }
  return (
    <Modal open onClose={onClose} title={`Extend validity · EWB ${eway?.ewb_no ?? ''}`} size="lg"
      footer={<><Button variant="secondary" onClick={onClose}>Cancel</Button><Button icon={Clock} onClick={submit} loading={save.isPending}>Extend</Button></>}>
      <div className="space-y-4">
        <p className="text-sm text-slate-600">Allowed from 8 hours before to 8 hours after the validity ends{eway?.valid_upto ? <> (<span className="font-semibold">{fmtDate(eway.valid_upto, true)}</span>)</> : null}, up to 360 days after generation. The new validity counts from the remaining distance.</p>
        <div className="@container">
          <div className="grid gap-4 @md:grid-cols-2">
            <Field label="Current place"><Input value={d.place} onChange={(e) => set('place', e.target.value)} maxLength={50} autoFocus /></Field>
            <Field label="State"><Select value={d.state} onChange={(e) => set('state', e.target.value)}><option value="">Choose…</option>{STATE_LIST.map(([c, n]) => <option key={c} value={c}>{c} · {n}</option>)}</Select></Field>
            <Field label="Current PIN"><Input value={d.pin} inputMode="numeric" className="num" onChange={(e) => set('pin', e.target.value.replace(/\D/g, '').slice(0, 6))} /></Field>
            <Field label="Remaining distance (km)"><Input value={d.km} inputMode="numeric" className="num" onChange={(e) => set('km', e.target.value.replace(/\D/g, ''))} /></Field>
            <Field label="Consignment"><Select value={d.status} onChange={(e) => set('status', e.target.value as 'M' | 'T')}><option value="M">In movement</option><option value="T">In transit (stored)</option></Select></Field>
            {d.status === 'T'
              ? <Field label="Transit type"><Select value={d.transit} onChange={(e) => set('transit', e.target.value as 'R' | 'W' | 'O')}><option value="R">Road</option><option value="W">Warehouse</option><option value="O">Others</option></Select></Field>
              : <Field label="Vehicle no" hint={`Blank keeps ${eway?.vehicle_no || 'the current vehicle'}`}><Input value={d.vehicle} onChange={(e) => set('vehicle', e.target.value.toUpperCase())} className="font-mono uppercase" /></Field>}
            <Field label="Reason"><Select value={d.reason} onChange={(e) => set('reason', Number(e.target.value))}>{EXTEND_REASONS.map((r) => <option key={r.code} value={r.code}>{r.label}</option>)}</Select></Field>
            <Field label="Remark"><Input value={d.remark} onChange={(e) => set('remark', e.target.value)} maxLength={50} /></Field>
            <Field label="Transport doc no"><Input value={d.doc_no} onChange={(e) => set('doc_no', e.target.value)} maxLength={15} placeholder="Optional" /></Field>
            <Field label="Transport doc date"><Input type="date" value={d.doc_date} max={todayIST()} onChange={(e) => set('doc_date', e.target.value)} /></Field>
          </div>
        </div>
        {local && <ErrorBox message={local} />}
        <ServerError error={save.error} />
      </div>
    </Modal>
  )
}

/** POST /kar/v1/eway-bills/{id}/cancel — within 24 h of generation. */
export function EwayCancelModal({ doc, eway, onClose }: { doc: DocRef; eway?: EWayRecord | null; onClose: () => void }) {
  const toast = useToast()
  const refresh = useComplianceRefresh()
  const [reason, setReason] = useState(2)
  const [remark, setRemark] = useState('')
  const [local, setLocal] = useState('')
  const cancel = useAction<EWayRecord, ComplianceCancelInput>((b) => post<EWayRecord>(`/kar/v1/eway-bills/${doc.id}/cancel`, b), {
    silent: true, onSuccess: () => { refresh(); toast(`E-way bill ${eway?.ewb_no ?? ''} cancelled`); onClose() },
  })
  return (
    <Modal open onClose={onClose} title={`Cancel e-way bill ${eway?.ewb_no ?? ''}`}
      footer={<><Button variant="secondary" onClick={onClose}>Keep it</Button><Button variant="danger" icon={Ban} loading={cancel.isPending} onClick={() => { setLocal(''); if (!remark.trim()) return setLocal('A remark is required.'); cancel.mutate({ reason_code: reason, remark: remark.trim() }) }}>Cancel e-way bill</Button></>}>
      <div className="space-y-4">
        <p className="text-sm text-slate-600">An e-way bill can be cancelled within 24 hours of generation{eway?.ewb_date ? <> (generated <span className="font-semibold">{fmtDate(eway.ewb_date, true)}</span>)</> : null}, and only if it has not been verified in transit.</p>
        <div className="grid gap-4 sm:grid-cols-[200px_1fr]">
          <Field label="Reason"><Select value={reason} onChange={(e) => setReason(Number(e.target.value))}>{EWB_CANCEL_REASONS.map((r) => <option key={r.code} value={r.code}>{r.code} · {r.label}</option>)}</Select></Field>
          <Field label="Remark" hint={`${remark.length}/50`}><Input value={remark} maxLength={50} onChange={(e) => setRemark(e.target.value)} autoFocus /></Field>
        </div>
        {local && <ErrorBox message={local} />}
        <ServerError error={cancel.error} />
      </div>
    </Modal>
  )
}

/* ---------------- one switch for all of them ---------------- */

export type ComplianceDialog =
  | { kind: 'cancel-irn'; doc: DocRef; einvoice?: EInvoiceRecord | null; eway?: EWayRecord | null }
  | { kind: 'eway'; doc: DocRef }
  | { kind: 'part-b' | 'extend' | 'cancel-ewb'; doc: DocRef; eway?: EWayRecord | null }

export function ComplianceDialogs({ dialog, onClose, onIrnCancelled }: { dialog: ComplianceDialog | null; onClose: () => void; onIrnCancelled?: (r: EInvoiceRecord) => void }) {
  if (!dialog) return null
  switch (dialog.kind) {
    case 'cancel-irn': return <CancelIrnModal doc={dialog.doc} einvoice={dialog.einvoice} eway={dialog.eway} onClose={onClose} onDone={onIrnCancelled} />
    case 'eway': return <EwayGenerateModal doc={dialog.doc} onClose={onClose} />
    case 'part-b': return <PartBModal doc={dialog.doc} eway={dialog.eway} onClose={onClose} />
    case 'extend': return <ExtendModal doc={dialog.doc} eway={dialog.eway} onClose={onClose} />
    case 'cancel-ewb': return <EwayCancelModal doc={dialog.doc} eway={dialog.eway} onClose={onClose} />
  }
}

/** Small "retry" button text for failed documents. */
export const RetryIcon = RefreshCw
