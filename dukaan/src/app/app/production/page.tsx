'use client'

import { Suspense, useState } from 'react'
import Link from 'next/link'
import { useRouter, useSearchParams } from 'next/navigation'
import { ChevronLeft, ChevronRight, Factory, Layers, Plus } from 'lucide-react'
import { Badge, Button, Card, Empty, IconButton, Money, PageHeader, QueryState, SearchInput, Select, Table, Tabs } from '@/components/ui'
import { fmtDate, fmtQty, useDebounced, useGet, usePerm, useStore } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import type { BomStatus, BomSummary, NirmaanList, ProductionRun, RunStatus } from '@/lib/types.more'
import { bomTone } from '@/components/production/BomViews'
import { PlanRunModal, RUN_LABEL, RunBadge } from '@/components/production/runs'

type Tab = 'boms' | 'runs'
const PAGE = 50

export default function ProductionPage() {
  return <Suspense><ProductionInner /></Suspense>
}

function ProductionInner() {
  const canDo = usePerm()
  const router = useRouter()
  const params = useSearchParams()
  const tab: Tab = params.get('tab') === 'runs' ? 'runs' : 'boms'
  const setTab = (t: Tab) => router.replace(`/app/production${t === 'runs' ? '?tab=runs' : ''}`)
  const [plan, setPlan] = useState(false)
  const canWrite = canDo(PERMS.bomManage)

  if (!canWrite && !canDo(PERMS.reportRead)) {
    return <Empty icon={Factory} title="Production" text="Store managers and owners manage bills of materials and production runs." />
  }

  return (
    <>
      <PageHeader title="Production" subtitle="Bills of materials and production runs"
        actions={canWrite && (
          <>
            <Button variant="secondary" icon={Factory} onClick={() => setPlan(true)}>Plan run</Button>
            <Link href="/app/production/boms/new"><Button icon={Plus}>New BOM</Button></Link>
          </>
        )} />
      <div className="mb-4"><Tabs value={tab} onChange={setTab} options={[{ value: 'boms', label: 'BOMs' }, { value: 'runs', label: 'Runs' }]} /></div>
      {tab === 'boms' ? <BomList /> : <RunList onPlan={canWrite ? () => setPlan(true) : undefined} />}
      <PlanRunModal open={plan} onClose={() => setPlan(false)} />
    </>
  )
}

/** nirmaan lists have no total: page forward while a full page comes back. */
function PagerNoTotal({ offset, count, onChange }: { offset: number; count: number; onChange: (o: number) => void }) {
  if (offset === 0 && count < PAGE) return null
  return (
    <div className="flex items-center justify-between border-t border-slate-100 px-4 py-3 text-sm text-slate-500">
      <span>{count === 0 ? 'No more results' : `${offset + 1}–${offset + count}`}</span>
      <div className="flex items-center gap-1">
        <IconButton icon={ChevronLeft} label="Previous page" disabled={offset === 0} onClick={() => onChange(Math.max(0, offset - PAGE))} />
        <IconButton icon={ChevronRight} label="Next page" disabled={count < PAGE} onClick={() => onChange(offset + PAGE)} />
      </div>
    </div>
  )
}

function BomList() {
  const [q, setQ] = useState('')
  const dq = useDebounced(q)
  const [status, setStatus] = useState<'' | BomStatus>('')
  const [offset, setOffset] = useState(0)
  const list = useGet<NirmaanList<BomSummary>>('/nirmaan/v1/boms', { q: dq, status, limit: PAGE, offset })
  return (
    <Card padded={false}>
      <div className="flex flex-wrap gap-2 p-4">
        <SearchInput value={q} onChange={(v) => { setQ(v); setOffset(0) }} placeholder="Finished item name" />
        <Select className="w-auto" value={status} onChange={(e) => { setStatus(e.target.value as '' | BomStatus); setOffset(0) }} aria-label="Status filter">
          <option value="">All statuses</option><option value="active">Active</option><option value="draft">Draft</option><option value="archived">Archived</option>
        </Select>
      </div>
      <QueryState isLoading={list.isLoading} error={list.error} data={list.data} retry={() => list.refetch()}>
        {(d) => d.items.length === 0 && offset === 0 ? (
          <Empty icon={Layers} title={dq || status ? 'No BOMs match' : 'No bills of materials yet'} text={dq || status ? 'Try another name or status.' : 'A BOM is the recipe for a finished item: components per batch, scrap and labour cost.'}
            action={!dq && !status ? <Link href="/app/production/boms/new"><Button icon={Plus}>Create the first BOM</Button></Link> : undefined} />
        ) : (
          <>
            <div className="md:hidden">
              <ul className="divide-y divide-slate-100">
                {d.items.map((b) => (
                  <li key={b.id}>
                    <Link href={`/app/production/boms/${b.id}`} className="flex items-center gap-3 px-4 py-3 hover:bg-slate-50">
                      <div className="min-w-0 flex-1">
                        <div className="truncate text-sm font-semibold">{b.item_name}</div>
                        <div className="text-xs text-slate-500">v{b.version} · {fmtQty(b.output_qty)} {b.unit}/batch · {b.components} components</div>
                      </div>
                      <Badge tone={bomTone(b.status)}>{b.status}</Badge>
                    </Link>
                  </li>
                ))}
              </ul>
            </div>
            <div className="hidden md:block">
              <Table>
                <thead><tr><th className="th">Finished item</th><th className="th">Version</th><th className="th">Status</th><th className="th text-right">Batch output</th><th className="th text-right">Components</th><th className="th">Updated</th></tr></thead>
                <tbody>
                  {d.items.map((b) => (
                    <tr key={b.id} className="cursor-pointer border-t border-slate-100 hover:bg-slate-50">
                      <td className="td"><Link href={`/app/production/boms/${b.id}`} className="font-semibold text-ink hover:underline">{b.item_name}</Link></td>
                      <td className="td">v{b.version}</td>
                      <td className="td"><Badge tone={bomTone(b.status)}>{b.status}</Badge></td>
                      <td className="td num text-right">{fmtQty(b.output_qty)} {b.unit}</td>
                      <td className="td num text-right">{b.components}</td>
                      <td className="td text-slate-500">{fmtDate(b.updated_at)}</td>
                    </tr>
                  ))}
                </tbody>
              </Table>
            </div>
            <PagerNoTotal offset={offset} count={d.items.length} onChange={setOffset} />
          </>
        )}
      </QueryState>
    </Card>
  )
}

function RunList({ onPlan }: { onPlan?: () => void }) {
  const { storeId, stores } = useStore()
  const [status, setStatus] = useState<'' | RunStatus>('')
  const [store, setStore] = useState<string>(storeId ?? '')
  const [offset, setOffset] = useState(0)
  const list = useGet<NirmaanList<ProductionRun>>('/nirmaan/v1/production-runs', { status, store_id: store, limit: PAGE, offset })
  const storeName = (id: string) => stores.find((s) => s.id === id)?.name ?? id.slice(0, 8)
  return (
    <Card padded={false}>
      <div className="flex flex-wrap gap-2 p-4">
        <Select className="w-auto" value={status} onChange={(e) => { setStatus(e.target.value as '' | RunStatus); setOffset(0) }} aria-label="Run status filter">
          <option value="">All statuses</option>
          {(Object.keys(RUN_LABEL) as RunStatus[]).map((s) => <option key={s} value={s}>{RUN_LABEL[s]}</option>)}
        </Select>
        {stores.length > 1 && (
          <Select className="w-auto" value={store} onChange={(e) => { setStore(e.target.value); setOffset(0) }} aria-label="Store filter">
            <option value="">All stores</option>
            {stores.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
          </Select>
        )}
      </div>
      <QueryState isLoading={list.isLoading} error={list.error} data={list.data} retry={() => list.refetch()}>
        {(d) => d.items.length === 0 && offset === 0 ? (
          <Empty icon={Factory} title="No production runs" text="Plan a run from an active BOM; starting it checks stock and completing it moves raw material into finished goods."
            action={onPlan ? <Button icon={Factory} onClick={onPlan}>Plan a run</Button> : undefined} />
        ) : (
          <>
            <div className="md:hidden">
              <ul className="divide-y divide-slate-100">
                {d.items.map((r) => (
                  <li key={r.id}>
                    <Link href={`/app/production/runs/${r.id}`} className="flex items-center gap-3 px-4 py-3 hover:bg-slate-50">
                      <div className="min-w-0 flex-1">
                        <div className="truncate text-sm font-semibold">{r.item_name}</div>
                        <div className="text-xs text-slate-500">{fmtQty(r.qty)} planned · {fmtDate(r.planned_date)}{stores.length > 1 ? ` · ${storeName(r.store_id)}` : ''}</div>
                      </div>
                      <RunBadge status={r.status} />
                    </Link>
                  </li>
                ))}
              </ul>
            </div>
            <div className="hidden md:block">
              <Table>
                <thead><tr><th className="th">Item</th><th className="th">Planned date</th><th className="th">Status</th><th className="th text-right">Planned qty</th><th className="th text-right">Output</th><th className="th text-right">Cost</th>{stores.length > 1 && <th className="th">Store</th>}</tr></thead>
                <tbody>
                  {d.items.map((r) => (
                    <tr key={r.id} className="border-t border-slate-100 hover:bg-slate-50">
                      <td className="td"><Link href={`/app/production/runs/${r.id}`} className="font-semibold text-ink hover:underline">{r.item_name}</Link>{r.output_batch_no && <span className="ml-2 text-xs text-slate-500">batch {r.output_batch_no}</span>}</td>
                      <td className="td">{fmtDate(r.planned_date)}</td>
                      <td className="td"><RunBadge status={r.status} /></td>
                      <td className="td num text-right">{fmtQty(r.qty)}</td>
                      <td className="td num text-right">{r.output_qty !== undefined ? fmtQty(r.output_qty) : '—'}</td>
                      <td className="td text-right">{r.total_cost_paise !== undefined ? <Money paise={r.total_cost_paise} /> : '—'}</td>
                      {stores.length > 1 && <td className="td text-slate-500">{storeName(r.store_id)}</td>}
                    </tr>
                  ))}
                </tbody>
              </Table>
            </div>
            <PagerNoTotal offset={offset} count={d.items.length} onChange={setOffset} />
          </>
        )}
      </QueryState>
    </Card>
  )
}
