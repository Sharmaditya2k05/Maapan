'use client'

import { useMemo, useState } from 'react'
import Link from 'next/link'
import { useQueries } from '@tanstack/react-query'
import { AlertTriangle, ArrowRight, BookOpen, Hourglass, Landmark, ListTree, Lock, LockOpen, Scale, TrendingUp, Wallet, type LucideIcon } from 'lucide-react'
import { Badge, Button, Card, Field, Input, Modal, PageHeader } from '@/components/ui'
import { api, get } from '@/lib/api'
import { fmtDate, todayIST, useGet, usePerm } from '@/lib/hooks'
import { PERMS, type Perm } from '@/lib/rbac'
import type { LedgerSummary, PeriodLock } from '@/lib/types.ledger'
import { fyLabel, fyMonths, fyYearOf } from '@/lib/ledger/util'
import { MonthlyChart, type MonthPoint } from '@/components/ledger/MonthlyChart'
import { NoPermission } from '@/components/ledger/bits'
import { useAction } from '@/components/ledger/useAction'

interface ReportCard { href: string; title: string; text: string; icon: LucideIcon; perm: Perm }

const CARDS: ReportCard[] = [
  { href: '/app/reports/daybook', title: 'Day book', text: 'Every voucher posted on a day, with its entries.', icon: BookOpen, perm: PERMS.reportRead },
  { href: '/app/reports/trial-balance', title: 'Trial balance', text: 'Closing balance of every account; debits must equal credits.', icon: Scale, perm: PERMS.reportRead },
  { href: '/app/reports/profit-loss', title: 'Profit & loss', text: 'Gross profit after cost of goods sold, then net profit.', icon: TrendingUp, perm: PERMS.reportRead },
  { href: '/app/reports/balance-sheet', title: 'Balance sheet', text: 'Assets against liabilities and capital as of a date.', icon: Landmark, perm: PERMS.reportRead },
  { href: '/app/reports/aging', title: 'Receivables & payables aging', text: 'Who owes what, and for how long — 0-30 / 31-60 / 61-90 / 90+ days.', icon: Hourglass, perm: PERMS.reportRead },
  { href: '/app/reports/cash-flow', title: 'Cash flow', text: 'Money in and out of cash and bank by voucher type.', icon: Wallet, perm: PERMS.reportRead },
  { href: '/app/reports/accounts', title: 'Chart of accounts', text: 'The account tree with balances; drill into any ledger.', icon: ListTree, perm: PERMS.ledgerRead },
  { href: '/app/reports/quarantine', title: 'Quarantined events', text: 'Documents the ledger could not post, waiting for review and retry.', icon: AlertTriangle, perm: PERMS.ledgerWrite },
]

export default function ReportsHub() {
  const canDo = usePerm()
  const today = todayIST()
  const fy = fyYearOf(today)
  const months = useMemo(() => fyMonths(fy, today), [fy, today])
  const canReport = canDo(PERMS.reportRead)
  const results = useQueries({
    queries: months.map((m) => ({
      queryKey: ['/bahi/v1/reports/summary', { from: m.from, to: m.to }],
      queryFn: () => get<LedgerSummary>('/bahi/v1/reports/summary', { from: m.from, to: m.to }),
      enabled: canReport,
      staleTime: 5 * 60_000,
    })),
  })
  const points: MonthPoint[] = months.map((m, i) => ({ label: m.label.slice(0, 3), sales_paise: results[i].data?.sales_paise ?? 0, purchases_paise: results[i].data?.purchases_paise ?? 0 }))
  const loading = results.some((r) => r.isLoading)
  const failed = results.filter((r) => r.error).length
  const cards = CARDS.filter((c) => canDo(c.perm))

  if (!canReport && !canDo(PERMS.ledgerRead)) return <><PageHeader title="Reports" /><Card><NoPermission what="reports" /></Card></>

  return (
    <>
      <PageHeader title="Reports" subtitle={`Financial year ${fyLabel(fy)}`} />
      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_320px]">
        {canReport && (
          <Card title="Sales vs purchases by month" actions={failed > 0 ? <Badge tone="amber">{failed} month{failed > 1 ? 's' : ''} failed to load</Badge> : undefined}>
            <MonthlyChart data={points} loading={loading} />
          </Card>
        )}
        <PeriodLockCard />
      </div>
      <div className="mt-4 grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        {cards.map((c) => (
          <Link key={c.href} href={c.href} className="card group flex items-start gap-3 p-4 transition hover:border-brand-300 hover:shadow-md">
            <div className="grid h-10 w-10 shrink-0 place-items-center rounded-xl bg-brand-50 text-brand-700"><c.icon size={18} /></div>
            <div className="min-w-0 flex-1">
              <div className="flex items-center justify-between gap-2"><div className="font-bold">{c.title}</div><ArrowRight size={14} className="text-slate-300 transition group-hover:translate-x-0.5 group-hover:text-brand-600" /></div>
              <p className="mt-0.5 text-xs text-slate-500">{c.text}</p>
            </div>
          </Link>
        ))}
      </div>
    </>
  )
}

/** Period lock: everyone with ledger:read sees it; settings:manage can move it. */
function PeriodLockCard() {
  const canDo = usePerm()
  const q = useGet<PeriodLock>(canDo(PERMS.ledgerRead) ? '/bahi/v1/settings/period-lock' : null)
  const [open, setOpen] = useState(false)
  const [date, setDate] = useState('')
  const save = useAction<PeriodLock, string>((locked_until) => api<PeriodLock>('/bahi/v1/settings/period-lock', { method: 'PUT', body: { locked_until } }), {
    invalidate: ['/bahi/v1/settings/period-lock'], successMessage: 'Period lock updated', onSuccess: () => setOpen(false),
  })
  if (!canDo(PERMS.ledgerRead)) return null
  const locked = q.data?.locked_until
  return (
    <Card title="Books lock" actions={canDo(PERMS.settingsManage) ? <Button size="sm" variant="secondary" onClick={() => { setDate(locked || ''); setOpen(true) }}>Change</Button> : undefined}>
      <div className="flex items-start gap-3">
        <div className={`grid h-10 w-10 shrink-0 place-items-center rounded-xl ${locked ? 'bg-amber-50 text-amber-700' : 'bg-emerald-50 text-emerald-700'}`}>{locked ? <Lock size={18} /> : <LockOpen size={18} />}</div>
        <div className="text-sm">
          <div className="font-bold">{q.isLoading ? 'Checking…' : locked ? `Locked up to ${fmtDate(locked)}` : 'Books are open'}</div>
          <p className="mt-0.5 text-xs text-slate-500">{locked ? 'Nothing can be posted on or before this date. Events dated inside the lock are quarantined.' : 'Lock closed months so nothing is posted into them by mistake.'}</p>
          {q.data?.locked_at && <p className="mt-1 text-[11px] text-slate-400">Set {fmtDate(q.data.locked_at, true)}</p>}
        </div>
      </div>
      <Modal open={open} onClose={() => setOpen(false)} title="Lock the books"
        footer={<>
          {locked && <Button variant="ghost" onClick={() => save.mutate('')} loading={save.isPending}>Unlock</Button>}
          <Button variant="secondary" onClick={() => setOpen(false)}>Cancel</Button>
          <Button onClick={() => save.mutate(date)} loading={save.isPending} disabled={!date}>Lock up to {date ? fmtDate(date) : '…'}</Button>
        </>}>
        <Field label="Locked until (inclusive)" hint="Cannot be in the future. Clear the lock before importing into a closed year."><Input type="date" value={date} max={todayIST()} onChange={(e) => setDate(e.target.value)} /></Field>
      </Modal>
    </Card>
  )
}
