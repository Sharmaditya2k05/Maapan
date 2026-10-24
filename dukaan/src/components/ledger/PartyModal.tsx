'use client'

/** Add / edit a party (bahi POST / PATCH /v1/parties) with GSTIN verification through kar. */
import { useEffect, useState } from 'react'
import { ShieldCheck } from 'lucide-react'
import { Button, Field, Input, Modal, MoneyInput, Select, Textarea } from '@/components/ui'
import { api, post } from '@/lib/api'
import { fyStartIST, todayIST, useSave } from '@/lib/hooks'
import { STATES, validateGSTIN } from '@/lib/gst'
import type { Party } from '@/lib/types'
import type { GstinResult, PartyInput, PartyPatch } from '@/lib/types.ledger'
import { GstinCard } from './GstinCard'
import { useAction } from './useAction'

export interface PartyModalProps {
  open: boolean
  onClose: () => void
  /** Editing an existing party; omit to create. */
  party?: Party | null
  defaultType?: Party['type']
  defaultName?: string
  onSaved?: (party: Party) => void
}

interface Draft {
  name: string; type: Party['type']; phone: string; email: string; gstin: string; state_code: string; address: string
  credit_limit_paise: number; credit_days: string
  opening_balance_paise: number; opening_type: 'dr' | 'cr'; opening_as_of: string
}

const fromParty = (p: Party | null | undefined, type: Party['type'], name: string): Draft => ({
  name: p?.name ?? name, type: p?.type ?? type, phone: p?.phone ?? '', email: p?.email ?? '', gstin: p?.gstin ?? '', state_code: p?.state_code ?? '',
  address: p?.address ?? '', credit_limit_paise: p?.credit_limit_paise ?? 0, credit_days: String(p?.credit_days ?? 0),
  opening_balance_paise: 0, opening_type: type === 'supplier' ? 'cr' : 'dr', opening_as_of: fyStartIST(),
})

export function PartyModal({ open, onClose, party, defaultType = 'customer', defaultName = '', onSaved }: PartyModalProps) {
  const [d, setD] = useState<Draft>(() => fromParty(party, defaultType, defaultName))
  const [verified, setVerified] = useState<GstinResult | null>(null)
  const [error, setError] = useState<string | null>(null)
  const set = <K extends keyof Draft>(k: K, v: Draft[K]) => setD((x) => ({ ...x, [k]: v }))

  useEffect(() => { if (open) { setD(fromParty(party, defaultType, defaultName)); setVerified(null); setError(null) } }, [open, party, defaultType, defaultName])

  const verify = useAction<GstinResult, string>((gstin) => post<GstinResult>('/kar/v1/gstin/verify', { gstin }), {
    onSuccess: (r) => {
      setVerified(r)
      if (r.valid) setD((x) => ({ ...x, gstin: r.gstin, state_code: r.state_code || x.state_code, name: x.name || r.trade_name || r.legal_name, address: x.address || r.address }))
    },
  })
  const create = useSave<Party, PartyInput>('/bahi/v1/parties', { invalidate: ['/bahi/v1/parties'], onSuccess: (p) => { onSaved?.(p); onClose() } })
  const patch = useAction<Party, PartyPatch>((body) => api<Party>(`/bahi/v1/parties/${party!.id}`, { method: 'PATCH', body }), {
    invalidate: ['/bahi/v1/parties'], successMessage: 'Party updated', onSuccess: (p) => { onSaved?.(p); onClose() },
  })
  const busy = create.isPending || patch.isPending

  const gstin = d.gstin.trim().toUpperCase()
  const gstinErr = gstin ? validateGSTIN(gstin) : null

  const submit = async () => {
    setError(null)
    if (!d.name.trim()) return setError('Name is required.')
    if (gstinErr) return setError(gstinErr)
    const creditDays = Math.max(0, Number(d.credit_days) || 0)
    if (party) {
      const body: PartyPatch = {}
      if (d.name.trim() !== party.name) body.name = d.name.trim()
      if (d.type !== party.type) body.type = d.type
      if (d.phone.trim() !== party.phone) body.phone = d.phone.trim()
      if (d.email.trim() !== party.email) body.email = d.email.trim()
      if (gstin !== party.gstin) body.gstin = gstin
      if (d.state_code !== party.state_code) body.state_code = d.state_code
      if (d.address.trim() !== party.address) body.address = d.address.trim()
      if (d.credit_limit_paise !== party.credit_limit_paise) body.credit_limit_paise = d.credit_limit_paise
      if (creditDays !== party.credit_days) body.credit_days = creditDays
      if (Object.keys(body).length === 0) return onClose()
      await patch.mutateAsync(body).catch(() => undefined)
    } else {
      const body: PartyInput = {
        name: d.name.trim(), type: d.type, phone: d.phone.trim(), email: d.email.trim(), gstin, state_code: d.state_code, address: d.address.trim(),
        credit_limit_paise: d.credit_limit_paise, credit_days: creditDays,
        ...(d.opening_balance_paise > 0 ? { opening_balance_paise: d.opening_balance_paise, opening_type: d.opening_type, opening_as_of: d.opening_as_of } : {}),
      }
      await create.mutateAsync(body).catch(() => undefined)
    }
  }

  return (
    <Modal open={open} onClose={onClose} size="lg" title={party ? `Edit ${party.name}` : 'Add party'}
      footer={<><Button variant="secondary" onClick={onClose}>Cancel</Button><Button onClick={submit} loading={busy}>{party ? 'Save changes' : 'Add party'}</Button></>}>
      <div className="space-y-4">
        <div className="grid gap-4 sm:grid-cols-[1fr_11rem]">
          <Field label="Name"><Input value={d.name} onChange={(e) => set('name', e.target.value)} autoFocus placeholder="Ramesh Traders" /></Field>
          <Field label="Type" hint={party ? 'Changing type does not move past entries between debtors and creditors.' : undefined}>
            <Select value={d.type} onChange={(e) => set('type', e.target.value as Party['type'])}><option value="customer">Customer</option><option value="supplier">Supplier</option><option value="both">Both</option></Select>
          </Field>
        </div>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Phone"><Input value={d.phone} inputMode="tel" onChange={(e) => set('phone', e.target.value)} placeholder="98765 43210" /></Field>
          <Field label="Email"><Input value={d.email} type="email" onChange={(e) => set('email', e.target.value)} placeholder="Optional" /></Field>
        </div>
        <Field label="GSTIN" error={gstin && gstinErr ? gstinErr : undefined} hint={!gstin ? 'Leave blank for an unregistered party.' : undefined}>
          <div className="flex gap-2">
            <Input value={d.gstin} onChange={(e) => { set('gstin', e.target.value.toUpperCase()); setVerified(null) }} className="font-mono uppercase" placeholder="27AAPFU0939F1ZV" maxLength={15} />
            <Button variant="secondary" icon={ShieldCheck} loading={verify.isPending} disabled={!gstin || !!gstinErr} onClick={() => verify.mutate(gstin)}>Verify</Button>
          </div>
        </Field>
        {verified && <GstinCard result={verified} compact={!verified.legal_name} />}
        {party?.gst_verified && !verified && gstin === party.gstin && <p className="text-xs text-emerald-700">GSTIN was verified with the GST portal when this party was saved.</p>}
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="State" hint="Place of supply for bills to this party">
            <Select value={d.state_code} onChange={(e) => set('state_code', e.target.value)}>
              <option value="">— Select —</option>
              {Object.entries(STATES).map(([code, name]) => <option key={code} value={code}>{code} · {name}</option>)}
            </Select>
          </Field>
          <Field label="Address"><Textarea rows={2} value={d.address} onChange={(e) => set('address', e.target.value)} placeholder="Shop 4, Market Road, Pune 411001" /></Field>
        </div>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Credit limit" hint="0 = no limit"><MoneyInput value={d.credit_limit_paise} onChange={(v) => set('credit_limit_paise', v)} /></Field>
          <Field label="Credit days"><Input type="number" min={0} inputMode="numeric" value={d.credit_days} onChange={(e) => set('credit_days', e.target.value)} /></Field>
        </div>
        {!party && (
          <fieldset className="rounded-xl border border-slate-200 p-4">
            <legend className="px-1 text-xs font-semibold uppercase tracking-wide text-slate-500">Opening balance</legend>
            <div className="grid gap-3 sm:grid-cols-3">
              <Field label="Amount"><MoneyInput value={d.opening_balance_paise} onChange={(v) => set('opening_balance_paise', v)} /></Field>
              <Field label="Side">
                <Select value={d.opening_type} onChange={(e) => set('opening_type', e.target.value as 'dr' | 'cr')}>
                  <option value="dr">Dr — they owe us</option><option value="cr">Cr — we owe them</option>
                </Select>
              </Field>
              <Field label="As of"><Input type="date" value={d.opening_as_of} max={todayIST()} onChange={(e) => set('opening_as_of', e.target.value)} /></Field>
            </div>
            <p className="mt-2 text-xs text-slate-500">Posted against Opening Balance Difference on the date given (defaults to 1 April of this FY).</p>
          </fieldset>
        )}
        {error && <p className="text-sm text-rose-600">{error}</p>}
      </div>
    </Modal>
  )
}
