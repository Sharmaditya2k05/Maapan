'use client'

/** Voucher detail (bahi GET /v1/vouchers/{id}) with entries, source links and Reverse for manual/opening vouchers. */
import { useState } from 'react'
import Link from 'next/link'
import { Undo2 } from 'lucide-react'
import { Badge, Button, Field, Input, Modal, Money, QueryState, statusTone, Textarea } from '@/components/ui'
import { post } from '@/lib/api'
import { fmtDate, todayIST, useGet, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import type { ReverseInput, VoucherView } from '@/lib/types.ledger'
import { KV } from './bits'
import { useAction } from './useAction'

export const VOUCHER_TYPE_LABEL: Record<string, string> = {
  sales: 'Sales', purchase: 'Purchase', receipt: 'Receipt', payment: 'Payment', journal: 'Journal', contra: 'Contra',
  credit_note: 'Credit note', debit_note: 'Debit note', production: 'Production', opening: 'Opening',
}

/** Where the source document of an event-driven voucher lives in dukaan. */
export function sourceHref(v: VoucherView): string | null {
  if (!v.source_id) return null
  if (v.source === 'aadesh.invoice' || v.source === 'aadesh.cancel') {
    if (v.type === 'sales') return `/app/sales/${v.source_id}`
    if (v.type === 'purchase') return `/app/purchases/${v.source_id}`
    if (v.type === 'credit_note' || v.type === 'debit_note') return `/app/returns/${v.source_id}`
  }
  if (v.source === 'lenden.payment') return `/app/payments?payment=${v.source_id}`
  return null
}

export function VoucherModal({ voucherId, onClose, onOpen }: { voucherId: string | null; onClose: () => void; onOpen?: (id: string) => void }) {
  const canDo = usePerm()
  const q = useGet<VoucherView>(voucherId ? `/bahi/v1/vouchers/${voucherId}` : null)
  const [reversing, setReversing] = useState(false)
  const [reason, setReason] = useState('')
  const [date, setDate] = useState(todayIST())
  const reverse = useAction<VoucherView, { id: string; body: ReverseInput }>((i) => post<VoucherView>(`/bahi/v1/vouchers/${i.id}/reverse`, i.body), {
    invalidate: ['/bahi/v1/vouchers', '/bahi/v1/parties', '/bahi/v1/reports', '/bahi/v1/daybook', '/bahi/v1/cashbook', '/bahi/v1/bankbook', '/bahi/v1/accounts'],
    successMessage: 'Reversal posted',
    onSuccess: (v) => { setReversing(false); setReason(''); onOpen?.(v.id) },
  })
  const v = q.data
  const canReverse = !!v && canDo(PERMS.ledgerWrite) && (v.source === 'manual' || v.source === 'opening') && v.status === 'posted' && !v.reversed_by
  const link = v ? sourceHref(v) : null
  const jump = (id: string) => (onOpen ? <button type="button" className="link cursor-pointer" onClick={() => onOpen(id)}>{id.slice(0, 8)}…</button> : <span className="font-mono text-xs">{id}</span>)

  return (
    <Modal open={!!voucherId} onClose={onClose} size="lg" title={v ? <span className="flex items-center gap-2"><span className="font-mono">{v.number}</span><Badge tone={statusTone(v.status)}>{v.status}</Badge></span> : 'Voucher'}
      footer={<>
        {canReverse && !reversing && <Button variant="danger" icon={Undo2} onClick={() => setReversing(true)}>Reverse</Button>}
        <Button variant="secondary" onClick={onClose}>Close</Button>
      </>}>
      <QueryState isLoading={q.isLoading} error={q.error} data={v} retry={() => q.refetch()}>
        {(vv) => (
          <div className="space-y-4">
            <dl className="grid gap-x-6 sm:grid-cols-2">
              <KV k="Type" v={VOUCHER_TYPE_LABEL[vv.type] ?? vv.type} />
              <KV k="Date" v={fmtDate(vv.date)} />
              <KV k="Source" v={<span>{vv.source}{link && <> · <Link href={link} className="link">open document</Link></>}</span>} />
              <KV k="Reference" v={vv.source_ref || '—'} />
              {vv.reversal_of && <KV k="Reversal of" v={jump(vv.reversal_of)} />}
              {vv.reversed_by && <KV k="Reversed by" v={jump(vv.reversed_by)} />}
              <KV k="Posted" v={fmtDate(vv.created_at, true)} />
            </dl>
            {vv.narration && <p className="rounded-xl bg-slate-50 px-4 py-3 text-sm">{vv.narration}</p>}
            <div className="overflow-x-auto rounded-xl border border-slate-100">
              <table className="w-full min-w-[480px]">
                <thead className="bg-slate-50/60"><tr><th className="th">Account</th><th className="th text-right">Debit</th><th className="th text-right">Credit</th></tr></thead>
                <tbody className="divide-y divide-slate-100">
                  {(vv.entries ?? []).map((e) => (
                    <tr key={e.line_no}>
                      <td className="td">
                        <div className="font-medium">{e.party_id ? <Link href={`/app/parties/${e.party_id}`} className="hover:underline">{e.account_name}</Link> : e.account_name}</div>
                        <div className="font-mono text-[11px] text-slate-500">{e.account_code}</div>
                      </td>
                      <td className="td num text-right">{e.debit_paise ? <Money paise={e.debit_paise} /> : ''}</td>
                      <td className="td num text-right">{e.credit_paise ? <Money paise={e.credit_paise} /> : ''}</td>
                    </tr>
                  ))}
                </tbody>
                <tfoot className="bg-slate-50 font-bold"><tr><td className="td">Total</td><td className="td num text-right"><Money paise={vv.total_paise} /></td><td className="td num text-right"><Money paise={vv.total_paise} /></td></tr></tfoot>
              </table>
            </div>
            {!canReverse && vv.source !== 'manual' && vv.source !== 'opening' && canDo(PERMS.ledgerWrite) && vv.status === 'posted' && (
              <p className="text-xs text-slate-500">Vouchers posted from {vv.source} are reversed by cancelling the source document, not here.</p>
            )}
            {reversing && (
              <div className="space-y-3 rounded-xl border border-rose-100 bg-rose-50/50 p-4">
                <div className="text-sm font-bold text-rose-800">Reverse this voucher</div>
                <p className="text-xs text-rose-700">A mirror-image voucher is posted and this one is marked reversed. Books inside a locked period are refused by the server.</p>
                <div className="grid gap-3 sm:grid-cols-[1fr_auto]">
                  <Field label="Reason"><Textarea rows={2} value={reason} onChange={(e) => setReason(e.target.value)} placeholder="Why is this entry wrong?" /></Field>
                  <Field label="Reversal date"><Input type="date" value={date} max={todayIST()} onChange={(e) => setDate(e.target.value)} /></Field>
                </div>
                <div className="flex justify-end gap-2">
                  <Button variant="secondary" size="sm" onClick={() => setReversing(false)}>Cancel</Button>
                  <Button variant="danger" size="sm" loading={reverse.isPending} disabled={!reason.trim()} onClick={() => reverse.mutate({ id: vv.id, body: { reason: reason.trim(), date } })}>Post reversal</Button>
                </div>
              </div>
            )}
          </div>
        )}
      </QueryState>
    </Modal>
  )
}
