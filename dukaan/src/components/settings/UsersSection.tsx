'use client'

import { useState } from 'react'
import { Pencil, Plus, UserMinus, Users } from 'lucide-react'
import { Avatar, Badge, Button, Card, Confirm, Empty, ErrorBox, Field, IconButton, Input, Modal, QueryState, Select, Table, Toggle, useToast } from '@/components/ui'
import { fmtDate, useGet, usePerm, useSave, useSessionUser } from '@/lib/hooks'
import { del, errorMessage } from '@/lib/api'
import { PERMS, ROLE_LABEL, type Role } from '@/lib/rbac'
import { isStoreScoped } from '@/lib/stock/scope'
import type { ListResponse, Store } from '@/lib/types'
import type { InviteInput, Member, MemberUpdate } from '@/lib/types.stock'
import { useAction } from '@/components/stock/hooks'

const ROLES = Object.keys(ROLE_LABEL) as Role[]
const ROLE_HINT: Record<Role, string> = {
  super_admin: 'Everything, every store', store_manager: 'Billing, purchases, items and stock in their stores',
  cashier: 'Counter billing and payments in their stores', accountant: 'Ledgers, reports, GST filing — no counter',
}

function StoreChecks({ value, onChange, stores }: { value: string[]; onChange: (ids: string[]) => void; stores: Store[] }) {
  return (
    <div className="flex flex-wrap gap-2">
      {stores.map((s) => {
        const on = value.includes(s.id)
        return (
          <label key={s.id} className={`inline-flex cursor-pointer items-center gap-2 rounded-xl border px-3 py-1.5 text-sm ${on ? 'border-brand-500 bg-brand-50 text-brand-700' : 'border-slate-200'}`}>
            <input type="checkbox" className="accent-brand-600" checked={on} onChange={() => onChange(on ? value.filter((x) => x !== s.id) : [...value, s.id])} />{s.name}
          </label>
        )
      })}
    </div>
  )
}

function InviteModal({ open, onClose, stores }: { open: boolean; onClose: () => void; stores: Store[] }) {
  const toast = useToast()
  const [d, setD] = useState<InviteInput>({ phone: '', name: '', role: 'cashier', store_ids: [] })
  const [err, setErr] = useState('')
  const save = useSave<Member, InviteInput>('/pehchaan/v1/users', { invalidate: ['/pehchaan/v1/users'], silent: true })
  const submit = async () => {
    setErr('')
    const phone = d.phone.replace(/\D/g, '')
    if (phone.length < 10) return setErr('Enter the 10-digit mobile number they log in with.')
    if (!d.name.trim()) return setErr('Name is required.')
    if (isStoreScoped(d.role) && d.store_ids.length === 0) return setErr(`${ROLE_LABEL[d.role]} needs at least one store.`)
    try {
      await save.mutateAsync({ ...d, phone, name: d.name.trim(), store_ids: isStoreScoped(d.role) ? d.store_ids : [] })
      toast('Invited — they log in with an OTP on that number')
      setD({ phone: '', name: '', role: 'cashier', store_ids: [] })
      onClose()
    } catch (e) { setErr(errorMessage(e)) }
  }
  return (
    <Modal open={open} onClose={onClose} title="Invite a user" footer={<><Button variant="secondary" onClick={onClose}>Cancel</Button><Button onClick={submit} loading={save.isPending}>Invite</Button></>}>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Mobile number" hint="No password: they sign in with an OTP"><Input value={d.phone} onChange={(e) => setD({ ...d, phone: e.target.value })} inputMode="tel" placeholder="98765 43210" autoFocus /></Field>
        <Field label="Name"><Input value={d.name} onChange={(e) => setD({ ...d, name: e.target.value })} /></Field>
        <Field label="Role" className="sm:col-span-2" hint={ROLE_HINT[d.role]}>
          <Select value={d.role} onChange={(e) => setD({ ...d, role: e.target.value as Role })}>{ROLES.map((r) => <option key={r} value={r}>{ROLE_LABEL[r]}</option>)}</Select>
        </Field>
        {isStoreScoped(d.role) && <Field label="Stores" className="sm:col-span-2"><StoreChecks value={d.store_ids} onChange={(ids) => setD({ ...d, store_ids: ids })} stores={stores} /></Field>}
      </div>
      {err && <div className="mt-4"><ErrorBox message={err} /></div>}
    </Modal>
  )
}

function EditModal({ member, onClose, stores }: { member: Member; onClose: () => void; stores: Store[] }) {
  const toast = useToast()
  const [name, setName] = useState(member.name ?? '')
  const [role, setRole] = useState<Role>(member.role)
  const [storeIds, setStoreIds] = useState<string[]>(member.store_ids ?? [])
  const [active, setActive] = useState(member.active)
  const [err, setErr] = useState('')
  const save = useSave<Member, MemberUpdate>(`/pehchaan/v1/users/${member.id}`, { method: 'PATCH', invalidate: ['/pehchaan/v1/users'], silent: true })
  const submit = async () => {
    setErr('')
    if (isStoreScoped(role) && storeIds.length === 0) return setErr(`${ROLE_LABEL[role]} needs at least one store.`)
    const body: MemberUpdate = {}
    if (name.trim() && name.trim() !== (member.name ?? '')) body.name = name.trim()
    if (role !== member.role) body.role = role
    if (isStoreScoped(role) && JSON.stringify([...storeIds].sort()) !== JSON.stringify([...(member.store_ids ?? [])].sort())) body.store_ids = storeIds
    if (active !== member.active) body.active = active
    if (Object.keys(body).length === 0) return onClose()
    try { await save.mutateAsync(body); toast('User updated'); onClose() } catch (e) { setErr(errorMessage(e)) }
  }
  return (
    <Modal open onClose={onClose} title={`Edit ${member.name || member.phone}`} footer={<><Button variant="secondary" onClick={onClose}>Cancel</Button><Button onClick={submit} loading={save.isPending}>Save</Button></>}>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Name"><Input value={name} onChange={(e) => setName(e.target.value)} /></Field>
        <Field label="Mobile"><Input value={member.phone} readOnly disabled /></Field>
        <Field label="Role" className="sm:col-span-2" hint={ROLE_HINT[role]}><Select value={role} onChange={(e) => setRole(e.target.value as Role)}>{ROLES.map((r) => <option key={r} value={r}>{ROLE_LABEL[r]}</option>)}</Select></Field>
        {isStoreScoped(role) && <Field label="Stores" className="sm:col-span-2"><StoreChecks value={storeIds} onChange={setStoreIds} stores={stores} /></Field>}
        <div className="sm:col-span-2"><Toggle checked={active} onChange={setActive} label="Active (can sign in to this business)" /></div>
      </div>
      <p className="mt-3 text-xs text-slate-500">Changes apply at their next token refresh (within 15 minutes). You cannot demote the last owner or yourself.</p>
      {err && <div className="mt-4"><ErrorBox message={err} /></div>}
    </Modal>
  )
}

export function UsersSection() {
  const canDo = usePerm()
  const me = useSessionUser()
  const toast = useToast()
  const users = useGet<ListResponse<Member>>(canDo(PERMS.userManage) ? '/pehchaan/v1/users' : null)
  const stores = useGet<ListResponse<Store>>('/pehchaan/v1/stores')
  const storeList = stores.data?.items ?? []
  const [invite, setInvite] = useState(false)
  const [edit, setEdit] = useState<Member | null>(null)
  const [remove, setRemove] = useState<Member | null>(null)
  const removeAction = useAction<void, string>((id) => del(`/pehchaan/v1/users/${id}`), { invalidate: ['/pehchaan/v1/users'], onSuccess: () => { setRemove(null); toast('Access removed') } })
  const storeNames = (ids: string[]) => ids.map((id) => storeList.find((s) => s.id === id)?.name ?? id.slice(0, 6)).join(', ')

  return (
    <Card title="Users" padded={false} actions={<Button size="sm" icon={Plus} onClick={() => setInvite(true)}>Invite</Button>}>
      <QueryState isLoading={users.isLoading} error={users.error} data={users.data} retry={() => users.refetch()}>
        {(d) => d.items.length === 0 ? <Empty icon={Users} title="Just you" text="Invite cashiers, managers and your accountant." /> : (
          <Table>
            <thead className="bg-slate-50/60"><tr><th className="th">User</th><th className="th">Role</th><th className="th">Stores</th><th className="th">Last login</th><th className="th">Status</th><th className="th w-24" /></tr></thead>
            <tbody className="divide-y divide-slate-100">
              {d.items.map((m) => (
                <tr key={m.id} className="hover:bg-slate-50/60">
                  <td className="td"><div className="flex items-center gap-3"><Avatar name={m.name || m.phone} className="h-8 w-8 text-xs" /><div><div className="font-semibold">{m.name || '—'}{m.id === me?.id && <Badge tone="brand" className="ml-2">you</Badge>}</div><div className="text-xs text-slate-500">{m.phone}{m.email ? ` · ${m.email}` : ''}</div></div></div></td>
                  <td className="td"><Badge tone={m.role === 'super_admin' ? 'brand' : 'gray'}>{ROLE_LABEL[m.role] ?? m.role}</Badge></td>
                  <td className="td text-xs text-slate-600">{isStoreScoped(m.role) ? storeNames(m.store_ids ?? []) || <span className="text-rose-600">no store</span> : 'All stores'}</td>
                  <td className="td text-xs text-slate-600">{m.last_login_at ? fmtDate(m.last_login_at, true) : 'never'}</td>
                  <td className="td"><Badge tone={m.active ? 'green' : 'red'}>{m.active ? 'active' : 'inactive'}</Badge></td>
                  <td className="td"><div className="flex justify-end gap-1"><IconButton icon={Pencil} label={`Edit ${m.name || m.phone}`} onClick={() => setEdit(m)} />{m.active && m.id !== me?.id && <IconButton icon={UserMinus} label={`Remove ${m.name || m.phone}`} onClick={() => setRemove(m)} className="hover:text-rose-600" />}</div></td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
      </QueryState>
      <InviteModal open={invite} onClose={() => setInvite(false)} stores={storeList} />
      {edit && <EditModal key={edit.id} member={edit} onClose={() => setEdit(null)} stores={storeList} />}
      <Confirm open={!!remove} onClose={() => setRemove(null)} onConfirm={() => remove && removeAction.mutate(remove.id)} loading={removeAction.isPending} danger confirmLabel="Remove access"
        title={`Remove ${remove?.name || remove?.phone}?`} text="Their membership is deactivated: they can no longer sign in to this business. Bills they made keep their name. You can re-activate them later from Edit." />
    </Card>
  )
}
