'use client'

/** Post-dated / pending cheques by cheque date (bahi GET /v1/cheques/pdc?as_of=&days=&direction=). */
import { useState } from 'react'
import Link from 'next/link'
import { CalendarClock, Download } from 'lucide-react'
import { Badge, Button, Empty, Input, Money, QueryState, Select, Table, type Tone } from '@/components/ui'
import { api } from '@/lib/api'
import { downloadFile, fmtDate, todayIST, useGet } from '@/lib/hooks'
import type { PdcBucket, PdcReport } from '@/lib/types.phase4'
import { useAction } from '../useAction'

const BUCKETS: { key: PdcBucket; label: string; tone: Tone }[] = [
  { key: 'overdue', label: 'Date reached, not presented', tone: 'red' },
  { key: 'due_soon', label: 'Due soon', tone: 'amber' },
  { key: 'later', label: 'Later', tone: 'blue' },
  { key: 'awaiting_clearance', label: 'Deposited, awaiting clearance', tone: 'gray' },
]

export function Pdc() {
  const [asOf, setAsOf] = useState(todayIST())
  const [days, setDays] = useState('7')
  const [direction, setDirection] = useState('')
  const query = { as_of: asOf, days: days || undefined, direction: direction || undefined }
  const q = useGet<PdcReport>('/bahi/v1/cheques/pdc', query)
  const csv = useAction<Blob>(() => api<Blob>('/bahi/v1/cheques/pdc', { method: 'GET', raw: true, query: { ...query, format: 'csv' } }), { onSuccess: (b) => downloadFile(`pdc-${asOf}.csv`, b) })
  const total = (dir: 'received' | 'issued', k: PdcBucket) => q.data?.totals_paise?.[dir]?.[k] ?? 0

  return (
    <>
      <div className="mb-4 flex flex-wrap items-center gap-2">
        <label className="flex items-center gap-2 text-xs text-slate-500">As of <Input type="date" className="input-sm w-40" value={asOf} onChange={(e) => setAsOf(e.target.value || todayIST())} /></label>
        <label className="flex items-center gap-2 text-xs text-slate-500">Due soon = within <Input className="input-sm w-16 num" inputMode="numeric" value={days} onChange={(e) => setDays(e.target.value.replace(/\D/g, ''))} /> days</label>
        <Select value={direction} onChange={(e) => setDirection(e.target.value)} className="w-auto input-sm" aria-label="Direction"><option value="">Received & issued</option><option value="received">Received</option><option value="issued">Issued</option></Select>
        <Button size="sm" variant="secondary" icon={Download} loading={csv.isPending} onClick={() => csv.mutate()} className="ml-auto">CSV</Button>
      </div>
      <QueryState isLoading={q.isLoading} error={q.error} data={q.data} retry={() => q.refetch()}>
        {(d) => d.items.length === 0 ? <Empty icon={CalendarClock} title="No pending cheques" text="Received cheques not yet deposited, deposits awaiting clearance and issued cheques not yet cleared show here by cheque date." /> : (
          <>
            <div className="mb-4 grid grid-cols-2 gap-3 lg:grid-cols-4">
              {BUCKETS.map((b) => (
                <div key={b.key} className="card p-3">
                  <div className="flex items-center gap-2 text-xs text-slate-500"><Badge tone={b.tone}>{b.label}</Badge></div>
                  <div className="mt-1 text-xs text-slate-500">In <Money paise={total('received', b.key)} className="font-semibold text-ink" /></div>
                  <div className="text-xs text-slate-500">Out <Money paise={total('issued', b.key)} className="font-semibold text-ink" /></div>
                </div>
              ))}
            </div>
            <Table>
              <thead className="bg-slate-50/60"><tr><th className="th">Cheque</th><th className="th">Party</th><th className="th">Direction</th><th className="th">When</th><th className="th text-right">Amount</th><th className="th">Status</th></tr></thead>
              <tbody className="divide-y divide-slate-100">
                {d.items.map((c) => {
                  const b = BUCKETS.find((x) => x.key === c.bucket)
                  return (
                    <tr key={c.id}>
                      <td className="td"><div className="font-mono text-sm font-semibold">{c.cheque_no}</div><div className="text-xs text-slate-500">{fmtDate(c.cheque_date)}{c.post_dated && ' · post-dated'}</div></td>
                      <td className="td"><Link href={`/app/parties/${c.party_id}`} className="font-medium hover:underline">{c.party_name}</Link>{c.drawee_bank && <div className="text-xs text-slate-400">{c.drawee_bank}</div>}</td>
                      <td className="td capitalize text-slate-600">{c.direction}</td>
                      <td className="td">{b && <Badge tone={b.tone}>{c.days_to_due > 0 ? `in ${c.days_to_due} d` : c.days_to_due === 0 ? 'today' : `${-c.days_to_due} d ago`}</Badge>}</td>
                      <td className="td num text-right font-semibold"><Money paise={c.amount_paise} /></td>
                      <td className="td text-slate-600">{c.status}</td>
                    </tr>
                  )
                })}
              </tbody>
            </Table>
          </>
        )}
      </QueryState>
    </>
  )
}
