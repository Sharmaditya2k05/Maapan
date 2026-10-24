'use client'

/** Small shared pieces of the import screens: stepper, status badge, counts, reconciliation, errors table. */
import { Download, Scale } from 'lucide-react'
import { Badge, Button, Empty, Money, Pagination, QueryState, Table, useToast } from '@/components/ui'
import { api, errorMessage } from '@/lib/api'
import { downloadFile, useList } from '@/lib/hooks'
import type { ImportJob, ImportReconciliation, ImportRow, ImportStatus } from '@/lib/types.more'
import { JOB_LABEL, STEPS, jobTone, type WizardStep } from './kinds'
import { useState } from 'react'

export function JobBadge({ status }: { status: ImportStatus }) { return <Badge tone={jobTone(status)}>{JOB_LABEL[status]}</Badge> }

export function Stepper({ current }: { current: WizardStep }) {
  const idx = STEPS.findIndex((s) => s.id === current)
  return (
    <ol className="mb-5 flex items-center gap-1 overflow-x-auto [scrollbar-width:none] sm:gap-2" aria-label="Import steps">
      {STEPS.map((s, i) => {
        const state = i < idx ? 'done' : i === idx ? 'current' : 'todo'
        return (
          <li key={s.id} className="flex shrink-0 items-center gap-1 sm:gap-2" aria-current={state === 'current' ? 'step' : undefined}>
            <span className={`grid h-7 w-7 place-items-center rounded-full text-xs font-bold ${state === 'done' ? 'bg-brand-500 text-white' : state === 'current' ? 'bg-brand-700 text-white ring-4 ring-brand-700/15' : 'bg-slate-200 text-slate-500'}`}>{i + 1}</span>
            <span className={`text-sm font-semibold ${state === 'current' ? 'text-ink' : 'text-slate-500'} ${state !== 'current' ? 'hidden md:inline' : ''}`}>{s.label}</span>
            {i < STEPS.length - 1 && <span className={`mx-1 h-px w-6 sm:w-10 ${i < idx ? 'bg-brand-500' : 'bg-slate-200'}`} aria-hidden />}
          </li>
        )
      })}
    </ol>
  )
}

export function Counts({ job }: { job: ImportJob }) {
  const total = Math.max(job.total_rows, 1)
  const pct = (n: number) => `${Math.round((100 * n) / total)}%`
  const cells: { label: string; value: number; tone: string }[] = [
    { label: 'Rows read', value: job.total_rows, tone: 'text-ink' },
    { label: 'Valid', value: job.valid_rows, tone: 'text-emerald-700' },
    { label: 'With errors', value: job.error_rows, tone: job.error_rows ? 'text-rose-700' : 'text-slate-500' },
    { label: 'Imported', value: job.imported, tone: 'text-brand-700' },
    { label: 'Rejected by server', value: job.failed, tone: job.failed ? 'text-rose-700' : 'text-slate-500' },
  ]
  return (
    <div>
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-5">
        {cells.map((c) => (
          <div key={c.label} className="card p-3">
            <div className="text-xs font-medium text-slate-500">{c.label}</div>
            <div className={`num mt-1 text-xl font-bold ${c.tone}`}>{c.value.toLocaleString('en-IN')}</div>
          </div>
        ))}
      </div>
      {job.total_rows > 0 && (
        <div className="mt-3 flex h-2 w-full overflow-hidden rounded-full bg-slate-100" role="img" aria-label={`${pct(job.valid_rows)} valid, ${pct(job.error_rows)} with errors`}>
          <div className="bg-emerald-500" style={{ width: pct(job.valid_rows) }} />
          <div className="bg-rose-400" style={{ width: pct(job.error_rows) }} />
        </div>
      )}
    </div>
  )
}

export function Recon({ r }: { r: ImportReconciliation }) {
  return (
    <div className={`rounded-xl border p-4 ${r.balanced ? 'border-emerald-200 bg-emerald-50' : 'border-amber-200 bg-amber-50'}`}>
      <div className="mb-2 flex items-center gap-2 text-sm font-bold"><Scale size={16} /> Dr / Cr reconciliation <Badge tone={r.balanced ? 'green' : 'amber'}>{r.balanced ? 'balanced' : 'difference'}</Badge></div>
      <dl className="grid grid-cols-3 gap-3 text-sm">
        <div><dt className="text-xs text-slate-500">Total Dr</dt><dd className="font-semibold"><Money paise={r.dr_total_paise} /></dd></div>
        <div><dt className="text-xs text-slate-500">Total Cr</dt><dd className="font-semibold"><Money paise={r.cr_total_paise} /></dd></div>
        <div><dt className="text-xs text-slate-500">Difference (Dr − Cr)</dt><dd className={`font-semibold ${r.balanced ? '' : 'text-amber-800'}`}><Money paise={r.difference_paise} signed /></dd></div>
      </dl>
      {!r.balanced && <p className="mt-2 text-xs text-amber-800">A non-zero difference usually means a capital, cash, bank or stock ledger is missing from the file. You can still import and post the balancing entry afterwards.</p>}
    </div>
  )
}

/** Download the full errors CSV (GET /v1/imports/{id}/errors?format=csv). */
export function DownloadErrors({ jobId, count }: { jobId: string; count?: number }) {
  const toast = useToast()
  const [busy, setBusy] = useState(false)
  const run = async () => {
    setBusy(true)
    try {
      const blob = await api<Blob>(`/aayaat/v1/imports/${jobId}/errors`, { method: 'GET', query: { format: 'csv' }, raw: true })
      downloadFile(`import-errors-${jobId.slice(0, 8)}.csv`, blob)
    } catch (e) { toast(errorMessage(e), 'error') } finally { setBusy(false) }
  }
  return <Button size="sm" variant="secondary" icon={Download} loading={busy} onClick={run}>Download errors{count ? ` (${count})` : ''} CSV</Button>
}

/** Invalid + failed rows with their messages and the original cell values. */
export function ErrorRows({ jobId, fieldLabel }: { jobId: string; fieldLabel?: (key: string) => string }) {
  const [offset, setOffset] = useState(0)
  const rows = useList<ImportRow>(`/aayaat/v1/imports/${jobId}/errors`, { limit: 20, offset })
  const label = fieldLabel ?? ((k: string) => k)
  return (
    <QueryState isLoading={rows.isLoading} error={rows.error} data={rows.data} retry={() => rows.refetch()}>
      {(d) => d.items.length === 0 ? (
        <Empty icon={Scale} title="No row errors" text="Every row passed validation." />
      ) : (
        <>
          <Table>
            <thead><tr><th className="th">Row</th><th className="th">Status</th><th className="th">Problem</th><th className="th">Values</th></tr></thead>
            <tbody>
              {d.items.map((r) => (
                <tr key={r.row_no} className="border-t border-slate-100 align-top">
                  <td className="td num">{r.row_no}</td>
                  <td className="td"><Badge tone="red">{r.status}</Badge></td>
                  <td className="td text-rose-700"><ul className="list-disc space-y-0.5 pl-4">{r.errors.map((e, i) => <li key={i}>{e}</li>)}</ul></td>
                  <td className="td text-xs text-slate-600">
                    {Object.entries(r.raw).filter(([, v]) => v !== '').map(([k, v]) => <span key={k} className="mr-3 inline-block"><span className="text-slate-400">{label(k)}:</span> {v}</span>)}
                  </td>
                </tr>
              ))}
            </tbody>
          </Table>
          <Pagination total={d.total} limit={d.limit} offset={d.offset} onChange={setOffset} />
        </>
      )}
    </QueryState>
  )
}
