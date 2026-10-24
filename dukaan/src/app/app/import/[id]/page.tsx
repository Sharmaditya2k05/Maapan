'use client'

import Link from 'next/link'
import { useParams, useRouter } from 'next/navigation'
import { ArrowRight, Upload } from 'lucide-react'
import { Button, Card, Empty, PageHeader, QueryState } from '@/components/ui'
import { fmtDate, useGet, usePerm, useStore } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import type { ImportJob } from '@/lib/types.more'
import { Counts, DownloadErrors, ErrorRows, JobBadge, Recon } from '@/components/import/bits'
import { fmtBytes, isJobBusy, kindLabel, kindMeta } from '@/components/import/kinds'

export default function ImportDetailPage() {
  const { id } = useParams<{ id: string }>()
  const router = useRouter()
  const canDo = usePerm()
  const { stores } = useStore()
  const job = useGet<ImportJob>(canDo(PERMS.importRun) ? `/aayaat/v1/imports/${id}` : null, undefined, {
    refetchInterval: (q) => (isJobBusy(q.state.data?.status) ? 2000 : false),
  })
  if (!canDo(PERMS.importRun)) return <Empty icon={Upload} title="Import data" text="Owners and accountants can view imports." />

  return (
    <QueryState isLoading={job.isLoading} error={job.error} data={job.data} retry={() => job.refetch()}>
      {(j) => {
        const meta = kindMeta(j.kind)
        const open = j.status !== 'completed'
        const errors = j.error_rows + j.failed
        return (
          <>
            <PageHeader back={() => router.push('/app/import')}
              title={<span className="inline-flex flex-wrap items-center gap-2">{j.file_name} <JobBadge status={j.status} /></span>}
              subtitle={`${kindLabel(j.kind)} · ${fmtBytes(j.file_size)} · uploaded ${fmtDate(j.created_at, true)}${j.finished_at ? ` · finished ${fmtDate(j.finished_at, true)}` : ''}`}
              actions={open && (
                <Link href={`/app/import/new?job=${j.id}`}><Button icon={ArrowRight}>{isJobBusy(j.status) ? 'Watch progress' : j.status === 'failed' ? 'Resume' : 'Continue'}</Button></Link>
              )} />
            <div className="space-y-4">
              <Counts job={j} />
              {j.error && <div className="rounded-xl bg-rose-50 px-4 py-3 text-sm text-rose-700">{j.error}</div>}
              {j.reconciliation && <Recon r={j.reconciliation} />}
              <div className="grid gap-4 lg:grid-cols-3">
                <Card title="Settings">
                  <dl className="space-y-2 text-sm">
                    <Row k="Goes to" v={meta?.goesTo ?? '—'} />
                    <Row k="Sheet" v={j.sheet || '—'} />
                    <Row k="Header row" v={j.header_row ? String(j.header_row) : 'auto'} />
                    {meta?.asOf && <Row k="As of" v={j.params.as_of ? fmtDate(j.params.as_of) : 'default (1 April)'} />}
                    {meta?.store && <Row k="Store" v={stores.find((s) => s.id === j.params.store_id)?.name ?? j.params.store_id ?? '—'} />}
                    {j.params.notify_phone && <Row k="Notify" v={j.params.notify_phone} />}
                  </dl>
                </Card>
                <Card title="Column mapping" className="lg:col-span-2">
                  {Object.keys(j.mapping).length === 0 ? <p className="text-sm text-slate-500">Not mapped yet.</p> : (
                    <dl className="grid gap-x-6 gap-y-1 text-sm sm:grid-cols-2">
                      {Object.entries(j.mapping).map(([k, v]) => <div key={k} className="flex justify-between gap-3 border-b border-slate-50 py-1"><dt className="text-slate-500">{k}</dt><dd className="truncate font-medium">{v}</dd></div>)}
                    </dl>
                  )}
                </Card>
              </div>
              {errors > 0 && (
                <Card title={`${errors.toLocaleString('en-IN')} rows not imported`} padded={false} actions={<DownloadErrors jobId={j.id} count={errors} />}>
                  <ErrorRows jobId={j.id} />
                </Card>
              )}
            </div>
          </>
        )
      }}
    </QueryState>
  )
}

function Row({ k, v }: { k: string; v: string }) {
  return <div className="flex justify-between gap-3"><dt className="text-slate-500">{k}</dt><dd className="truncate font-medium">{v}</dd></div>
}
