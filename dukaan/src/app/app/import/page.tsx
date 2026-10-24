'use client'

import { useState } from 'react'
import Link from 'next/link'
import { Plus, Upload } from 'lucide-react'
import { Button, Card, Empty, PageHeader, Pagination, QueryState, Select, Table } from '@/components/ui'
import { fmtDate, useList, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import type { ImportJob, ImportKind } from '@/lib/types.more'
import { JobBadge } from '@/components/import/bits'
import { IMPORT_KINDS, fmtBytes, isJobBusy, kindLabel } from '@/components/import/kinds'

export default function ImportPage() {
  const canDo = usePerm()
  const [kind, setKind] = useState<'' | ImportKind>('')
  const [offset, setOffset] = useState(0)
  const list = useList<ImportJob>(canDo(PERMS.importRun) ? '/aayaat/v1/imports' : null, { kind, limit: 25, offset }, {
    refetchInterval: (q) => (q.state.data?.items.some((j) => isJobBusy(j.status)) ? 3000 : false),
  })

  if (!canDo(PERMS.importRun)) return <Empty icon={Upload} title="Import data" text="Owners and accountants can import parties, items and opening balances from Excel." />

  return (
    <>
      <PageHeader title="Import data" subtitle="Bring parties, items, opening balances and stock in from Excel or CSV"
        actions={<Link href="/app/import/new"><Button icon={Plus}>New import</Button></Link>} />
      <Card padded={false}>
        <div className="flex flex-wrap gap-2 p-4">
          <Select className="w-auto" value={kind} onChange={(e) => { setKind(e.target.value as '' | ImportKind); setOffset(0) }} aria-label="Filter by kind">
            <option value="">All kinds</option>
            {IMPORT_KINDS.map((k) => <option key={k.kind} value={k.kind}>{k.label}</option>)}
          </Select>
        </div>
        <QueryState isLoading={list.isLoading} error={list.error} data={list.data} retry={() => list.refetch()}>
          {(d) => d.items.length === 0 ? (
            <Empty icon={Upload} title="No imports yet" text="Start with parties, then items, then opening balances and stock."
              action={<Link href="/app/import/new"><Button icon={Plus}>Start an import</Button></Link>} />
          ) : (
            <>
              <ul className="divide-y divide-slate-100 md:hidden">
                {d.items.map((j) => (
                  <li key={j.id}>
                    <Link href={`/app/import/${j.id}`} className="flex items-center gap-3 px-4 py-3 hover:bg-slate-50">
                      <div className="min-w-0 flex-1">
                        <div className="truncate text-sm font-semibold">{j.file_name}</div>
                        <div className="text-xs text-slate-500">{kindLabel(j.kind)} · {fmtDate(j.created_at, true)} · {j.imported}/{j.total_rows} rows</div>
                      </div>
                      <JobBadge status={j.status} />
                    </Link>
                  </li>
                ))}
              </ul>
              <div className="hidden md:block">
                <Table>
                  <thead><tr><th className="th">File</th><th className="th">Kind</th><th className="th">Status</th><th className="th text-right">Rows</th><th className="th text-right">Imported</th><th className="th text-right">Errors</th><th className="th">Started</th></tr></thead>
                  <tbody>
                    {d.items.map((j) => (
                      <tr key={j.id} className="border-t border-slate-100 hover:bg-slate-50">
                        <td className="td"><Link href={`/app/import/${j.id}`} className="font-semibold text-ink hover:underline">{j.file_name}</Link><div className="text-xs text-slate-500">{fmtBytes(j.file_size)}</div></td>
                        <td className="td">{kindLabel(j.kind)}</td>
                        <td className="td"><JobBadge status={j.status} /></td>
                        <td className="td num text-right">{j.total_rows.toLocaleString('en-IN')}</td>
                        <td className="td num text-right text-emerald-700">{j.imported.toLocaleString('en-IN')}</td>
                        <td className={`td num text-right ${j.error_rows + j.failed ? 'text-rose-700' : 'text-slate-400'}`}>{(j.error_rows + j.failed).toLocaleString('en-IN')}</td>
                        <td className="td text-slate-500">{fmtDate(j.created_at, true)}</td>
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
    </>
  )
}
