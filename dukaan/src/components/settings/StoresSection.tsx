'use client'

import { useState } from 'react'
import { Pencil, Plus, Store as StoreIcon } from 'lucide-react'
import { Badge, Button, Card, Empty, ErrorBox, Field, IconButton, Input, Modal, QueryState, Select, Table, Textarea, Toggle, useToast } from '@/components/ui'
import { useGet, usePerm, useSave } from '@/lib/hooks'
import { errorMessage } from '@/lib/api'
import { STATES } from '@/lib/gst'
import { registrationLabel } from '@/lib/compliance'
import { PERMS } from '@/lib/rbac'
import { refreshSessionUser } from '@/lib/pehchaan'
import type { ListResponse, Store } from '@/lib/types'
import type { StoreInput } from '@/lib/types.stock'
import type { Registration } from '@/lib/types.kar'
import { REGISTRATIONS_PATH, useRegistrations } from '@/components/compliance/registrations'
import { StateSelect } from './StateSelect'

type Draft = Required<Omit<StoreInput, 'active' | 'gstin'>> & { active: boolean }
const empty = (registration_id = ''): Draft => ({ name: '', code: '', state_code: '', address: '', phone: '', active: true, registration_id })
const fromStore = (s: Store): Draft => ({ name: s.name, code: s.code, state_code: s.state_code, address: s.address ?? '', phone: s.phone ?? '', active: s.active !== false, registration_id: s.registration_id ?? '' })

function StoreModal({ store, onClose, registrations, primaryId }: { store: Store | 'new' | null; onClose: () => void; registrations: Registration[]; primaryId?: string }) {
  const toast = useToast()
  const editing = store && store !== 'new' ? store : null
  const [d, setD] = useState<Draft>(() => {
    const base = editing ? fromStore(editing) : empty(primaryId)
    const reg = registrations.find((r) => r.id === base.registration_id)
    return !editing && reg ? { ...base, state_code: reg.state_code } : base
  })
  const [err, setErr] = useState('')
  const save = useSave<Store, StoreInput>(editing ? `/pehchaan/v1/stores/${editing.id}` : '/pehchaan/v1/stores', { method: editing ? 'PUT' : 'POST', invalidate: ['/pehchaan/v1/stores', REGISTRATIONS_PATH], silent: true })
  const reg = registrations.find((r) => r.id === d.registration_id)
  // Inactive registrations are not offered, except the one the store already bills under.
  const options = registrations.filter((r) => r.status === 'active' || r.id === editing?.registration_id)

  const pickRegistration = (id: string) => {
    const r = registrations.find((x) => x.id === id)
    setD((x) => ({ ...x, registration_id: id, state_code: r?.state_code || x.state_code }))
  }
  const submit = async () => {
    setErr('')
    if (!d.name.trim() || !d.code.trim()) return setErr('Name and code are required.')
    if (d.code.trim().length > 8) return setErr('Code is at most 8 characters.')
    if (!d.registration_id && registrations.length > 0) return setErr('Choose the GST registration this store bills under.')
    if (!d.state_code) return setErr('Choose the state.')
    if (reg?.gstin && reg.state_code !== d.state_code) return setErr(`A store billing under ${reg.gstin} must be in ${STATES[reg.state_code] ?? reg.state_code}.`)
    const body: StoreInput = { name: d.name.trim(), code: d.code.trim().toUpperCase(), state_code: d.state_code, gstin: reg?.gstin ?? '', address: d.address.trim(), phone: d.phone.trim() }
    if (d.registration_id) body.registration_id = d.registration_id
    if (editing) body.active = d.active
    try {
      await save.mutateAsync(body)
      toast(editing ? 'Store updated' : 'Store added')
      refreshSessionUser().catch(() => {})
      onClose()
    } catch (e) { setErr(errorMessage(e)) }
  }

  return (
    <Modal open={!!store} onClose={onClose} title={editing ? `Edit ${editing.name}` : 'New store'}
      footer={<><Button variant="secondary" onClick={onClose}>Cancel</Button><Button onClick={submit} loading={save.isPending}>{editing ? 'Save' : 'Add store'}</Button></>}>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Store name"><Input value={d.name} onChange={(e) => setD((p) => ({ ...p, name: e.target.value }))} autoFocus placeholder="Andheri West" /></Field>
        <Field label="Code" hint="Up to 8 characters; appears in reports"><Input value={d.code} onChange={(e) => setD((p) => ({ ...p, code: e.target.value.toUpperCase().slice(0, 8) }))} className="font-mono uppercase" placeholder="ANDW" /></Field>
        <Field label="GST registration" className="sm:col-span-2" hint={reg ? `${reg.gstin ? 'Invoices carry this GSTIN and its number series' : 'Bills of supply without GST'}${reg.is_primary ? ' · primary' : ''}. Add GSTINs under Settings → GST registrations.` : 'The GSTIN this store invoices under'}>
          <Select value={d.registration_id} onChange={(e) => pickRegistration(e.target.value)}>
            {!d.registration_id && <option value="">Choose a registration…</option>}
            {options.map((r) => <option key={r.id} value={r.id}>{registrationLabel(r)}{r.is_primary ? ' (primary)' : ''}{r.status !== 'active' ? ' — inactive' : ''}</option>)}
          </Select>
        </Field>
        <Field label="State" hint={reg?.gstin ? 'Follows the registration' : undefined}><StateSelect value={d.state_code} onChange={(v) => setD((p) => ({ ...p, state_code: v }))} disabled={!!reg?.gstin} /></Field>
        <Field label="Phone"><Input value={d.phone} onChange={(e) => setD((p) => ({ ...p, phone: e.target.value }))} inputMode="tel" /></Field>
        {editing && <div className="flex items-end pb-2 sm:col-span-2"><Toggle checked={d.active} onChange={(v) => setD((p) => ({ ...p, active: v }))} label="Active (shows in the store switcher)" /></div>}
        <Field label="Address" className="sm:col-span-2"><Textarea rows={2} value={d.address} onChange={(e) => setD((p) => ({ ...p, address: e.target.value }))} /></Field>
      </div>
      {err && <div className="mt-4"><ErrorBox message={err} /></div>}
    </Modal>
  )
}

export function StoresSection() {
  const canDo = usePerm()
  const stores = useGet<ListResponse<Store>>('/pehchaan/v1/stores')
  const regs = useRegistrations()
  const regById = new Map(regs.all.map((r) => [r.id, r]))
  const [modal, setModal] = useState<Store | 'new' | null>(null)
  const canEdit = canDo(PERMS.settingsManage)
  return (
    <Card title="Stores" padded={false} actions={canEdit && <Button size="sm" icon={Plus} onClick={() => setModal('new')}>Add store</Button>}>
      <QueryState isLoading={stores.isLoading} error={stores.error} data={stores.data} retry={() => stores.refetch()}>
        {(d) => d.items.length === 0 ? <Empty icon={StoreIcon} title="No stores" /> : (
          <Table>
            <thead className="bg-slate-50/60"><tr><th className="th">Store</th><th className="th">Code</th><th className="th">State</th><th className="th">Registration</th><th className="th">Phone</th><th className="th">Status</th><th className="th w-12" /></tr></thead>
            <tbody className="divide-y divide-slate-100">
              {d.items.map((s) => (
                <tr key={s.id} className="hover:bg-slate-50/60">
                  <td className="td"><div className="font-semibold">{s.name}</div>{s.address && <div className="max-w-xs truncate text-xs text-slate-500">{s.address}</div>}</td>
                  <td className="td font-mono text-xs">{s.code}</td>
                  <td className="td text-xs text-slate-600">{s.state_code} {STATES[s.state_code] ?? ''}</td>
                  <td className="td"><span className="font-mono text-xs">{s.gstin || <span className="font-sans text-slate-400">unregistered</span>}</span>{s.registration_id && regById.get(s.registration_id)?.is_primary && <Badge tone="brand" className="ml-1.5">primary</Badge>}{s.registration_id && regById.get(s.registration_id)?.status === 'inactive' && <Badge tone="amber" className="ml-1.5">inactive registration</Badge>}</td>
                  <td className="td text-xs text-slate-600">{s.phone || '—'}</td>
                  <td className="td"><Badge tone={s.active === false ? 'gray' : 'green'}>{s.active === false ? 'inactive' : 'active'}</Badge></td>
                  <td className="td">{canEdit && <IconButton icon={Pencil} label={`Edit ${s.name}`} onClick={() => setModal(s)} />}</td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
      </QueryState>
      {modal && <StoreModal key={modal === 'new' ? 'new' : modal.id} store={modal} onClose={() => setModal(null)} registrations={regs.all} primaryId={regs.primary?.id} />}
    </Card>
  )
}
