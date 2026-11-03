'use client'

/**
 * GST registrations (pehchaan /v1/registrations): every GSTIN the business holds, or its
 * unregistered identity. Exactly one is primary; every store bills under one. Add / edit,
 * make primary, deactivate (refused with 409 while stores still bill under it).
 */
import { useState } from 'react'
import { BadgeCheck, Ban, FileBadge, Pencil, Plus, RotateCcw, Star } from 'lucide-react'
import { Badge, Button, Card, Confirm, Empty, ErrorBox, Field, IconButton, Input, Modal, QueryState, Table, Textarea, Toggle, useToast } from '@/components/ui'
import { api, errorMessage } from '@/lib/api'
import { usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import { STATES } from '@/lib/gst'
import { draftFromRegistration, emptyRegistrationDraft, normaliseGSTIN, registrationBody, stateForGSTIN, validateRegistration, type RegistrationDraft } from '@/lib/compliance'
import { refreshSessionUser } from '@/lib/pehchaan'
import type { Registration, RegistrationInput } from '@/lib/types.kar'
import { REGISTRATIONS_PATH, useRegistrations } from '@/components/compliance/registrations'
import { useAction } from '@/components/ledger/useAction'
import { StateSelect } from './StateSelect'

const INVALIDATE = [REGISTRATIONS_PATH, '/pehchaan/v1/stores', '/pehchaan/v1/tenant', '/kar/v1']

function RegistrationModal({ reg, onClose }: { reg: Registration | 'new'; onClose: () => void }) {
  const toast = useToast()
  const editing = reg !== 'new' ? reg : null
  const [d, setD] = useState<RegistrationDraft>(() => (editing ? draftFromRegistration(editing) : emptyRegistrationDraft()))
  const [primary, setPrimary] = useState(false)
  const [shown, setShown] = useState(false)
  const [err, setErr] = useState('')
  const errs = validateRegistration(d)
  const show = (k: keyof RegistrationDraft) => (shown ? errs[k] : undefined)
  const save = useAction<Registration, RegistrationInput>(
    (body) => api<Registration>(editing ? `${REGISTRATIONS_PATH}/${editing.id}` : REGISTRATIONS_PATH, { method: editing ? 'PUT' : 'POST', body }),
    { invalidate: INVALIDATE, silent: true },
  )
  const set = <K extends keyof RegistrationDraft>(k: K, v: RegistrationDraft[K]) => setD((p) => ({ ...p, [k]: v }))
  const gstinLocksState = !!d.gstin && stateForGSTIN(d.gstin, '') !== ''

  const submit = async () => {
    setShown(true)
    setErr('')
    if (Object.keys(errs).length) return
    const body = registrationBody(d)
    if (!editing && primary) body.is_primary = true
    try {
      await save.mutateAsync(body)
      toast(editing ? 'Registration updated' : 'Registration added')
      refreshSessionUser().catch(() => {})
      onClose()
    } catch (e) { setErr(errorMessage(e)) }
  }

  return (
    <Modal open onClose={onClose} title={editing ? `Edit ${editing.gstin || 'unregistered identity'}` : 'Add a GST registration'} size="lg"
      footer={<><Button variant="secondary" onClick={onClose}>Cancel</Button><Button onClick={submit} loading={save.isPending}>{editing ? 'Save' : 'Add registration'}</Button></>}>
      <form onSubmit={(e) => { e.preventDefault(); void submit() }} noValidate className="grid gap-4 sm:grid-cols-2">
        <Field label="GSTIN" error={show('gstin')} hint="Leave empty for the unregistered identity. The state is read from it.">
          <Input value={d.gstin} onChange={(e) => { const g = normaliseGSTIN(e.target.value); setD((p) => ({ ...p, gstin: g, state_code: stateForGSTIN(g, p.state_code) })) }} className="font-mono uppercase" placeholder="27AAPFU0939F1ZV" autoFocus disabled={!!editing?.gstin} />
        </Field>
        <Field label="State" error={show('state_code')}><StateSelect value={d.state_code} onChange={(v) => set('state_code', v)} disabled={gstinLocksState} /></Field>
        <Field label="Legal name" hint="As on the GST certificate; defaults to the business name"><Input value={d.legal_name} onChange={(e) => set('legal_name', e.target.value)} /></Field>
        <Field label="Trade name"><Input value={d.trade_name} onChange={(e) => set('trade_name', e.target.value)} /></Field>
        <Field label="Address" className="sm:col-span-2" hint="Principal place of business for this GSTIN (printed on invoices and the IRP payload)"><Textarea rows={2} value={d.address} onChange={(e) => set('address', e.target.value)} /></Field>
        <Field label="City / location" error={show('city')}><Input value={d.city} onChange={(e) => set('city', e.target.value)} placeholder="Pune" /></Field>
        <Field label="PIN code" error={show('pin')} hint="6 digits; the IRP rejects e-invoices without it"><Input value={d.pin} inputMode="numeric" onChange={(e) => set('pin', e.target.value.replace(/\D/g, '').slice(0, 6))} placeholder="411001" className="num" /></Field>
        <Field label="Phone"><Input value={d.phone} inputMode="tel" onChange={(e) => set('phone', e.target.value)} /></Field>
        <Field label="Email" error={show('email')}><Input type="email" value={d.email} onChange={(e) => set('email', e.target.value)} /></Field>
        <div className="space-y-3 sm:col-span-2">
          <div>
            <Toggle checked={d.composition} onChange={(v) => set('composition', v)} label={<span>Composition scheme <span className="block text-xs text-slate-500">Bills of supply, CMP-08 / GSTR-4 instead of GSTR-1 / 3B</span></span>} />
            {show('composition') && <p className="mt-1 text-xs text-rose-600">{show('composition')}</p>}
          </div>
          <div>
            <Toggle checked={d.einvoice_applicable} onChange={(v) => set('einvoice_applicable', v)} label={<span>e-Invoicing applies <span className="block text-xs text-slate-500">Aggregate turnover above the notified limit (₹5 crore). B2B invoices of this GSTIN get an IRN automatically.</span></span>} />
            {show('einvoice_applicable') && <p className="mt-1 text-xs text-rose-600">{show('einvoice_applicable')}</p>}
          </div>
          {!editing && <Toggle checked={primary} onChange={setPrimary} label={<span>Make this the primary registration <span className="block text-xs text-slate-500">The business profile follows the primary; the old primary stays active</span></span>} />}
        </div>
        <button type="submit" hidden />
      </form>
      {err && <div className="mt-4"><ErrorBox message={err} /></div>}
    </Modal>
  )
}

export function RegistrationsSection() {
  const canDo = usePerm()
  const toast = useToast()
  const regs = useRegistrations()
  const [modal, setModal] = useState<Registration | 'new' | null>(null)
  const [deactivate, setDeactivate] = useState<Registration | null>(null)
  const [deactErr, setDeactErr] = useState('')
  const canEdit = canDo(PERMS.settingsManage)

  const put = useAction<Registration, { reg: Registration; patch: RegistrationInput; done: string }>(
    ({ reg, patch }) => api<Registration>(`${REGISTRATIONS_PATH}/${reg.id}`, { method: 'PUT', body: { ...registrationBody(draftFromRegistration(reg)), ...patch } }),
    { invalidate: INVALIDATE, onSuccess: (_, i) => { toast(i.done); refreshSessionUser().catch(() => {}) } },
  )
  const del = useAction<void, Registration>((r) => api<void>(`${REGISTRATIONS_PATH}/${r.id}`, { method: 'DELETE' }), {
    invalidate: INVALIDATE, silent: true,
    onSuccess: (_, r) => { toast(`${r.gstin || 'Registration'} deactivated`); setDeactivate(null) },
  })

  return (
    <Card title="GST registrations" padded={false} actions={canEdit && <Button size="sm" icon={Plus} onClick={() => setModal('new')}>Add registration</Button>}>
      <p className="border-b border-slate-100 px-4 py-3 text-xs text-slate-500 sm:px-5">One per GSTIN (a branch in another state needs its own). Each registration has its own invoice series, returns and e-invoice settings; stores pick the registration they bill under in <span className="font-semibold">Stores</span>.</p>
      <QueryState isLoading={regs.isLoading} error={regs.error} data={regs.data} retry={() => regs.refetch()}>
        {(d) => d.items.length === 0 ? <Empty icon={FileBadge} title="No registrations" /> : (
          <Table>
            <thead className="bg-slate-50/60"><tr><th className="th">GSTIN</th><th className="th">State</th><th className="th">City / PIN</th><th className="th">Scheme</th><th className="th">e-Invoice</th><th className="th text-right">Stores</th><th className="th">Status</th><th className="th w-28" /></tr></thead>
            <tbody className="divide-y divide-slate-100">
              {d.items.map((r) => {
                const active = r.status === 'active'
                return (
                  <tr key={r.id} className={`hover:bg-slate-50/60 ${active ? '' : 'opacity-60'}`}>
                    <td className="td">
                      <div className="flex flex-wrap items-center gap-1.5"><span className="font-mono text-xs font-semibold">{r.gstin || <span className="font-sans text-slate-400">unregistered</span>}</span>{r.is_primary && <Badge tone="brand"><Star size={11} />primary</Badge>}</div>
                      {(r.trade_name || r.legal_name) && <div className="max-w-xs truncate text-xs text-slate-500">{r.trade_name || r.legal_name}</div>}
                    </td>
                    <td className="td text-xs text-slate-600">{r.state_code} {STATES[r.state_code] ?? ''}</td>
                    <td className="td text-xs">{r.city || <span className="text-slate-400">—</span>}{r.pin ? <span className="ml-1 font-mono text-slate-500">{r.pin}</span> : <Badge tone="amber" className="ml-1">no PIN</Badge>}</td>
                    <td className="td"><Badge tone={r.composition ? 'amber' : 'gray'}>{r.composition ? 'composition' : 'regular'}</Badge></td>
                    <td className="td">{r.einvoice_applicable ? <Badge tone="green"><BadgeCheck size={11} />applies</Badge> : <span className="text-xs text-slate-400">no</span>}</td>
                    <td className="td num text-right">{r.store_count}</td>
                    <td className="td"><Badge tone={active ? 'green' : 'gray'}>{r.status}</Badge></td>
                    <td className="td">
                      {canEdit && (
                        <div className="flex justify-end gap-0.5">
                          <IconButton icon={Pencil} label={`Edit ${r.gstin || 'registration'}`} onClick={() => setModal(r)} />
                          {active && !r.is_primary && r.gstin && <IconButton icon={Star} label="Make primary" disabled={put.isPending} onClick={() => put.mutate({ reg: r, patch: { is_primary: true }, done: `${r.gstin} is now the primary registration` })} />}
                          {active && !r.is_primary && <IconButton icon={Ban} label="Deactivate" className="hover:text-rose-600" onClick={() => { setDeactErr(''); setDeactivate(r) }} />}
                          {!active && <IconButton icon={RotateCcw} label="Reactivate" disabled={put.isPending} onClick={() => put.mutate({ reg: r, patch: { status: 'active' }, done: `${r.gstin || 'Registration'} reactivated` })} />}
                        </div>
                      )}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </Table>
        )}
      </QueryState>
      {modal && <RegistrationModal key={modal === 'new' ? 'new' : modal.id} reg={modal} onClose={() => setModal(null)} />}
      <Confirm open={!!deactivate} onClose={() => setDeactivate(null)} danger confirmLabel="Deactivate" loading={del.isPending}
        title={`Deactivate ${deactivate?.gstin || 'this registration'}?`}
        text="It stops appearing in pickers and returns. Invoices already issued keep pointing at it. A registration cannot be deactivated while stores still bill under it — move those stores first."
        onConfirm={() => { if (deactivate) del.mutate(deactivate, { onError: (e) => setDeactErr(errorMessage(e)) }) }}>
        {deactivate && deactivate.store_count > 0 && <p className="mt-3 rounded-xl bg-amber-50 px-3 py-2 text-xs text-amber-800">{deactivate.store_count} store{deactivate.store_count === 1 ? '' : 's'} bill under this GSTIN.</p>}
        {deactErr && <div className="mt-3"><ErrorBox message={deactErr} /></div>}
      </Confirm>
    </Card>
  )
}
