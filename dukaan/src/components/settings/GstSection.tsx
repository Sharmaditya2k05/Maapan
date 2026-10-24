'use client'

import { useState } from 'react'
import { Save } from 'lucide-react'
import { Button, Card, ErrorBox, Field, Input, MoneyInput, QueryState, Toggle, useToast } from '@/components/ui'
import { fmtDate, useGet, usePerm, useSave } from '@/lib/hooks'
import { errorMessage } from '@/lib/api'
import { validateGSTIN } from '@/lib/gst'
import { PERMS } from '@/lib/rbac'
import type { TaxSettings, TaxSettingsUpdate } from '@/lib/types.stock'
import { StateSelect } from './StateSelect'

function GstForm({ s }: { s: TaxSettings }) {
  const canDo = usePerm()
  const toast = useToast()
  const [d, setD] = useState({ state_code: s.state_code, gstin: s.gstin, composition: s.composition, einvoice_enabled: s.einvoice_enabled, eway_threshold_paise: s.eway_threshold_paise, b2cl_threshold_paise: s.b2cl_threshold_paise })
  const [err, setErr] = useState('')
  const save = useSave<TaxSettings, TaxSettingsUpdate>('/kar/v1/settings', { method: 'PUT', invalidate: ['/kar/v1/settings'], silent: true })
  const canEdit = canDo(PERMS.settingsManage)
  const gstinErr = d.gstin ? validateGSTIN(d.gstin) : null

  const submit = async () => {
    setErr('')
    if (gstinErr) return setErr(gstinErr)
    // Only changed fields: kar merges them (COALESCE), so untouched settings stay as they are.
    const body: TaxSettingsUpdate = {}
    if (d.state_code !== s.state_code) body.state_code = d.state_code
    if (d.gstin !== s.gstin) body.gstin = d.gstin
    if (d.composition !== s.composition) body.composition = d.composition
    if (d.einvoice_enabled !== s.einvoice_enabled) body.einvoice_enabled = d.einvoice_enabled
    if (d.eway_threshold_paise !== s.eway_threshold_paise) body.eway_threshold_paise = d.eway_threshold_paise
    if (d.b2cl_threshold_paise !== s.b2cl_threshold_paise) body.b2cl_threshold_paise = d.b2cl_threshold_paise
    if (Object.keys(body).length === 0) return toast('Nothing changed', 'info')
    try { await save.mutateAsync(body); toast('GST settings saved') } catch (e) { setErr(errorMessage(e)) }
  }

  return (
    <form onSubmit={(e) => { e.preventDefault(); void submit() }} noValidate>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="GSTIN used for returns" error={gstinErr} hint="Usually the business GSTIN; branches file under their own"><Input value={d.gstin} onChange={(e) => setD((p) => ({ ...p, gstin: e.target.value.toUpperCase().replace(/[^0-9A-Z]/g, '').slice(0, 15) }))} className="font-mono uppercase" disabled={!canEdit} /></Field>
        <Field label="State"><StateSelect value={d.state_code} onChange={(v) => setD((p) => ({ ...p, state_code: v }))} disabled={!canEdit} /></Field>
        <div className="space-y-3 sm:col-span-2">
          <Toggle checked={d.composition} onChange={(v) => canEdit && setD((p) => ({ ...p, composition: v }))} label={<span>Composition scheme <span className="block text-xs text-slate-500">Bills of supply instead of tax invoices; no tax collected on sales, quarterly CMP-08</span></span>} />
          <Toggle checked={d.einvoice_enabled} onChange={(v) => canEdit && setD((p) => ({ ...p, einvoice_enabled: v }))} label={<span>e-Invoicing (IRN) enabled <span className="block text-xs text-slate-500">Mandatory above ₹5 crore turnover; generates the IRP JSON for B2B invoices</span></span>} />
        </div>
        <Field label="e-Way bill threshold" hint="Consignment value above which an e-way bill is needed (₹50,000 by default)"><MoneyInput value={d.eway_threshold_paise} onChange={(v) => setD((p) => ({ ...p, eway_threshold_paise: v }))} disabled={!canEdit} /></Field>
        <Field label="B2C large invoice threshold" hint="Inter-state B2C invoices above this are reported line-wise in GSTR-1 (₹1,00,000)"><MoneyInput value={d.b2cl_threshold_paise} onChange={(v) => setD((p) => ({ ...p, b2cl_threshold_paise: v }))} disabled={!canEdit} /></Field>
      </div>
      <p className="mt-3 text-xs text-slate-500">Last updated {fmtDate(s.updated_at, true)}.{!canEdit && ' Only owners can change these.'}</p>
      {err && <div className="mt-4"><ErrorBox message={err} /></div>}
      {canEdit && <div className="mt-5 flex justify-end"><Button type="submit" icon={Save} loading={save.isPending}>Save</Button></div>}
    </form>
  )
}

export function GstSection() {
  const settings = useGet<TaxSettings>('/kar/v1/settings')
  return (
    <Card title="GST settings">
      <QueryState isLoading={settings.isLoading} error={settings.error} data={settings.data} retry={() => settings.refetch()}>
        {(s) => <GstForm key={s.updated_at} s={s} />}
      </QueryState>
    </Card>
  )
}
