'use client'

/** Cash book / bank book tab: bahi GET /v1/cashbook | /v1/bankbook with an optional account filter. */
import { useState } from 'react'
import { QueryState, Select } from '@/components/ui'
import { downloadFile, todayIST, useGet } from '@/lib/hooks'
import type { Statement } from '@/lib/types'
import { csvFileName, toCSV } from '@/lib/ledger/csv'
import { presetRange, type DateRange } from '@/lib/ledger/util'
import { useAccounts } from '../AccountPicker'
import { ReportShell } from '../ReportShell'
import { StatementTable, statementCSV } from '../StatementTable'

export function MoneyBook({ kind }: { kind: 'cash' | 'bank' }) {
  const [range, setRange] = useState<DateRange>(() => presetRange('this_month', todayIST()))
  const [accountId, setAccountId] = useState('')
  const { accounts } = useAccounts()
  const options = accounts.filter((a) => (kind === 'cash' ? a.is_cash : a.is_bank))
  const title = kind === 'cash' ? 'Cash book' : 'Bank book'
  const q = useGet<Statement>(`/bahi/v1/${kind}book`, { from: range.from, to: range.to, account_id: accountId || undefined })
  const onCSV = () => { if (!q.data) return; const c = statementCSV(q.data); downloadFile(csvFileName(title, `${range.from}-${range.to}`), toCSV(c.header, c.rows), 'text/csv') }
  return (
    <ReportShell embedded title={title} subtitle={kind === 'cash' ? 'Every rupee through the cash drawer(s)' : 'Bank accounts, including gateway settlements'} range={{ value: range, onChange: setRange }} onCSV={q.data ? onCSV : undefined}
      filters={options.length > 1 ? (
        <Select value={accountId} onChange={(e) => setAccountId(e.target.value)} className="w-auto input-sm" aria-label={`${title} account`}>
          <option value="">All {kind} accounts</option>
          {options.map((a) => <option key={a.id} value={a.id}>{a.code} · {a.name}</option>)}
        </Select>
      ) : undefined}>
      <QueryState isLoading={q.isLoading} error={q.error} data={q.data} retry={() => q.refetch()}>
        {(st) => <StatementTable statement={st} emptyText={`No ${kind} movements in this period.`} />}
      </QueryState>
    </ReportShell>
  )
}
