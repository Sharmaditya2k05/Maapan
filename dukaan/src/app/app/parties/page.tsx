'use client'

import { Suspense, useState } from 'react'
import Link from 'next/link'
import { useRouter } from 'next/navigation'
import { ArrowDownLeft, ArrowUpRight, Download, Plus, Users } from 'lucide-react'
import { Avatar, Badge, Button, Card, Empty, Money, PageHeader, Pagination, QueryState, SearchInput, Skeleton, Table, Tabs, Toggle } from '@/components/ui'
import { downloadFile, useDebounced, useGet, useList, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import { STATES } from '@/lib/gst'
import type { Party } from '@/lib/types'
import type { LedgerSummary } from '@/lib/types.ledger'
import { csvRupees, toCSV } from '@/lib/ledger/csv'
import { balanceLabel } from '@/lib/ledger/util'
import { BalancePhrase, MiniStat, MobileRow, NoPermission, useTabParam } from '@/components/ledger/bits'
import { PartyModal } from '@/components/ledger/PartyModal'

const TABS = ['customers', 'suppliers', 'all'] as const
type Tab = (typeof TABS)[number]
const LIMIT = 50

export default function PartiesPage() {
  return <Suspense fallback={<Skeleton />}><PartiesInner /></Suspense>
}

function PartiesInner() {
  const canDo = usePerm()
  const router = useRouter()
  const [tab, setTab] = useTabParam<Tab>(TABS, 'customers')
  const [q, setQ] = useState('')
  const dq = useDebounced(q)
  const [archived, setArchived] = useState(false)
  const [offset, setOffset] = useState(0)
  const [modal, setModal] = useState<{ open: boolean; name?: string }>({ open: false })

  const type = tab === 'customers' ? 'customer' : tab === 'suppliers' ? 'supplier' : undefined
  const list = useList<Party>(canDo(PERMS.partyManage) ? '/bahi/v1/parties' : null, { q: dq, type, include_archived: archived ? true : undefined, limit: LIMIT, offset })
  const summary = useGet<LedgerSummary>(canDo(PERMS.reportRead) ? '/bahi/v1/reports/summary' : null)

  const change = (t: Tab) => { setTab(t); setOffset(0) }

  const exportCSV = () => {
    const items = list.data?.items ?? []
    downloadFile('parties.csv', toCSV(['Name', 'Type', 'Phone', 'Email', 'GSTIN', 'State', 'Balance (₹, + to collect)', 'Credit limit', 'Credit days', 'Archived'],
      items.map((p) => [p.name, p.type, p.phone, p.email, p.gstin, STATES[p.state_code] ?? p.state_code, csvRupees(p.balance_paise), csvRupees(p.credit_limit_paise), p.credit_days, p.archived ? 'yes' : ''])), 'text/csv')
  }

  if (!canDo(PERMS.partyManage)) return <><PageHeader title="Parties & Ledgers" /><Card><NoPermission what="parties" /></Card></>

  return (
    <>
      <PageHeader title="Parties & Ledgers" subtitle="Customers and suppliers with live balances"
        actions={<>
          <Button variant="secondary" icon={Download} onClick={exportCSV} disabled={!list.data?.items.length}>CSV</Button>
          <Button icon={Plus} onClick={() => setModal({ open: true })}>Add party</Button>
        </>} />

      {summary.data && (
        <div className="mb-4 grid grid-cols-2 gap-3 sm:gap-4">
          <MiniStat label="To collect from customers" value={<Money paise={summary.data.receivables_paise} />} tone="green" icon={ArrowDownLeft} />
          <MiniStat label="To pay suppliers" value={<Money paise={summary.data.payables_paise} />} tone="red" icon={ArrowUpRight} />
        </div>
      )}

      <Card padded={false}>
        <div className="flex flex-col gap-3 border-b border-slate-100 p-3 sm:p-4 md:flex-row md:items-center md:justify-between">
          <Tabs value={tab} onChange={change} options={[{ value: 'customers', label: 'Customers' }, { value: 'suppliers', label: 'Suppliers' }, { value: 'all', label: 'All' }]} />
          <div className="flex flex-wrap items-center gap-3">
            <Toggle checked={archived} onChange={(v) => { setArchived(v); setOffset(0) }} label={<span className="text-slate-600">Show archived</span>} />
            <SearchInput value={q} onChange={(v) => { setQ(v); setOffset(0) }} placeholder="Name, phone, GSTIN" />
          </div>
        </div>
        <QueryState isLoading={list.isLoading} error={list.error} data={list.data} retry={() => list.refetch()}>
          {(d) => d.items.length === 0 ? (
            <Empty icon={Users} title={dq ? 'No party matches' : `No ${tab === 'all' ? 'parties' : tab} yet`} text="Add parties to bill them on credit and track what they owe."
              action={<Button icon={Plus} onClick={() => setModal({ open: true, name: dq })}>Add {dq ? `“${dq}”` : 'party'}</Button>} />
          ) : (
            <>
              <div className="divide-y divide-slate-100 md:hidden">
                {d.items.map((p) => {
                  const b = balanceLabel(p.balance_paise)
                  return (
                    <MobileRow key={p.id} onClick={() => router.push(`/app/parties/${p.id}`)} left={<Avatar name={p.name} />}
                      title={<span className="flex items-center gap-1.5">{p.name}{p.archived && <Badge tone="gray">archived</Badge>}</span>}
                      subtitle={[p.phone, p.gstin || 'Unregistered', STATES[p.state_code]].filter(Boolean).join(' · ')}
                      right={<span className={`num ${b.tone === 'green' ? 'text-emerald-700' : b.tone === 'red' ? 'text-rose-700' : 'text-slate-400'}`}>{b.direction === 'settled' ? '—' : <Money paise={Math.abs(p.balance_paise)} />}</span>}
                      rightSub={b.direction === 'settled' ? 'settled' : b.direction === 'collect' ? 'to collect' : 'to pay'} />
                  )
                })}
              </div>
              <div className="hidden md:block">
                <Table>
                  <thead className="bg-slate-50/60">
                    <tr><th className="th">Name</th><th className="th">Phone</th><th className="th">GSTIN</th><th className="th">State</th><th className="th text-right">Balance</th><th className="th text-right">Credit limit</th></tr>
                  </thead>
                  <tbody className="divide-y divide-slate-100">
                    {d.items.map((p) => (
                      <tr key={p.id} onClick={() => router.push(`/app/parties/${p.id}`)} className="cursor-pointer hover:bg-brand-50/40">
                        <td className="td">
                          <div className="flex items-center gap-3">
                            <Avatar name={p.name} className="h-9 w-9 text-xs" />
                            <div className="min-w-0">
                              <Link href={`/app/parties/${p.id}`} className="font-semibold hover:underline" onClick={(e) => e.stopPropagation()}>{p.name}</Link>
                              <div className="flex items-center gap-1.5 text-xs text-slate-500"><span className="capitalize">{p.type}</span>{p.archived && <Badge tone="gray">archived</Badge>}</div>
                            </div>
                          </div>
                        </td>
                        <td className="td text-slate-600">{p.phone || '—'}</td>
                        <td className="td font-mono text-xs text-slate-600">{p.gstin ? <span className="flex items-center gap-1.5">{p.gstin}{p.gst_verified && <Badge tone="green">verified</Badge>}</span> : <span className="font-sans text-slate-400">Unregistered</span>}</td>
                        <td className="td text-slate-600">{STATES[p.state_code] ?? p.state_code ?? '—'}</td>
                        <td className="td text-right"><BalancePhrase paise={p.balance_paise} /></td>
                        <td className="td num text-right text-slate-600">{p.credit_limit_paise ? <Money paise={p.credit_limit_paise} /> : <span className="text-slate-400">—</span>}</td>
                      </tr>
                    ))}
                  </tbody>
                </Table>
              </div>
              <Pagination total={d.total} limit={d.limit} offset={d.offset} onChange={setOffset} />
            </>
          )}
        </QueryState>
      </Card>

      <PartyModal open={modal.open} onClose={() => setModal({ open: false })} defaultType={type ?? 'customer'} defaultName={modal.name ?? ''}
        onSaved={(p) => router.push(`/app/parties/${p.id}`)} />
    </>
  )
}
