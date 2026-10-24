'use client'

import { useState } from 'react'
import { Lock, LockOpen } from 'lucide-react'
import { Badge, Button, Card, Confirm, ErrorBox, Field, Input, QueryState, useToast } from '@/components/ui'
import { fmtDate, todayIST, useGet, usePerm, useSave } from '@/lib/hooks'
import { errorMessage } from '@/lib/api'
import { PERMS } from '@/lib/rbac'
import type { PeriodLock } from '@/lib/types.stock'

function LockForm({ lock }: { lock: PeriodLock }) {
  const canDo = usePerm()
  const toast = useToast()
  const [date, setDate] = useState(lock.locked_until)
  const [err, setErr] = useState('')
  const [confirmClear, setConfirmClear] = useState(false)
  const save = useSave<PeriodLock, { locked_until: string }>('/bahi/v1/settings/period-lock', { method: 'PUT', invalidate: ['/bahi/v1/settings/period-lock'], silent: true })
  const canEdit = canDo(PERMS.settingsManage)
  const today = todayIST()

  const apply = async (until: string) => {
    setErr('')
    if (until && until > today) return setErr('The lock date cannot be in the future.')
    try {
      await save.mutateAsync({ locked_until: until })
      toast(until ? `Books locked up to ${fmtDate(until)}` : 'Books unlocked')
      setConfirmClear(false)
      if (!until) setDate('')
    } catch (e) { setErr(errorMessage(e)) }
  }

  return (
    <>
      <div className="mb-5 flex items-start gap-3 rounded-xl bg-slate-50 px-4 py-3 text-sm">
        {lock.locked_until ? <Lock size={18} className="mt-0.5 shrink-0 text-amber-600" /> : <LockOpen size={18} className="mt-0.5 shrink-0 text-emerald-600" />}
        <div>
          {lock.locked_until ? <><span className="font-semibold">Locked up to {fmtDate(lock.locked_until)}</span>{lock.locked_at && <span className="text-slate-500"> · set {fmtDate(lock.locked_at, true)}{lock.locked_by ? ` by ${lock.locked_by.slice(0, 8)}` : ''}</span>}</> : <span className="font-semibold">Books are open</span>}
          <p className="mt-1 text-slate-600">A lock rejects every voucher dated on or before that day: sales and purchase bills, credit and debit notes, receipts and payments, journal entries, and the stock and COGS postings that follow them. Anything a service tries to post into the locked period is quarantined for review instead of silently changing a filed return. Use it after filing GSTR-3B for the month, or after closing the financial year.</p>
        </div>
      </div>
      <div className="flex flex-wrap items-end gap-3">
        <Field label="Lock books up to and including" hint="Today or earlier"><Input type="date" value={date} max={today} onChange={(e) => setDate(e.target.value)} disabled={!canEdit} className="w-48" /></Field>
        {canEdit && <Button icon={Lock} onClick={() => apply(date)} loading={save.isPending} disabled={!date || date === lock.locked_until}>Lock</Button>}
        {canEdit && lock.locked_until && <Button variant="ghost" icon={LockOpen} onClick={() => setConfirmClear(true)} disabled={save.isPending}>Unlock</Button>}
        {lock.locked_until && date && date < lock.locked_until && <Badge tone="amber">moving the lock earlier re-opens {fmtDate(date)} – {fmtDate(lock.locked_until)}</Badge>}
      </div>
      {err && <div className="mt-4"><ErrorBox message={err} /></div>}
      <Confirm open={confirmClear} onClose={() => setConfirmClear(false)} onConfirm={() => apply('')} loading={save.isPending} danger confirmLabel="Unlock"
        title="Unlock the books?" text="Back-dated entries into the previously locked period become possible again, which can change returns you have already filed." />
    </>
  )
}

export function PeriodLockSection() {
  const lock = useGet<PeriodLock>('/bahi/v1/settings/period-lock')
  return (
    <Card title="Period lock">
      <QueryState isLoading={lock.isLoading} error={lock.error} data={lock.data} retry={() => lock.refetch()}>
        {(l) => <LockForm key={l.locked_until} lock={l} />}
      </QueryState>
    </Card>
  )
}
