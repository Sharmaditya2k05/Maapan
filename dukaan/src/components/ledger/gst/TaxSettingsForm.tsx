'use client'

/** kar tax settings (GET/PUT /v1/settings): state, GSTIN, composition, e-invoicing, e-way and B2CL thresholds. */
import { useEffect, useState } from 'react'
import { ShieldCheck } from 'lucide-react'
import { Button, Card, Field, Input, MoneyInput, QueryState, Select, Toggle } from '@/components/ui'
import { api, post } from '@/lib/api'
import { fmtDate, useGet, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import { STATES, validateGSTIN } from '@/lib/gst'
import type { GstinResult, TaxSettings, TaxSettingsUpdate } from '@/lib/types.ledger'
import { GstinCard } from '../GstinCard'
import { NoPermission } from '../bits'
import { useAction } from '../useAction'

export function TaxSettingsForm() {
  const canDo = usePerm()
  const q = useGet<TaxSettings>(canDo(PERMS.reportRead) || canDo(PERMS.gstFile) || canDo(PERMS.settingsManage) ? '/kar/v1/settings' : null)
  if (!q.data && !q.isLoading && !q.error) return <NoPermission what="tax settings" />
  return (
    <QueryState isLoading={q.isLoading} error={q.error} data={q.data} retry={() => q.refetch()}>
      {(s) => <Form settings={s} readOnly={!canDo(PERMS.settingsManage)} />}
    </QueryState>
  )
}

function Form({ settings: s, readOnly }: { settings: TaxSettings; readOnly: boolean }) {
  const canDo = usePerm()
  const canVerify = canDo(PERMS.partyManage)
  const [d, setD] = useState<TaxSettings>(s)
  const [verified, setVerified] = useState<GstinResult | null>(null)
  const [error, setError] = useState<string | null>(null)
  useEffect(() => setD(s), [s])
  const set = <K extends keyof TaxSettings>(k: K, v: TaxSettings[K]) => setD((x) => ({ ...x, [k]: v }))
  const save = useAction<TaxSettings, TaxSettingsUpdate>((body) => api<TaxSettings>('/kar/v1/settings', { method: 'PUT', body }), {
    invalidate: ['/kar/v1/settings', '/kar/v1/returns'], successMessage: 'Tax settings saved',
  })
  const verify = useAction<GstinResult, string>((gstin) => post<GstinResult>('/kar/v1/gstin/verify', { gstin }), {
    onSuccess: (r) => { setVerified(r); if (r.valid && r.state_code) set('state_code', r.state_code) },
  })
  const gstin = d.gstin.trim().toUpperCase()
  const gstinErr = gstin ? validateGSTIN(gstin) : null
  const dirty = JSON.stringify(d) !== JSON.stringify(s)

  const submit = () => {
    setError(null)
    if (gstinErr) return setError(gstinErr)
    const body: TaxSettingsUpdate = {}
    if (d.state_code !== s.state_code) body.state_code = d.state_code
    if (gstin !== s.gstin) body.gstin = gstin
    if (d.composition !== s.composition) body.composition = d.composition
    if (d.einvoice_enabled !== s.einvoice_enabled) body.einvoice_enabled = d.einvoice_enabled
    if (d.eway_threshold_paise !== s.eway_threshold_paise) body.eway_threshold_paise = d.eway_threshold_paise
    if (d.b2cl_threshold_paise !== s.b2cl_threshold_paise) body.b2cl_threshold_paise = d.b2cl_threshold_paise
    if (Object.keys(body).length === 0) return
    save.mutate(body)
  }

  return (
    <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_320px]">
      <Card>
        <div className="space-y-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Registered state" hint="Decides CGST+SGST vs IGST on every bill">
              <Select value={d.state_code} disabled={readOnly} onChange={(e) => set('state_code', e.target.value)}>{Object.entries(STATES).map(([c, n]) => <option key={c} value={c}>{c} · {n}</option>)}</Select>
            </Field>
            <Field label="GSTIN" error={gstin && gstinErr ? gstinErr : undefined} hint={!gstin ? 'Blank = unregistered business (no GST on bills)' : undefined}>
              <div className="flex gap-2">
                <Input value={d.gstin} disabled={readOnly} onChange={(e) => { set('gstin', e.target.value.toUpperCase()); setVerified(null) }} className="font-mono uppercase" maxLength={15} />
                <Button variant="secondary" icon={ShieldCheck} disabled={!gstin || !!gstinErr || !canVerify} loading={verify.isPending} onClick={() => verify.mutate(gstin)}>Verify</Button>
              </div>
            </Field>
          </div>
          {verified && <GstinCard result={verified} />}
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="rounded-xl border border-slate-200 p-4">
              <Toggle checked={d.composition} onChange={(v) => !readOnly && set('composition', v)} label={<span className="font-semibold">Composition scheme</span>} />
              <p className="mt-1 text-xs text-slate-500">Composition dealers pay tax on turnover and cannot charge GST on bills or claim ITC.</p>
            </div>
            <div className="rounded-xl border border-slate-200 p-4">
              <Toggle checked={d.einvoice_enabled} onChange={(v) => !readOnly && set('einvoice_enabled', v)} label={<span className="font-semibold">e-Invoicing applies</span>} />
              <p className="mt-1 text-xs text-slate-500">Mandatory above the turnover limit notified by CBIC. kar warns when JSON is generated while this is off.</p>
            </div>
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="e-Way bill threshold" hint="Consignment value above which an e-way bill is required (₹50,000 nationally; some states differ)"><MoneyInput value={d.eway_threshold_paise} disabled={readOnly} onChange={(v) => set('eway_threshold_paise', v)} /></Field>
            <Field label="B2CL threshold" hint="Inter-state invoice value above which unregistered sales are reported invoice-wise in GSTR-1 (₹1,00,000)"><MoneyInput value={d.b2cl_threshold_paise} disabled={readOnly} onChange={(v) => set('b2cl_threshold_paise', v)} /></Field>
          </div>
          {error && <p className="text-sm text-rose-600">{error}</p>}
          {!readOnly && <div className="flex justify-end gap-2"><Button variant="secondary" disabled={!dirty} onClick={() => setD(s)}>Reset</Button><Button onClick={submit} loading={save.isPending} disabled={!dirty}>Save settings</Button></div>}
          {readOnly && <p className="text-xs text-slate-500">Only the owner (settings access) can change these.</p>}
        </div>
      </Card>
      <Card title="About">
        <ul className="space-y-2 text-sm text-slate-600">
          <li>Settings were seeded from your business profile when the account was created.</li>
          <li>Returns are built per GSTIN: stores in other states need their own registration — pass that GSTIN on the GSTR tabs.</li>
          <li>Last updated {fmtDate(s.updated_at, true)}.</li>
        </ul>
      </Card>
    </div>
  )
}
