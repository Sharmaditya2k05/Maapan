'use client'

import { useState } from 'react'
import { Pencil, Plus, Store as StoreIcon } from 'lucide-react'
import { Badge, Button, Card, Empty, ErrorBox, Field, IconButton, Input, Modal, QueryState, Table, Textarea, Toggle, useToast } from '@/components/ui'
import { useGet, usePerm, useSave } from '@/lib/hooks'
import { errorMessage } from '@/lib/api'
import { STATES, validateGSTIN } from '@/lib/gst'
import { PERMS } from '@/lib/rbac'
import { refreshSessionUser } from '@/lib/pehchaan'
import type { ListResponse, Store } from '@/lib/types'
import type { StoreInput } from '@/lib/types.stock'
import { StateSelect } from './StateSelect'

type Draft = Required<Omit<StoreInput, 'active'>> & { active: boolean }
const empty = (): Draft => ({ name: '', code: '', state_code: '', gstin: '', address: '', phone: '', active: true })
const fromStore = (s: Store): Draft => ({ name: s.name, code: s.code, state_code: s.state_code, gstin: s.gstin ?? '', address: s.address ?? '', phone: s.phone ?? '', active: s.active !== false })

function StoreModal({ store, onClose }: { store: Store | 'new' | null; onClose: () => void }) {
  const toast = useToast()
  const editing = store && store !== 'new' ? store : null
  const [d, setD] = useState<Draft>(() => (editing ? fromStore(editing) : empty()))
  const [err, setErr] = useState('')
  const save = useSave<Store, StoreInput>(editing ? `/pehchaan/v1/stores/${editing.id}` : '/pehchaan/v1/stores', { method: editing ? 'PUT' : 'POST', invalidate: ['/pehchaan/v1/stores'], silent: true })
  const gstinErr = d.gstin ? validateGSTIN(d.gstin) : null

  const setGstin = (raw: string) => {
    const g = raw.toUpperCase().replace(/[^0-9A-Z]/g, '').slice(0, 15)
    setD((x) => ({ ...x, gstin: g, state_code: g.length === 15 && !validateGSTIN(g) ? g.slice(0, 2) : x.state_code }))
  }
  const submit = async () => {
    setErr('')
    if (!d.name.trim() || !d.code.trim()) return setErr('Name and code are required.')
    if (d.code.trim().length > 8) return setErr('Code is at most 8 characters.')
    if (gstinErr) return setErr(gstinErr)
    if (!d.state_code) return setErr('Choose the state.')
    if (d.gstin && d.gstin.slice(0, 2) !== d.state_code) return setErr('State does not match the GSTIN state code.')
    const body: StoreInput = { name: d.name.trim(), code: d.code.trim().toUpperCase(), state_code: d.state_code, gstin: d.gstin, address: d.address.trim(), phone: d.phone.trim() }
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
        <Field label="GSTIN" error={gstinErr} hint="Each GSTIN gets its own invoice series"><Input value={d.gstin} onChange={(e) => setGstin(e.target.value)} className="font-mono uppercase" /></Field>
        <Field label="State"><StateSelect value={d.state_code} onChange={(v) => setD((p) => ({ ...p, state_code: v }))} disabled={!!d.gstin && !gstinErr} /></Field>
        <Field label="Phone"><Input value={d.phone} onChange={(e) => setD((p) => ({ ...p, phone: e.target.value }))} inputMode="tel" /></Field>
        {editing && <div className="flex items-end pb-2"><Toggle checked={d.active} onChange={(v) => setD((p) => ({ ...p, active: v }))} label="Active (shows in the store switcher)" /></div>}
        <Field label="Address" className="sm:col-span-2"><Textarea rows={2} value={d.address} onChange={(e) => setD((p) => ({ ...p, address: e.target.value }))} /></Field>
      </div>
      {err && <div className="mt-4"><ErrorBox message={err} /></div>}
    </Modal>
  )
}

export function StoresSection() {
  const canDo = usePerm()
  const stores = useGet<ListResponse<Store>>('/pehchaan/v1/stores')
  const [modal, setModal] = useState<Store | 'new' | null>(null)
  const canEdit = canDo(PERMS.settingsManage)
  return (
    <Card title="Stores" padded={false} actions={canEdit && <Button size="sm" icon={Plus} onClick={() => setModal('new')}>Add store</Button>}>
      <QueryState isLoading={stores.isLoading} error={stores.error} data={stores.data} retry={() => stores.refetch()}>
        {(d) => d.items.length === 0 ? <Empty icon={StoreIcon} title="No stores" /> : (
          <Table>
            <thead className="bg-slate-50/60"><tr><th className="th">Store</th><th className="th">Code</th><th className="th">State</th><th className="th">GSTIN</th><th className="th">Phone</th><th className="th">Status</th><th className="th w-12" /></tr></thead>
            <tbody className="divide-y divide-slate-100">
              {d.items.map((s) => (
                <tr key={s.id} className="hover:bg-slate-50/60">
                  <td className="td"><div className="font-semibold">{s.name}</div>{s.address && <div className="max-w-xs truncate text-xs text-slate-500">{s.address}</div>}</td>
                  <td className="td font-mono text-xs">{s.code}</td>
                  <td className="td text-xs text-slate-600">{s.state_code} {STATES[s.state_code] ?? ''}</td>
                  <td className="td font-mono text-xs">{s.gstin || <span className="text-slate-400">unregistered</span>}</td>
                  <td className="td text-xs text-slate-600">{s.phone || '—'}</td>
                  <td className="td"><Badge tone={s.active === false ? 'gray' : 'green'}>{s.active === false ? 'inactive' : 'active'}</Badge></td>
                  <td className="td">{canEdit && <IconButton icon={Pencil} label={`Edit ${s.name}`} onClick={() => setModal(s)} />}</td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
      </QueryState>
      {modal && <StoreModal key={modal === 'new' ? 'new' : modal.id} store={modal} onClose={() => setModal(null)} />}
    </Card>
  )
}
