'use client'

/**
 * Audit log: who changed users, roles, stores and devices (pehchaan GET /v1/audit, cursor `before`), and every edit
 * to the books with before / after images (bahi GET /v1/audit, offset paging).
 */
import { Fragment, useState } from 'react'
import { History } from 'lucide-react'
import { Badge, Button, Card, Empty, Input, Pagination, QueryState, Select, Table, Tabs } from '@/components/ui'
import { fmtDate, fyStartIST, todayIST, useGet, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import type { IdentityAuditPage, LedgerAuditEntry } from '@/lib/types.phase4'
import { JsonBlock, NoPermission } from '@/components/ledger/bits'

export function AuditSection() {
  const canDo = usePerm()
  const tabs = [
    ...(canDo(PERMS.userManage) ? [{ value: 'identity' as const, label: 'Users & devices' }] : []),
    ...(canDo(PERMS.ledgerRead) ? [{ value: 'books' as const, label: 'Books' }] : []),
  ]
  const [tab, setTab] = useState<'identity' | 'books'>(tabs[0]?.value ?? 'identity')
  if (tabs.length === 0) return <Card><NoPermission what="the audit log" /></Card>
  return (
    <div className="space-y-4">
      {tabs.length > 1 && <Tabs value={tab} onChange={setTab} options={tabs} />}
      {tab === 'identity' && canDo(PERMS.userManage) && <IdentityAudit />}
      {tab === 'books' && canDo(PERMS.ledgerRead) && <BooksAudit />}
    </div>
  )
}

const ACTIONS = [['', 'All actions'], ['user.', 'Users'], ['device.', 'Devices'], ['login', 'Logins'], ['store.', 'Stores'], ['registration.', 'Registrations'], ['tenant.', 'Business']] as const

function IdentityAudit() {
  const [action, setAction] = useState('')
  const [cursors, setCursors] = useState<number[]>([])
  const before = cursors[cursors.length - 1]
  const q = useGet<IdentityAuditPage>('/pehchaan/v1/audit', { action: action || undefined, before, limit: 50 })
  const [open, setOpen] = useState<number | null>(null)
  return (
    <Card title="Users, roles, stores and devices" padded={false}>
      <div className="flex flex-wrap gap-2 p-4">
        <Select value={action} onChange={(e) => { setAction(e.target.value); setCursors([]) }} className="input-sm w-auto" aria-label="Action">{ACTIONS.map(([v, l]) => <option key={v} value={v}>{l}</option>)}</Select>
      </div>
      <QueryState isLoading={q.isLoading} error={q.error} data={q.data} retry={() => q.refetch()}>
        {(d) => d.items.length === 0 ? <Empty icon={History} title="Nothing logged" /> : (
          <>
            <Table>
              <thead className="bg-slate-50/60"><tr><th className="th">When</th><th className="th">Who</th><th className="th">Action</th><th className="th">Target</th><th className="th">IP</th></tr></thead>
              <tbody className="divide-y divide-slate-100">
                {d.items.map((e) => (
                  <Fragment key={e.id}>
                    <tr className="cursor-pointer hover:bg-slate-50/60" onClick={() => setOpen(open === e.id ? null : e.id)}>
                      <td className="td whitespace-nowrap text-xs text-slate-600">{fmtDate(e.created_at, true)}</td>
                      <td className="td">{e.actor_name || <span className="font-mono text-xs">{e.actor_id.slice(0, 8)}</span>}</td>
                      <td className="td"><Badge tone={e.action.startsWith('device.') ? 'blue' : e.action.startsWith('user.') ? 'brand' : 'gray'}>{e.action}</Badge></td>
                      <td className="td font-mono text-xs text-slate-500">{e.target ?? '—'}</td>
                      <td className="td font-mono text-xs text-slate-400">{e.ip ?? ''}</td>
                    </tr>
                    {open === e.id && <tr><td colSpan={5} className="bg-slate-50/50 px-4 py-2"><JsonBlock value={e.details} maxHeight="14rem" /></td></tr>}
                  </Fragment>
                ))}
              </tbody>
            </Table>
            <div className="flex justify-between border-t border-slate-100 px-4 py-3">
              <Button size="sm" variant="secondary" disabled={cursors.length === 0} onClick={() => setCursors((c) => c.slice(0, -1))}>Newer</Button>
              <Button size="sm" variant="secondary" disabled={!d.next_before || d.items.length < 50} onClick={() => d.next_before && setCursors((c) => [...c, d.next_before as number])}>Older</Button>
            </div>
          </>
        )}
      </QueryState>
    </Card>
  )
}

const ENTITIES = ['', 'vouchers', 'voucher_entries', 'parties', 'accounts', 'cheques', 'bills', 'bill_allocations', 'bank_statements', 'bank_statement_lines', 'bank_transactions', 'tds_entries', 'tds_challans', 'tds_settings', 'period_locks', 'gst_setoffs']

function BooksAudit() {
  const [from, setFrom] = useState(fyStartIST())
  const [to, setTo] = useState(todayIST())
  const [entity, setEntity] = useState('')
  const [action, setAction] = useState('')
  const [offset, setOffset] = useState(0)
  const [open, setOpen] = useState<number | null>(null)
  const limit = 50
  const q = useGet<{ items: LedgerAuditEntry[]; limit: number; offset: number }>('/bahi/v1/audit', { from, to, entity: entity || undefined, action: action || undefined, limit, offset })
  const reset = () => setOffset(0)
  return (
    <Card title="Edits to the books" padded={false}>
      <div className="flex flex-wrap items-center gap-2 p-4">
        <Input type="date" className="input-sm w-40" value={from} onChange={(e) => { setFrom(e.target.value); reset() }} aria-label="From" />
        <Input type="date" className="input-sm w-40" value={to} max={todayIST()} onChange={(e) => { setTo(e.target.value); reset() }} aria-label="To" />
        <Select value={entity} onChange={(e) => { setEntity(e.target.value); reset() }} className="input-sm w-auto" aria-label="Table">{ENTITIES.map((x) => <option key={x} value={x}>{x || 'Everything'}</option>)}</Select>
        <Select value={action} onChange={(e) => { setAction(e.target.value); reset() }} className="input-sm w-auto" aria-label="Action"><option value="">insert / update / delete</option><option value="insert">insert</option><option value="update">update</option><option value="delete">delete</option></Select>
      </div>
      <QueryState isLoading={q.isLoading} error={q.error} data={q.data} retry={() => q.refetch()}>
        {(d) => d.items.length === 0 ? <Empty icon={History} title="No edits in this period" /> : (
          <>
            <Table>
              <thead className="bg-slate-50/60"><tr><th className="th">When</th><th className="th">Who</th><th className="th">Action</th><th className="th">Record</th></tr></thead>
              <tbody className="divide-y divide-slate-100">
                {d.items.map((e) => (
                  <Fragment key={e.id}>
                    <tr className="cursor-pointer hover:bg-slate-50/60" onClick={() => setOpen(open === e.id ? null : e.id)}>
                      <td className="td whitespace-nowrap text-xs text-slate-600">{fmtDate(e.at, true)}</td>
                      <td className="td font-mono text-xs">{e.actor ? e.actor.slice(0, 8) : 'system'}</td>
                      <td className="td"><Badge tone={e.action === 'delete' ? 'red' : e.action === 'update' ? 'amber' : 'green'}>{e.action}</Badge></td>
                      <td className="td text-xs"><span className="font-semibold">{e.entity}</span> <span className="font-mono text-slate-400">{e.entity_id}</span></td>
                    </tr>
                    {open === e.id && (
                      <tr><td colSpan={4} className="bg-slate-50/50 px-4 py-2">
                        <div className="grid gap-2 md:grid-cols-2">
                          <div><div className="mb-1 text-[11px] font-semibold text-slate-500">Before</div>{e.before ? <JsonBlock value={e.before} maxHeight="14rem" /> : <span className="text-xs text-slate-400">—</span>}</div>
                          <div><div className="mb-1 text-[11px] font-semibold text-slate-500">After</div>{e.after ? <JsonBlock value={e.after} maxHeight="14rem" /> : <span className="text-xs text-slate-400">—</span>}</div>
                        </div>
                      </td></tr>
                    )}
                  </Fragment>
                ))}
              </tbody>
            </Table>
            <Pagination total={d.items.length < limit ? offset + d.items.length : offset + limit + 1} limit={limit} offset={offset} onChange={setOffset} />
          </>
        )}
      </QueryState>
    </Card>
  )
}
