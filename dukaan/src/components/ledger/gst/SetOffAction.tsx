'use client'

/**
 * GSTR-3B → books: posts the month's Rule 88A set-off journal (and optionally the PMT-06 cash deposit) in bahi
 * (POST /v1/gst/setoff). The computation is not sent: bahi fetches kar's GSTR-3B with the caller's token, so
 * the books use exactly what kar computed. GET /v1/gst/setoffs tells whether the period is already done.
 */
import { useState } from 'react'
import Link from 'next/link'
import { BookCheck, CheckCircle2 } from 'lucide-react'
import { Button, Card, Field, Input, Modal, Select, Toggle } from '@/components/ui'
import { post } from '@/lib/api'
import { fmtDate, todayIST, useGet, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import type { SetOffInput, SetOffRecord, SetOffResult } from '@/lib/types.phase4'
import { useAccounts } from '../AccountPicker'
import { voucherHref } from '../StatementTable'
import { WarningList } from '../bits'
import { useAction } from '../useAction'

export function SetOffAction({ gstin, period, cashTotal }: { gstin: string; period: string; cashTotal: number }) {
  const canDo = usePerm()
  const canPost = canDo(PERMS.ledgerWrite) && canDo(PERMS.gstFile)
  const list = useGet<{ items: SetOffRecord[] }>(canDo(PERMS.ledgerRead) ? '/bahi/v1/gst/setoffs' : null)
  const done = (list.data?.items ?? []).find((s) => s.period === period && (!gstin || s.gstin === gstin))
  const [open, setOpen] = useState(false)
  if (!canDo(PERMS.ledgerRead)) return null
  return (
    <Card>
      <div className="flex flex-wrap items-center gap-3">
        <BookCheck size={18} className="text-brand-700" />
        <div className="min-w-0 flex-1 text-sm">
          {done ? (
            <span className="flex flex-wrap items-center gap-2 text-emerald-700"><CheckCircle2 size={15} /> Set off in the books on {fmtDate(done.created_at, true)}
              {done.setoff_voucher_id && <Link href={voucherHref(done.setoff_voucher_id)} className="link text-xs">set-off journal</Link>}
              {done.cash_voucher_id && <Link href={voucherHref(done.cash_voucher_id)} className="link text-xs">PMT-06 deposit</Link>}</span>
          ) : (
            <><div className="font-semibold">Post this set-off to the books</div><p className="text-xs text-slate-500">Moves the used ITC against output tax and, optionally, books the cash deposit to the electronic cash ledger. Once per GSTIN and month.</p></>
          )}
        </div>
        {!done && canPost && <Button size="sm" onClick={() => setOpen(true)}>Post set-off</Button>}
      </div>
      {open && <SetOffModal gstin={gstin} period={period} cashTotal={cashTotal} onClose={() => setOpen(false)} />}
    </Card>
  )
}

function SetOffModal({ gstin, period, cashTotal, onClose }: { gstin: string; period: string; cashTotal: number; onClose: () => void }) {
  const { accounts } = useAccounts()
  const banks = accounts.filter((a) => a.is_bank)
  const [date, setDate] = useState(todayIST())
  const [pay, setPay] = useState(cashTotal > 0)
  const [bankId, setBankId] = useState('')
  const bank = bankId || banks[0]?.id || ''
  const save = useAction<SetOffResult, SetOffInput>((b) => post('/bahi/v1/gst/setoff', b), {
    invalidate: ['/bahi/v1/gst', '/bahi/v1/vouchers', '/bahi/v1/reports', '/bahi/v1/accounts', '/bahi/v1/bankbook'], successMessage: 'Set-off posted',
  })
  const r = save.data
  return (
    <Modal open onClose={onClose} title={`Set-off ${period.slice(0, 2)}/${period.slice(2)} · ${gstin || 'primary GSTIN'}`}
      footer={r ? <Button onClick={onClose}>Done</Button> : <><Button variant="secondary" onClick={onClose}>Cancel</Button>
        <Button loading={save.isPending} onClick={() => save.mutate({ gstin: gstin || undefined, period, date, ...(pay && bank ? { pay_from_bank_account_id: bank } : {}) })}>Post</Button></>}>
      {r ? (
        <div className="space-y-3 text-sm">
          <p className="font-semibold text-emerald-700">Posted.</p>
          {r.setoff_voucher && <p>Set-off journal <Link href={voucherHref(r.setoff_voucher.id)} className="link font-mono">{r.setoff_voucher.number}</Link></p>}
          {r.cash_voucher && <p>PMT-06 deposit <Link href={voucherHref(r.cash_voucher.id)} className="link font-mono">{r.cash_voucher.number}</Link></p>}
          <WarningList items={r.warnings ?? undefined} />
        </div>
      ) : (
        <div className="space-y-4">
          <Field label="Date" hint="Normally the GSTR-3B filing date"><Input type="date" value={date} max={todayIST()} onChange={(e) => setDate(e.target.value)} /></Field>
          <Toggle checked={pay} onChange={setPay} label="Also book the cash deposit (PMT-06): Dr Electronic Cash Ledger · Cr Bank" />
          {pay && <Field label="Paid from"><Select value={bank} onChange={(e) => setBankId(e.target.value)}>{banks.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}</Select></Field>}
          <p className="text-xs text-slate-500">Reverse-charge tax, interest and late fee are not part of the set-off; book their payment separately. A second set-off for the same month is refused.</p>
        </div>
      )}
    </Modal>
  )
}
