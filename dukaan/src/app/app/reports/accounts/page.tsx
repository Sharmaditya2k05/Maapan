'use client'

import { Suspense, useMemo, useState } from 'react'
import { ChevronDown, ChevronRight, ListTree } from 'lucide-react'
import { Badge, Card, Empty, Money, QueryState, Skeleton } from '@/components/ui'
import { downloadFile, todayIST, useGet, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import type { Statement } from '@/lib/types'
import type { Account } from '@/lib/types.ledger'
import { csvFileName, toCSV } from '@/lib/ledger/csv'
import { presetRange, type DateRange } from '@/lib/ledger/util'
import { GROUP_LABEL } from '@/components/ledger/AccountPicker'
import { ReportShell } from '@/components/ledger/ReportShell'
import { StatementTable, statementCSV } from '@/components/ledger/StatementTable'
import { NoPermission, useQueryParam } from '@/components/ledger/bits'

export default function AccountsPage() {
  return <Suspense fallback={<Skeleton />}><AccountsInner /></Suspense>
}

function AccountsInner() {
  const canDo = usePerm()
  const [selected, setSelected] = useQueryParam('account')
  const [range, setRange] = useState<DateRange>(() => presetRange('this_fy', todayIST()))
  const [open, setOpen] = useState<Record<string, boolean>>({})
  const tree = useGet<{ items: Account[] }>(canDo(PERMS.ledgerRead) ? '/bahi/v1/accounts' : null, { view: 'tree' })
  const flat = useMemo(() => { const out: Account[] = []; const walk = (as: Account[]) => as.forEach((a) => { out.push(a); if (a.children) walk(a.children) }); walk(tree.data?.items ?? []); return out }, [tree.data])
  const current = flat.find((a) => a.id === selected)
  const st = useGet<Statement>(selected && canDo(PERMS.ledgerRead) ? `/bahi/v1/accounts/${selected}/statement` : null, { from: range.from, to: range.to })
  const onCSV = () => { if (!st.data) return; const c = statementCSV(st.data); downloadFile(csvFileName(`ledger ${st.data.account_name}`, `${range.from}-${range.to}`), toCSV(c.header, c.rows), 'text/csv') }
  if (!canDo(PERMS.ledgerRead)) return <Card><NoPermission what="the ledger" /></Card>

  const toggle = (id: string) => setOpen((o) => ({ ...o, [id]: !(o[id] ?? true) }))
  const isOpen = (a: Account, depth: number) => open[a.id] ?? depth < 1

  return (
    <ReportShell title={current ? `${current.code} · ${current.name}` : 'Chart of accounts'} subtitle={current ? `${GROUP_LABEL[current.group] ?? current.group}${current.is_group ? ' · group (includes all children)' : ''}` : 'Pick an account to see its ledger'}
      range={current ? { value: range, onChange: setRange } : undefined} onCSV={st.data ? onCSV : undefined}>
      <div className="grid gap-4 lg:grid-cols-[360px_minmax(0,1fr)]">
        <Card padded={false} className="no-print max-h-[70vh] overflow-auto p-2">
          <QueryState isLoading={tree.isLoading} error={tree.error} data={tree.data} retry={() => tree.refetch()}>
            {(d) => d.items.length === 0 ? <Empty icon={ListTree} title="No accounts" /> : <ul>{d.items.map((a) => <TreeNode key={a.id} a={a} depth={0} selected={selected} isOpen={isOpen} toggle={toggle} onSelect={setSelected} />)}</ul>}
          </QueryState>
        </Card>
        <Card padded={false}>
          {!selected ? <Empty icon={ListTree} title="Select an account" text="Its running-balance ledger for the chosen period appears here." /> : (
            <QueryState isLoading={st.isLoading} error={st.error} data={st.data} retry={() => st.refetch()}>
              {(s) => <StatementTable statement={s} />}
            </QueryState>
          )}
        </Card>
      </div>
    </ReportShell>
  )
}

function TreeNode({ a, depth, selected, isOpen, toggle, onSelect }: {
  a: Account; depth: number; selected: string | null; isOpen: (a: Account, depth: number) => boolean; toggle: (id: string) => void; onSelect: (id: string) => void
}) {
  const expanded = isOpen(a, depth)
  const active = a.id === selected
  return (
    <li>
      <div className={`flex items-center gap-1 rounded-lg py-1 pr-2 text-sm ${active ? 'bg-brand-50 font-semibold text-brand-800' : 'hover:bg-slate-50'}`} style={{ paddingLeft: `${depth * 14 + 4}px` }}>
        {a.children?.length ? <button type="button" onClick={() => toggle(a.id)} aria-label={expanded ? `Collapse ${a.name}` : `Expand ${a.name}`} className="grid h-6 w-6 place-items-center rounded text-slate-400 hover:text-ink cursor-pointer">{expanded ? <ChevronDown size={14} /> : <ChevronRight size={14} />}</button> : <span className="w-6" />}
        <button type="button" onClick={() => onSelect(a.id)} className="flex min-w-0 flex-1 items-center gap-2 text-left cursor-pointer">
          <span className="font-mono text-[11px] text-slate-400">{a.code}</span>
          <span className="truncate">{a.name}</span>
          {a.is_group && <Badge tone="gray">group</Badge>}
        </button>
        <Money paise={a.balance_paise} className={`shrink-0 text-xs ${a.balance_paise === 0 ? 'text-slate-300' : 'text-slate-600'}`} />
      </div>
      {expanded && a.children && a.children.length > 0 && <ul>{a.children.map((c) => <TreeNode key={c.id} a={c} depth={depth + 1} selected={selected} isOpen={isOpen} toggle={toggle} onSelect={onSelect} />)}</ul>}
    </li>
  )
}
