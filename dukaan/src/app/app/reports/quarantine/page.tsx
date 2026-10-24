'use client'

import { useState } from 'react'
import { useRouter } from 'next/navigation'
import { AlertTriangle, RefreshCw, ShieldCheck } from 'lucide-react'
import { Badge, Button, Card, Empty, PageHeader, Pagination, QueryState } from '@/components/ui'
import { post } from '@/lib/api'
import { fmtDate, useList, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import type { QuarantinedEvent } from '@/lib/types.ledger'
import { JsonBlock, NoPermission } from '@/components/ledger/bits'
import { useAction } from '@/components/ledger/useAction'

export default function QuarantinePage() {
  const canDo = usePerm()
  const router = useRouter()
  const [offset, setOffset] = useState(0)
  const [openId, setOpenId] = useState<string | null>(null)
  const list = useList<QuarantinedEvent>(canDo(PERMS.ledgerRead) ? '/bahi/v1/quarantine' : null, { limit: 25, offset })
  const retry = useAction<QuarantinedEvent, string>((id) => post<QuarantinedEvent>(`/bahi/v1/quarantine/${id}/retry`), {
    invalidate: ['/bahi/v1/quarantine', '/bahi/v1/vouchers', '/bahi/v1/parties', '/bahi/v1/reports', '/bahi/v1/accounts'],
    successMessage: 'Event posted and resolved',
  })
  if (!canDo(PERMS.ledgerRead)) return <><PageHeader title="Quarantined events" /><Card><NoPermission what="the ledger" /></Card></>

  return (
    <>
      <PageHeader title="Quarantined events" subtitle="Documents bahi could not post as delivered. Fix the cause (missing party, locked period, unbalanced totals), then retry." back={() => router.push('/app/reports')}
        actions={<Button variant="secondary" size="sm" icon={RefreshCw} onClick={() => list.refetch()}>Refresh</Button>} />
      <Card padded={false}>
        <QueryState isLoading={list.isLoading} error={list.error} data={list.data} retry={() => list.refetch()}>
          {(d) => d.items.length === 0 ? <Empty icon={ShieldCheck} title="Nothing in quarantine" text="Every event has been posted. Retries that failed show up here with their new reason." /> : (
            <>
              <ul className="divide-y divide-slate-100">
                {d.items.map((e) => {
                  const open = openId === e.event_id
                  const busy = retry.isPending && retry.variables === e.event_id
                  return (
                    <li key={e.event_id} className="px-4 py-3">
                      <div className="flex flex-wrap items-start gap-3">
                        <AlertTriangle size={18} className="mt-0.5 shrink-0 text-amber-500" />
                        <div className="min-w-0 flex-1">
                          <div className="flex flex-wrap items-center gap-2"><Badge tone="amber">{e.event_type}</Badge><span className="text-xs text-slate-500">{fmtDate(e.created_at, true)}</span><span className="font-mono text-[11px] text-slate-400">{e.event_id}</span></div>
                          <p className="mt-1 text-sm text-slate-700">{e.reason || 'No reason recorded'}</p>
                          <button type="button" onClick={() => setOpenId(open ? null : e.event_id)} className="mt-1 text-xs font-semibold text-brand-700 hover:underline cursor-pointer">{open ? 'Hide payload' : 'Show payload'}</button>
                        </div>
                        {canDo(PERMS.ledgerWrite) && <Button size="sm" variant="secondary" icon={RefreshCw} loading={busy} disabled={retry.isPending} onClick={() => retry.mutate(e.event_id)}>Retry</Button>}
                      </div>
                      {open && <div className="mt-3"><JsonBlock value={e.payload} /></div>}
                    </li>
                  )
                })}
              </ul>
              <Pagination total={d.total} limit={d.limit} offset={d.offset} onChange={setOffset} />
            </>
          )}
        </QueryState>
      </Card>
      {!canDo(PERMS.ledgerWrite) && <p className="mt-3 text-xs text-slate-500">Retrying needs ledger write access (accountant or owner).</p>}
    </>
  )
}
