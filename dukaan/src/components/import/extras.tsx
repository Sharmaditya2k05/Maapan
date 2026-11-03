'use client'

/**
 * Import wizard additions (aayaat): per-kind params (store_map, account_map, doc_type, side, bills_mode, source),
 * saved mapping templates, warnings download, dry run, batch journal and rollback.
 */
import { useMemo, useState } from 'react'
import { AlertTriangle, Download, FlaskConical, Layers, RotateCcw, Trash2 } from 'lucide-react'
import { Badge, Button, Card, Confirm, Empty, ErrorBox, Field, IconButton, QueryState, Select, Table, Textarea, useToast } from '@/components/ui'
import { api, del, errorMessage, post } from '@/lib/api'
import { downloadFile, fmtDate, useGet, useInvalidate, useStore } from '@/lib/hooks'
import type { ImportBatch, ImportDryRun, ImportJob, ImportParams, ImportRollbackResult, MappingTemplate } from '@/lib/types.more'
import { distinctColumn, mapToText, textToMap } from '@/lib/import/params'
import type { KindMeta } from './kinds'

export const SOURCES = [['', 'Not specified'], ['marg', 'MargBooks'], ['tally', 'Tally Prime / ERP 9'], ['busy', 'Busy'], ['vyapar', 'Vyapar'], ['excel', 'Other Excel']] as const

/** doc_type / side / bills_mode / source selects (shown at upload and mapping). */
export function KindParams({ meta, params, onChange }: { meta: KindMeta; params: ImportParams; onChange: (p: ImportParams) => void }) {
  return (
    <>
      <Field label="Exported from"><Select value={params.source ?? ''} onChange={(e) => onChange({ ...params, source: e.target.value as ImportParams['source'] })}>{SOURCES.map(([v, l]) => <option key={v} value={v}>{l}</option>)}</Select></Field>
      {meta.docType && (
        <Field label="Document type" hint="Used when the file has no type column">
          <Select value={params.doc_type ?? ''} onChange={(e) => onChange({ ...params, doc_type: e.target.value as ImportParams['doc_type'] })}>
            <option value="">Sales (default)</option><option value="purchase">Purchases</option><option value="sale_return">Credit notes (sales returns)</option><option value="purchase_return">Debit notes (purchase returns)</option>
          </Select>
        </Field>
      )}
      {meta.bills && (
        <>
          <Field label="Bills are" hint="When the file does not say Dr / Cr"><Select value={params.side ?? ''} onChange={(e) => onChange({ ...params, side: e.target.value as ImportParams['side'] })}><option value="">Receivable (customers)</option><option value="cr">Payable (suppliers)</option></Select></Field>
          <Field label="Import as" hint="Party totals = one opening balance per party, no bill detail">
            <Select value={params.bills_mode ?? ''} onChange={(e) => onChange({ ...params, bills_mode: e.target.value as ImportParams['bills_mode'] })}><option value="">Bill by bill (aging by due date)</option><option value="party_totals">Party totals only</option></Select>
          </Field>
        </>
      )}
    </>
  )
}

/** Store / godown names in the file → stores, and ledger names → account codes. */
export function MapParams({ meta, params, onChange, headers, rows, storeColumn, ledgerColumn }: {
  meta: KindMeta; params: ImportParams; onChange: (p: ImportParams) => void
  headers: string[]; rows: string[][]; storeColumn?: string; ledgerColumn?: string
}) {
  const { stores } = useStore()
  const all = useGet<{ items: { id: string; name: string }[] }>('/pehchaan/v1/stores')
  const storeList = all.data?.items ?? stores
  const names = useMemo(() => distinctColumn(headers, rows, storeColumn), [headers, rows, storeColumn])
  const ledgers = useMemo(() => distinctColumn(headers, rows, ledgerColumn), [headers, rows, ledgerColumn])
  const [acctText, setAcctText] = useState(() => mapToText(params.account_map))
  const acct = textToMap(acctText)
  const sm = params.store_map ?? {}
  const setStore = (name: string, id: string) => {
    const next = { ...sm }
    if (id) next[name] = id; else delete next[name]
    onChange({ ...params, store_map: Object.keys(next).length ? next : undefined })
  }
  return (
    <>
      {meta.store && storeColumn && (
        <div className="sm:col-span-3">
          <div className="label">Store per row <span className="font-normal text-slate-400">— values of “{storeColumn}” in the preview</span></div>
          {names.length === 0 ? <p className="text-xs text-slate-500">No values in the preview rows; the default store is used.</p> : (
            <div className="grid gap-2 sm:grid-cols-2">
              {names.map((n) => (
                <div key={n} className="flex items-center gap-2">
                  <span className="min-w-0 flex-1 truncate text-sm">{n}</span>
                  <Select className="input-sm w-48" value={sm[n] ?? ''} onChange={(e) => setStore(n, e.target.value)} aria-label={`Store for ${n}`}>
                    <option value="">Default store</option>{storeList.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
                  </Select>
                </div>
              ))}
            </div>
          )}
          <p className="mt-1 text-xs text-slate-500">A store id in the column also works. Only the first 20 rows are previewed; unmapped names fall back to the default store.</p>
        </div>
      )}
      {meta.accountMap && (
        <div className="sm:col-span-3">
          <Field label="Ledger / group → account code" hint={`One per line: “Output CGST = 2210”. Cash-in-Hand, Bank Accounts, Capital and parties map themselves.${ledgers.length ? ` In the preview: ${ledgers.slice(0, 8).join(', ')}${ledgers.length > 8 ? '…' : ''}` : ''}`}
            error={acct.bad.length ? `Line${acct.bad.length > 1 ? 's' : ''} ${acct.bad.join(', ')} not understood` : undefined}>
            <Textarea rows={4} className="font-mono text-xs" value={acctText} onChange={(e) => { setAcctText(e.target.value); const m = textToMap(e.target.value).map; onChange({ ...params, account_map: Object.keys(m).length ? m : undefined }) }} placeholder={'Duties & Taxes = 2200\nSundry Expenses = 5590'} />
          </Field>
        </div>
      )}
    </>
  )
}

/** Saved mappings offered by the preview; applying one fills mapping, sheet, header row and reusable params. */
export function TemplatePicker({ templates, onApply }: { templates: MappingTemplate[] | undefined; onApply: (t: MappingTemplate) => void }) {
  const list = templates ?? []
  if (list.length === 0) return null
  return (
    <div className="flex flex-wrap items-center gap-2 rounded-xl bg-brand-50 px-4 py-3 text-sm">
      <Layers size={16} className="text-brand-700" /><span className="font-semibold">Saved mappings:</span>
      {list.map((t) => (
        <button key={t.id} type="button" onClick={() => onApply(t)} className={`rounded-lg px-2.5 py-1 text-xs font-semibold ring-1 ring-inset cursor-pointer ${t.matches ? 'bg-white text-brand-700 ring-brand-300' : 'bg-white/60 text-slate-500 ring-slate-200'}`} title={t.matches ? 'Every mapped column is in this file' : 'Some mapped columns are missing from this file'}>
          {t.name}{t.matches && ' ✓'}
        </button>
      ))}
    </div>
  )
}

/** GET /v1/imports/{id}/warnings?format=csv */
export function DownloadWarnings({ jobId, count }: { jobId: string; count?: number }) {
  const toast = useToast()
  const [busy, setBusy] = useState(false)
  const run = async () => {
    setBusy(true)
    try { downloadFile(`import-warnings-${jobId.slice(0, 8)}.csv`, await api<Blob>(`/aayaat/v1/imports/${jobId}/warnings`, { method: 'GET', query: { format: 'csv' }, raw: true })) } catch (e) { toast(errorMessage(e), 'error') } finally { setBusy(false) }
  }
  return <Button size="sm" variant="secondary" icon={Download} loading={busy} onClick={run}>Warnings{count ? ` (${count})` : ''} CSV</Button>
}

export function WarningRows({ jobId }: { jobId: string }) {
  const q = useGet<{ items: { row_no: number; warnings?: string[]; raw: Record<string, string> }[] }>(`/aayaat/v1/imports/${jobId}/warnings`, { limit: 20 })
  return (
    <QueryState isLoading={q.isLoading} error={q.error} data={q.data} retry={() => q.refetch()}>
      {(d) => d.items.length === 0 ? <p className="px-4 py-4 text-sm text-slate-500">No warnings.</p> : (
        <Table>
          <thead><tr><th className="th">Row</th><th className="th">Worth a look</th></tr></thead>
          <tbody>{d.items.map((r) => <tr key={r.row_no} className="border-t border-slate-100 align-top"><td className="td num">{r.row_no}</td><td className="td text-amber-800"><ul className="list-disc space-y-0.5 pl-4">{(r.warnings ?? []).map((w, i) => <li key={i}>{w}</li>)}</ul></td></tr>)}</tbody>
        </Table>
      )}
    </QueryState>
  )
}

/** POST /v1/imports/{id}/dry-run — what a commit would send; nothing changes. */
export function DryRunPanel({ job }: { job: ImportJob }) {
  const [res, setRes] = useState<ImportDryRun | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const run = async () => {
    setBusy(true); setErr(null)
    try { setRes(await post<ImportDryRun>(`/aayaat/v1/imports/${job.id}/dry-run`)) } catch (e) { setErr(errorMessage(e)) } finally { setBusy(false) }
  }
  return (
    <Card title={<span className="inline-flex items-center gap-2"><FlaskConical size={16} /> Dry run</span>} actions={<Button size="sm" variant="secondary" loading={busy} onClick={run}>{res ? 'Run again' : 'Run dry run'}</Button>}>
      {!res && !err && <p className="text-sm text-slate-500">See exactly which calls the import would make, a sample of each payload, references nothing imports, and fields that are validated but not sent yet — without sending anything.</p>}
      {err && <ErrorBox message={err} />}
      {res && (
        <div className="space-y-3 text-sm">
          <p><span className="font-semibold">{res.would_send.toLocaleString('en-IN')}</span> rows would be sent{res.zero_rows ? `, ${res.zero_rows} skipped (netting to zero)` : ''}.</p>
          {(res.notes ?? []).length > 0 && <ul className="space-y-1 rounded-xl bg-amber-50 px-4 py-3 text-amber-800">{(res.notes ?? []).map((n, i) => <li key={i} className="flex gap-2"><AlertTriangle size={14} className="mt-0.5 shrink-0" />{n}</li>)}</ul>}
          {(res.not_sent_fields ?? []).length > 0 && <p className="text-xs text-slate-500">Validated but not sent yet: {(res.not_sent_fields ?? []).join(', ')}</p>}
          {(res.unresolved ?? []).length > 0 && (
            <div><div className="mb-1 text-xs font-semibold text-slate-500">References not found in any import</div>
              <ul className="text-xs">{(res.unresolved ?? []).slice(0, 10).map((u) => <li key={u.ref}><span className="font-mono">{u.ref}</span> — {u.count} row{u.count === 1 ? '' : 's'} (e.g. {u.rows.slice(0, 3).join(', ')})</li>)}</ul></div>
          )}
          {(res.calls ?? []).length > 0 && (
            <Table>
              <thead className="bg-slate-50/60"><tr><th className="th">Phase</th><th className="th">Call</th><th className="th">Rows</th><th className="th text-right">Records</th></tr></thead>
              <tbody className="divide-y divide-slate-100">
                {(res.calls ?? []).map((c, i) => (
                  <tr key={i} className="align-top">
                    <td className="td text-xs">{c.phase}</td>
                    <td className="td"><span className="font-mono text-xs">{c.service} {c.path}</span>{c.store_id && <div className="text-[11px] text-slate-400">store {c.store_id.slice(0, 8)}</div>}
                      {c.sample?.length > 0 && <details className="mt-1"><summary className="cursor-pointer text-[11px] text-brand-700">sample payload</summary><pre className="mt-1 max-h-48 overflow-auto rounded-lg bg-slate-50 p-2 text-[11px]">{JSON.stringify(c.sample, null, 2)}</pre></details>}</td>
                    <td className="td text-xs">{c.first_row}–{c.last_row}</td>
                    <td className="td num text-right">{c.records}</td>
                  </tr>
                ))}
              </tbody>
            </Table>
          )}
        </div>
      )}
    </Card>
  )
}

/** The bulk-call journal (GET /v1/imports/{id}/batches) with per-batch rollback. */
export function BatchesCard({ job }: { job: ImportJob }) {
  const q = useGet<{ items: ImportBatch[] }>(`/aayaat/v1/imports/${job.id}/batches`)
  const items = q.data?.items ?? []
  return (
    <Card title="Batches sent" padded={false} actions={<RollbackButton job={job} />}>
      <QueryState isLoading={q.isLoading} error={q.error} data={q.data} retry={() => q.refetch()}>
        {() => items.length === 0 ? <Empty icon={Layers} title="Nothing sent yet" /> : (
          <Table>
            <thead className="bg-slate-50/60"><tr><th className="th">#</th><th className="th">Call</th><th className="th">Rows</th><th className="th text-right">Created</th><th className="th text-right">Updated</th><th className="th text-right">Errors</th><th className="th">Status</th><th className="th" /></tr></thead>
            <tbody className="divide-y divide-slate-100">
              {items.map((b) => (
                <tr key={b.batch_no}>
                  <td className="td num">{b.batch_no}</td>
                  <td className="td"><span className="font-mono text-xs">{b.path}</span><div className="text-[11px] text-slate-400">{b.phase} · {fmtDate(b.sent_at, true)}</div></td>
                  <td className="td text-xs">{b.first_row}–{b.last_row} ({b.records})</td>
                  <td className="td num text-right">{b.created}</td><td className="td num text-right">{b.updated}</td>
                  <td className={`td num text-right ${b.errors ? 'text-rose-700' : 'text-slate-400'}`}>{b.errors}</td>
                  <td className="td"><Badge tone={b.status === 'recorded' ? 'green' : b.status === 'rolled_back' ? 'gray' : b.status === 'failed' ? 'red' : 'amber'}>{b.status}</Badge>{b.error && <div className="max-w-[200px] truncate text-[11px] text-rose-600" title={b.error}>{b.error}</div>}</td>
                  <td className="td text-right">{b.status === 'recorded' && <RollbackButton job={job} batch={b.batch_no} />}</td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
      </QueryState>
    </Card>
  )
}

/** POST /v1/imports/{id}/rollback[?batch=N]. */
export function RollbackButton({ job, batch }: { job: ImportJob; batch?: number }) {
  const toast = useToast()
  const invalidate = useInvalidate()
  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  if (job.imported === 0 || job.status === 'rolled_back' || job.status === 'running' || job.status === 'queued') return null
  const run = async () => {
    setBusy(true)
    try {
      const r = await api<ImportRollbackResult>(`/aayaat/v1/imports/${job.id}/rollback`, { method: 'POST', query: { batch } })
      const failed = r.failed?.length ?? 0
      toast(failed ? `${r.rolled_back} rows undone, ${failed} could not be undone` : `${r.rolled_back} rows undone`, failed ? 'error' : undefined)
      invalidate(`/aayaat/v1/imports/${job.id}`, '/aayaat/v1/imports', '/bahi/v1/parties', '/bahi/v1/accounts', '/bhandar/v1/items', '/bhandar/v1/stock')
      setOpen(false)
    } catch (e) { toast(errorMessage(e), 'error') } finally { setBusy(false) }
  }
  return (
    <>
      {batch ? <IconButton icon={RotateCcw} label={`Roll back batch ${batch}`} onClick={() => setOpen(true)} /> : <Button size="sm" variant="secondary" icon={RotateCcw} onClick={() => setOpen(true)}>Roll back import</Button>}
      <Confirm open={open} onClose={() => setOpen(false)} onConfirm={run} loading={busy} danger confirmLabel="Roll back"
        title={batch ? `Roll back batch ${batch}?` : 'Roll back this import?'}
        text="Records this import created are archived and its opening entries reversed in the books and stock. Records it only updated are left as they are. Anything used since (a bill settled, stock sold) is refused per row." />
    </>
  )
}

/** Saved mappings on the import history page (GET/DELETE /v1/mapping-templates). */
export function TemplatesCard({ kindLabel }: { kindLabel: (k: string) => string }) {
  const q = useGet<{ items: MappingTemplate[] }>('/aayaat/v1/mapping-templates')
  const invalidate = useInvalidate()
  const toast = useToast()
  const [busy, setBusy] = useState<string | null>(null)
  const items = q.data?.items ?? []
  if (items.length === 0) return null
  const remove = async (id: string) => {
    setBusy(id)
    try { await del(`/aayaat/v1/mapping-templates/${id}`); invalidate('/aayaat/v1/mapping-templates') } catch (e) { toast(errorMessage(e), 'error') } finally { setBusy(null) }
  }
  return (
    <Card title="Saved column mappings" padded={false}>
      <Table>
        <thead className="bg-slate-50/60"><tr><th className="th">Name</th><th className="th">Kind</th><th className="th">Source</th><th className="th text-right">Columns</th><th className="th">Updated</th><th className="th" /></tr></thead>
        <tbody className="divide-y divide-slate-100">
          {items.map((t) => (
            <tr key={t.id}><td className="td font-semibold">{t.name}</td><td className="td">{kindLabel(t.kind)}</td><td className="td text-slate-600">{t.source || '—'}</td><td className="td num text-right">{Object.keys(t.mapping).length}</td><td className="td text-xs text-slate-500">{fmtDate(t.updated_at, true)}</td>
              <td className="td text-right"><IconButton icon={Trash2} label={`Delete ${t.name}`} disabled={busy === t.id} onClick={() => remove(t.id)} className="hover:text-rose-600" /></td></tr>
          ))}
        </tbody>
      </Table>
    </Card>
  )
}

