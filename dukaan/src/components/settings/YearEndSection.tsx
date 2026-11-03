'use client'

/** Year-end close / reopen (bahi POST /v1/year-end/close, GET /v1/year-end, POST /v1/year-end/{fy}/reopen). */
import { useState } from 'react'
import Link from 'next/link'
import { CalendarCheck } from 'lucide-react'
import { Badge, Button, Card, Confirm, Empty, Field, Money, QueryState, Select, Table, Toggle } from '@/components/ui'
import { post } from '@/lib/api'
import { todayIST, useGet, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import type { YearEnd } from '@/lib/types.phase4'
import { fyCodeOptions, lastEndedFY } from '@/lib/ledger/phase4'
import { voucherHref } from '@/components/ledger/StatementTable'
import { useAction } from '@/components/ledger/useAction'

const INVALIDATE = ['/bahi/v1/year-end', '/bahi/v1/settings/period-lock', '/bahi/v1/reports', '/bahi/v1/vouchers', '/bahi/v1/accounts']

export function YearEndSection() {
  const canDo = usePerm()
  const today = todayIST()
  const list = useGet<{ items: YearEnd[] }>('/bahi/v1/year-end')
  const ended = fyCodeOptions(today, 5).slice(1)
  const [fy, setFy] = useState(lastEndedFY(today))
  const [drawings, setDrawings] = useState(true)
  const [lock, setLock] = useState(true)
  const [confirm, setConfirm] = useState(false)
  const [reopen, setReopen] = useState<YearEnd | null>(null)
  const close = useAction<YearEnd, void>(() => post('/bahi/v1/year-end/close', { fy, close_drawings: drawings, lock }), { invalidate: INVALIDATE, successMessage: `FY 20${fy} closed`, onSuccess: () => setConfirm(false) })
  const undo = useAction<unknown, string>((f) => post(`/bahi/v1/year-end/${f}/reopen`), { invalidate: INVALIDATE, successMessage: 'Year reopened', onSuccess: () => setReopen(null) })
  const items = list.data?.items ?? []
  const closedAlready = items.some((y) => y.fy === fy && y.status === 'closed')
  const canEdit = canDo(PERMS.settingsManage)

  return (
    <div className="space-y-4">
      <Card title="Close a financial year">
        <p className="mb-4 text-sm text-slate-600">One journal dated 31 March zeroes every income and expense account (and Drawings) and carries the net profit to Capital. The P&amp;L of a closed year still shows its profit; balance sheet accounts carry forward on their own.</p>
        <div className="flex flex-wrap items-end gap-4">
          <Field label="Financial year"><Select value={fy} onChange={(e) => setFy(e.target.value)} className="w-40">{ended.map((f) => <option key={f} value={f}>FY 20{f}</option>)}</Select></Field>
          <Toggle checked={drawings} onChange={setDrawings} label="Close Drawings to Capital" />
          <Toggle checked={lock} onChange={setLock} label="Lock the books up to 31 March" />
          {canEdit && <Button icon={CalendarCheck} disabled={closedAlready} onClick={() => setConfirm(true)}>{closedAlready ? 'Already closed' : `Close FY 20${fy}`}</Button>}
        </div>
      </Card>
      <Card title="Closings" padded={false}>
        <QueryState isLoading={list.isLoading} error={list.error} data={list.data} retry={() => list.refetch()}>
          {() => items.length === 0 ? <Empty icon={CalendarCheck} title="No year closed yet" /> : (
            <Table>
              <thead className="bg-slate-50/60"><tr><th className="th">Year</th><th className="th">Journal</th><th className="th text-right">Carried to capital</th><th className="th">Status</th><th className="th" /></tr></thead>
              <tbody className="divide-y divide-slate-100">
                {items.map((y) => (
                  <tr key={`${y.fy}-${y.voucher_id}`}>
                    <td className="td font-semibold">FY 20{y.fy}</td>
                    <td className="td">{y.voucher_id ? <Link href={voucherHref(y.voucher_id)} className="link font-mono text-xs">{y.voucher_number}</Link> : '—'}</td>
                    <td className="td num text-right"><Money paise={y.net_profit_paise} signed /></td>
                    <td className="td"><Badge tone={y.status === 'closed' ? 'green' : 'gray'}>{y.status}</Badge></td>
                    <td className="td text-right">{canEdit && y.status === 'closed' && <Button size="sm" variant="ghost" onClick={() => setReopen(y)}>Reopen</Button>}</td>
                  </tr>
                ))}
              </tbody>
            </Table>
          )}
        </QueryState>
      </Card>
      <Confirm open={confirm} onClose={() => setConfirm(false)} onConfirm={() => close.mutate()} loading={close.isPending} title={`Close FY 20${fy}?`} confirmLabel="Close the year"
        text={`Posts the closing journal on 31 March${lock ? ' and locks the books up to that date' : ''}. You can reopen it later (unlock the period first).`} />
      <Confirm open={!!reopen} onClose={() => setReopen(null)} onConfirm={() => reopen && undo.mutate(reopen.fy)} loading={undo.isPending} danger title={`Reopen FY 20${reopen?.fy}?`} confirmLabel="Reopen"
        text="Reverses the closing journal. Move the period lock before 31 March first (Settings → Period lock), otherwise the reversal is refused." />
    </div>
  )
}
