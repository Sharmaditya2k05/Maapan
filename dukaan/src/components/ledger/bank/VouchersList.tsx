'use client'

/** Vouchers tab: bahi GET /v1/vouchers with type / source / search / period filters; rows open VoucherModal. */
import { useEffect, useState } from 'react'
import Link from 'next/link'
import { BookOpen, Plus } from 'lucide-react'
import { Badge, Button, Empty, Money, Pagination, QueryState, SearchInput, Select, statusTone, Table } from '@/components/ui'
import { downloadFile, fmtDate, todayIST, useDebounced, useList, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import type { VoucherView } from '@/lib/types.ledger'
import { csvRupees, toCSV } from '@/lib/ledger/csv'
import { presetRange, type DateRange } from '@/lib/ledger/util'
import { DateRangePicker } from '../DateRangePicker'
import { MobileRow } from '../bits'
import { VOUCHER_TYPE_LABEL } from '../VoucherModal'

const SOURCES = ['manual', 'opening', 'reversal', 'bank', 'cheque', 'aadesh.invoice', 'aadesh.cancel', 'lenden.payment', 'lenden.refund', 'lenden.settlement', 'bhandar.stock.moved', 'nirmaan.production']

export function VouchersList({ onOpen }: { onOpen: (id: string) => void }) {
  const canDo = usePerm()
  const [range, setRange] = useState<DateRange>(() => presetRange('this_month', todayIST()))
  const [type, setType] = useState('')
  const [source, setSource] = useState('')
  const [q, setQ] = useState('')
  const dq = useDebounced(q)
  const [offset, setOffset] = useState(0)
  useEffect(() => setOffset(0), [range.from, range.to, type, source, dq])
  const list = useList<VoucherView>('/bahi/v1/vouchers', { from: range.from, to: range.to, type: type || undefined, source: source || undefined, q: dq, limit: 50, offset })
  const exportCSV = () => downloadFile(`vouchers-${range.from}-${range.to}.csv`, toCSV(['Date', 'Number', 'Type', 'Narration', 'Source', 'Reference', 'Status', 'Total'],
    (list.data?.items ?? []).map((v) => [v.date, v.number, v.type, v.narration, v.source, v.source_ref ?? '', v.status, csvRupees(v.total_paise)])), 'text/csv')

  return (
    <>
      <div className="mb-4 flex flex-wrap items-center gap-2">
        <DateRangePicker value={range} onChange={setRange} />
        <Select value={type} onChange={(e) => setType(e.target.value)} className="w-auto input-sm" aria-label="Voucher type"><option value="">All types</option>{Object.entries(VOUCHER_TYPE_LABEL).map(([k, l]) => <option key={k} value={k}>{l}</option>)}</Select>
        <Select value={source} onChange={(e) => setSource(e.target.value)} className="w-auto input-sm" aria-label="Source"><option value="">All sources</option>{SOURCES.map((s) => <option key={s} value={s}>{s}</option>)}</Select>
        <SearchInput value={q} onChange={setQ} placeholder="Number, narration, reference" className="md:w-60" />
        <div className="ml-auto flex gap-2">
          <Button size="sm" variant="secondary" onClick={exportCSV} disabled={!list.data?.items.length}>CSV</Button>
          {canDo(PERMS.ledgerWrite) && <Link href="/app/bank/vouchers/new"><Button size="sm" icon={Plus}>New voucher</Button></Link>}
        </div>
      </div>
      <QueryState isLoading={list.isLoading} error={list.error} data={list.data} retry={() => list.refetch()}>
        {(d) => d.items.length === 0 ? <Empty icon={BookOpen} title="No vouchers match" text="Every invoice, payment and stock movement becomes a voucher here automatically." /> : (
          <>
            <div className="divide-y divide-slate-100 md:hidden">
              {d.items.map((v) => (
                <MobileRow key={v.id} onClick={() => onOpen(v.id)} title={<span className="flex items-center gap-1.5"><span className="font-mono">{v.number}</span>{v.status !== 'posted' && <Badge tone={statusTone(v.status)}>{v.status}</Badge>}</span>}
                  subtitle={`${fmtDate(v.date)} · ${VOUCHER_TYPE_LABEL[v.type] ?? v.type}${v.narration ? ` · ${v.narration}` : ''}`} right={<Money paise={v.total_paise} />} rightSub={v.source} />
              ))}
            </div>
            <div className="hidden md:block">
              <Table>
                <thead className="bg-slate-50/60"><tr><th className="th">Date</th><th className="th">Number</th><th className="th">Type</th><th className="th">Narration</th><th className="th">Source</th><th className="th">Status</th><th className="th text-right">Amount</th></tr></thead>
                <tbody className="divide-y divide-slate-100">
                  {d.items.map((v) => (
                    <tr key={v.id} onClick={() => onOpen(v.id)} className="cursor-pointer hover:bg-brand-50/40">
                      <td className="td whitespace-nowrap text-slate-600">{fmtDate(v.date)}</td>
                      <td className="td font-mono text-xs font-semibold text-brand-700">{v.number}</td>
                      <td className="td">{VOUCHER_TYPE_LABEL[v.type] ?? v.type}</td>
                      <td className="td max-w-[24rem] truncate text-slate-600" title={v.narration}>{v.narration || '—'}{v.source_ref && <span className="ml-1 text-xs text-slate-400">· {v.source_ref}</span>}</td>
                      <td className="td text-xs text-slate-500">{v.source}</td>
                      <td className="td"><Badge tone={statusTone(v.status)}>{v.status}</Badge></td>
                      <td className="td num text-right font-semibold"><Money paise={v.total_paise} /></td>
                    </tr>
                  ))}
                </tbody>
              </Table>
            </div>
            <Pagination total={d.total} limit={d.limit} offset={d.offset} onChange={setOffset} />
          </>
        )}
      </QueryState>
    </>
  )
}
