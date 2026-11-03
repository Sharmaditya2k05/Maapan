'use client'

/**
 * Party → Bill-wise tab (bahi GET /v1/parties/{id}/statement?view=bills): open (or all) bills with their
 * settlements, on-account receipts/payments, FIFO auto-allocation (POST /v1/parties/{id}/allocations/auto)
 * and manual against-reference allocation of one voucher (PUT /v1/vouchers/{id}/allocations).
 */
import { Fragment, useEffect, useMemo, useState } from 'react'
import Link from 'next/link'
import { Download, ListChecks, Wand2 } from 'lucide-react'
import { Badge, Button, Empty, ErrorBox, Input, Modal, Money, MoneyInput, QueryState, Select, Table, useToast } from '@/components/ui'
import { api, errorMessage, post, put } from '@/lib/api'
import { downloadFile, fmtDate, todayIST, useGet, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import type { Bill, BillwiseStatement, OnAccount, VoucherAllocations } from '@/lib/types.phase4'
import { allocTotal, checkAllocations, cleanAllocations, fifoAllocate, type Alloc } from '@/lib/ledger/phase4'
import { voucherHref } from '../StatementTable'
import { useAction } from '../useAction'

const INVALIDATE = ['/bahi/v1/parties', '/bahi/v1/vouchers', '/bahi/v1/reports']

export function BillsTab({ partyId, partyName }: { partyId: string; partyName: string }) {
  const canDo = usePerm()
  const toast = useToast()
  const [asOf, setAsOf] = useState(todayIST())
  const [status, setStatus] = useState<'' | 'all'>('')
  const [allocFor, setAllocFor] = useState<OnAccount | null>(null)
  const q = useGet<BillwiseStatement>(`/bahi/v1/parties/${partyId}/statement`, { view: 'bills', as_of: asOf, status: status || undefined })
  const auto = useAction<{ allocations_created: number }>(() => post(`/bahi/v1/parties/${partyId}/allocations/auto`), {
    invalidate: INVALIDATE, onSuccess: (r) => toast(r.allocations_created ? `${r.allocations_created} allocation${r.allocations_created === 1 ? '' : 's'} made` : 'Nothing to allocate'),
  })
  const csv = useAction<Blob>(() => api<Blob>(`/bahi/v1/parties/${partyId}/statement`, { method: 'GET', raw: true, query: { view: 'bills', as_of: asOf, status: status || undefined, format: 'csv' } }), {
    onSuccess: (b) => downloadFile(`billwise-${partyName}-${asOf}.csv`, b),
  })

  return (
    <div>
      <div className="no-print flex flex-wrap items-center gap-2 border-b border-slate-100 p-3 sm:p-4">
        <label className="flex items-center gap-2 text-xs text-slate-500">As of <Input type="date" className="input-sm w-40" value={asOf} max={todayIST()} onChange={(e) => setAsOf(e.target.value || todayIST())} /></label>
        <Select className="input-sm w-auto" value={status} onChange={(e) => setStatus(e.target.value as '' | 'all')} aria-label="Bills shown"><option value="">Open bills</option><option value="all">All bills (incl. settled)</option></Select>
        <div className="ml-auto flex gap-2">
          <Button size="sm" variant="secondary" icon={Download} loading={csv.isPending} onClick={() => csv.mutate()}>CSV</Button>
          {canDo(PERMS.ledgerWrite) && <Button size="sm" icon={Wand2} loading={auto.isPending} onClick={() => auto.mutate()} title="Settle open bills oldest due date first with unallocated receipts / payments">Auto-allocate (FIFO)</Button>}
        </div>
      </div>
      <QueryState isLoading={q.isLoading} error={q.error} data={q.data} retry={() => q.refetch()}>
        {(st) => (
          <div className="space-y-4 p-3 sm:p-4">
            <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
              <Stat label="Ledger balance" paise={st.balance_paise} />
              <Stat label="Bills outstanding" paise={st.bills_outstanding_paise} />
              <Stat label="Overdue" paise={st.overdue_paise} tone={st.overdue_paise > 0 ? 'text-rose-700' : ''} />
              <Stat label="On account (unallocated)" paise={st.unallocated_paise} hint={st.credit_days ? `${st.credit_days} credit days` : undefined} />
            </div>
            {st.bills.length === 0 ? <Empty icon={ListChecks} title={status === 'all' ? 'No bills' : 'No open bills'} text="Sales, purchase and opening vouchers raise a bill per party; receipts and payments settle them." /> : (
              <Table>
                <thead className="bg-slate-50/60"><tr><th className="th">Bill</th><th className="th">Date</th><th className="th">Due</th><th className="th text-right">Amount</th><th className="th text-right">Settled</th><th className="th text-right">Outstanding</th><th className="th">Status</th></tr></thead>
                <tbody className="divide-y divide-slate-100">
                  {st.bills.map((b) => (
                    <Fragment key={b.id}>
                      <tr className="hover:bg-slate-50/60">
                        <td className="td"><Link href={voucherHref(b.voucher_id)} className="font-mono text-xs font-semibold text-brand-700 hover:underline">{b.ref || b.voucher_number}</Link><div className="text-[11px] text-slate-400">{b.voucher_type} · {b.side === 'dr' ? 'receivable' : 'payable'}</div></td>
                        <td className="td whitespace-nowrap text-slate-600">{fmtDate(b.bill_date)}</td>
                        <td className="td whitespace-nowrap text-slate-600">{fmtDate(b.due_date)}</td>
                        <td className="td num text-right"><Money paise={b.amount_paise} /></td>
                        <td className="td num text-right text-slate-600">{b.allocated_paise ? <Money paise={b.allocated_paise} /> : '—'}</td>
                        <td className="td num text-right font-semibold">{b.outstanding_paise ? <Money paise={b.outstanding_paise} /> : '—'}</td>
                        <td className="td">{b.overdue_days > 0 ? <Badge tone="red">{b.overdue_days} d overdue</Badge> : <Badge tone={b.outstanding_paise ? 'blue' : 'green'}>{b.outstanding_paise ? 'not due' : b.status}</Badge>}</td>
                      </tr>
                      {(b.allocations ?? []).map((a) => (
                        <tr key={a.id} className="bg-slate-50/40 text-xs text-slate-500">
                          <td className="px-4 py-1.5 pl-8" colSpan={2}>↳ <Link href={voucherHref(a.voucher_id)} className="font-mono hover:underline">{a.voucher_number}</Link> {a.voucher_type} · {a.method}</td>
                          <td className="px-4 py-1.5">{fmtDate(a.date)}</td><td />
                          <td className="num px-4 py-1.5 text-right"><Money paise={a.amount_paise} /></td><td colSpan={2} />
                        </tr>
                      ))}
                    </Fragment>
                  ))}
                </tbody>
              </Table>
            )}
            {st.on_account.length > 0 && (
              <div>
                <h3 className="mb-2 text-sm font-bold">On account — not yet set against a bill</h3>
                <Table>
                  <thead className="bg-slate-50/60"><tr><th className="th">Voucher</th><th className="th">Date</th><th className="th text-right">Amount</th><th className="th text-right">Allocated</th><th className="th text-right">Unallocated</th><th className="th" /></tr></thead>
                  <tbody className="divide-y divide-slate-100">
                    {st.on_account.map((o) => (
                      <tr key={o.voucher_id}>
                        <td className="td"><Link href={voucherHref(o.voucher_id)} className="font-mono text-xs font-semibold text-brand-700 hover:underline">{o.voucher_number}</Link><div className="text-[11px] text-slate-400">{o.voucher_type}</div></td>
                        <td className="td text-slate-600">{fmtDate(o.date)}</td>
                        <td className="td num text-right"><Money paise={o.amount_paise} /></td>
                        <td className="td num text-right text-slate-600">{o.allocated_paise ? <Money paise={o.allocated_paise} /> : '—'}</td>
                        <td className="td num text-right font-semibold"><Money paise={o.unallocated_paise} /></td>
                        <td className="td text-right">{canDo(PERMS.ledgerWrite) && <Button size="sm" variant="secondary" onClick={() => setAllocFor(o)}>Allocate</Button>}</td>
                      </tr>
                    ))}
                  </tbody>
                </Table>
              </div>
            )}
          </div>
        )}
      </QueryState>
      {allocFor && <AllocateModal partyId={partyId} voucher={allocFor} onClose={() => setAllocFor(null)} />}
    </div>
  )
}

function Stat({ label, paise, tone = '', hint }: { label: string; paise: number; tone?: string; hint?: string }) {
  return <div className="card p-3"><div className="text-xs text-slate-500">{label}</div><div className={`num mt-0.5 text-base font-bold ${tone}`}><Money paise={paise} /></div>{hint && <div className="text-[11px] text-slate-400">{hint}</div>}</div>
}

/** Open bills a settling movement on `side` can be set against (cr settles receivable dr bills and vice versa). */
export function useOpenBills(partyId: string | null | undefined, settlingSide: 'dr' | 'cr') {
  const q = useGet<{ items: Bill[] }>(partyId ? `/bahi/v1/parties/${partyId}/bills` : null)
  const bills = useMemo(() => (q.data?.items ?? []).filter((b) => b.outstanding_paise > 0 && b.side !== settlingSide), [q.data, settlingSide])
  return { ...q, bills }
}

/** Editable allocation grid used by the modal here and by the payment form. */
export function AllocationGrid({ bills, value, onChange, available, extra = {} }: {
  bills: Bill[]; value: Alloc[]; onChange: (a: Alloc[]) => void; available: number
  /** Amounts already allocated by this voucher, added back to each bill's outstanding. */
  extra?: Record<string, number>
}) {
  const amountOf = (id: string) => value.find((a) => a.bill_id === id)?.amount_paise ?? 0
  const set = (id: string, amt: number) => onChange([...value.filter((a) => a.bill_id !== id), ...(amt > 0 ? [{ bill_id: id, amount_paise: amt }] : [])])
  const total = allocTotal(value)
  const open = bills.map((b) => ({ ...b, outstanding_paise: b.outstanding_paise + (extra[b.id] ?? 0) }))
  if (open.length === 0) return <p className="rounded-xl bg-slate-50 px-4 py-3 text-sm text-slate-500">No open bills — the whole amount stays on account.</p>
  return (
    <div>
      <div className="mb-2 flex items-center justify-between gap-2 text-xs">
        <span className="text-slate-500">Against reference: <Money paise={total} className="font-semibold text-ink" /> · on account: <Money paise={Math.max(0, available - total)} /></span>
        <span className="flex gap-2">
          <button type="button" className="link cursor-pointer" onClick={() => onChange(fifoAllocate(open, available))}>Fill oldest first</button>
          <button type="button" className="link cursor-pointer" onClick={() => onChange([])}>Clear</button>
        </span>
      </div>
      <div className="max-h-64 overflow-y-auto rounded-xl border border-slate-200">
        <table className="w-full text-sm">
          <thead className="sticky top-0 bg-slate-50"><tr><th className="th">Bill</th><th className="th">Due</th><th className="th text-right">Outstanding</th><th className="th w-40 text-right">Settle</th></tr></thead>
          <tbody className="divide-y divide-slate-100">
            {open.map((b) => (
              <tr key={b.id}>
                <td className="td"><span className="font-mono text-xs font-semibold">{b.ref || b.voucher_number}</span><div className="text-[11px] text-slate-400">{fmtDate(b.bill_date)}</div></td>
                <td className="td text-xs">{fmtDate(b.due_date)}{b.overdue_days > 0 && <div className="text-rose-600">{b.overdue_days} d overdue</div>}</td>
                <td className="td num text-right"><Money paise={b.outstanding_paise} /></td>
                <td className="td"><MoneyInput value={amountOf(b.id)} onChange={(p) => set(b.id, Math.min(p, b.outstanding_paise))} aria-label={`Amount against ${b.ref || b.voucher_number}`} /></td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}

function AllocateModal({ partyId, voucher, onClose }: { partyId: string; voucher: OnAccount; onClose: () => void }) {
  const { bills, isLoading, error } = useOpenBills(partyId, voucher.side)
  const current = useGet<VoucherAllocations>(`/bahi/v1/vouchers/${voucher.voucher_id}/allocations`)
  const [allocs, setAllocs] = useState<Alloc[] | null>(null)
  const [err, setErr] = useState<string | null>(null)
  const existing = useMemo(() => {
    const o: Record<string, number> = {}
    for (const a of current.data?.allocations ?? []) o[a.bill_id] = (o[a.bill_id] ?? 0) + a.amount_paise
    return o
  }, [current.data])
  useEffect(() => {
    if (current.data && allocs === null) setAllocs(Object.entries(existing).map(([bill_id, amount_paise]) => ({ bill_id, amount_paise })))
  }, [current.data, existing, allocs])
  // Bills fully settled by this voucher are no longer "open"; keep them in the grid via their existing allocation.
  const save = useAction<VoucherAllocations, Alloc[]>((a) => put(`/bahi/v1/vouchers/${voucher.voucher_id}/allocations`, { allocations: a }), {
    invalidate: INVALIDATE, successMessage: 'Allocations saved', onSuccess: onClose,
  })
  const submit = () => {
    const a = cleanAllocations(allocs ?? [])
    const outstanding: Record<string, number> = {}
    for (const b of bills) outstanding[b.id] = b.outstanding_paise + (existing[b.id] ?? 0)
    for (const [id, amt] of Object.entries(existing)) if (outstanding[id] === undefined) outstanding[id] = amt
    const problem = checkAllocations(a, outstanding, voucher.amount_paise)
    if (problem) return setErr(problem)
    setErr(null)
    save.mutate(a)
  }
  return (
    <Modal open onClose={onClose} title={`Allocate ${voucher.voucher_number}`} size="lg"
      footer={<><Button variant="secondary" onClick={onClose}>Cancel</Button><Button onClick={submit} loading={save.isPending} disabled={allocs === null}>Save allocations</Button></>}>
      <p className="mb-3 text-sm text-slate-600">{voucher.voucher_type} of <Money paise={voucher.amount_paise} className="font-semibold" /> dated {fmtDate(voucher.date)}. Saving replaces this voucher’s allocations; whatever is left stays on account.</p>
      {(isLoading || current.isLoading) && <p className="text-sm text-slate-500">Loading bills…</p>}
      {(error || current.error) && <ErrorBox message={errorMessage(error ?? current.error)} />}
      {allocs !== null && <AllocationGrid bills={bills} value={allocs} onChange={setAllocs} available={voucher.amount_paise} extra={existing} />}
      {err && <p className="mt-3 text-sm text-rose-600">{err}</p>}
    </Modal>
  )
}
