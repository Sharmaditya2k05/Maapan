'use client'

import { useState } from 'react'
import { Save } from 'lucide-react'
import { Button, Card, ErrorBox, Field, Input, QueryState, Textarea, useToast } from '@/components/ui'
import { useGet, usePerm, useSave } from '@/lib/hooks'
import { errorMessage } from '@/lib/api'
import { validateGSTIN } from '@/lib/gst'
import { PERMS } from '@/lib/rbac'
import { refreshSessionUser } from '@/lib/pehchaan'
import type { Tenant, TenantInput } from '@/lib/types.stock'
import { StateSelect } from './StateSelect'

function BusinessForm({ tenant }: { tenant: Tenant }) {
  const canDo = usePerm()
  const toast = useToast()
  const [d, setD] = useState<TenantInput>({
    business_name: tenant.business_name, gstin: tenant.gstin ?? '', state_code: tenant.state_code,
    address: tenant.address ?? '', email: tenant.email ?? '', phone: tenant.phone ?? '',
  })
  const [err, setErr] = useState('')
  const save = useSave<Tenant, TenantInput>('/pehchaan/v1/tenant', { method: 'PUT', invalidate: ['/pehchaan/v1/tenant'], silent: true })
  const gstinErr = d.gstin ? validateGSTIN(d.gstin) : null
  const canEdit = canDo(PERMS.settingsManage)

  const setGstin = (raw: string) => {
    const g = raw.toUpperCase().replace(/[^0-9A-Z]/g, '').slice(0, 15)
    setD((x) => ({ ...x, gstin: g, state_code: g.length === 15 && !validateGSTIN(g) ? g.slice(0, 2) : x.state_code }))
  }

  const submit = async () => {
    setErr('')
    if (!d.business_name.trim()) return setErr('Business name is required.')
    if (gstinErr) return setErr(gstinErr)
    if (!d.state_code) return setErr('Choose the state of registration.')
    if (d.gstin && d.gstin.slice(0, 2) !== d.state_code) return setErr('State does not match the GSTIN state code.')
    try {
      await save.mutateAsync({ ...d, business_name: d.business_name.trim(), address: d.address?.trim(), email: d.email?.trim(), phone: d.phone?.trim() })
      toast('Business profile saved')
      refreshSessionUser().catch(() => {})
    } catch (e) { setErr(errorMessage(e)) }
  }

  return (
    <form onSubmit={(e) => { e.preventDefault(); void submit() }} noValidate>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Business name" className="sm:col-span-2"><Input value={d.business_name} onChange={(e) => setD({ ...d, business_name: e.target.value })} disabled={!canEdit} /></Field>
        <Field label="GSTIN" error={gstinErr} hint="Leave empty if not registered; the state is read from it"><Input value={d.gstin} onChange={(e) => setGstin(e.target.value)} className="font-mono uppercase" placeholder="27AAPFU0939F1ZV" disabled={!canEdit} /></Field>
        <Field label="State of registration"><StateSelect value={d.state_code} onChange={(v) => setD({ ...d, state_code: v })} disabled={!canEdit || (!!d.gstin && !gstinErr)} /></Field>
        <Field label="Phone"><Input value={d.phone} onChange={(e) => setD({ ...d, phone: e.target.value })} inputMode="tel" disabled={!canEdit} /></Field>
        <Field label="Email"><Input type="email" value={d.email} onChange={(e) => setD({ ...d, email: e.target.value })} disabled={!canEdit} /></Field>
        <Field label="Address" className="sm:col-span-2" hint="Printed on invoices"><Textarea rows={3} value={d.address} onChange={(e) => setD({ ...d, address: e.target.value })} disabled={!canEdit} /></Field>
      </div>
      {tenant.pan && <p className="mt-3 text-xs text-slate-500">PAN {tenant.pan} (derived from the GSTIN) · tenant {tenant.id.slice(0, 8)} · {tenant.status}</p>}
      {err && <div className="mt-4"><ErrorBox message={err} /></div>}
      {canEdit && <div className="mt-5 flex justify-end"><Button type="submit" icon={Save} loading={save.isPending}>Save changes</Button></div>}
    </form>
  )
}

export function BusinessSection() {
  const tenant = useGet<Tenant>('/pehchaan/v1/tenant')
  return (
    <Card title="Business profile">
      <QueryState isLoading={tenant.isLoading} error={tenant.error} data={tenant.data} retry={() => tenant.refetch()}>
        {(t) => <BusinessForm key={t.id} tenant={t} />}
      </QueryState>
    </Card>
  )
}
