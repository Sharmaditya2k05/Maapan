'use client'

import { useState } from 'react'
import Link from 'next/link'
import { useParams, useRouter } from 'next/navigation'
import { ArrowRight, Ban, PackageCheck, Truck } from 'lucide-react'
import { Badge, Button, Card, Confirm, ErrorBox, Money, PageHeader, QueryState, Table, statusTone } from '@/components/ui'
import { fmtDate, fmtQty, useGet, usePerm, useStore } from '@/lib/hooks'
import { errorMessage, post } from '@/lib/api'
import { PERMS } from '@/lib/rbac'
import { TRANSFER_STATUS_LABEL, transferActions } from '@/lib/stock/reasons'
import { storeName } from '@/lib/stock/scope'
import type { Transfer, TransferAction } from '@/lib/types.stock'
import { useAction } from '@/components/stock/hooks'

const ACTION_TEXT: Record<TransferAction, { title: string; text: string; label: string }> = {
  dispatch: { title: 'Dispatch this transfer?', text: 'Stock leaves the source store now at its average cost. The destination receives it when they confirm.', label: 'Dispatch' },
  receive: { title: 'Receive this transfer?', text: 'Stock is added to the destination store at the dispatch cost. This cannot be undone.', label: 'Receive' },
  cancel: { title: 'Cancel this transfer?', text: 'A dispatched transfer returns its stock to the source store; a draft is simply closed.', label: 'Cancel transfer' },
}

export default function TransferDetailPage() {
  const { id } = useParams<{ id: string }>()
  const router = useRouter()
  const canDo = usePerm()
  const { stores } = useStore()
  const tr = useGet<Transfer>(`/bhandar/v1/transfers/${id}`)
  const [pending, setPending] = useState<TransferAction | null>(null)
  const [err, setErr] = useState('')
  const act = useAction<Transfer, TransferAction>((action) => post(`/bhandar/v1/transfers/${id}/${action}`), {
    invalidate: ['/bhandar/v1/transfers', `/bhandar/v1/transfers/${id}`, '/bhandar/v1/stock'], silent: true,
    onSuccess: () => { setPending(null); setErr('') },
  })
  const canAccess = (storeId: string) => stores.some((s) => s.id === storeId)
  const run = async (a: TransferAction) => { try { await act.mutateAsync(a) } catch (e) { setPending(null); setErr(errorMessage(e)) } }

  const t = tr.data
  const allowed = t ? transferActions(t.status) : { dispatch: false, receive: false, cancel: false }
  const manage = canDo(PERMS.stockManage)
  const value = t?.lines.reduce((a, l) => a + Math.round(l.qty * l.unit_cost_paise), 0) ?? 0

  return (
    <>
      <PageHeader back={() => router.push('/app/stock?tab=transfers')}
        title={t ? <span className="inline-flex flex-wrap items-center gap-2">Transfer <span className="font-mono text-base text-slate-400">{t.id.slice(0, 8)}</span><Badge tone={statusTone(t.status)}>{TRANSFER_STATUS_LABEL[t.status]}</Badge></span> : 'Transfer'}
        subtitle={t ? <span className="inline-flex items-center gap-1.5">{storeName(stores, t.from_store_id)} <ArrowRight size={14} /> {storeName(stores, t.to_store_id)}</span> : undefined}
        actions={t && manage && (
          <>
            {allowed.dispatch && <Button icon={Truck} onClick={() => setPending('dispatch')} disabled={!canAccess(t.from_store_id)} title={canAccess(t.from_store_id) ? undefined : 'Only the source store can dispatch'}>Dispatch</Button>}
            {allowed.receive && <Button icon={PackageCheck} onClick={() => setPending('receive')} disabled={!canAccess(t.to_store_id)} title={canAccess(t.to_store_id) ? undefined : 'Only the destination store can receive'}>Receive</Button>}
            {allowed.cancel && <Button variant="ghost" icon={Ban} className="text-rose-600" onClick={() => setPending('cancel')} disabled={!canAccess(t.from_store_id)} title={canAccess(t.from_store_id) ? undefined : 'Only the source store can cancel'}>Cancel</Button>}
          </>
        )} />

      {err && <div className="mb-4"><ErrorBox message={err} /></div>}

      <QueryState isLoading={tr.isLoading} error={tr.error} data={t} retry={() => tr.refetch()}>
        {(x) => (
          <div className="grid gap-4 lg:grid-cols-3">
            <Card title="Timeline">
              <ol className="space-y-3 text-sm">
                <li><div className="font-semibold">Created</div><div className="text-slate-500">{fmtDate(x.created_at, true)} · {x.created_by}</div></li>
                <li className={x.dispatched_at ? '' : 'opacity-50'}><div className="font-semibold">Dispatched from {storeName(stores, x.from_store_id)}</div><div className="text-slate-500">{fmtDate(x.dispatched_at, true) || 'pending'}</div></li>
                <li className={x.received_at ? '' : 'opacity-50'}><div className="font-semibold">Received at {storeName(stores, x.to_store_id)}</div><div className="text-slate-500">{fmtDate(x.received_at, true) || 'pending'}</div></li>
                {x.cancelled_at && <li className="text-rose-600"><div className="font-semibold">Cancelled</div><div>{fmtDate(x.cancelled_at, true)}</div></li>}
              </ol>
              {x.notes && <p className="mt-4 rounded-xl bg-slate-50 px-3 py-2 text-sm text-slate-600">{x.notes}</p>}
              {!manage && <p className="mt-4 text-xs text-slate-500">Dispatch and receive need stock:manage.</p>}
            </Card>
            <Card title={`Lines (${x.lines.length})`} padded={false} className="lg:col-span-2">
              <Table>
                <thead className="bg-slate-50/60"><tr><th className="th">#</th><th className="th">Item</th><th className="th">Batch</th><th className="th text-right">Qty</th><th className="th text-right">Unit cost</th><th className="th text-right">Value</th></tr></thead>
                <tbody className="divide-y divide-slate-100">
                  {x.lines.map((l) => (
                    <tr key={l.line_no}>
                      <td className="td text-slate-400">{l.line_no}</td>
                      <td className="td"><Link href={`/app/items/${l.item_id}`} className="font-semibold hover:underline">{l.item_name || l.item_id.slice(0, 8)}</Link></td>
                      <td className="td font-mono text-xs">{l.batch_no || '—'}</td>
                      <td className="td num text-right font-semibold">{fmtQty(l.qty)}</td>
                      <td className="td text-right">{l.unit_cost_paise ? <Money paise={l.unit_cost_paise} className="text-slate-600" /> : <span className="text-xs text-slate-400">set on dispatch</span>}</td>
                      <td className="td text-right">{l.unit_cost_paise ? <Money paise={Math.round(l.qty * l.unit_cost_paise)} /> : '—'}</td>
                    </tr>
                  ))}
                </tbody>
                {value > 0 && <tfoot><tr className="border-t border-slate-200 bg-slate-50/60 font-semibold"><td className="td" colSpan={5}>Total at dispatch cost</td><td className="td text-right"><Money paise={value} /></td></tr></tfoot>}
              </Table>
            </Card>
          </div>
        )}
      </QueryState>

      {pending && (
        <Confirm open onClose={() => setPending(null)} onConfirm={() => run(pending)} loading={act.isPending} danger={pending === 'cancel'}
          title={ACTION_TEXT[pending].title} text={ACTION_TEXT[pending].text} confirmLabel={ACTION_TEXT[pending].label} />
      )}
    </>
  )
}
