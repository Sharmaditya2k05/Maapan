'use client'

import { useState } from 'react'
import Link from 'next/link'
import { useParams, useRouter } from 'next/navigation'
import { Factory, PackageCheck, Play, XCircle } from 'lucide-react'
import { Badge, Button, Card, Confirm, Empty, Money, PageHeader, QueryState, Table, useToast } from '@/components/ui'
import { fmtDate, fmtQty, useGet, usePerm, useSave, useStore } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import type { ProductionRun, RunShortage } from '@/lib/types.more'
import { CompleteRunModal, RunBadge, RunTimeline, ShortageList, shortagesFromError } from '@/components/production/runs'

export default function RunPage() {
  const { id } = useParams<{ id: string }>()
  const router = useRouter()
  const canDo = usePerm()
  const toast = useToast()
  const { stores } = useStore()
  const canWrite = canDo(PERMS.bomManage)
  const run = useGet<ProductionRun>(canWrite || canDo(PERMS.reportRead) ? `/nirmaan/v1/production-runs/${id}` : null)
  const [complete, setComplete] = useState(false)
  const [cancel, setCancel] = useState(false)
  const [startShort, setStartShort] = useState<RunShortage[] | null>(null)

  const inv = ['/nirmaan/v1/production-runs', `/nirmaan/v1/production-runs/${id}`]
  const start = useSave<ProductionRun, { force: boolean }>(`/nirmaan/v1/production-runs/${id}/start`, { invalidate: inv, silent: true })
  const cancelRun = useSave<ProductionRun>(`/nirmaan/v1/production-runs/${id}/cancel`, { invalidate: inv, onSuccess: () => { toast('Run cancelled'); setCancel(false) } })

  const doStart = async (force: boolean) => {
    try {
      await start.mutateAsync({ force })
      setStartShort(null)
      toast(force ? 'Run started (forced)' : 'Run started')
    } catch (e) {
      const sh = shortagesFromError(e)
      if (sh) setStartShort(sh)
      else toast((e as Error).message, 'error')
    }
  }

  if (!canWrite && !canDo(PERMS.reportRead)) return <Empty icon={Factory} title="No access" text="Only store managers and owners can view production runs." />

  return (
    <QueryState isLoading={run.isLoading} error={run.error} data={run.data} retry={() => run.refetch()}>
      {(r) => {
        const lines = r.lines ?? []
        const consume = lines.filter((l) => l.kind === 'consume')
        const produce = lines.find((l) => l.kind === 'produce')
        const open = r.status === 'planned' || r.status === 'in_progress'
        const shortages = startShort ?? r.shortages ?? []
        const store = stores.find((s) => s.id === r.store_id)
        const outputQty = r.output_qty ?? r.qty
        return (
          <>
            <PageHeader back={() => router.push('/app/production?tab=runs')}
              title={<span className="inline-flex flex-wrap items-center gap-2">{r.item_name} <RunBadge status={r.status} /></span>}
              subtitle={<>Planned {fmtQty(r.qty)} {produce?.unit ?? ''} on {fmtDate(r.planned_date)}{store ? ` · ${store.name}` : ''} · <Link href={`/app/production/boms/${r.bom_id}`} className="link">view BOM</Link></>}
              actions={canWrite && open && (
                <>
                  {r.status === 'planned' && (shortages.length > 0
                    ? <Button variant="danger" icon={Play} loading={start.isPending} onClick={() => doStart(true)}>Start anyway (force)</Button>
                    : <Button icon={Play} loading={start.isPending} onClick={() => doStart(false)}>Start</Button>)}
                  <Button icon={PackageCheck} variant={r.status === 'in_progress' ? 'primary' : 'secondary'} onClick={() => setComplete(true)}>Complete</Button>
                  <Button variant="ghost" icon={XCircle} onClick={() => setCancel(true)}>Cancel run</Button>
                </>
              )} />

            <div className="grid gap-4 lg:grid-cols-3">
              <div className="space-y-4 lg:col-span-2">
                {open && <ShortageList shortages={shortages} title={startShort ? 'Cannot start: not enough material. Force-start records the shortfall and you fix stock later.' : 'Not enough material in this store'} />}
                <Card title="Material" padded={false}>
                  <Table>
                    <thead><tr><th className="th">Component</th><th className="th text-right">Planned</th><th className="th text-right">Actual</th><th className="th text-right">Unit cost</th><th className="th text-right">Cost</th></tr></thead>
                    <tbody>
                      {consume.map((l) => (
                        <tr key={l.line_no} className="border-t border-slate-100">
                          <td className="td font-medium">{l.item_name}{l.is_sub_assembly && <Badge tone="brand" className="ml-2">sub-assembly</Badge>}</td>
                          <td className="td num text-right">{fmtQty(l.planned_qty)} {l.unit}</td>
                          <td className="td num text-right">{l.actual_qty !== undefined ? `${fmtQty(l.actual_qty)} ${l.unit}` : '—'}</td>
                          <td className="td text-right">{l.unit_cost_paise !== undefined ? <Money paise={l.unit_cost_paise} /> : '—'}</td>
                          <td className="td text-right">{l.unit_cost_paise !== undefined ? <Money paise={Math.round((l.actual_qty ?? l.planned_qty) * l.unit_cost_paise)} /> : '—'}</td>
                        </tr>
                      ))}
                      {produce && (
                        <tr className="border-t border-slate-200 bg-emerald-50/50 font-semibold">
                          <td className="td">→ {produce.item_name} <span className="text-xs font-normal text-slate-500">(output{r.output_batch_no ? ` · batch ${r.output_batch_no}` : ''})</span></td>
                          <td className="td num text-right">{fmtQty(produce.planned_qty)} {produce.unit}</td>
                          <td className="td num text-right">{produce.actual_qty !== undefined ? `${fmtQty(produce.actual_qty)} ${produce.unit}` : '—'}</td>
                          <td className="td text-right">{produce.unit_cost_paise !== undefined ? <Money paise={produce.unit_cost_paise} /> : '—'}</td>
                          <td className="td text-right">{r.total_cost_paise !== undefined ? <Money paise={r.total_cost_paise} /> : '—'}</td>
                        </tr>
                      )}
                    </tbody>
                  </Table>
                </Card>
                {r.notes && <Card title="Notes"><p className="whitespace-pre-wrap text-sm text-slate-600">{r.notes}</p></Card>}
              </div>
              <div className="space-y-4">
                <Card title="Timeline"><RunTimeline run={r} /></Card>
                <Card title="Cost">
                  {r.status === 'completed' && r.total_cost_paise !== undefined ? (
                    <dl className="space-y-2 text-sm">
                      <div className="flex justify-between"><dt className="text-slate-500">Total production cost</dt><dd className="font-bold"><Money paise={r.total_cost_paise} /></dd></div>
                      <div className="flex justify-between"><dt className="text-slate-500">Output</dt><dd className="num">{fmtQty(outputQty)} {produce?.unit}</dd></div>
                      <div className="flex justify-between"><dt className="text-slate-500">Cost per {produce?.unit ?? 'unit'}</dt><dd className="font-bold"><Money paise={produce?.unit_cost_paise ?? (outputQty > 0 ? Math.round(r.total_cost_paise / outputQty) : 0)} /></dd></div>
                      {r.output_qty !== undefined && r.output_qty < r.qty && <p className="text-xs text-amber-700">Output was {fmtQty(r.qty - r.output_qty)} below plan, so the unit cost includes the wastage.</p>}
                    </dl>
                  ) : r.status === 'cancelled' ? <p className="text-sm text-slate-500">Cancelled before completion — no stock moved.</p>
                    : <p className="text-sm text-slate-500">Costed on completion: raw material at purchase price, sub-assemblies at their rolled-up cost, plus BOM operations. See the <Link href={`/app/production/boms/${r.bom_id}`} className="link">BOM cost view</Link> for an estimate.</p>}
                </Card>
              </div>
            </div>

            <CompleteRunModal run={r} open={complete} onClose={() => setComplete(false)} onDone={() => { setComplete(false); void run.refetch() }} />
            <Confirm open={cancel} onClose={() => setCancel(false)} onConfirm={() => cancelRun.mutate(undefined)} loading={cancelRun.isPending} danger title="Cancel this run?" text="No stock has moved yet. The run is kept for history as cancelled." confirmLabel="Cancel run" />
          </>
        )
      }}
    </QueryState>
  )
}
