'use client'

/** POS terminals registered to stores (pehchaan GET/POST /v1/devices, DELETE /v1/devices/{id} = revoke). */
import { useEffect, useState } from 'react'
import { MonitorSmartphone, Plus } from 'lucide-react'
import { Badge, Button, Card, Confirm, Empty, ErrorBox, Field, Input, Modal, QueryState, Select, Table } from '@/components/ui'
import { del, errorMessage, post } from '@/lib/api'
import { fmtDate, useGet, useList } from '@/lib/hooks'
import { deviceId } from '@/lib/session'
import type { Store } from '@/lib/types'
import type { DeviceInput, DevicePlatform, PosDevice } from '@/lib/types.phase4'
import { useAction } from '@/components/ledger/useAction'

const PLATFORMS: DevicePlatform[] = ['android', 'ios', 'windows', 'linux', 'macos', 'web', 'other']

export function PosDevicesSection() {
  const stores = useList<Store>('/pehchaan/v1/stores')
  const storeList = stores.data?.items ?? []
  const [storeId, setStoreId] = useState('')
  const [status, setStatus] = useState('active')
  const q = useGet<{ items: PosDevice[] }>('/pehchaan/v1/devices', { store_id: storeId || undefined, status: status || undefined })
  const [add, setAdd] = useState(false)
  const [revoke, setRevoke] = useState<PosDevice | null>(null)
  const [here, setHere] = useState('')
  useEffect(() => { setHere(deviceId()) }, [])
  const revokeAct = useAction<unknown, string>((id) => del(`/pehchaan/v1/devices/${id}`), { invalidate: ['/pehchaan/v1/devices'], successMessage: 'Device revoked', onSuccess: () => setRevoke(null) })
  const storeName = (id: string) => storeList.find((s) => s.id === id)?.name ?? id.slice(0, 8)
  const items = q.data?.items ?? []

  return (
    <Card title="POS devices" padded={false} actions={<Button size="sm" icon={Plus} onClick={() => setAdd(true)}>Register</Button>}>
      <div className="flex flex-wrap gap-2 p-4">
        <Select value={storeId} onChange={(e) => setStoreId(e.target.value)} className="input-sm w-auto" aria-label="Store"><option value="">All stores</option>{storeList.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}</Select>
        <Select value={status} onChange={(e) => setStatus(e.target.value)} className="input-sm w-auto" aria-label="Status"><option value="active">Active</option><option value="revoked">Revoked</option><option value="">All</option></Select>
      </div>
      <QueryState isLoading={q.isLoading} error={q.error} data={q.data} retry={() => q.refetch()}>
        {() => items.length === 0 ? <Empty icon={MonitorSmartphone} title="No devices registered" text="Register each counter PC or tablet to its store; a revoked device can no longer bill." /> : (
          <Table>
            <thead className="bg-slate-50/60"><tr><th className="th">Device</th><th className="th">Store</th><th className="th">Platform</th><th className="th">Last seen</th><th className="th">Status</th><th className="th" /></tr></thead>
            <tbody className="divide-y divide-slate-100">
              {items.map((d) => (
                <tr key={d.id}>
                  <td className="td"><div className="font-semibold">{d.name || '—'}{d.device_id === here && <Badge tone="brand" className="ml-2">this device</Badge>}</div><div className="font-mono text-[11px] text-slate-400">{d.device_id}</div></td>
                  <td className="td">{storeName(d.store_id)}</td>
                  <td className="td text-slate-600">{d.platform ?? '—'}</td>
                  <td className="td text-xs text-slate-600">{d.last_seen_at ? fmtDate(d.last_seen_at, true) : 'never'}</td>
                  <td className="td"><Badge tone={d.status === 'active' ? 'green' : 'gray'}>{d.status}</Badge>{d.revoked_at && <div className="text-[11px] text-slate-400">{fmtDate(d.revoked_at, true)}</div>}</td>
                  <td className="td text-right">{d.status === 'active' && <Button size="sm" variant="ghost" onClick={() => setRevoke(d)}>Revoke</Button>}</td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
      </QueryState>
      {add && <RegisterModal stores={storeList} here={here} onClose={() => setAdd(false)} />}
      <Confirm open={!!revoke} onClose={() => setRevoke(null)} onConfirm={() => revoke && revokeAct.mutate(revoke.id)} loading={revokeAct.isPending} danger confirmLabel="Revoke"
        title={`Revoke ${revoke?.name || revoke?.device_id}?`} text="The device stops billing for this business. Its history stays; register it again to move it to another store." />
    </Card>
  )
}

function RegisterModal({ stores, here, onClose }: { stores: Store[]; here: string; onClose: () => void }) {
  const [d, setD] = useState<DeviceInput>({ device_id: here, store_id: stores[0]?.id ?? '', name: '', platform: 'web' })
  const [err, setErr] = useState<string | null>(null)
  const save = useAction<PosDevice, DeviceInput>((b) => post('/pehchaan/v1/devices', b), { invalidate: ['/pehchaan/v1/devices'], silent: true, successMessage: 'Device registered', onSuccess: onClose })
  const submit = async () => {
    if (!/^[A-Za-z0-9\-_.:]{1,128}$/.test(d.device_id)) return setErr('Device id: 1–128 letters, digits, - _ . or :')
    if (!d.store_id) return setErr('Pick the store.')
    setErr(null)
    try { await save.mutateAsync({ ...d, name: d.name?.trim() || undefined }) } catch (e) { setErr(errorMessage(e)) }
  }
  return (
    <Modal open onClose={onClose} title="Register a POS device" footer={<><Button variant="secondary" onClick={onClose}>Cancel</Button><Button loading={save.isPending} onClick={submit}>Register</Button></>}>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Device id" className="sm:col-span-2" hint="Pre-filled with this browser’s id (Settings → Device & printer shows it on each counter)"><Input value={d.device_id} onChange={(e) => setD({ ...d, device_id: e.target.value.trim() })} className="font-mono text-xs" /></Field>
        <Field label="Name"><Input value={d.name ?? ''} onChange={(e) => setD({ ...d, name: e.target.value })} placeholder="Counter 1" /></Field>
        <Field label="Platform"><Select value={d.platform} onChange={(e) => setD({ ...d, platform: e.target.value as DevicePlatform })}>{PLATFORMS.map((p) => <option key={p} value={p}>{p}</option>)}</Select></Field>
        <Field label="Store" className="sm:col-span-2"><Select value={d.store_id} onChange={(e) => setD({ ...d, store_id: e.target.value })}>{stores.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}</Select></Field>
      </div>
      {err && <div className="mt-4"><ErrorBox message={err} /></div>}
    </Modal>
  )
}
